package exclusionmap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

// Runner orchestrates bounded triple-exclusion map construction.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, accepted int)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed exclusion-map outputs.
type RunResult struct {
	Map      TripleExclusionMap
	Ledger   ExperimentLedger
	Manifest MapManifest
	Summary  RunSummary
}

// Run executes bounded exclusion evaluation with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g10protocolv2.HistoryDataHash {
		return RunResult{}, errors.New("exclusionmap: catalog data hash mismatch")
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}

	constitutionHash, err := ConstitutionHash()
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

	ann, err := LoadAnnouncementEnvelope(r.Config.AnnouncementDir)
	if err != nil {
		return RunResult{}, err
	}
	if loaded, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(
			loaded, r.Config, r.Catalog.DataHash, constitutionHash,
			mapManifestHashes, ann.TapeHash, ann.ManifestHash,
		); err != nil {
			return RunResult{}, err
		}
		if loaded.Done {
			return r.loadCompleted()
		}
	}
	if ann.Incomplete {
		return r.completeFailClosed(
			ctx, constitutionHash, bundles, mapManifestHashes, mapArtifactHashes, ann, RejectIncompletePIT,
		)
	}

	workItems, err := CollectWorkItems(bundles, r.Catalog, r.Config.HistoryRoot, r.Config.MaxFiles, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	triggeredSeedCount := len(workItems)
	cellStats := buildCellLookup(bundles)

	var cp Checkpoint
	if loaded, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(
			loaded, r.Config, r.Catalog.DataHash, constitutionHash,
			mapManifestHashes, ann.TapeHash, ann.ManifestHash,
		); err != nil {
			return RunResult{}, err
		}
		cp = loaded
	} else {
		cp = NewCheckpoint(r.Config, r.Catalog.DataHash, constitutionHash, mapManifestHashes, ann.TapeHash, ann.ManifestHash)
	}

	attemptedEvents := attemptedEventIDs(cp.Attempts)
	barCache := map[string][]eventmap.Bar{}
	fundingCache := map[string][]profitmap.FundingPoint{}
	instrumentCat, hasInstrumentCat := loadInstrumentCatalog(r.Config.HistoryRoot)

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
				result := EvaluationResult{EventID: item.Event.EventID, RejectReasons: []string{RejectBarReplayMiss}}
				proof, _ := BuildProof(item, result, constitutionHash)
				cp.Attempts = append(cp.Attempts, NewAttempt(len(cp.Attempts)+1, item, result, proof))
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

		cellCount, cellWinnerCount := cellStats.lookup(item)
		mctx, err := buildMetricContext(item, bars, spec, funding, ann, constitutionHash, cellCount, cellWinnerCount, instrumentCat, hasInstrumentCat)
		if err != nil {
			result := EvaluationResult{EventID: item.Event.EventID, RejectReasons: []string{RejectBarReplayMiss}}
			proof, _ := BuildProof(item, result, constitutionHash)
			cp.Attempts = append(cp.Attempts, NewAttempt(len(cp.Attempts)+1, item, result, proof))
			cp.NextWorkIndex = i + 1
			cp.SeenEventIDs = append(cp.SeenEventIDs, item.Event.EventID)
			attemptedEvents[item.Event.EventID] = true
			if err := r.maybeProgressAndCheckpoint(ctx, &cp); err != nil {
				return RunResult{}, err
			}
			continue
		}
		gross := bestDirectionGross(item, bars, spec, funding, mctx.DecisionBarIdx)
		result := EvaluateEvent(mctx, gross)
		proof, err := BuildProof(item, result, constitutionHash)
		if err != nil {
			return RunResult{}, err
		}
		attempt := NewAttempt(len(cp.Attempts)+1, item, result, proof)
		if result.Accepted && len(result.RejectReasons) == 0 {
			if cp.AcceptedCount >= r.Config.CandidateLimit {
				attempt.Status = LedgerStatusSkipped
				attempt.StatusReason = "candidate_limit"
			} else {
				cp.Proofs = append(cp.Proofs, proof)
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

	return r.finalizeRun(ctx, constitutionHash, bundles, mapManifestHashes, mapArtifactHashes, ann, triggeredSeedCount, cp, "")
}

type cellLookup struct {
	byProfile map[string]map[string]cellMeta
}

type cellMeta struct {
	Count       int
	WinnerCount int
}

func buildCellLookup(bundles []reverseevent.MapBundle) cellLookup {
	out := cellLookup{byProfile: map[string]map[string]cellMeta{}}
	for _, bundle := range bundles {
		profile := bundle.Manifest.ProfileID
		if out.byProfile[profile] == nil {
			out.byProfile[profile] = map[string]cellMeta{}
		}
		for _, cell := range bundle.Discovery.Cells {
			key := cellKeyString(cell.Key)
			meta := out.byProfile[profile][key]
			meta.Count = cell.Stats.Count
			meta.WinnerCount = len(cell.Winners)
			out.byProfile[profile][key] = meta
		}
	}
	return out
}

func (c cellLookup) lookup(item EventWorkItem) (count, winners int) {
	profile := c.byProfile[item.ProfileID]
	if profile == nil {
		return 0, 0
	}
	key := cellKeyString(eventmap.CellKey{
		EventFamily: item.Event.EventFamily,
		Horizon:     bestHorizonFromItem(item),
		Direction:   "long",
		DelayBars:   bestDelay(item),
		CapitalTier: 30,
	})
	meta, ok := profile[key]
	if !ok {
		return 0, 0
	}
	return meta.Count, meta.WinnerCount
}

func cellKeyString(key eventmap.CellKey) string {
	return fmt.Sprintf("%s|%s|%s|%d|%d", key.EventFamily, key.Horizon, key.Direction, key.DelayBars, key.CapitalTier)
}

func bestHorizonFromItem(item EventWorkItem) string {
	if item.Event.Horizon != "" {
		return item.Event.Horizon
	}
	return "1h"
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
	return r.checkCancelAndCheckpoint(ctx, cp)
}

func (r *Runner) checkCancelAndCheckpoint(ctx context.Context, cp *Checkpoint) error {
	select {
	case <-ctx.Done():
		if err := SaveCheckpoint(r.CheckpointPath, *cp); err != nil {
			return err
		}
		return ctx.Err()
	default:
	}
	if len(cp.Attempts) == 0 || len(cp.Attempts)%r.Config.ProgressEvery != 0 {
		return nil
	}
	return SaveCheckpoint(r.CheckpointPath, *cp)
}

func (r *Runner) finalizeRun(
	ctx context.Context,
	constitutionHash string,
	bundles []reverseevent.MapBundle,
	mapManifestHashes, mapArtifactHashes map[string]string,
	ann AnnouncementEnvelope,
	triggeredSeedCount int,
	cp Checkpoint,
	failClosedReason string,
) (RunResult, error) {
	exclusionMap, err := BuildExclusionMap(r.Config, constitutionHash, cp.Proofs)
	if err != nil {
		return RunResult{}, err
	}
	ledger, err := BuildLedger(constitutionHash, mapManifestHashes, ann.TapeHash, ann.ManifestHash, cp.Attempts)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(
		r.Config, r.Catalog.DataHash, constitutionHash,
		mapManifestHashes, mapArtifactHashes,
		ann.TapeHash, ann.ManifestHash,
		exclusionMap, ledger, nil,
	)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(r.Config, constitutionHash, triggeredSeedCount, ledger, exclusionMap, manifest, failClosedReason)
	artifactHashes, err := WriteOutputs(r.Config.OutputDir, exclusionMap, ledger, manifest, summary)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err = BuildManifest(
		r.Config, r.Catalog.DataHash, constitutionHash,
		mapManifestHashes, mapArtifactHashes,
		ann.TapeHash, ann.ManifestHash,
		exclusionMap, ledger, artifactHashes,
	)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	summary = BuildSummary(r.Config, constitutionHash, triggeredSeedCount, ledger, exclusionMap, manifest, failClosedReason)
	if err := writeJSONAtomic(filepath.Join(r.Config.OutputDir, SummaryFileName), summary); err != nil {
		return RunResult{}, err
	}
	cp.Done = true
	cp.MapHash = exclusionMap.MapHash
	cp.LedgerHash = ledger.LedgerHash
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	_ = bundles
	return RunResult{Map: exclusionMap, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	mapPath := filepath.Join(r.Config.OutputDir, OutputFileName)
	ledgerPath := filepath.Join(r.Config.OutputDir, LedgerFileName)
	manifestPath := filepath.Join(r.Config.OutputDir, ManifestFileName)
	summaryPath := filepath.Join(r.Config.OutputDir, SummaryFileName)

	exclusionMap, err := LoadExclusionMap(mapPath)
	if err != nil {
		return RunResult{}, err
	}
	rawLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		return RunResult{}, err
	}
	var ledger ExperimentLedger
	if err := unmarshalJSON(rawLedger, &ledger); err != nil {
		return RunResult{}, err
	}
	if err := ValidateLedger(ledger); err != nil {
		return RunResult{}, err
	}
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return RunResult{}, err
	}
	var manifest MapManifest
	if err := unmarshalJSON(rawManifest, &manifest); err != nil {
		return RunResult{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return RunResult{}, err
	}
	rawSummary, err := os.ReadFile(summaryPath)
	if err != nil {
		return RunResult{}, err
	}
	var summary RunSummary
	if err := unmarshalJSON(rawSummary, &summary); err != nil {
		return RunResult{}, err
	}
	if summary.ManifestHash != manifest.ManifestHash {
		return RunResult{}, fmt.Errorf("exclusionmap: summary manifest hash mismatch")
	}
	if err := VerifyOutputArtifacts(r.Config.OutputDir, manifest, summary); err != nil {
		return RunResult{}, err
	}
	return RunResult{Map: exclusionMap, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) completeFailClosed(
	ctx context.Context,
	constitutionHash string,
	bundles []reverseevent.MapBundle,
	mapManifestHashes, mapArtifactHashes map[string]string,
	ann AnnouncementEnvelope,
	reason string,
) (RunResult, error) {
	cp := NewCheckpoint(r.Config, r.Catalog.DataHash, constitutionHash, mapManifestHashes, ann.TapeHash, ann.ManifestHash)
	return r.finalizeRun(ctx, constitutionHash, bundles, mapManifestHashes, mapArtifactHashes, ann, 0, cp, reason)
}
