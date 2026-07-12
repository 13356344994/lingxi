package reverseevent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

// Runner orchestrates reverse EventCard derivation across one or more event-map outputs.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(attempts, cards int)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed discovery outputs.
type RunResult struct {
	Cards    []FrozenEventCard
	Ledger   ExperimentLedger
	Manifest EventCardManifest
	Summary  RunSummary
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

	groups := CollectWinnerGroups(bundles)
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

	cards, ledger, err := Derive(groups, thresholds, r.Config.CardLimit, resume)
	if err != nil {
		return RunResult{}, err
	}
	if r.OnProgress != nil {
		r.OnProgress(len(ledger.Attempts), len(cards))
	}

	coverage, coverageHash, err := BuildCoverage(groups, cards)
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(
		r.Config, r.Catalog.DataHash, mapManifestHashes, mapArtifactHashes,
		cards, ledger, coverage, coverageHash,
	)
	if err != nil {
		return RunResult{}, err
	}
	summary := BuildSummary(groups, cards, ledger, manifest, winnerSeedCount, r.Config.CardLimit)

	if err := WriteOutputs(r.Config.OutputDir, cards, ledger, manifest, summary); err != nil {
		return RunResult{}, err
	}

	cardsHash, err := HashEventCards(cards)
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
		CardCount:          len(cards),
		Cards:              cards,
		LedgerAttempts:     ledger.Attempts,
		EventCardHash:      cardsHash,
		LedgerHash:         ledger.LedgerHash,
		Done:               true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Cards: cards, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func (r *Runner) loadBundles() ([]MapBundle, map[string]string, map[string]string, map[string]eventmap.FrozenThresholds, error) {
	bundles := make([]MapBundle, 0, len(r.Config.MapDirs))
	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	thresholds := map[string]eventmap.FrozenThresholds{}
	for _, dir := range r.Config.MapDirs {
		bundle, err := LoadMapBundle(dir)
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
	manifest, err := LoadManifest(filepath.Join(r.Config.OutputDir, "event_card_manifest.json"))
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyOutputArtifacts(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_event_cards.json"))
	if err != nil {
		return RunResult{}, err
	}
	var cardPayload struct {
		Cards []FrozenEventCard `json:"cards"`
	}
	if err := json.Unmarshal(raw, &cardPayload); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_event_card_ledger.json"))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_reverse_events_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	if summary.CoverageHash == "" || summary.CoverageHash != manifest.CoverageHash {
		return RunResult{}, fmt.Errorf("summary coverage hash mismatch")
	}
	return RunResult{
		Cards: cardPayload.Cards, Ledger: ledger, Manifest: manifest, Summary: summary,
	}, nil
}

func countWinners(groups []WinnerGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Winners)
	}
	return total
}
