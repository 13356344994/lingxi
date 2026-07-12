package playbook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

// Runner orchestrates reverse Event Playbook derivation across residual map outputs.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, playbooks int)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed discovery outputs.
type RunResult struct {
	Playbooks []FrozenEventPlaybook
	Ledger    ExperimentLedger
	Manifest  PlaybookManifest
	Summary   RunSummary
}

// Run executes bounded reverse derivation with optional checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("catalog data hash mismatch")
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}

	bundles, mapManifestHashes, mapArtifactHashes, thresholds, err := r.loadBundles()
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyCatalogBinding(
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

	groups := CollectResidualGroups(bundles)
	winnerSeedCount := countWinners(groups)

	var resume *Checkpoint
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted()
		}
		resume = &cp
	}

	playbooks, ledger, err := Derive(groups, thresholds, r.Config.PlaybookLimit, resume)
	if err != nil {
		return RunResult{}, err
	}
	ledger.MapManifestHashes = mapManifestHashes
	if sealed, err := SealLedger(ledger); err != nil {
		return RunResult{}, err
	} else {
		ledger = sealed
	}
	for i := range playbooks {
		playbooks[i].ExperimentHash = ledger.LedgerHash
	}
	if r.OnProgress != nil {
		r.OnProgress(len(ledger.Attempts), len(playbooks))
	}

	coverage, coverageHash, err := BuildCoverage(groups, playbooks)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(
		r.Config, r.Catalog.DataHash, mapManifestHashes, mapArtifactHashes,
		playbooks, ledger, coverage, coverageHash,
	)
	if err != nil {
		return RunResult{}, err
	}
	noCandidatesReason, residualFailClosedReason, err := resolveNoCandidatesBinding(bundles, winnerSeedCount, len(playbooks))
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(
		groups, playbooks, ledger, manifest, winnerSeedCount, r.Config.PlaybookLimit,
		noCandidatesReason, residualFailClosedReason,
	)

	if err := WriteOutputs(r.Config.OutputDir, playbooks, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}

	playbooksHash, err := HashPlaybooks(playbooks)
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		Generation:         protocol.Generation,
		ProtocolHash:       protocol.RouterProtocolHash,
		HistoryDataHash:    protocol.HistoryDataHash,
		ConfigHash:         manifest.ConfigHash,
		CatalogHash:        r.Catalog.DataHash,
		MapManifestHashes:  mapManifestHashes,
		OutputDir:          r.Config.OutputDir,
		NextAttemptOrdinal: len(ledger.Attempts) + 1,
		PlaybookCount:      len(playbooks),
		Playbooks:          playbooks,
		LedgerAttempts:     ledger.Attempts,
		PlaybookHash:       playbooksHash,
		LedgerHash:         ledger.LedgerHash,
		Done:               true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Playbooks: playbooks, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) loadBundles() ([]ResidualMapBundle, map[string]string, map[string]string, map[string]eventmap.FrozenThresholds, error) {
	bundles := make([]ResidualMapBundle, 0, len(r.Config.ResidualMapDirs))
	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	thresholds := map[string]eventmap.FrozenThresholds{}
	for _, dir := range r.Config.ResidualMapDirs {
		bundle, err := LoadResidualMapBundle(dir)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		bundles = append(bundles, bundle)
		mapManifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
		thresholds[bundle.Manifest.ProfileID] = bundle.Thresholds
		for name, hash := range bundle.Manifest.ArtifactHashes {
			mapArtifactHashes[bundle.Manifest.ProfileID+"/"+name] = hash
		}
	}
	return bundles, mapManifestHashes, mapArtifactHashes, thresholds, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	manifestPath := filepath.Join(r.Config.OutputDir, "event_playbook_manifest.json")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyOutputArtifacts(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	playbooksPath := filepath.Join(r.Config.OutputDir, "g10_event_playbooks.json")
	raw, err := os.ReadFile(playbooksPath)
	if err != nil {
		return RunResult{}, err
	}
	var payload struct {
		Playbooks []FrozenEventPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return RunResult{}, err
	}
	ledgerPath := filepath.Join(r.Config.OutputDir, "g10_event_playbook_ledger.json")
	raw, err = os.ReadFile(ledgerPath)
	if err != nil {
		return RunResult{}, err
	}
	var ledger ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return RunResult{}, err
	}
	summaryPath := filepath.Join(r.Config.OutputDir, "g10_playbooks_summary.json")
	raw, err = os.ReadFile(summaryPath)
	if err != nil {
		return RunResult{}, err
	}
	var summary RunSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		Playbooks: payload.Playbooks,
		Ledger:    ledger,
		Manifest:  manifest,
		Summary:   summary,
	}, nil
}
