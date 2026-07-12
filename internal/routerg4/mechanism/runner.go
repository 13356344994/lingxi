package mechanism

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
	"lingxi/internal/routerg4/validation"
)

// Runner orchestrates mechanism attribution and strict walk-forward with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, symbol string)
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
	ExplainedKeys []profitmap.CellKey
}

// Run executes bounded mechanism attribution and strict WF for validation survivors.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("mechanism: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	validationBundle, err := LoadValidationBundle(r.Config.ValidationOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if validationBundle.Report.ReportHash != r.Config.ValidationReportHash {
		return RunResult{}, fmt.Errorf("mechanism: validation report hash mismatch")
	}
	candidateBundle, err := validation.LoadCandidateBundle(r.Config.CandidateOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if candidateBundle.Manifest.ManifestHash != r.Config.CandidateManifestHash {
		return RunResult{}, fmt.Errorf("mechanism: candidate manifest hash mismatch")
	}

	bundles := make([]validation.MapBundle, 0, len(r.Config.MapDirs))
	mapManifestHashes := map[string]string{}
	for _, dir := range r.Config.MapDirs {
		bundle, err := validation.LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		bundles = append(bundles, bundle)
		mapManifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
	}
	input := InputBundle{
		Validation: validationBundle,
		Candidates: candidateBundle,
		Maps:       bundles,
	}
	if err := VerifyInputBinding(r.Catalog, r.Config.HistoryRoot, input, r.Config.MaxFiles, r.Config.MaxLines); err != nil {
		return RunResult{}, err
	}

	decisions, err := BuildAttributionDecisions(validationBundle.Survivors.Survivors, candidateBundle.Candidates)
	if err != nil {
		return RunResult{}, err
	}
	items, err := BuildSurvivorWorkItems(validationBundle.Survivors.Survivors, candidateBundle.Candidates, decisions)
	if err != nil {
		return RunResult{}, err
	}

	mechanismReport, err := BuildMechanismReport(r.Config, validationBundle.Report, validationBundle.Survivors, decisions)
	if err != nil {
		return RunResult{}, err
	}

	explainedItems := filterExplained(items)
	accumulators := map[string]*wfAccumulator{}
	for _, item := range explainedItems {
		horizon, err := time.ParseDuration(item.Survivor.CellKey.Horizon)
		if err != nil || horizon <= 0 {
			return RunResult{}, fmt.Errorf(
				"mechanism: invalid candidate horizon %q", item.Survivor.CellKey.Horizon,
			)
		}
		accumulators[item.Survivor.CanonicalHash] = newWFAccumulator(horizon)
	}

	var wfResults []WFResult
	if len(explainedItems) == 0 {
		wfResults = []WFResult{}
	} else {
		profiles, err := r.buildProfileContexts(bundles, explainedItems)
		if err != nil {
			return RunResult{}, err
		}
		startByProfile := map[string]int{}
		var linesProcessed int64
		if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
			return RunResult{}, err
		} else if ok {
			if err := VerifyCheckpointBinding(
				cp, r.Config, r.Catalog.DataHash, validationBundle,
				candidateBundle.Manifest.CandidatesHash, candidateBundle.Manifest.LedgerHash,
				mapManifestHashes,
			); err != nil {
				return RunResult{}, err
			}
			if cp.Done {
				return r.loadCompleted(input, mechanismReport)
			}
			startByProfile = cp.NextSymbolIndexByProfile
			linesProcessed = cp.LinesProcessed
			accumulators = restoreAccumulatorsFromCheckpoint(cp.WFStatsByHash)
			for _, item := range explainedItems {
				if accumulators[item.Survivor.CanonicalHash] == nil {
					horizon, err := time.ParseDuration(item.Survivor.CellKey.Horizon)
					if err != nil || horizon <= 0 {
						return RunResult{}, fmt.Errorf(
							"mechanism: invalid candidate horizon %q", item.Survivor.CellKey.Horizon,
						)
					}
					accumulators[item.Survivor.CanonicalHash] = newWFAccumulator(horizon)
				}
			}
		}

		explainedByHash := map[string]SurvivorWorkItem{}
		for _, item := range explainedItems {
			explainedByHash[item.Survivor.CanonicalHash] = item
		}
		candidateIndex := validation.BuildCandidateIndex(candidateBundle.Candidates)

		for _, profile := range profiles {
			start := startByProfile[profile.ProfileID]
			for i := start; i < len(profile.Symbols); i++ {
				select {
				case <-ctx.Done():
					return RunResult{}, ctx.Err()
				default:
				}
				symbol := profile.Symbols[i]
				if err := r.scanSymbol(ctx, profile, symbol, explainedByHash, candidateIndex, candidateBundle.Candidates, accumulators, &linesProcessed); err != nil {
					return RunResult{}, err
				}
				startByProfile[profile.ProfileID] = i + 1
				if err := r.saveProgressCheckpoint(
					startByProfile, accumulators, linesProcessed, false,
					validationBundle, candidateBundle, mapManifestHashes,
				); err != nil {
					return RunResult{}, err
				}
			}
		}
		wfResults = BuildWFResults(explainedItems, accumulators)
		if err := r.saveProgressCheckpoint(
			startByProfile, accumulators, linesProcessed, true,
			validationBundle, candidateBundle, mapManifestHashes,
		); err != nil {
			return RunResult{}, err
		}
	}

	wfReport, err := BuildStrictWFReport(mechanismReport, wfResults)
	if err != nil {
		return RunResult{}, err
	}
	finalLeaves, err := BuildFinalLeaves(mechanismReport, wfReport, items)
	if err != nil {
		return RunResult{}, err
	}
	stageSummary := BuildStageSummary(validationBundle, mechanismReport, wfReport, finalLeaves)
	receipt, err := BuildReceipt(r.Config, input, mechanismReport, wfReport, finalLeaves, stageSummary)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, mechanismReport, wfReport, finalLeaves, stageSummary, receipt); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		MechanismReport: mechanismReport,
		StrictWFReport:  wfReport,
		FinalLeaves:     finalLeaves,
		StageSummary:    stageSummary,
		Receipt:         receipt,
	}, nil
}

func filterExplained(items []SurvivorWorkItem) []SurvivorWorkItem {
	out := make([]SurvivorWorkItem, 0)
	for _, item := range items {
		if item.Decision.AttributionStatus == protocol.AttributionExplained {
			out = append(out, item)
		}
	}
	return out
}

func (r *Runner) loadCompleted(input InputBundle, mechanismReport MechanismReport) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_strict_wf_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var wfReport StrictWFReport
	if err := json.Unmarshal(raw, &wfReport); err != nil {
		return RunResult{}, err
	}
	want, err := hashStrictWFReport(wfReport)
	if err != nil {
		return RunResult{}, err
	}
	if wfReport.ReportHash != want {
		return RunResult{}, fmt.Errorf("mechanism: strict wf report hash mismatch")
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_final_leaves.json"))
	if err != nil {
		return RunResult{}, err
	}
	var finalLeaves FinalLeaves
	if err := json.Unmarshal(raw, &finalLeaves); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_stage_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	var stageSummary StageSummary
	if err := json.Unmarshal(raw, &stageSummary); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g4_mechanism_receipt.json"))
	if err != nil {
		return RunResult{}, err
	}
	var receipt MechanismReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	if err := ValidateReceipt(receipt); err != nil {
		return RunResult{}, err
	}
	if err := ValidateCompletedOutputs(r.Config.OutputDir, receipt); err != nil {
		return RunResult{}, err
	}
	if mechanismReport.ReportHash != receipt.MechanismReportHash {
		return RunResult{}, fmt.Errorf("mechanism: completed mechanism report binding mismatch")
	}
	_ = input
	return RunResult{
		MechanismReport: mechanismReport,
		StrictWFReport:  wfReport,
		FinalLeaves:     finalLeaves,
		StageSummary:    stageSummary,
		Receipt:         receipt,
	}, nil
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

func (r *Runner) buildProfileContexts(bundles []validation.MapBundle, explained []SurvivorWorkItem) ([]profileContext, error) {
	keysByProfile := map[string]map[string]bool{}
	for _, item := range explained {
		if keysByProfile[item.Survivor.ProfileID] == nil {
			keysByProfile[item.Survivor.ProfileID] = map[string]bool{}
		}
		keysByProfile[item.Survivor.ProfileID][cellKeyString(item.Survivor.CellKey)] = true
	}
	profileIDs := make([]string, 0, len(bundles))
	byID := map[string]validation.MapBundle{}
	for _, b := range bundles {
		byID[b.Manifest.ProfileID] = b
		profileIDs = append(profileIDs, b.Manifest.ProfileID)
	}
	sort.Strings(profileIDs)
	var out []profileContext
	for _, profileID := range profileIDs {
		keySet := keysByProfile[profileID]
		if len(keySet) == 0 {
			continue
		}
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
			return nil, fmt.Errorf("mechanism: boundaries hash mismatch for %s", profileID)
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
			return nil, fmt.Errorf("mechanism: cutpoints hash mismatch for %s", profileID)
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
			return nil, fmt.Errorf("mechanism: symbol class hash mismatch for %s", profileID)
		}
		skySeries, err := profitmap.BuildBTCSkySeries(r.Catalog, r.Config.HistoryRoot, r.Config.MaxLines)
		if err != nil {
			return nil, err
		}
		var explainedKeys []profitmap.CellKey
		for _, item := range explained {
			if item.Survivor.ProfileID == profileID {
				explainedKeys = append(explainedKeys, item.Survivor.CellKey)
			}
		}
		out = append(out, profileContext{
			ProfileID: profileID, Spec: spec, Manifest: bundle.Manifest,
			Boundaries: boundaries, Cutpoints: cutpoints, SymbolClass: symbolClass,
			ClassLabels: classLabels, SkySeries: skySeries, BTCBars: btcBars,
			Symbols: symbols, ExplainedKeys: explainedKeys,
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
	explainedByHash map[string]SurvivorWorkItem,
	index validation.CandidateIndex,
	candidates []reversemine.Candidate,
	accumulators map[string]*wfAccumulator,
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
			return fmt.Errorf("mechanism: parse funding %s: %w", symbol, err)
		}
	}
	var optional wfOptionalSeries
	if profile.ProfileID == profitmap.ProfileOKX1H {
		optional, err = loadOptionalSeries(r.Catalog, r.Config.HistoryRoot, symbol, r.Config.MaxLines)
		if err != nil {
			return err
		}
	}
	keySet := map[string]bool{}
	for _, key := range profile.ExplainedKeys {
		keySet[cellKeyString(key)] = true
	}
	scanCfg := WFScanConfig{
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
	if err := StreamWFSymbol(bars, symbol, scanCfg, func(ev validationEventContext) error {
		if !keySet[cellKeyString(ev.CellKey)] {
			return nil
		}
		bucket := index.Lookup(profile.ProfileID, ev.CellKey)
		if len(bucket) == 0 {
			return nil
		}
		for _, candIdx := range bucket {
			candidate := candidates[candIdx]
			item, ok := explainedByHash[candidate.CanonicalHash]
			if !ok || item.Decision.AttributionStatus != protocol.AttributionExplained {
				continue
			}
			if candidate.CellKey != ev.CellKey {
				continue
			}
			event := reversemine.ValidationEvent{
				ProfileID: profile.ProfileID,
				CellKey:   ev.CellKey,
				Decision:  ev.DecisionTime,
			}
			features, err := r.computeFeatures(
				profile.Spec, bars, profile.BTCBars, ev.DecisionTime,
				optional.featuresAt(ev.DecisionTime),
			)
			if err != nil {
				continue
			}
			if err := reversemine.RejectForbiddenEventFeatures(features); err != nil {
				return err
			}
			event.Features = features
			fires, _ := candidate.Fires(event)
			if !fires {
				continue
			}
			sampleID := symbol + "|" + ev.DecisionTime.UTC().Format(time.RFC3339Nano) +
				"|" + cellKeyString(ev.CellKey) + "|" + candidate.CanonicalHash
			accumulators[candidate.CanonicalHash].add(sampleFromWFEvent(ev), sampleID)
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
	accumulators map[string]*wfAccumulator,
	linesProcessed int64,
	done bool,
	validationBundle ValidationBundle,
	candidateBundle validation.CandidateBundle,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return err
	}
	statsByHash := map[string]checkpointWFStats{}
	for hash, acc := range accumulators {
		statsByHash[hash] = exportWFStats(acc)
	}
	cp := Checkpoint{
		Generation:              protocol.Generation,
		ProtocolHash:            protocol.RouterProtocolHash,
		HistoryDataHash:         protocol.HistoryDataHash,
		ConfigHash:              cfgHash,
		CatalogHash:             r.Catalog.DataHash,
		ValidationReportHash:    validationBundle.Report.ReportHash,
		ValidationSurvivorsHash: validationBundle.Survivors.SurvivorsHash,
		ValidationReceiptHash:   validationBundle.Receipt.ReceiptHash,
		CandidateManifestHash:   candidateBundle.Manifest.ManifestHash,
		CandidatesHash:          candidateBundle.Manifest.CandidatesHash,
		LedgerHash:              candidateBundle.Manifest.LedgerHash,
		MapManifestHashes:       cloneStringMap(mapManifestHashes),
		OutputDir:               r.Config.OutputDir,
		NextSymbolIndexByProfile: nextByProfile,
		WFStatsByHash:           statsByHash,
		LinesProcessed:          linesProcessed,
		Done:                    done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

type wfOptionalSeries struct {
	bybitFunding map[int64]float64
	rubikTaker   map[int64]float64
	rubikLS      map[int64]float64
}

func (o wfOptionalSeries) featuresAt(decision time.Time) map[string]float64 {
	out := map[string]float64{}
	if o.bybitFunding != nil {
		if v, ok := o.bybitFunding[decision.Unix()]; ok {
			out["okx_bybit_funding_spread"] = v
		}
	}
	if o.rubikTaker != nil {
		if v, ok := o.rubikTaker[decision.Unix()]; ok {
			out["rubik_taker_imbalance_1h"] = v
		}
	}
	if o.rubikLS != nil {
		if v, ok := o.rubikLS[decision.Unix()]; ok {
			out["rubik_lsratio_1h"] = v
		}
	}
	return out
}

func loadOptionalSeries(
	catalog historydata.HistoryCatalog,
	historyRoot, symbol string,
	maxLines int,
) (wfOptionalSeries, error) {
	_ = catalog
	_ = historyRoot
	_ = symbol
	_ = maxLines
	return wfOptionalSeries{}, nil
}
