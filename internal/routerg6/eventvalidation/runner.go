package eventvalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// Runner orchestrates validation screening with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, cardHash, symbol string)
	CatalogValidator func(historydata.HistoryCatalog) error
}

type profileContext struct {
	ProfileID    string
	Spec         eventmap.ProfileSpec
	Boundaries   eventmap.Boundaries
	Thresholds   eventmap.FrozenThresholds
	BTCBars      []eventmap.Bar
	ShockCluster func(time.Time) string
	Symbols      []string
}

// Run executes bounded validation screening for frozen EventCards.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("eventvalidation: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	bundle, err := LoadCardBundle(r.Config.CardOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if bundle.Manifest.ManifestHash != r.Config.CardManifestHash {
		return RunResult{}, fmt.Errorf("eventvalidation: card manifest hash mismatch")
	}
	for _, card := range bundle.Cards {
		if err := reverseevent.ValidateEventCard(card); err != nil {
			return RunResult{}, fmt.Errorf("frozen card %s: %w", card.EventCardHash, err)
		}
	}

	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	mapBundles := make([]reverseevent.MapBundle, 0, len(r.Config.MapDirs))
	profilesByID := map[string]profileContext{}
	for _, dir := range r.Config.MapDirs {
		mapBundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		mapBundles = append(mapBundles, mapBundle)
		mapManifestHashes[mapBundle.Manifest.ProfileID] = mapBundle.Manifest.ManifestHash
		for name, hash := range mapBundle.Manifest.ArtifactHashes {
			mapArtifactHashes[mapBundle.Manifest.ProfileID+"/"+name] = hash
		}
		if _, ok := profilesByID[mapBundle.Manifest.ProfileID]; ok {
			return RunResult{}, fmt.Errorf("eventvalidation: duplicate profile map %q", mapBundle.Manifest.ProfileID)
		}
		profile, err := r.buildProfileContext(mapBundle)
		if err != nil {
			return RunResult{}, err
		}
		profilesByID[mapBundle.Manifest.ProfileID] = profile
	}
	if err := VerifyBindings(r.Config, r.Catalog, bundle, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	if err := reverseevent.VerifyCatalogBinding(
		r.Catalog, r.Config.HistoryRoot, mapBundles,
		r.Config.MaxFiles, r.Config.MaxLines, r.Config.AllowTruncatedSmoke,
	); err != nil {
		return RunResult{}, err
	}

	accumulators := make([]*cardAccumulator, len(bundle.Cards))
	for i := range accumulators {
		accumulators[i] = newCardAccumulator()
	}
	nextSymbolByCard := map[string]int{}
	startCard := 0
	var linesProcessed int64

	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, bundle, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted(bundle, mapManifestHashes, mapArtifactHashes)
		}
		startCard = cp.NextCardIndex
		nextSymbolByCard = cp.NextSymbolIndexByCard
		linesProcessed = cp.LinesProcessed
		if len(cp.CardStats) == len(accumulators) {
			accumulators = restoreAccumulators(cp.CardStats)
		}
	}

	canonicalDataHashes := map[string]string{
		"catalog_hash":           r.Catalog.DataHash,
		"event_card_hash":        bundle.Manifest.EventCardHash,
		"experiment_hash":        bundle.Manifest.ExperimentHash,
		"card_manifest_hash":     bundle.Manifest.ManifestHash,
	}

	for cardIdx := startCard; cardIdx < len(bundle.Cards); cardIdx++ {
		card := bundle.Cards[cardIdx]
		profile, ok := profilesByID[card.ProfileID]
		if !ok {
			return RunResult{}, fmt.Errorf("eventvalidation: missing map profile %q for card %s", card.ProfileID, card.EventCardHash)
		}
		startSymbol := nextSymbolByCard[card.EventCardHash]
		tracker := eventmap.NewWindowTracker()
		for symIdx := startSymbol; symIdx < len(profile.Symbols); symIdx++ {
			select {
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			default:
			}
			symbol := profile.Symbols[symIdx]
			if err := r.scanSymbol(ctx, card, profile, symbol, tracker, accumulators[cardIdx], &linesProcessed); err != nil {
				return RunResult{}, err
			}
			nextSymbolByCard[card.EventCardHash] = symIdx + 1
			if err := r.saveProgressCheckpoint(
				cardIdx, nextSymbolByCard, accumulators, linesProcessed, false,
				bundle, mapManifestHashes,
			); err != nil {
				return RunResult{}, err
			}
			if r.OnProgress != nil {
				r.OnProgress(linesProcessed, card.EventCardHash, symbol)
			}
		}
		nextSymbolByCard[card.EventCardHash] = len(profile.Symbols)
	}

	results := BuildCardResults(bundle.Cards, accumulators, bundle.Ledger)
	report, err := BuildReport(r.Config, bundle, mapManifestHashes, results, bundle.Ledger)
	if err != nil {
		return RunResult{}, err
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(r.Config, bundle, mapManifestHashes, mapArtifactHashes, canonicalDataHashes, report, survivors, bundle.Ledger)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, report, survivors, receipt); err != nil {
		return RunResult{}, err
	}
	if err := r.saveProgressCheckpoint(
		len(bundle.Cards), nextSymbolByCard, accumulators, linesProcessed, true,
		bundle, mapManifestHashes,
	); err != nil {
		return RunResult{}, err
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}

func (r *Runner) buildProfileContext(bundle reverseevent.MapBundle) (profileContext, error) {
	spec, err := eventmap.ProfileSpecFor(bundle.Manifest.ProfileID)
	if err != nil {
		return profileContext{}, err
	}
	selection, err := eventmap.SelectPrimaryFiles(
		r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, bundle.Manifest.CatalogSymbolCount,
	)
	if err != nil {
		return profileContext{}, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if bundle.Manifest.ProfileID == eventmap.ProfileOKX1H {
		btcRel = "okx_1h/BTC-USDT-SWAP.jsonl"
	} else if bundle.Manifest.ProfileID == eventmap.ProfileOKX5M {
		btcRel = "okx_5m/BTC-USDT-SWAP.jsonl"
	}
	btcBars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
	if err != nil {
		return profileContext{}, err
	}
	boundaries, err := eventmap.ComputeBoundaries(spec, btcBars)
	if err != nil {
		return profileContext{}, err
	}
	shockIndex := eventmap.BuildShockIndex(btcBars, spec, bundle.Thresholds)
	return profileContext{
		ProfileID:    bundle.Manifest.ProfileID,
		Spec:         spec,
		Boundaries:   boundaries,
		Thresholds:   bundle.Thresholds,
		BTCBars:      btcBars,
		ShockCluster: shockIndex.Lookup,
		Symbols:      symbols,
	}, nil
}

func (r *Runner) scanSymbol(
	ctx context.Context,
	card reverseevent.FrozenEventCard,
	profile profileContext,
	symbol string,
	tracker *eventmap.WindowTracker,
	acc *cardAccumulator,
	linesProcessed *int64,
) error {
	rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
	if err := eventmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
		return err
	}
	bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
	if err != nil {
		return err
	}
	var funding []profitmap.FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
		if err := eventmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, fundingRel); err != nil {
			return err
		}
		funding, err = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
		if err != nil {
			return fmt.Errorf("parse optional funding %s: %w", symbol, err)
		}
	}
	cfg := ScanConfig{
		Context:        ctx,
		Spec:           profile.Spec,
		Boundaries:     profile.Boundaries,
		Thresholds:     profile.Thresholds,
		BTCBars:        profile.BTCBars,
		ShockClusterAt: profile.ShockCluster,
		Funding:        funding,
		MaxLines:       r.Config.MaxLines,
		ProgressEvery:  r.Config.ProgressEvery,
	}
	if err := ScanCardSymbol(bars, symbol, card, cfg, tracker, acc); err != nil {
		return err
	}
	*linesProcessed += int64(len(bars))
	return nil
}

func (r *Runner) saveProgressCheckpoint(
	nextCard int,
	nextSymbolByCard map[string]int,
	accumulators []*cardAccumulator,
	linesProcessed int64,
	done bool,
	bundle CardBundle,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		ConfigHash:            cfgHash,
		CatalogHash:           r.Catalog.DataHash,
		CardManifestHash:      bundle.Manifest.ManifestHash,
		EventCardHash:         bundle.Manifest.EventCardHash,
		ExperimentHash:        bundle.Manifest.ExperimentHash,
		MapManifestHashes:     cloneStringMap(mapManifestHashes),
		OutputDir:             r.Config.OutputDir,
		NextCardIndex:         nextCard,
		NextSymbolIndexByCard: cloneIntMapString(nextSymbolByCard),
		CardStats:             exportCardStats(accumulators, bundle.Cards),
		LinesProcessed:        linesProcessed,
		Done:                  done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

func (r *Runner) loadCompleted(
	bundle CardBundle,
	mapManifestHashes, mapArtifactHashes map[string]string,
) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_validation_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var report ValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return RunResult{}, err
	}
	if err := ValidateReport(report); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_validation_survivors.json"))
	if err != nil {
		return RunResult{}, err
	}
	var survivors ValidationSurvivors
	if err := json.Unmarshal(raw, &survivors); err != nil {
		return RunResult{}, err
	}
	if err := ValidateSurvivors(survivors); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_validation_receipt.json"))
	if err != nil {
		return RunResult{}, err
	}
	var receipt ValidationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	if err := ValidateReceipt(receipt); err != nil {
		return RunResult{}, err
	}
	_ = bundle
	_ = mapManifestHashes
	_ = mapArtifactHashes
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}

func cloneIntMapString(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
