package outcome

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// Runner orchestrates one bounded profile reward-cube scan with checkpoint resume.
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
	Store       *CubeStore
	Boundaries  profitmap.Boundaries
	Cutpoints   sky.Cutpoints
	SymbolClass profitmap.SymbolClassCutpoints
	Symbols     []string
	Summaries   []CubeSummary
	Manifest    CubeManifest
}

// Run executes the bounded reward-cube scan for one profile.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	spec, err := profitmap.ProfileSpecFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g5protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("outcome: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}
	selection, err := profitmap.SelectPrimaryFiles(r.Catalog, r.Config.ProfileID, r.Config.MaxFiles, r.Config.CatalogSymbolLimit)
	if err != nil {
		return RunResult{}, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	scope, err := ValidateSelectionScope(selection, r.Config.CatalogSymbolLimit, r.Config.AllowTruncatedSmoke)
	if err != nil {
		return RunResult{}, fmt.Errorf("outcome: selection scope: %w", err)
	}
	if len(symbols) == 0 {
		return RunResult{}, fmt.Errorf("outcome: no symbols selected for profile %q", r.Config.ProfileID)
	}
	btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, btcRel); err != nil {
		return RunResult{}, err
	}
	btcBars, err := profitmap.LoadBars(filepath.Join(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	boundaries, err := profitmap.ComputeBoundaries(spec, btcBars)
	if err != nil {
		return RunResult{}, err
	}
	bHash, err := profitmap.HashBoundaries(boundaries)
	if err != nil {
		return RunResult{}, err
	}
	cutpoints, err := profitmap.FitSkyCutpoints(r.Catalog, r.Config.HistoryRoot, boundaries, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	cHash, err := profitmap.HashCutpoints(cutpoints)
	if err != nil {
		return RunResult{}, err
	}
	skySeries, err := profitmap.BuildBTCSkySeries(r.Catalog, r.Config.HistoryRoot, r.Config.MaxLines)
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
	actionSpaceHash, err := ActionSpaceHashForProfile(spec)
	if err != nil {
		return RunResult{}, err
	}

	startIndex := 0
	var linesProcessed int64
	store := NewCubeStore()
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, len(symbols), selectedFilesHash, actionSpaceHash, scope, bHash, cHash, scHash); err != nil {
			return RunResult{}, err
		}
		store = RestoreCubeStore(cp.Accumulator)
		restoredHash, err := store.Hash()
		if err != nil || restoredHash != cp.CellHash {
			return RunResult{}, fmt.Errorf("outcome: checkpoint accumulator hash mismatch")
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

	if err := r.scanSymbolsFrom(ctx, spec, symbols, startIndex, boundaries, cutpoints, skySeries, symbolClass, classLabels, store, &linesProcessed, cfgHash, selectedFilesHash, actionSpaceHash, scope, bHash, cHash, scHash); err != nil {
		return RunResult{}, err
	}
	return r.finalize(spec, selection, scope, symbols, boundaries, cutpoints, symbolClass, cfgHash, selectedFilesHash, actionSpaceHash, store)
}

func loadCompletedMetadata(outputDir, profileID string, scope CubeScope) (CubeManifest, []CubeSummary, error) {
	raw, err := os.ReadFile(filepath.Join(outputDir, "cube_manifest.json"))
	if err != nil {
		return CubeManifest{}, nil, err
	}
	var manifest CubeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return CubeManifest{}, nil, err
	}
	want, err := hashManifest(manifest)
	if err != nil {
		return CubeManifest{}, nil, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return CubeManifest{}, nil, fmt.Errorf("outcome: completed manifest self-hash mismatch")
	}
	if err := validateOutputScope(manifest.AvailablePrimaryFiles, manifest.SelectedPrimaryFiles, manifest.SelectionTruncated, manifest.FullMarket, manifest.CubeScope, scope); err != nil {
		return CubeManifest{}, nil, fmt.Errorf("outcome: completed manifest scope: %w", err)
	}
	raw, err = os.ReadFile(filepath.Join(outputDir, "g5_reward_cube_summary.json"))
	if err != nil {
		return CubeManifest{}, nil, err
	}
	var payload struct {
		Summaries []CubeSummary `json:"summaries"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CubeManifest{}, nil, err
	}
	if len(payload.Summaries) != 3 {
		return CubeManifest{}, nil, fmt.Errorf("outcome: completed summary partitions missing")
	}
	for _, partition := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		raw, err := os.ReadFile(filepath.Join(outputDir, fmt.Sprintf("cube_%s_%s.json", partition, profileID)))
		if err != nil {
			return CubeManifest{}, nil, err
		}
		var cube PartitionCube
		if err := json.Unmarshal(raw, &cube); err != nil {
			return CubeManifest{}, nil, err
		}
		if err := validateOutputScope(cube.AvailablePrimaryFiles, cube.SelectedPrimaryFiles, cube.SelectionTruncated, cube.FullMarket, cube.CubeScope, scope); err != nil {
			return CubeManifest{}, nil, fmt.Errorf("outcome: completed partition scope: %w", err)
		}
	}
	if manifest.TrainingLabelHash != "" {
		if _, err := os.Stat(filepath.Join(outputDir, trainingLabelsArtifact)); err != nil {
			return CubeManifest{}, nil, fmt.Errorf("outcome: completed training labels missing: %w", err)
		}
	}
	return manifest, payload.Summaries, nil
}

func (r *Runner) scanSymbolsFrom(
	ctx context.Context,
	spec profitmap.ProfileSpec,
	symbols []string,
	startIndex int,
	boundaries profitmap.Boundaries,
	cutpoints sky.Cutpoints,
	skySeries sky.Series,
	symbolClass profitmap.SymbolClassCutpoints,
	classLabels map[string]string,
	store *CubeStore,
	linesProcessed *int64,
	cfgHash, selectedFilesHash, actionSpaceHash string,
	scope CubeScope, bHash, cHash, scHash string,
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
		if (i+1)%ScanCheckpointEverySymbols != 0 && i+1 != len(symbols) {
			continue
		}
		cellHash, err := store.Hash()
		if err != nil {
			return err
		}
		cp := Checkpoint{
			ProfileID:          r.Config.ProfileID,
			Generation:         r.Config.Generation,
			ProtocolHash:       r.Config.ProtocolHash,
			HistoryDataHash:    r.Config.HistoryDataHash,
			CatalogHash:        r.Catalog.DataHash,
			CatalogSymbolCount: len(symbols),
			SelectedFilesHash:  selectedFilesHash,
			ActionSpaceHash:    actionSpaceHash,
			FullMarket:         scope.FullMarket,
			CubeScope:          scope.CubeScope,
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

func (r *Runner) scanOneSymbol(
	spec profitmap.ProfileSpec,
	symbol string,
	boundaries profitmap.Boundaries,
	cutpoints sky.Cutpoints,
	skySeries sky.Series,
	symbolClass profitmap.SymbolClassCutpoints,
	classLabels map[string]string,
	store *CubeStore,
	linesProcessed *int64,
) error {
	rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
	if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
		return err
	}
	bars, err := profitmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
	if err != nil {
		return err
	}
	var funding []profitmap.FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := profitmap.CatalogFile(r.Catalog, fundingRel); declared {
		if !stats.Empty {
			if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, fundingRel); err != nil {
				return err
			}
			funding, err = profitmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return fmt.Errorf("outcome: parse optional funding %s: %w", symbol, err)
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
		ProgressEvery:    r.Config.ProgressEvery,
		OnProgress: func(lines int) {
			if r.OnProgress != nil {
				r.OnProgress(*linesProcessed+int64(lines), symbol)
			}
		},
	}
	if err := StreamSymbol(bars, symbol, scanCfg, func(rec RewardRecord, liq, symbolClass string) error {
		store.Add(rec, liq, symbolClass)
		return nil
	}); err != nil {
		return err
	}
	*linesProcessed += int64(len(bars))
	return nil
}

func (r *Runner) fitSymbolClasses(spec profitmap.ProfileSpec, boundaries profitmap.Boundaries, symbols []string) (profitmap.SymbolClassCutpoints, map[string]string, string, error) {
	var volumes []float64
	labels := map[string]string{}
	for _, symbol := range symbols {
		rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
		if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
			return profitmap.SymbolClassCutpoints{}, nil, "", err
		}
		bars, err := profitmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return profitmap.SymbolClassCutpoints{}, nil, "", err
		}
		avg := profitmap.DiscoveryAvgQuoteVolume(bars, spec, boundaries)
		volumes = append(volumes, avg)
	}
	cutpoints := profitmap.FitSymbolClassCutpoints(volumes)
	hash, err := cutpoints.Hash()
	if err != nil {
		return profitmap.SymbolClassCutpoints{}, nil, "", err
	}
	for i, symbol := range symbols {
		if i < len(volumes) {
			labels[symbol] = cutpoints.Classify(volumes[i])
		}
	}
	return cutpoints, labels, hash, nil
}

func (r *Runner) finalize(
	spec profitmap.ProfileSpec,
	selection profitmap.Selection,
	scope CubeScope,
	symbols []string,
	boundaries profitmap.Boundaries,
	cutpoints sky.Cutpoints,
	symbolClass profitmap.SymbolClassCutpoints,
	cfgHash, selectedFilesHash, actionSpaceHash string,
	store *CubeStore,
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
	labels := store.TrainingLabels()
	labelHash, err := HashTrainingLabels(labels)
	if err != nil {
		return RunResult{}, err
	}
	labelArtifactHash, err := WriteTrainingLabels(r.Config.OutputDir, labels)
	if err != nil {
		return RunResult{}, err
	}
	partitions := []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd}
	summaries := make([]CubeSummary, 0, len(partitions))
	for _, partition := range partitions {
		summary := BuildCubeSummary(spec.ID, store.ExportPartitionScoped(spec.ID, partition, scope))
		summary.Generation = g5protocol.Generation
		summary.ProtocolHash = g5protocol.RouterProtocolHash
		summary.ExamActions = g5protocol.ExamActions
		summaries = append(summaries, summary)
	}
	if err := WriteSummary(r.Config.OutputDir, summaries); err != nil {
		return RunResult{}, err
	}
	artifactHashes := map[string]string{}
	for _, partition := range partitions {
		name := fmt.Sprintf("cube_%s_%s.json", partition, spec.ID)
		hash, err := HashFile(filepath.Join(r.Config.OutputDir, name))
		if err != nil {
			return RunResult{}, err
		}
		artifactHashes[name] = hash
	}
	summaryName := "g5_reward_cube_summary.json"
	artifactHashes[summaryName], err = HashFile(filepath.Join(r.Config.OutputDir, summaryName))
	if err != nil {
		return RunResult{}, err
	}
	artifactHashes[trainingLabelsArtifact] = labelArtifactHash
	manifest, err := BuildManifest(
		spec.ID, cfgHash, r.Catalog.DataHash, actionSpaceHash, selection, symbols,
		boundaries, cutpoints, symbolClass, cellHash, reservoirRiskHash,
		len(labels), labelHash, artifactHashes, partitions, scope,
	)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	manifestName := "cube_manifest.json"
	artifactHashes[manifestName], err = HashFile(filepath.Join(r.Config.OutputDir, manifestName))
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		ProfileID:          r.Config.ProfileID,
		Generation:         r.Config.Generation,
		ProtocolHash:       r.Config.ProtocolHash,
		HistoryDataHash:    r.Config.HistoryDataHash,
		CatalogHash:        r.Catalog.DataHash,
		CatalogSymbolCount: len(symbols),
		SelectedFilesHash:  selectedFilesHash,
		ActionSpaceHash:    actionSpaceHash,
		FullMarket:         scope.FullMarket,
		CubeScope:          scope.CubeScope,
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

func writeOutputs(outputDir, profileID string, scope CubeScope, store *CubeStore) error {
	for _, part := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		cube := store.ExportPartitionScoped(profileID, part, scope)
		if err := WritePartitionCube(outputDir, cube); err != nil {
			return err
		}
	}
	return nil
}

func mustBoundariesHash(b profitmap.Boundaries) string {
	h, err := profitmap.HashBoundaries(b)
	if err != nil {
		panic(err)
	}
	return h
}

func mustCutpointsHash(c sky.Cutpoints) string {
	h, err := profitmap.HashCutpoints(c)
	if err != nil {
		panic(err)
	}
	return h
}

func mustSymbolClassHash(c profitmap.SymbolClassCutpoints) string {
	h, err := c.Hash()
	if err != nil {
		panic(err)
	}
	return h
}
