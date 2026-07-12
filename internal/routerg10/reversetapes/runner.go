package reversetapes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg6/reverseevent"
	"lingxi/internal/routerg6/eventmap"
)

// Runner orchestrates bounded reverse-tape reconstruction.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, executable, discovery int)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed reverse-tapes outputs.
type RunResult struct {
	ExecutableTapes []ExecutableProfitTape
	DiscoveryTapes  []ExecutableProfitTape
	Ledger          ExperimentLedger
	Manifest        TapeManifest
	Summary         RunSummary
}

// Run executes bounded reverse reconstruction with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g10protocolv2.HistoryDataHash {
		return RunResult{}, errors.New("reversetapes: catalog data hash mismatch")
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}

	if err := os.MkdirAll(r.Config.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}

	bundle, err := LoadInputBundle(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	if bundle.Announcements.Incomplete {
		return r.failClosed(bundle, RejectIncompletePIT)
	}

	if len(r.Config.MapDirs) > 0 {
		mapBundles, _, _, err := exclusionmap.LoadMapBundles(r.Config.MapDirs)
		if err != nil {
			return RunResult{}, err
		}
		if err := reverseevent.VerifyCatalogBinding(
			r.Catalog, r.Config.HistoryRoot, mapBundles,
			r.Config.MaxFiles, r.Config.MaxLines, r.Config.AllowTruncatedSmoke,
		); err != nil {
			return RunResult{}, err
		}
	}

	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	seeds := CollectAcceptedSeeds(bundle)
	var cp Checkpoint
	if loaded, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(loaded, r.Config, r.Catalog.DataHash, bundle.ConstitutionHash, bundle); err != nil {
			return RunResult{}, err
		}
		if loaded.Done && loaded.NextWorkIndex >= len(seeds) {
			return r.loadCompleted(bundle)
		}
		cp = loaded
	} else {
		cp, err = NewCheckpoint(r.Config, r.Catalog.DataHash, bundle.ConstitutionHash, bundle)
		if err != nil {
			return RunResult{}, err
		}
	}

	usedShocks := shockSetFromCheckpoint(cp)
	ordinal := len(cp.Attempts)

	for i := cp.NextWorkIndex; i < len(seeds); i++ {
		select {
		case <-ctx.Done():
			cp.NextWorkIndex = i
			_ = SaveCheckpoint(r.CheckpointPath, cp)
			return RunResult{}, ctx.Err()
		default:
		}

		proof := seeds[i]
		evalIn, missing, err := BindProofs(bundle, proof)
		if err != nil {
			return RunResult{}, err
		}
		var result EvaluationResult
		if len(missing) > 0 {
			result = EvaluationResult{
				RejectReasons: []string{RejectMissingProofBinding},
			}
		} else {
			result = Evaluate(evalIn, usedShocks)
		}

		ordinal++
		attempt := NewAttempt(ordinal, proof, result)
		cp.Attempts = append(cp.Attempts, attempt)

		if result.Accepted {
			if result.Status == TapeStatusDiscoveryOnly {
				if cp.DiscoveryCount < r.Config.CandidateLimit {
					cp.DiscoveryTapes = append(cp.DiscoveryTapes, result.Tape)
					cp.DiscoveryCount++
				}
			} else if cp.ExecutableCount < r.Config.CandidateLimit {
				cp.ExecutableTapes = append(cp.ExecutableTapes, result.Tape)
				cp.ExecutableCount++
				appendShockUsed(usedShocks, &cp, proof.Symbol, proof.ShockCluster)
			}
		}

		cp.NextWorkIndex = i + 1
		progressEvery := r.Config.ProgressEvery
		if progressEvery <= 0 {
			progressEvery = ProgressEvery
		}
		if r.OnProgress != nil && ordinal%maxInt(1, progressEvery) == 0 {
			r.OnProgress(ordinal, cp.ExecutableCount, cp.DiscoveryCount)
		}
	}

	cp.Done = true
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return r.sealOutputs(bundle, cp, len(seeds), "")
}

func (r *Runner) failClosed(bundle InputBundle, reason string) (RunResult, error) {
	ledger, err := BuildLedger(
		bundle.ConstitutionHash,
		bundle.ExclusionMap.MapHash, bundle.ExclusionManifest.ManifestHash,
		bundle.EntryManifest.ManifestHash, bundle.ExecutionManifest.ManifestHash,
		bundle.Announcements.TapeHash, bundle.Announcements.ManifestHash,
		nil,
	)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(r.Config, r.Catalog.DataHash, bundle.ConstitutionHash, bundle, ledger, map[string]string{}, 0, 0, 0)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(r.Config, bundle.ConstitutionHash, ledger, manifest, 0, reason)
	if err := r.writeArtifacts([]ExecutableProfitTape{}, []ExecutableProfitTape{}, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}
	return RunResult{Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) sealOutputs(bundle InputBundle, cp Checkpoint, seedCount int, failClosed string) (RunResult, error) {
	ledger, err := BuildLedger(
		bundle.ConstitutionHash,
		bundle.ExclusionMap.MapHash, bundle.ExclusionManifest.ManifestHash,
		bundle.EntryManifest.ManifestHash, bundle.ExecutionManifest.ManifestHash,
		bundle.Announcements.TapeHash, bundle.Announcements.ManifestHash,
		cp.Attempts,
	)
	if err != nil {
		return RunResult{}, err
	}
	cp.LedgerHash = ledger.LedgerHash

	artifactHashes := map[string]string{}
	execPath := filepath.Join(r.Config.OutputDir, OutputTapesFile)
	if err := WriteTapesJSONL(execPath, cp.ExecutableTapes); err != nil {
		return RunResult{}, err
	}
	if h, err := hashFile(execPath); err == nil {
		artifactHashes[OutputTapesFile] = h
	}
	discPath := filepath.Join(r.Config.OutputDir, OutputDiscoveryFile)
	if err := WriteTapesJSONL(discPath, cp.DiscoveryTapes); err != nil {
		return RunResult{}, err
	}
	if h, err := hashFile(discPath); err == nil {
		artifactHashes[OutputDiscoveryFile] = h
	}

	manifest, err := BuildManifest(
		r.Config, r.Catalog.DataHash, bundle.ConstitutionHash, bundle, ledger, artifactHashes,
		cp.ExecutableCount, cp.DiscoveryCount, countRejected(cp.Attempts),
	)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(r.Config, bundle.ConstitutionHash, ledger, manifest, seedCount, failClosed)

	if err := r.writeArtifacts(cp.ExecutableTapes, cp.DiscoveryTapes, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}

	return RunResult{
		ExecutableTapes: cp.ExecutableTapes,
		DiscoveryTapes:  cp.DiscoveryTapes,
		Ledger:          ledger,
		Manifest:        manifest,
		Summary:         summary,
	}, nil
}

func (r *Runner) writeArtifacts(
	executable, discovery []ExecutableProfitTape,
	ledger ExperimentLedger,
	manifest TapeManifest,
	summary RunSummary,
) error {
	if err := WriteTapesJSONL(filepath.Join(r.Config.OutputDir, OutputTapesFile), executable); err != nil {
		return err
	}
	if err := WriteTapesJSONL(filepath.Join(r.Config.OutputDir, OutputDiscoveryFile), discovery); err != nil {
		return err
	}
	if err := WriteLedger(r.Config.OutputDir, ledger); err != nil {
		return err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return err
	}
	return WriteSummary(r.Config.OutputDir, summary)
}

func (r *Runner) loadCompleted(bundle InputBundle) (RunResult, error) {
	cp, ok, err := LoadCheckpoint(r.CheckpointPath)
	if err != nil || !ok {
		return RunResult{}, fmt.Errorf("reversetapes: load completed checkpoint: %w", err)
	}
	return r.sealOutputs(bundle, cp, len(CollectAcceptedSeeds(bundle)), "")
}

func countRejected(attempts []ExperimentAttempt) int {
	n := 0
	for _, a := range attempts {
		if a.Status == LedgerStatusRejected {
			n++
		}
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hashFile(path string) (string, error) {
	return eventmap.HashFile(path)
}
