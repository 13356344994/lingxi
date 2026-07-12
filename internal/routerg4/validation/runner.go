package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

// Runner orchestrates validation screening with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, symbol string)
	OnCandidateScan  func(scanned, total int)
	CatalogValidator func(historydata.HistoryCatalog) error
	LoadBars         func(path string, maxLines int) ([]profitmap.Bar, error)
	ComputeFeatures  func(
		spec profitmap.ProfileSpec,
		symbolBars []profitmap.Bar,
		btcBars []profitmap.Bar,
		decisionTime time.Time,
		optional map[string]float64,
	) (map[string]float64, error)
}

type profileContext struct {
	ProfileID     string
	Spec          profitmap.ProfileSpec
	Manifest      profitmap.MapManifest
	Boundaries    profitmap.Boundaries
	Cutpoints     sky.Cutpoints
	SymbolClass   profitmap.SymbolClassCutpoints
	ClassLabels   map[string]string
	SkySeries     sky.Series
	BTCBars       []profitmap.Bar
	Symbols       []string
	CandidateKeys []profitmap.CellKey
}

// Run executes bounded validation screening for frozen candidates.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("validation: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	candidateBundle, err := LoadCandidateBundle(r.Config.CandidateOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if candidateBundle.Manifest.ManifestHash != r.Config.CandidateManifestHash {
		return RunResult{}, fmt.Errorf("validation: candidate manifest hash mismatch")
	}
	if err := VerifyCandidateFrozen(candidateBundle.Manifest, candidateBundle.Candidates); err != nil {
		return RunResult{}, err
	}

	bundles := make([]MapBundle, 0, len(r.Config.MapDirs))
	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	for _, dir := range r.Config.MapDirs {
		bundle, err := LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		bundles = append(bundles, bundle)
		mapManifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
		for name, hash := range bundle.Manifest.ArtifactHashes {
			mapArtifactHashes[bundle.Manifest.ProfileID+"/"+name] = hash
		}
	}
	if err := VerifyInputBinding(r.Catalog, r.Config.HistoryRoot, bundles, candidateBundle, r.Config.MaxFiles, r.Config.MaxLines); err != nil {
		return RunResult{}, err
	}

	index := BuildCandidateIndex(candidateBundle.Candidates)
	profiles, err := r.buildProfileContexts(bundles, index)
	if err != nil {
		return RunResult{}, err
	}
	accumulators, err := buildCandidateAccumulators(candidateBundle.Candidates, profiles)
	if err != nil {
		return RunResult{}, err
	}

	startByProfile := map[string]int{}
	var linesProcessed int64
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(
			cp, r.Config, r.Catalog.DataHash,
			candidateBundle.Manifest.CandidatesHash, candidateBundle.Manifest.LedgerHash,
			mapManifestHashes,
		); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted(candidateBundle, mapManifestHashes, mapArtifactHashes)
		}
		startByProfile = cp.NextSymbolIndexByProfile
		linesProcessed = cp.LinesProcessed
		if len(cp.CandidateStats) == len(accumulators) {
			accumulators = restoreAccumulatorsFromCheckpoint(cp.CandidateStats)
		}
	}

	canonicalDataHashes := map[string]string{
		"catalog_hash":            r.Catalog.DataHash,
		"candidates_hash":         candidateBundle.Manifest.CandidatesHash,
		"ledger_hash":             candidateBundle.Manifest.LedgerHash,
		"candidate_manifest_hash": candidateBundle.Manifest.ManifestHash,
	}

	for _, profile := range profiles {
		start := startByProfile[profile.ProfileID]
		for i := start; i < len(profile.Symbols); i++ {
			select {
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			default:
			}
			symbol := profile.Symbols[i]
			if err := r.scanSymbol(ctx, profile, symbol, index, candidateBundle.Candidates, accumulators, &linesProcessed); err != nil {
				return RunResult{}, err
			}
			startByProfile[profile.ProfileID] = i + 1
			if err := r.saveProgressCheckpoint(
				startByProfile, accumulators, linesProcessed, false,
				candidateBundle, mapManifestHashes,
			); err != nil {
				return RunResult{}, err
			}
		}
	}

	results := BuildCandidateResults(candidateBundle.Candidates, accumulators)
	report, err := BuildReport(r.Config, candidateBundle, mapManifestHashes, results)
	if err != nil {
		return RunResult{}, err
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(r.Config, candidateBundle, mapManifestHashes, mapArtifactHashes, canonicalDataHashes, report, survivors)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, report, survivors, receipt); err != nil {
		return RunResult{}, err
	}
	if err := r.saveProgressCheckpoint(startByProfile, accumulators, linesProcessed, true, candidateBundle, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}

func buildCandidateAccumulators(
	candidates []reversemine.Candidate,
	profiles []profileContext,
) ([]*firingAccumulator, error) {
	byProfile := make(map[string]profileContext, len(profiles))
	for _, profile := range profiles {
		byProfile[profile.ProfileID] = profile
	}
	out := make([]*firingAccumulator, len(candidates))
	for i, candidate := range candidates {
		profile, ok := byProfile[candidate.ProfileID]
		if !ok {
			return nil, fmt.Errorf("validation: candidate profile %q missing", candidate.ProfileID)
		}
		var horizon time.Duration
		found := false
		for _, spec := range profile.Spec.Horizons {
			if spec.Label == candidate.CellKey.Horizon {
				horizon, found = spec.Duration, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf(
				"validation: candidate horizon %q missing for profile %q",
				candidate.CellKey.Horizon, candidate.ProfileID,
			)
		}
		blocks := protocol.UTCAnchoredNonOverlapBlocks(
			profile.Boundaries.ValidationStart,
			profile.Boundaries.ValidationEnd,
			horizon,
		)
		out[i] = newFiringAccumulator(blocks)
	}
	return out, nil
}

func (r *Runner) loadBars(path string, maxLines int) ([]profitmap.Bar, error) {
	if r.LoadBars != nil {
		return r.LoadBars(path, maxLines)
	}
	return profitmap.LoadBars(path, maxLines)
}

func (r *Runner) computeFeatures(
	spec profitmap.ProfileSpec,
	symbolBars, btcBars []profitmap.Bar,
	decisionTime time.Time,
	optional map[string]float64,
) (map[string]float64, error) {
	if r.ComputeFeatures != nil {
		return r.ComputeFeatures(spec, symbolBars, btcBars, decisionTime, optional)
	}
	return reversemine.ComputeFeatures(spec, symbolBars, btcBars, decisionTime, optional)
}

func (r *Runner) loadCompleted(
	candidateBundle CandidateBundle,
	mapManifestHashes, mapArtifactHashes map[string]string,
) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_validation_report.json"))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "validation_survivors.json"))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "validation_receipt.json"))
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
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	if receipt.ReportHash != report.ReportHash ||
		receipt.SurvivorsHash != survivors.SurvivorsHash ||
		survivors.ReportHash != report.ReportHash ||
		report.CandidateManifestHash != candidateBundle.Manifest.ManifestHash ||
		report.CandidatesHash != candidateBundle.Manifest.CandidatesHash ||
		report.LedgerHash != candidateBundle.Manifest.LedgerHash ||
		receipt.CandidateManifestHash != candidateBundle.Manifest.ManifestHash ||
		receipt.CandidatesHash != candidateBundle.Manifest.CandidatesHash ||
		receipt.LedgerHash != candidateBundle.Manifest.LedgerHash ||
		receipt.CatalogHash != r.Catalog.DataHash ||
		receipt.ConfigHash != cfgHash ||
		receipt.CanonicalDataHashes["catalog_hash"] != r.Catalog.DataHash ||
		receipt.CanonicalDataHashes["candidate_manifest_hash"] != candidateBundle.Manifest.ManifestHash ||
		receipt.CanonicalDataHashes["candidates_hash"] != candidateBundle.Manifest.CandidatesHash ||
		receipt.CanonicalDataHashes["ledger_hash"] != candidateBundle.Manifest.LedgerHash ||
		!sameStringMap(report.MapManifestHashes, mapManifestHashes) ||
		!sameStringMap(receipt.MapManifestHashes, mapManifestHashes) ||
		!sameStringMap(receipt.MapArtifactHashes, mapArtifactHashes) {
		return RunResult{}, fmt.Errorf("validation: completed receipt/report binding mismatch")
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func (r *Runner) buildProfileContexts(bundles []MapBundle, index CandidateIndex) ([]profileContext, error) {
	profileIDs := make([]string, 0, len(bundles))
	byID := map[string]MapBundle{}
	for _, b := range bundles {
		byID[b.Manifest.ProfileID] = b
		profileIDs = append(profileIDs, b.Manifest.ProfileID)
	}
	sort.Strings(profileIDs)
	var out []profileContext
	for _, profileID := range profileIDs {
		bundle := byID[profileID]
		spec, err := profitmap.ProfileSpecFor(profileID)
		if err != nil {
			return nil, err
		}
		btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
		btcBars, err := r.loadBars(profitmap.CatalogFilePath(r.Config.HistoryRoot, btcRel), r.Config.MaxLines)
		if err != nil {
			return nil, err
		}
		boundaries, err := profitmap.ComputeBoundaries(spec, btcBars)
		if err != nil {
			return nil, err
		}
		bHash, err := profitmap.HashBoundaries(boundaries)
		if err != nil {
			return nil, err
		}
		if bHash != bundle.Manifest.BoundariesHash {
			return nil, fmt.Errorf("validation: boundaries hash mismatch for %s", profileID)
		}
		cutpoints, err := profitmap.FitSkyCutpoints(r.Catalog, r.Config.HistoryRoot, boundaries, r.Config.MaxLines)
		if err != nil {
			return nil, err
		}
		cHash, err := profitmap.HashCutpoints(cutpoints)
		if err != nil {
			return nil, err
		}
		if cHash != bundle.Manifest.CutpointsHash {
			return nil, fmt.Errorf("validation: cutpoints hash mismatch for %s", profileID)
		}
		selection, err := profitmap.SelectPrimaryFiles(r.Catalog, profileID, r.Config.MaxFiles, bundle.Manifest.CatalogSymbolCount)
		if err != nil {
			return nil, err
		}
		symbols := make([]string, len(selection.Files))
		for i := range selection.Files {
			symbols[i] = selection.Files[i].Symbol
		}
		symbolClass, classLabels, scHash, err := fitSymbolClasses(
			r.Catalog, r.Config.HistoryRoot, spec, boundaries, symbols, r.Config.MaxLines,
			r.loadBars, map[string][]profitmap.Bar{"BTC-USDT-SWAP": btcBars},
		)
		if err != nil {
			return nil, err
		}
		if scHash != bundle.Manifest.SymbolClassHash {
			return nil, fmt.Errorf("validation: symbol class hash mismatch for %s", profileID)
		}
		skySeries, err := profitmap.BuildBTCSkySeries(r.Catalog, r.Config.HistoryRoot, r.Config.MaxLines)
		if err != nil {
			return nil, err
		}
		out = append(out, profileContext{
			ProfileID:     profileID,
			Spec:          spec,
			Manifest:      bundle.Manifest,
			Boundaries:    boundaries,
			Cutpoints:     cutpoints,
			SymbolClass:   symbolClass,
			ClassLabels:   classLabels,
			SkySeries:     skySeries,
			BTCBars:       btcBars,
			Symbols:       symbols,
			CandidateKeys: index.KeysForProfile(profileID),
		})
	}
	return out, nil
}

func fitSymbolClasses(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	spec profitmap.ProfileSpec,
	boundaries profitmap.Boundaries,
	symbols []string,
	maxLines int,
	loadBars func(string, int) ([]profitmap.Bar, error),
	cached map[string][]profitmap.Bar,
) (profitmap.SymbolClassCutpoints, map[string]string, string, error) {
	var volumes []float64
	labels := map[string]string{}
	for _, symbol := range symbols {
		rel := spec.PrimaryFamily + "/" + symbol + ".jsonl"
		if err := profitmap.VerifyCatalogFile(catalog, historyRoot, rel); err != nil {
			return profitmap.SymbolClassCutpoints{}, nil, "", err
		}
		bars, ok := cached[symbol]
		if !ok {
			var err error
			bars, err = loadBars(profitmap.CatalogFilePath(historyRoot, rel), maxLines)
			if err != nil {
				return profitmap.SymbolClassCutpoints{}, nil, "", err
			}
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

func (r *Runner) scanSymbol(
	ctx context.Context,
	profile profileContext,
	symbol string,
	index CandidateIndex,
	candidates []reversemine.Candidate,
	accumulators []*firingAccumulator,
	linesProcessed *int64,
) error {
	_ = ctx
	rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
	if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, rel); err != nil {
		return err
	}
	bars := profile.BTCBars
	var err error
	if symbol != "BTC-USDT-SWAP" {
		bars, err = r.loadBars(profitmap.CatalogFilePath(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return err
		}
	}
	var funding []profitmap.FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := profitmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
		if err := profitmap.VerifyCatalogFile(r.Catalog, r.Config.HistoryRoot, fundingRel); err != nil {
			return err
		}
		funding, err = profitmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
		if err != nil {
			return fmt.Errorf("validation: parse funding %s: %w", symbol, err)
		}
	}
	var optional optionalSeries
	if profile.ProfileID == profitmap.ProfileOKX1H {
		optional, err = loadOptionalSeries(r.Catalog, r.Config.HistoryRoot, symbol, r.Config.MaxLines)
		if err != nil {
			return err
		}
	}
	keySet := map[string]bool{}
	for _, key := range profile.CandidateKeys {
		keySet[cellKeyString(key)] = true
	}
	scanCfg := ScanConfig{
		Context:          ctx,
		Spec:             profile.Spec,
		Boundaries:       profile.Boundaries,
		SkyCutpoints:     profile.Cutpoints,
		SkySeries:        profile.SkySeries,
		SymbolClass:      profile.SymbolClass,
		SymbolClassLabel: profile.ClassLabels[symbol],
		Funding:          funding,
		MaxLines:         r.Config.MaxLines,
		ProgressEvery:    r.Config.ProgressEvery,
		OnProgress: func(lines int) {
			if r.OnProgress != nil {
				r.OnProgress(*linesProcessed+int64(lines), symbol)
			}
		},
	}
	var lastDecision time.Time
	var lastFeatures map[string]float64
	if err := StreamValidationSymbol(bars, symbol, scanCfg, func(ev EventContext) error {
		ev.ProfileID = profile.ProfileID
		if !keySet[cellKeyString(ev.CellKey)] {
			return nil
		}
		bucket := index.Lookup(profile.ProfileID, ev.CellKey)
		if len(bucket) == 0 {
			return nil
		}
		if !ev.DecisionTime.Equal(lastDecision) {
			features, err := r.computeFeatures(
				profile.Spec, bars, profile.BTCBars, ev.DecisionTime,
				optional.featuresAt(ev.DecisionTime),
			)
			if err != nil {
				lastDecision = ev.DecisionTime
				lastFeatures = nil
				return nil
			}
			if err := reversemine.RejectForbiddenEventFeatures(features); err != nil {
				return err
			}
			lastDecision = ev.DecisionTime
			lastFeatures = features
		}
		if lastFeatures == nil {
			return nil
		}
		ev.Features = lastFeatures
		event := reversemine.ValidationEvent{
			ProfileID: profile.ProfileID,
			CellKey:   ev.CellKey,
			Features:  lastFeatures,
			Decision:  ev.DecisionTime,
		}
		for _, candIdx := range bucket {
			candidate := candidates[candIdx]
			if candidate.CellKey != ev.CellKey {
				continue
			}
			fires, _ := candidate.Fires(event)
			if !fires {
				continue
			}
			sampleID := symbol + "|" + ev.DecisionTime.UTC().Format(time.RFC3339Nano) +
				"|" + cellKeyString(ev.CellKey) + "|" + candidate.CanonicalHash
			accumulators[candIdx].add(sampleFromEvent(ev), sampleID)
			if r.OnCandidateScan != nil {
				r.OnCandidateScan(candIdx, len(candidates))
			}
		}
		return nil
	}); err != nil {
		return err
	}
	*linesProcessed += int64(len(bars))
	return nil
}

func (r *Runner) saveProgressCheckpoint(
	nextByProfile map[string]int,
	accumulators []*firingAccumulator,
	linesProcessed int64,
	done bool,
	candidateBundle CandidateBundle,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation:               protocol.Generation,
		ProtocolHash:             protocol.RouterProtocolHash,
		HistoryDataHash:          protocol.HistoryDataHash,
		ConfigHash:               cfgHash,
		CatalogHash:              r.Catalog.DataHash,
		CandidateManifestHash:    candidateBundle.Manifest.ManifestHash,
		CandidatesHash:           candidateBundle.Manifest.CandidatesHash,
		LedgerHash:               candidateBundle.Manifest.LedgerHash,
		MapManifestHashes:        cloneStringMap(mapManifestHashes),
		OutputDir:                r.Config.OutputDir,
		NextSymbolIndexByProfile: nextByProfile,
		CandidateStats:           exportCheckpointCandidateStats(accumulators),
		LinesProcessed:           linesProcessed,
		Done:                     done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

// HashReportFile returns sha256 of on-disk report for tamper checks.
func HashReportFile(path string) (string, error) {
	return profitmap.HashFile(path)
}

// DefaultRunConfig builds config with protocol bindings.
func DefaultRunConfig(historyRoot, candidateDir, outputDir string, mapDirs []string, maxFiles, maxLines int, manifestHash string) RunConfig {
	return RunConfig{
		SchemaVersion:         SchemaVersionConfig,
		HistoryRoot:           historyRoot,
		CandidateOutputDir:    candidateDir,
		OutputDir:             outputDir,
		MapDirs:               append([]string(nil), mapDirs...),
		MaxFiles:              maxFiles,
		MaxLines:              maxLines,
		ProgressEvery:         protocol.ScanProgressEveryLines,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		CandidateManifestHash: manifestHash,
	}
}
