package profitmap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/protocol"
)

// Runner orchestrates one bounded profile scan with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, symbol string)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds outputs from one profile scan.
type RunResult struct {
	ProfileID   string
	Store       *MapStore
	Boundaries  Boundaries
	Cutpoints   sky.Cutpoints
	SymbolClass SymbolClassCutpoints
	Symbols     []string
	Summaries   []DiscoverySummary
	Manifest    MapManifest
}

// Run executes the full bounded profit map scan for one profile.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	spec, err := ProfileSpecFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("profitmap: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}
	selection, err := SelectPrimaryFiles(r.Catalog, r.Config.ProfileID, r.Config.MaxFiles, r.Config.CatalogSymbolLimit)
	if err != nil {
		return RunResult{}, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	scope, err := ValidateSelectionScope(selection, r.Config.CatalogSymbolLimit, r.Config.AllowTruncatedSmoke)
	if err != nil {
		return RunResult{}, fmt.Errorf("profitmap: selection scope: %w", err)
	}
	if len(symbols) == 0 {
		return RunResult{}, fmt.Errorf("profitmap: no symbols selected for profile %q", r.Config.ProfileID)
	}
	btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, btcRel); err != nil {
		return RunResult{}, err
	}
	btcBars, err := LoadBars(filepath.Join(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	boundaries, err := ComputeBoundaries(spec, btcBars)
	if err != nil {
		return RunResult{}, err
	}
	bHash, err := HashBoundaries(boundaries)
	if err != nil {
		return RunResult{}, err
	}
	cutpoints, err := FitSkyCutpoints(r.Catalog, r.Config.HistoryRoot, boundaries, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	cHash, err := HashCutpoints(cutpoints)
	if err != nil {
		return RunResult{}, err
	}
	skySeries, err := BuildBTCSkySeries(r.Catalog, r.Config.HistoryRoot, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	symbolClass, classLabels, scHash, err := r.fitSymbolClasses(spec, boundaries, symbols)
	if err != nil {
		return RunResult{}, err
	}
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	selectedFilesHash, _, err := datatruth.HashJSON(selection.Files)
	if err != nil {
		return RunResult{}, err
	}

	startIndex := 0
	var linesProcessed int64
	store := NewMapStore()
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, len(symbols), selectedFilesHash, scope, bHash, cHash, scHash); err != nil {
			return RunResult{}, err
		}
		store = RestoreMapStore(cp.Accumulator)
		restoredHash, err := store.Hash()
		if err != nil || restoredHash != cp.CellHash {
			return RunResult{}, fmt.Errorf("profitmap: checkpoint accumulator hash mismatch")
		}
		if cp.Done {
			if err := VerifyArtifactHashes(r.Config.OutputDir, cp.ArtifactHashes); err != nil {
				return RunResult{}, err
			}
			manifest, summaries, err := loadCompletedMetadata(r.Config.OutputDir, spec.ID, scope)
			if err != nil {
				return RunResult{}, err
			}
			return RunResult{
				ProfileID:   spec.ID,
				Store:       store,
				Boundaries:  boundaries,
				Cutpoints:   cutpoints,
				SymbolClass: symbolClass,
				Symbols:     symbols,
				Summaries:   summaries,
				Manifest:    manifest,
			}, nil
		}
		startIndex = cp.NextSymbolIndex
		linesProcessed = cp.LinesProcessed
	}

	if err := r.scanSymbolsFrom(ctx, spec, symbols, startIndex, boundaries, cutpoints, skySeries, symbolClass, classLabels, store, &linesProcessed, cfgHash, selectedFilesHash, scope, bHash, cHash, scHash); err != nil {
		return RunResult{}, err
	}
	return r.finalize(spec, selection, scope, symbols, boundaries, cutpoints, symbolClass, cfgHash, selectedFilesHash, store)
}

func loadCompletedMetadata(outputDir, profileID string, scope MapScope) (MapManifest, []DiscoverySummary, error) {
	raw, err := os.ReadFile(filepath.Join(outputDir, "map_manifest.json"))
	if err != nil {
		return MapManifest{}, nil, err
	}
	var manifest MapManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return MapManifest{}, nil, err
	}
	want, err := hashManifest(manifest)
	if err != nil {
		return MapManifest{}, nil, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return MapManifest{}, nil, fmt.Errorf("profitmap: completed manifest self-hash mismatch")
	}
	if err := validateOutputScope(manifest.AvailablePrimaryFiles, manifest.SelectedPrimaryFiles, manifest.SelectionTruncated, manifest.FullMarket, manifest.MapScope, scope); err != nil {
		return MapManifest{}, nil, fmt.Errorf("profitmap: completed manifest scope: %w", err)
	}
	raw, err = os.ReadFile(filepath.Join(outputDir, "g4_profit_map_summary.json"))
	if err != nil {
		return MapManifest{}, nil, err
	}
	var payload struct {
		Summaries []DiscoverySummary `json:"summaries"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return MapManifest{}, nil, err
	}
	if len(payload.Summaries) != 3 {
		return MapManifest{}, nil, fmt.Errorf("profitmap: completed summary partitions missing")
	}
	for _, summary := range payload.Summaries {
		if err := validateOutputScope(summary.AvailablePrimaryFiles, summary.SelectedPrimaryFiles, summary.SelectionTruncated, summary.FullMarket, summary.MapScope, scope); err != nil {
			return MapManifest{}, nil, fmt.Errorf("profitmap: completed summary scope: %w", err)
		}
	}
	for _, partition := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		raw, err := os.ReadFile(filepath.Join(outputDir, fmt.Sprintf("map_%s_%s.json", partition, profileID)))
		if err != nil {
			return MapManifest{}, nil, err
		}
		var pm PartitionMap
		if err := json.Unmarshal(raw, &pm); err != nil {
			return MapManifest{}, nil, err
		}
		if err := validateOutputScope(pm.AvailablePrimaryFiles, pm.SelectedPrimaryFiles, pm.SelectionTruncated, pm.FullMarket, pm.MapScope, scope); err != nil {
			return MapManifest{}, nil, fmt.Errorf("profitmap: completed partition scope: %w", err)
		}
	}
	return manifest, payload.Summaries, nil
}

func validateOutputScope(available, selected int, truncated, fullMarket bool, mapScope string, expected MapScope) error {
	if available != expected.AvailablePrimaryFiles || selected != expected.SelectedPrimaryFiles ||
		truncated != expected.SelectionTruncated || fullMarket != expected.FullMarket || mapScope != expected.MapScope {
		return fmt.Errorf("scope fields mismatch")
	}
	return nil
}

func (r *Runner) scanSymbolsFrom(
	ctx context.Context,
	spec ProfileSpec,
	symbols []string,
	startIndex int,
	boundaries Boundaries,
	cutpoints sky.Cutpoints,
	skySeries sky.Series,
	symbolClass SymbolClassCutpoints,
	classLabels map[string]string,
	store *MapStore,
	linesProcessed *int64,
	cfgHash, selectedFilesHash string, scope MapScope, bHash, cHash, scHash string,
) error {
	for i := startIndex; i < len(symbols); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		symbol := symbols[i]
		if err := r.scanOneSymbol(spec, symbol, boundaries, cutpoints, skySeries, symbolClass, classLabels, store, linesProcessed); err != nil {
			return err
		}
		if r.OnProgress != nil {
			r.OnProgress(*linesProcessed, symbol)
		}
		// Persist a bounded recovery point in chunks. Rewriting the complete
		// accumulator after every symbol makes full-market scans I/O bound.
		if (i+1)%protocol.ScanCheckpointEverySymbols != 0 && i+1 != len(symbols) {
			continue
		}
		cellHash, err := store.Hash()
		if err != nil {
			return err
		}
		cp := Checkpoint{
			ProfileID:          r.Config.ProfileID,
			Generation:         protocol.Generation,
			ProtocolHash:       protocol.RouterProtocolHash,
			HistoryDataHash:    protocol.HistoryDataHash,
			CatalogHash:        r.Catalog.DataHash,
			CatalogSymbolCount: len(symbols),
			SelectedFilesHash:  selectedFilesHash,
			FullMarket:         scope.FullMarket,
			MapScope:           scope.MapScope,
			ConfigHash:         cfgHash,
			BoundariesHash:     bHash,
			CutpointsHash:      cHash,
			SymbolClassHash:    scHash,
			OutputDir:          r.Config.OutputDir,
			NextSymbolIndex:    i + 1,
			CompletedSymbols:   append([]string(nil), symbols[:i+1]...),
			CellHash:           cellHash,
			Accumulator:        store.Snapshot(),
			LinesProcessed:     *linesProcessed,
			Done:               false,
		}
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) scanSymbols(
	ctx context.Context,
	spec ProfileSpec,
	symbols []string,
	boundaries Boundaries,
	cutpoints sky.Cutpoints,
	skySeries sky.Series,
	symbolClass SymbolClassCutpoints,
	classLabels map[string]string,
	store *MapStore,
	linesProcessed *int64,
) error {
	for i := 0; i < len(symbols); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := r.scanOneSymbol(spec, symbols[i], boundaries, cutpoints, skySeries, symbolClass, classLabels, store, linesProcessed); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) scanOneSymbol(
	spec ProfileSpec,
	symbol string,
	boundaries Boundaries,
	cutpoints sky.Cutpoints,
	skySeries sky.Series,
	symbolClass SymbolClassCutpoints,
	classLabels map[string]string,
	store *MapStore,
	linesProcessed *int64,
) error {
	rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
	if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
		return err
	}
	bars, err := LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
	if err != nil {
		return err
	}
	var funding []FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := CatalogFile(r.Catalog, fundingRel); declared {
		if stats.Empty {
			funding = nil
		} else {
			if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, fundingRel); err != nil {
				return err
			}
			funding, err = LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return fmt.Errorf("profitmap: parse optional funding %s: %w", symbol, err)
			}
		}
	}
	scanCfg := ScanSymbolConfig{
		Spec:             spec,
		Boundaries:       boundaries,
		SkyCutpoints:     cutpoints,
		SkySeries:        skySeries,
		SymbolClass:      symbolClass,
		SymbolClassLabel: classLabels[symbol],
		Funding:          funding,
		MaxLines:         r.Config.MaxLines,
		ProgressEvery:    r.Config.ProgressEvery,
		OnProgress: func(lines int) {
			if r.OnProgress != nil {
				r.OnProgress(*linesProcessed+int64(lines), symbol)
			}
		},
	}
	if err := StreamSymbol(bars, symbol, scanCfg, func(opp Opportunity) error {
		store.Add(opp)
		return nil
	}); err != nil {
		return err
	}
	*linesProcessed += int64(len(bars))
	return nil
}

func (r *Runner) fitSymbolClasses(spec ProfileSpec, boundaries Boundaries, symbols []string) (SymbolClassCutpoints, map[string]string, string, error) {
	var volumes []float64
	labels := map[string]string{}
	for _, symbol := range symbols {
		rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
		if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
			return SymbolClassCutpoints{}, nil, "", err
		}
		bars, err := LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return SymbolClassCutpoints{}, nil, "", err
		}
		avg := DiscoveryAvgQuoteVolume(bars, spec, boundaries)
		volumes = append(volumes, avg)
	}
	cutpoints := FitSymbolClassCutpoints(volumes)
	hash, err := cutpoints.Hash()
	if err != nil {
		return SymbolClassCutpoints{}, nil, "", err
	}
	for i, symbol := range symbols {
		if i < len(volumes) {
			labels[symbol] = cutpoints.Classify(volumes[i])
		}
	}
	return cutpoints, labels, hash, nil
}

func (r *Runner) finalize(
	spec ProfileSpec,
	selection Selection,
	scope MapScope,
	symbols []string,
	boundaries Boundaries,
	cutpoints sky.Cutpoints,
	symbolClass SymbolClassCutpoints,
	cfgHash string,
	selectedFilesHash string,
	store *MapStore,
) (RunResult, error) {
	if err := writeOutputs(r.Config.OutputDir, spec.ID, scope, store); err != nil {
		return RunResult{}, err
	}
	cellHash, err := store.Hash()
	if err != nil {
		return RunResult{}, err
	}
	reservoirRiskHash, err := store.ReservoirRiskHash()
	if err != nil {
		return RunResult{}, err
	}
	partitions := []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd}
	summaries := make([]DiscoverySummary, 0, len(partitions))
	for _, partition := range partitions {
		summary := BuildDiscoverySummary(spec.ID, store.ExportPartitionScoped(spec.ID, partition, scope))
		summary.Generation = protocol.Generation
		summary.ProtocolHash = protocol.RouterProtocolHash
		summary.ExamActions = protocol.ExamActions
		summaries = append(summaries, summary)
	}
	if err := WriteSummary(r.Config.OutputDir, summaries); err != nil {
		return RunResult{}, err
	}
	artifactHashes := map[string]string{}
	for _, partition := range partitions {
		name := fmt.Sprintf("map_%s_%s.json", partition, spec.ID)
		hash, err := HashFile(filepath.Join(r.Config.OutputDir, name))
		if err != nil {
			return RunResult{}, err
		}
		artifactHashes[name] = hash
	}
	summaryName := "g4_profit_map_summary.json"
	artifactHashes[summaryName], err = HashFile(filepath.Join(r.Config.OutputDir, summaryName))
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(
		spec.ID, cfgHash, r.Catalog.DataHash, selection, symbols,
		boundaries, cutpoints, symbolClass, cellHash, reservoirRiskHash, artifactHashes, partitions,
	)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	manifestName := "map_manifest.json"
	artifactHashes[manifestName], err = HashFile(filepath.Join(r.Config.OutputDir, manifestName))
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		ProfileID:          r.Config.ProfileID,
		Generation:         protocol.Generation,
		ProtocolHash:       protocol.RouterProtocolHash,
		HistoryDataHash:    protocol.HistoryDataHash,
		CatalogHash:        r.Catalog.DataHash,
		CatalogSymbolCount: len(symbols),
		SelectedFilesHash:  selectedFilesHash,
		FullMarket:         scope.FullMarket,
		MapScope:           scope.MapScope,
		ConfigHash:         cfgHash,
		BoundariesHash:     mustBoundariesHash(boundaries),
		CutpointsHash:      mustCutpointsHash(cutpoints),
		SymbolClassHash:    mustSymbolClassHash(symbolClass),
		OutputDir:          r.Config.OutputDir,
		NextSymbolIndex:    len(symbols),
		CompletedSymbols:   append([]string(nil), symbols...),
		CellHash:           cellHash,
		Accumulator:        store.Snapshot(),
		ArtifactHashes:     artifactHashes,
		Done:               true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		ProfileID:   spec.ID,
		Store:       store,
		Boundaries:  boundaries,
		Cutpoints:   cutpoints,
		SymbolClass: symbolClass,
		Symbols:     symbols,
		Summaries:   summaries,
		Manifest:    manifest,
	}, nil
}

func writeOutputs(outputDir, profileID string, scope MapScope, store *MapStore) error {
	for _, part := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		pm := store.ExportPartitionScoped(profileID, part, scope)
		if err := WritePartitionMap(outputDir, pm); err != nil {
			return err
		}
	}
	return nil
}

func mustBoundariesHash(b Boundaries) string {
	h, err := HashBoundaries(b)
	if err != nil {
		panic(err)
	}
	return h
}

func mustCutpointsHash(c sky.Cutpoints) string {
	h, err := HashCutpoints(c)
	if err != nil {
		panic(err)
	}
	return h
}

func mustSymbolClassHash(c SymbolClassCutpoints) string {
	h, err := c.Hash()
	if err != nil {
		panic(err)
	}
	return h
}
