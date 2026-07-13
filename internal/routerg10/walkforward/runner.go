package walkforward

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	g10validation "lingxi/internal/routerg10/validation"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

// Runner orchestrates bounded G10 playbook walk-forward replay.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	Fingerprint      g10validation.FingerprintBinding
	OnProgress       func(survivorIndex int, playbookHash, symbol string)
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

// Run executes walk-forward folds for validation survivors and writes artifacts.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != protocol.HistoryDataHash {
		return RunResult{}, fmt.Errorf("walkforward: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	validationReport, survivors, valErr := VerifyValidationInputs(r.Config.ValidationDir, r.Config.PlaybookManifestHash)
	cfg := r.Config
	cfg.ValidationReportHash = validationReport.ReportHash
	cfg.SurvivorsHash = survivors.SurvivorsHash
	if valErr != nil {
		reason := ReasonValidationNotPass
		if validationReport.ReportHash == "" {
			reason = ReasonValidationTamper
		}
		wf, summary, mechanism, execution, skipErr := BuildSkippedReport(cfg, reason, validationReport, survivors)
		if skipErr != nil {
			return RunResult{}, skipErr
		}
		receipt, recErr := BuildStageReceipt(cfg, r.Config.PlaybookManifestHash, validationReport.PlaybookHash, validationReport.ExperimentHash, nil, wf, mechanism, execution, map[string]string{
			"catalog_hash": r.Catalog.DataHash,
		})
		if recErr != nil {
			return RunResult{}, recErr
		}
		if writeErr := WriteOutputs(r.Config.OutputDir, wf, summary, mechanism, execution, receipt); writeErr != nil {
			return RunResult{}, writeErr
		}
		return RunResult{WFReport: wf, Summary: summary, MechanismReport: mechanism, ExecutionReport: execution, Receipt: receipt, Validation: validationReport, Survivors: survivors}, nil
	}
	if len(survivors.Survivors) == 0 {
		wf, summary, mechanism, execution, err := BuildSkippedReport(cfg, ReasonPlaybookUnavailable, validationReport, survivors)
		if err != nil {
			return RunResult{}, err
		}
		receipt, err := BuildStageReceipt(cfg, r.Config.PlaybookManifestHash, validationReport.PlaybookHash, validationReport.ExperimentHash, nil, wf, mechanism, execution, map[string]string{
			"catalog_hash": r.Catalog.DataHash,
		})
		if err != nil {
			return RunResult{}, err
		}
		if err := WriteOutputs(r.Config.OutputDir, wf, summary, mechanism, execution, receipt); err != nil {
			return RunResult{}, err
		}
		return RunResult{WFReport: wf, Summary: summary, MechanismReport: mechanism, ExecutionReport: execution, Receipt: receipt, Validation: validationReport, Survivors: survivors}, nil
	}

	bundle, err := g10validation.LoadPlaybookBundle(r.Config.PlaybookOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if bundle.Manifest.ManifestHash != r.Config.PlaybookManifestHash {
		return RunResult{}, fmt.Errorf("walkforward: playbook manifest hash mismatch")
	}

	mapManifestHashes := map[string]string{}
	profilesByID := map[string]profileContext{}
	for _, dir := range r.Config.ResidualMapDirs {
		mapBundle, err := playbook.LoadResidualMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		mapManifestHashes[mapBundle.Manifest.ProfileID] = mapBundle.Manifest.ManifestHash
		if _, ok := profilesByID[mapBundle.Manifest.ProfileID]; ok {
			return RunResult{}, fmt.Errorf("walkforward: duplicate profile map %q", mapBundle.Manifest.ProfileID)
		}
		profile, err := r.buildProfileContext(mapBundle)
		if err != nil {
			return RunResult{}, err
		}
		profilesByID[mapBundle.Manifest.ProfileID] = profile
	}
	if err := g10validation.VerifyBindings(g10validation.RunConfig{
		SchemaVersion: g10validation.SchemaVersionConfig, HistoryRoot: cfg.HistoryRoot,
		PlaybookOutputDir: cfg.PlaybookOutputDir, OutputDir: cfg.OutputDir,
		ResidualMapDirs: cfg.ResidualMapDirs, MaxFiles: cfg.MaxFiles, MaxLines: cfg.MaxLines,
		ProgressEvery: cfg.ProgressEvery, AllowTruncatedSmoke: cfg.AllowTruncatedSmoke,
		Generation: cfg.Generation, ProtocolHash: cfg.ProtocolHash, HistoryDataHash: cfg.HistoryDataHash,
		PlaybookManifestHash: cfg.PlaybookManifestHash,
	}, r.Catalog, bundle, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	if err := g10validation.VerifyFormalHistoryScope(r.Catalog, cfg.MaxFiles, cfg.AllowTruncatedSmoke, loadResidualBundles(r.Config.ResidualMapDirs)); err != nil {
		return RunResult{}, err
	}

	fpBinding, err := g10validation.LoadFingerprintBinding(cfg.FingerprintPath)
	if err != nil {
		return RunResult{}, err
	}
	if err := g10validation.VerifyFingerprintBinding(fpBinding); err != nil {
		return RunResult{}, err
	}
	if cfg.FingerprintHash != fpBinding.Hash {
		return RunResult{}, fmt.Errorf("walkforward: fingerprint hash binding mismatch")
	}
	r.Fingerprint = fpBinding
	eligibleSymbols := residualmap.CriticalKnownSymbolSet(fpBinding.Fingerprint)
	for profileID, profile := range profilesByID {
		profile.Symbols = filterEligibleSymbols(profile.Symbols, eligibleSymbols)
		profilesByID[profileID] = profile
	}

	playbooksByHash := map[string]playbook.FrozenEventPlaybook{}
	for _, pb := range bundle.Playbooks {
		playbooksByHash[pb.PlaybookHash] = pb
	}

	startSurvivor := 0
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, cfg, bundle.Manifest.ManifestHash, survivors.SurvivorsHash, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted(validationReport, survivors)
		}
		startSurvivor = cp.NextSurvivorIndex
	}

	var playbookResults []PlaybookWFResult
	var totalStats ReplayStats
	for i := startSurvivor; i < len(survivors.Survivors); i++ {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		default:
		}
		entry := survivors.Survivors[i]
		pb, ok := playbooksByHash[entry.PlaybookHash]
		if !ok {
			return RunResult{}, fmt.Errorf("walkforward: missing playbook for survivor %s", entry.PlaybookHash)
		}
		result, stats, err := r.runPlaybookWF(ctx, pb, profilesByID[pb.ProfileID], entry, bundle.Ledger)
		if err != nil {
			return RunResult{}, err
		}
		playbookResults = append(playbookResults, result)
		totalStats = addStats(totalStats, stats)
		if err := r.saveCheckpoint(i+1, false, cfg, bundle, survivors, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if r.OnProgress != nil {
			r.OnProgress(i, pb.PlaybookHash, pb.ProfileID)
		}
	}

	overallStatus := StatusWFPass
	overallReason := ""
	for _, pr := range playbookResults {
		if pr.Status != StatusWFPass {
			overallStatus = StatusWFFail
			if pr.FailReason != "" {
				overallReason = pr.FailReason
			}
			break
		}
	}
	wfReport, err := BuildWFReport(cfg, survivors, playbookResults, overallStatus, overallReason)
	if err != nil {
		return RunResult{}, err
	}
	wfReport.PlaybookHash = bundle.Manifest.PlaybookHash
	wfReport.ExperimentHash = bundle.Manifest.ExperimentHash
	mechanism := BuildMechanismReport(wfReport)
	execution := BuildExecutionReport(wfReport, totalStats)
	summary := BuildWFSummary(wfReport, mechanism, execution)
	receipt, err := BuildStageReceipt(cfg, bundle.Manifest.ManifestHash, bundle.Manifest.PlaybookHash, bundle.Manifest.ExperimentHash, mapManifestHashes, wfReport, mechanism, execution, map[string]string{
		"catalog_hash": r.Catalog.DataHash, "playbook_manifest_hash": bundle.Manifest.ManifestHash,
		"validation_report_hash": validationReport.ReportHash,
	})
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(cfg.OutputDir, wfReport, summary, mechanism, execution, receipt); err != nil {
		return RunResult{}, err
	}
	if err := r.saveCheckpoint(len(survivors.Survivors), true, cfg, bundle, survivors, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	return RunResult{WFReport: wfReport, Summary: summary, MechanismReport: mechanism, ExecutionReport: execution, Receipt: receipt, Validation: validationReport, Survivors: survivors}, nil
}

func filterEligibleSymbols(symbols []string, eligible map[string]bool) []string {
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		normalized := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(symbol)), "-SWAP")
		if eligible[normalized] {
			out = append(out, symbol)
		}
	}
	return out
}

func (r *Runner) runPlaybookWF(
	ctx context.Context,
	pb playbook.FrozenEventPlaybook,
	profile profileContext,
	survivor g10validation.SurvivorEntry,
	ledger playbook.ExperimentLedger,
) (PlaybookWFResult, ReplayStats, error) {
	embargo := PurgeEmbargoDuration(profile.Spec, pb)
	var allEvents []WFEvent
	barsBySymbol := map[string][]profitmap.Bar{}
	fundingBySymbol := map[string][]profitmap.FundingPoint{}
	tracker := eventmap.NewWindowTracker()

	for _, symbol := range profile.Symbols {
		select {
		case <-ctx.Done():
			return PlaybookWFResult{}, ReplayStats{}, ctx.Err()
		default:
		}
		rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
		bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return PlaybookWFResult{}, ReplayStats{}, err
		}
		barsBySymbol[symbol] = bars
		var funding []profitmap.FundingPoint
		fundingRel := "okx_funding/" + symbol + ".jsonl"
		if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
			funding, err = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return PlaybookWFResult{}, ReplayStats{}, fmt.Errorf("walkforward: funding %s: %w", symbol, err)
			}
		}
		fundingBySymbol[symbol] = funding
		events, err := ScanPlaybookSymbolWalkForward(bars, symbol, pb, ScanConfig{
			Context: ctx, Spec: profile.Spec, ProfileID: profile.ProfileID, Boundaries: profile.Boundaries, Thresholds: profile.Thresholds,
			BTCBars: profile.BTCBars, ShockClusterAt: profile.ShockCluster, Funding: funding, MaxLines: r.Config.MaxLines,
			Fingerprint: r.Fingerprint,
		}, tracker)
		if err != nil {
			return PlaybookWFResult{}, ReplayStats{}, err
		}
		allEvents = append(allEvents, events...)
	}

	if len(allEvents) < protocol.WFFoldCount {
		return PlaybookWFResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
			ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, Horizon: pb.Horizon,
			Status: StatusInconclusive, FailReason: ReasonInsufficientEvents,
			PurgeEmbargoHours: embargo.Hours(), PayerStatus: protocol.UnexplainedPersonalEdgeStatus,
		}, ReplayStats{}, nil
	}

	degSummary, err := g10validation.CheckBrainDegeneracy(allEvents, r.Fingerprint.Fingerprint, pb.ProfileID)
	if err != nil {
		return PlaybookWFResult{}, ReplayStats{}, err
	}
	if degSummary.Degenerate {
		return PlaybookWFResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
			ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, Horizon: pb.Horizon,
			Status: StatusWFFail, FailReason: ReasonBrainDegeneracy,
			PurgeEmbargoHours: embargo.Hours(), PayerStatus: protocol.UnexplainedPersonalEdgeStatus,
			BrainDegeneracy: degSummary,
		}, ReplayStats{}, nil
	}

	timestamps := make([]string, len(allEvents))
	for i, ev := range allEvents {
		timestamps[i] = ev.DecisionTime.UTC().Format(time.RFC3339Nano)
	}
	folds, err := BuildFolds(timestamps, protocol.WFFoldCount, embargo)
	if err != nil {
		return PlaybookWFResult{}, ReplayStats{}, err
	}
	foldEvents := PartitionEventsByFold(allEvents, folds)

	var allFoldResults []FoldResult
	var totalStats ReplayStats
	tierStatuses := map[int]string{}
	tierReasons := map[int]string{}
	tierEligible := map[int]string{}
	var tierGateResults []TierWFResult
	var tierDailyByEquity = map[int]DailyEquityReplay{}
	maxCap := survivor.MaxCapacityUSDT

	for _, equity := range protocol.ValidationInitialEquities {
		sizingInput := FrozenSizingFromSurvivor(survivor, int(equity))
		var tierFolds []FoldResult
		tierDaily := DailyEquityReplay{
			DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
			EventPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
		}
		for _, fold := range folds {
			events := foldEvents[fold.Index]
			scenarios, daily, stats := ReplayFoldEvents(events, barsBySymbol, fundingBySymbol, equity, sizingInput, pb, r.Fingerprint)
			totalStats = addStats(totalStats, stats)
			tierFolds = append(tierFolds, SummarizeFoldResult(fold.Index, int(equity), scenarios))
			mergeDaily(&tierDaily, daily)
		}
		allFoldResults = append(allFoldResults, tierFolds...)
		lofo := BuildLOFOResults(folds, tierFolds)
		attempts := BuildAttemptSignResults(ledger, pb.PlaybookHash, tierDaily)
		status, checks, reason := EvaluateWFGates(tierFolds, lofo, tierDaily, attempts)
		tierStatuses[int(equity)] = status
		tierReasons[int(equity)] = reason
		if status == StatusWFPass {
			tierEligible[int(equity)] = tierLabel(int(equity))
		}
		tierDailyByEquity[int(equity)] = tierDaily
		tierGateResults = append(tierGateResults, TierWFResult{
			CapitalTier: int(equity), Status: status, FailReason: reason,
			GateChecks: checks, LOFOResults: lofo,
		})
	}

	status, eligible := CollapseTierStatuses(tierStatuses)
	failReason := ""
	if status != StatusWFPass {
		for _, tier := range protocol.ValidationInitialEquities {
			if r := tierReasons[int(tier)]; r != "" {
				failReason = r
				break
			}
		}
	}
	if maxCap <= 0 {
		maxCap = survivor.MaxCapacityUSDT
	}
	finalChecks := WFGateChecks{}
	var finalLOFO []LOFOResult
	var finalAttempts []AttemptSignResult
	for _, tr := range tierGateResults {
		if tr.Status == StatusWFPass {
			finalChecks = tr.GateChecks
			finalLOFO = tr.LOFOResults
			finalAttempts = BuildAttemptSignResults(ledger, pb.PlaybookHash, tierDailyByEquity[tr.CapitalTier])
			break
		}
	}
	if len(finalAttempts) == 0 && len(tierGateResults) > 0 {
		tr := tierGateResults[0]
		finalChecks = tr.GateChecks
		finalLOFO = tr.LOFOResults
		finalAttempts = BuildAttemptSignResults(ledger, pb.PlaybookHash, tierDailyByEquity[tr.CapitalTier])
	}

	return PlaybookWFResult{
		Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
		ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, Horizon: pb.Horizon,
		Status: status, FailReason: failReason, FoldCount: len(folds), PurgeEmbargoHours: embargo.Hours(),
		Folds: folds, FoldResults: allFoldResults, TierGateResults: tierGateResults,
		LOFOResults: finalLOFO, GateChecks: finalChecks,
		AttemptSignResults: finalAttempts, EligibleCapitalTiers: eligible, MaxCapacityUSDT: maxCap,
		TradeAllowed: false, ExamEligible: false, PayerStatus: protocol.UnexplainedPersonalEdgeStatus,
		BrainDegeneracy: degSummary,
	}, totalStats, nil
}

func mergeDaily(dst *DailyEquityReplay, src DailyEquityReplay) {
	if dst.DailyPnL == nil {
		dst.DailyPnL = map[string]float64{}
	}
	if dst.ShockDayPnL == nil {
		dst.ShockDayPnL = map[string]float64{}
	}
	if dst.EventPnL == nil {
		dst.EventPnL = map[string]float64{}
	}
	if dst.SymbolPnL == nil {
		dst.SymbolPnL = map[string]float64{}
	}
	for k, v := range src.DailyPnL {
		dst.DailyPnL[k] += v
	}
	for k, v := range src.ShockDayPnL {
		dst.ShockDayPnL[k] += v
	}
	for k, v := range src.EventPnL {
		dst.EventPnL[k] += v
	}
	for k, v := range src.SymbolPnL {
		dst.SymbolPnL[k] += v
	}
}

func addStats(a, b ReplayStats) ReplayStats {
	a.EventsSeen += b.EventsSeen
	a.EventsTraded += b.EventsTraded
	a.EventsSkipped += b.EventsSkipped
	a.SkipBelowMinNotional += b.SkipBelowMinNotional
	a.SkipBelowLotFloor += b.SkipBelowLotFloor
	a.SkipCapacity += b.SkipCapacity
	a.SkipConcurrencyCap += b.SkipConcurrencyCap
	a.CapacityFails += b.CapacityFails
	return a
}

func (r *Runner) buildProfileContext(bundle playbook.ResidualMapBundle) (profileContext, error) {
	spec, err := eventmap.ProfileSpecFor(bundle.Manifest.ProfileID)
	if err != nil {
		return profileContext{}, err
	}
	selection, err := eventmap.SelectPrimaryFiles(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, 0)
	if err != nil {
		return profileContext{}, err
	}
	if err := g10validation.RequireFullCatalogSelection(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke); err != nil {
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
		ProfileID: bundle.Manifest.ProfileID, Spec: spec, Boundaries: boundaries,
		Thresholds: bundle.Thresholds, BTCBars: btcBars, ShockCluster: shockIndex.Lookup, Symbols: symbols,
	}, nil
}

func (r *Runner) saveCheckpoint(nextSurvivor int, done bool, cfg RunConfig, bundle g10validation.PlaybookBundle, survivors g10validation.ValidationSurvivors, mapManifestHashes map[string]string) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		PlaybookManifestHash: bundle.Manifest.ManifestHash, PlaybookHash: bundle.Manifest.PlaybookHash,
		ExperimentHash: bundle.Manifest.ExperimentHash, ValidationReportHash: cfg.ValidationReportHash,
		SurvivorsHash: survivors.SurvivorsHash, MapManifestHashes: cloneStringMap(mapManifestHashes),
		OutputDir: cfg.OutputDir, NextSurvivorIndex: nextSurvivor, Done: done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

func (r *Runner) loadCompleted(validation g10validation.ValidationReport, survivors g10validation.ValidationSurvivors) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_wf_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var wf WFReport
	if err := json.Unmarshal(raw, &wf); err != nil {
		return RunResult{}, err
	}
	if err := ValidateWFReport(wf); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_event_mechanism_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var mechanism MechanismReport
	if err := json.Unmarshal(raw, &mechanism); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_execution_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var execution ExecutionReport
	if err := json.Unmarshal(raw, &execution); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_wf_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	var summary WFSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_wf_stage_receipt.json"))
	if err != nil {
		return RunResult{}, err
	}
	var receipt StageReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	return RunResult{WFReport: wf, Summary: summary, MechanismReport: mechanism, ExecutionReport: execution, Receipt: receipt, Validation: validation, Survivors: survivors}, nil
}

func loadResidualBundles(dirs []string) []playbook.ResidualMapBundle {
	var out []playbook.ResidualMapBundle
	for _, dir := range dirs {
		if b, err := playbook.LoadResidualMapBundle(dir); err == nil {
			out = append(out, b)
		}
	}
	return out
}
