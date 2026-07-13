package residualmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

// Runner orchestrates bounded personal residual map construction.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, accepted int)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed residual-map outputs.
type RunResult struct {
	Map      PersonalResidualMap
	Ledger   ExperimentLedger
	Manifest MapManifest
	Summary  RunSummary
}

// Run executes bounded residual evaluation with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g10protocol.HistoryDataHash {
		return RunResult{}, errors.New("catalog data hash mismatch")
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}

	fpEnv, err := LoadFingerprint(r.Config.FingerprintPath)
	if err != nil {
		return RunResult{}, err
	}
	bundles, mapManifestHashes, mapArtifactHashes, err := LoadMapBundles(r.Config.MapDirs)
	if err != nil {
		return RunResult{}, err
	}
	if err := reverseevent.VerifyCatalogBinding(
		r.Catalog, r.Config.HistoryRoot, bundles,
		r.Config.MaxFiles, r.Config.MaxLines, r.Config.AllowTruncatedSmoke,
	); err != nil {
		return RunResult{}, err
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	failClosed, err := ShouldFailClosedZeroCandidates(
		fpEnv.Fingerprint, bundles, r.Catalog, r.Config.MaxFiles,
	)
	if err != nil {
		return RunResult{}, err
	}
	if failClosed {
		return r.completeZeroCandidates(
			ctx, fpEnv, bundles, mapManifestHashes, mapArtifactHashes, FailClosedFingerprintCoverage,
		)
	}

	eligibleSymbols := CriticalKnownSymbolSet(fpEnv.Fingerprint)
	workItems, err := CollectTriggeredEventWorkItemsForSymbols(
		bundles, r.Catalog, r.Config.HistoryRoot, r.Config.MaxFiles, r.Config.MaxLines, eligibleSymbols,
	)
	if err != nil {
		return RunResult{}, err
	}
	triggeredSeedCount := len(workItems)

	var cp Checkpoint
	if loaded, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(
			loaded, r.Config, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash, mapManifestHashes,
		); err != nil {
			return RunResult{}, err
		}
		if loaded.Done && loaded.NextWorkIndex >= len(workItems) {
			return r.loadCompleted()
		}
		if loaded.Done {
			loaded.Done = false
		}
		cp = loaded
	} else {
		cp = NewCheckpoint(r.Config, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash, mapManifestHashes)
	}

	attemptedEvents := attemptedEventIDs(cp.Attempts)
	barCache := map[string][]eventmap.Bar{}
	fundingCache := map[string][]profitmap.FundingPoint{}

	for i := cp.NextWorkIndex; i < len(workItems); i++ {
		if err := r.checkCancelAndCheckpoint(ctx, &cp); err != nil {
			return RunResult{}, err
		}
		item := workItems[i]
		if attemptedEvents[item.Event.EventID] {
			cp.NextWorkIndex = i + 1
			continue
		}

		spec, err := eventmap.ProfileSpecFor(item.ProfileID)
		if err != nil {
			return RunResult{}, err
		}
		bars, ok := barCache[item.Event.Symbol]
		if !ok {
			rel := spec.PrimaryFamily + "/" + item.Event.Symbol + ".jsonl"
			bars, err = eventmap.LoadBars(eventmap.CatalogFilePath(r.Config.HistoryRoot, rel), r.Config.MaxLines)
			if err != nil {
				result := EvaluationResult{
					EventID: item.Event.EventID, RejectReasons: []string{RejectBarReplayMiss},
				}
				cp.Attempts = append(cp.Attempts, NewAttempt(len(cp.Attempts)+1, item, result))
				cp.NextWorkIndex = i + 1
				cp.SeenEventIDs = append(cp.SeenEventIDs, item.Event.EventID)
				attemptedEvents[item.Event.EventID] = true
				if err := r.maybeProgressAndCheckpoint(ctx, &cp); err != nil {
					return RunResult{}, err
				}
				continue
			}
			barCache[item.Event.Symbol] = bars
		}
		funding, ok := fundingCache[item.Event.Symbol]
		if !ok {
			funding, _ = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, item.Event.Symbol, r.Config.MaxLines)
			fundingCache[item.Event.Symbol] = funding
		}

		result, err := EvaluateTriggeredEvent(item, bars, spec, funding, fpEnv.Fingerprint)
		if err != nil {
			return RunResult{}, err
		}
		attempt := NewAttempt(len(cp.Attempts)+1, item, result)
		if result.Accepted && len(result.RejectReasons) == 0 {
			if cp.AcceptedCount >= r.Config.CandidateLimit {
				attempt.Status = LedgerStatusSkipped
				attempt.StatusReason = "candidate_limit"
			} else {
				opp, err := BuildOpportunity(item, result, fpEnv.Fingerprint.FingerprintHash)
				if err != nil {
					return RunResult{}, err
				}
				attempt.ResidualHash = opp.ResidualHash
				cp.Opportunities = append(cp.Opportunities, opp)
				cp.AcceptedCount++
			}
		}
		cp.Attempts = append(cp.Attempts, attempt)
		cp.NextWorkIndex = i + 1
		cp.SeenEventIDs = append(cp.SeenEventIDs, item.Event.EventID)
		attemptedEvents[item.Event.EventID] = true

		if err := r.maybeProgressAndCheckpoint(ctx, &cp); err != nil {
			return RunResult{}, err
		}
	}

	return r.finalizeRun(
		ctx, fpEnv, bundles, mapManifestHashes, mapArtifactHashes, triggeredSeedCount, cp, "",
	)
}

func attemptedEventIDs(attempts []ExperimentAttempt) map[string]bool {
	out := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		out[attempt.EventID] = true
	}
	return out
}

func (r *Runner) maybeProgressAndCheckpoint(ctx context.Context, cp *Checkpoint) error {
	attemptCount := len(cp.Attempts)
	if attemptCount == 0 || attemptCount%r.Config.ProgressEvery != 0 {
		return nil
	}
	if r.OnProgress != nil {
		r.OnProgress(attemptCount, cp.AcceptedCount)
	}
	select {
	case <-ctx.Done():
		if err := SaveCheckpoint(r.CheckpointPath, *cp); err != nil {
			return err
		}
		return ctx.Err()
	default:
	}
	return SaveCheckpoint(r.CheckpointPath, *cp)
}

func (r *Runner) checkCancelAndCheckpoint(ctx context.Context, cp *Checkpoint) error {
	select {
	case <-ctx.Done():
		if err := SaveCheckpoint(r.CheckpointPath, *cp); err != nil {
			return err
		}
		return ctx.Err()
	default:
		return nil
	}
}

func (r *Runner) completeZeroCandidates(
	ctx context.Context,
	fpEnv FingerprintEnvelope,
	bundles []reverseevent.MapBundle,
	mapManifestHashes map[string]string,
	mapArtifactHashes map[string]string,
	failClosedReason string,
) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	cp := NewCheckpoint(r.Config, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash, mapManifestHashes)
	return r.finalizeRun(
		ctx, fpEnv, bundles, mapManifestHashes, mapArtifactHashes, 0, cp, failClosedReason,
	)
}

func (r *Runner) finalizeRun(
	ctx context.Context,
	fpEnv FingerprintEnvelope,
	bundles []reverseevent.MapBundle,
	mapManifestHashes map[string]string,
	mapArtifactHashes map[string]string,
	triggeredSeedCount int,
	cp Checkpoint,
	failClosedReason string,
) (RunResult, error) {
	select {
	case <-ctx.Done():
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
		return RunResult{}, ctx.Err()
	default:
	}

	residualMap, err := BuildResidualMap(r.Config, fpEnv.Fingerprint.FingerprintHash, cp.Opportunities)
	if err != nil {
		return RunResult{}, err
	}
	ledger, err := BuildLedger(fpEnv.Fingerprint.FingerprintHash, mapManifestHashes, cp.Attempts)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(
		r.Config, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash, fpEnv.Path,
		mapManifestHashes, mapArtifactHashes, residualMap, ledger, nil,
	)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(
		r.Config, fpEnv.Fingerprint.FingerprintHash, triggeredSeedCount, ledger, residualMap, manifest,
	)
	summary.FailClosedReason = failClosedReason
	artifactHashes, err := WriteOutputs(r.Config.OutputDir, residualMap, ledger, manifest, summary)
	if err != nil {
		return RunResult{}, err
	}
	profileHashes, err := WriteProfileBundles(
		r.Config.OutputDir, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash,
		cp.Opportunities, bundles, failClosedReason,
	)
	if err != nil {
		return RunResult{}, err
	}
	for k, v := range profileHashes {
		artifactHashes[k] = v
	}
	manifest.ArtifactHashes = artifactHashes
	manifest, err = BuildManifest(
		r.Config, r.Catalog.DataHash, fpEnv.Fingerprint.FingerprintHash, fpEnv.Path,
		mapManifestHashes, mapArtifactHashes, residualMap, ledger, artifactHashes,
	)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	summary = BuildSummary(
		r.Config, fpEnv.Fingerprint.FingerprintHash, triggeredSeedCount, ledger, residualMap, manifest,
	)
	summary.FailClosedReason = failClosedReason
	if err := writeJSON(filepath.Join(r.Config.OutputDir, SummaryFileName), summary); err != nil {
		return RunResult{}, err
	}

	cp.Done = true
	cp.MapHash = residualMap.MapHash
	cp.LedgerHash = ledger.LedgerHash
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Map: residualMap, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	manifest, err := LoadManifest(filepath.Join(r.Config.OutputDir, ManifestFileName))
	if err != nil {
		return RunResult{}, err
	}
	residualMap, err := LoadResidualMap(filepath.Join(r.Config.OutputDir, OutputFileName))
	if err != nil {
		return RunResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, LedgerFileName))
	if err != nil {
		return RunResult{}, err
	}
	var ledger ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return RunResult{}, err
	}
	if err := ValidateLedger(ledger); err != nil {
		return RunResult{}, err
	}
	var summary RunSummary
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, SummaryFileName))
	if err != nil {
		return RunResult{}, err
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	if summary.ManifestHash != manifest.ManifestHash {
		return RunResult{}, fmt.Errorf("summary manifest hash mismatch")
	}
	return RunResult{Map: residualMap, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

// Validate checks required bounded CLI knobs.
func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.OutputDir == "" || c.FingerprintPath == "" {
		return errors.New("history_root, output_dir, and fingerprint_path are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.CandidateLimit <= 0 {
		return errors.New("candidate_limit must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
	}
	if c.Generation == "" {
		c.Generation = Generation
	}
	if c.ProtocolHash == "" {
		c.ProtocolHash = DefaultProtocolHash()
	}
	if c.HistoryDataHash == "" {
		c.HistoryDataHash = DefaultHistoryDataHash()
	}
	return nil
}
