package eventmap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/protocol"
)

// Runner orchestrates one bounded profile event-map scan with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, symbol string, events int64)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds outputs from one profile scan.
type RunResult struct {
	ProfileID  string
	Store      *MapStore
	Boundaries Boundaries
	Thresholds FrozenThresholds
	Symbols    []string
	Summaries  []DiscoverySummary
	Manifest   MapManifest
}

// Run executes the full bounded event map scan for one profile.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	spec, err := ProfileSpecFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, errf("catalog data hash mismatch")
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
		return RunResult{}, fmt.Errorf("eventmap: selection scope: %w", err)
	}
	if len(symbols) == 0 {
		return RunResult{}, errf("no symbols selected for profile %q", r.Config.ProfileID)
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
	thresholds, tHash, err := r.fitThresholds(spec, boundaries, symbols, btcBars)
	if err != nil {
		return RunResult{}, err
	}
	shockIndex := BuildShockIndex(btcBars, spec, thresholds)
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
	var eventsDetected int64
	store := NewMapStore()
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, len(symbols), selectedFilesHash, scope, bHash, tHash); err != nil {
			return RunResult{}, err
		}
		store = RestoreMapStore(cp.Accumulator)
		restoredHash, err := store.Hash()
		if err != nil || restoredHash != cp.CellHash {
			return RunResult{}, errf("checkpoint accumulator hash mismatch")
		}
		if cp.Done {
			if err := verifyArtifactHashes(r.Config.OutputDir, cp.ArtifactHashes); err != nil {
				return RunResult{}, err
			}
			manifest, summaries, err := loadCompletedMetadata(r.Config.OutputDir, spec.ID, scope)
			if err != nil {
				return RunResult{}, err
			}
			return RunResult{
				ProfileID:  spec.ID,
				Store:      store,
				Boundaries: boundaries,
				Thresholds: thresholds,
				Symbols:    symbols,
				Summaries:  summaries,
				Manifest:   manifest,
			}, nil
		}
		startIndex = cp.NextSymbolIndex
		linesProcessed = cp.LinesProcessed
		eventsDetected = cp.EventsDetected
	}

	tracker := NewWindowTracker()
	if err := r.scanSymbolsFrom(ctx, spec, symbols, startIndex, boundaries, thresholds, btcBars, shockIndex, tracker, store, &linesProcessed, &eventsDetected, cfgHash, selectedFilesHash, scope, bHash, tHash); err != nil {
		return RunResult{}, err
	}
	return r.finalize(spec, selection, scope, symbols, boundaries, thresholds, cfgHash, selectedFilesHash, store)
}

func (r *Runner) fitThresholds(spec ProfileSpec, boundaries Boundaries, symbols []string, btcBars []Bar) (FrozenThresholds, string, error) {
	symbolBars := map[string][]Bar{}
	for _, symbol := range symbols {
		rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
		if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
			return FrozenThresholds{}, "", err
		}
		bars, err := LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return FrozenThresholds{}, "", err
		}
		symbolBars[symbol] = bars
	}
	th, err := FitThresholds(spec, boundaries, symbolBars, btcBars)
	if err != nil {
		return FrozenThresholds{}, "", err
	}
	hash, err := HashThresholds(th)
	if err != nil {
		return FrozenThresholds{}, "", err
	}
	return th, hash, nil
}

func (r *Runner) scanSymbolsFrom(
	ctx context.Context,
	spec ProfileSpec,
	symbols []string,
	startIndex int,
	boundaries Boundaries,
	thresholds FrozenThresholds,
	btcBars []Bar,
	shockIndex ShockIndex,
	tracker *WindowTracker,
	store *MapStore,
	linesProcessed, eventsDetected *int64,
	cfgHash, selectedFilesHash string,
	scope MapScope,
	bHash, tHash string,
) error {
	for i := startIndex; i < len(symbols); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		symbol := symbols[i]
		n, err := r.scanOneSymbol(spec, symbol, boundaries, thresholds, btcBars, shockIndex, tracker, store, linesProcessed)
		if err != nil {
			return err
		}
		*eventsDetected += int64(n)
		if r.OnProgress != nil {
			r.OnProgress(*linesProcessed, symbol, *eventsDetected)
		}
		if (i+1)%scanCheckpointEverySymbols != 0 && i+1 != len(symbols) {
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
			ThresholdsHash:     tHash,
			OutputDir:          r.Config.OutputDir,
			NextSymbolIndex:    i + 1,
			CompletedSymbols:   append([]string(nil), symbols[:i+1]...),
			CellHash:           cellHash,
			Accumulator:        store.Snapshot(),
			EventsDetected:     *eventsDetected,
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
	spec ProfileSpec,
	symbol string,
	boundaries Boundaries,
	thresholds FrozenThresholds,
	btcBars []Bar,
	shockIndex ShockIndex,
	tracker *WindowTracker,
	store *MapStore,
	linesProcessed *int64,
) (int, error) {
	rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
	if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
		return 0, err
	}
	bars, err := LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
	if err != nil {
		return 0, err
	}
	var funding []profitmap.FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := CatalogFile(r.Catalog, fundingRel); declared {
		if !stats.Empty {
			if err := VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, fundingRel); err != nil {
				return 0, err
			}
			funding, err = LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return 0, fmt.Errorf("parse optional funding %s: %w", symbol, err)
			}
		}
	}
	optional, err := LoadOptionalContext(r.Config.HistoryRoot, symbol, spec.ID, r.Config.MaxLines)
	if err != nil {
		return 0, err
	}
	scanCfg := ScanSymbolConfig{
		Spec:            spec,
		Boundaries:      boundaries,
		Thresholds:      thresholds,
		BTCBars:         btcBars,
		ShockClusterAt:  shockIndex.Lookup,
		Funding:         funding,
		Optional:        optional.Values,
		OptionalMissing: optional.Missing,
		MaxLines:        r.Config.MaxLines,
		ProgressEvery:   r.Config.ProgressEvery,
		WindowBars:      RollingWindowBars(spec),
		OnProgress: func(lines int) {
			if r.OnProgress != nil {
				r.OnProgress(*linesProcessed+int64(lines), symbol, 0)
			}
		},
	}
	n, err := StreamSymbol(bars, symbol, scanCfg, tracker, func(out EventOutcome) error {
		store.Add(out)
		return nil
	})
	if err != nil {
		return 0, err
	}
	*linesProcessed += int64(len(bars))
	return n, nil
}

func (r *Runner) finalize(
	spec ProfileSpec,
	selection profitmap.Selection,
	scope MapScope,
	symbols []string,
	boundaries Boundaries,
	thresholds FrozenThresholds,
	cfgHash, selectedFilesHash string,
	store *MapStore,
) (RunResult, error) {
	if err := os.MkdirAll(r.Config.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}
	if err := WriteThresholds(r.Config.OutputDir, thresholds); err != nil {
		return RunResult{}, err
	}
	partitions := []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd}
	artifactHashes := map[string]string{}
	for _, partition := range partitions {
		pm := store.ExportPartitionScoped(spec.ID, partition, scope)
		if err := WritePartitionMap(r.Config.OutputDir, pm); err != nil {
			return RunResult{}, err
		}
		name := fmt.Sprintf("event_map_%s_%s.json", partition, spec.ID)
		hash, err := HashFile(filepath.Join(r.Config.OutputDir, name))
		if err != nil {
			return RunResult{}, err
		}
		artifactHashes[name] = hash
	}
	thHash, err := HashFile(filepath.Join(r.Config.OutputDir, "event_map_thresholds.json"))
	if err != nil {
		return RunResult{}, err
	}
	artifactHashes["event_map_thresholds.json"] = thHash

	var summaries []DiscoverySummary
	for _, partition := range partitions {
		pm := store.ExportPartitionScoped(spec.ID, partition, scope)
		summaries = append(summaries, BuildDiscoverySummary(spec.ID, pm))
	}
	if err := WriteSummary(r.Config.OutputDir, summaries); err != nil {
		return RunResult{}, err
	}
	sumHash, err := HashFile(filepath.Join(r.Config.OutputDir, "g6_event_map_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	artifactHashes["g6_event_map_summary.json"] = sumHash

	cellHash, err := store.Hash()
	if err != nil {
		return RunResult{}, err
	}
	reservoirHash, err := store.ReservoirRiskHash()
	if err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(spec.ID, cfgHash, r.Catalog.DataHash, selection, symbols, boundaries, thresholds, cellHash, reservoirHash, artifactHashes, partitions)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	manifestHash, err := HashFile(filepath.Join(r.Config.OutputDir, "event_map_manifest.json"))
	if err != nil {
		return RunResult{}, err
	}
	artifactHashes["event_map_manifest.json"] = manifestHash

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
		ThresholdsHash:     mustThresholdsHash(thresholds),
		OutputDir:          r.Config.OutputDir,
		NextSymbolIndex:    len(symbols),
		CompletedSymbols:   append([]string(nil), symbols...),
		CellHash:           cellHash,
		Accumulator:        store.Snapshot(),
		ArtifactHashes:     artifactHashes,
		LinesProcessed:     0,
		Done:               true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		ProfileID:  spec.ID,
		Store:      store,
		Boundaries: boundaries,
		Thresholds: thresholds,
		Symbols:    symbols,
		Summaries:  summaries,
		Manifest:   manifest,
	}, nil
}

func loadCompletedMetadata(outputDir, profileID string, scope MapScope) (MapManifest, []DiscoverySummary, error) {
	raw, err := os.ReadFile(filepath.Join(outputDir, "event_map_manifest.json"))
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
		return MapManifest{}, nil, errf("completed manifest self-hash mismatch")
	}
	raw, err = os.ReadFile(filepath.Join(outputDir, "g6_event_map_summary.json"))
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
		return MapManifest{}, nil, errf("completed summary partitions missing")
	}
	for _, partition := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		name := fmt.Sprintf("event_map_%s_%s.json", partition, profileID)
		if _, err := os.ReadFile(filepath.Join(outputDir, name)); err != nil {
			return MapManifest{}, nil, err
		}
	}
	_ = scope
	return manifest, payload.Summaries, nil
}

func verifyArtifactHashes(outputDir string, hashes map[string]string) error {
	for name, want := range hashes {
		got, err := HashFile(filepath.Join(outputDir, name))
		if err != nil {
			return err
		}
		if got != want {
			return errf("artifact hash mismatch for %s", name)
		}
	}
	return nil
}

func mustBoundariesHash(b Boundaries) string {
	hash, err := HashBoundaries(b)
	if err != nil {
		panic(err)
	}
	return hash
}

func mustThresholdsHash(th FrozenThresholds) string {
	hash, err := HashThresholds(th)
	if err != nil {
		panic(err)
	}
	return hash
}

// ApplyLowCPU configures GOMAXPROCS=1 when requested.
func ApplyLowCPU(enabled bool) {
	if !enabled {
		return
	}
	// runtime.GOMAXPROCS is set from main to avoid import cycle in tests.
}
