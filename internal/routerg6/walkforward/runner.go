package walkforward

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
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// Runner orchestrates bounded G6 event walk-forward replay.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(survivorIndex int, cardHash, symbol string)
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

	validationReport, survivors, valErr := VerifyValidationInputs(r.Config.ValidationDir, r.Config.CardManifestHash)
	if valErr != nil {
		reason := ReasonValidationNotPass
		if validationReport.ReportHash == "" {
			reason = ReasonValidationTamper
		}
		wf, summary, mechanism, execution, skipErr := BuildSkippedReport(r.Config, reason, validationReport, survivors)
		if skipErr != nil {
			return RunResult{}, skipErr
		}
		receipt, recErr := BuildStageReceipt(r.Config, eventvalidation.CardBundle{}, nil, wf, mechanism, execution, map[string]string{
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
		wf, summary, mechanism, execution, err := BuildSkippedReport(r.Config, ReasonEventCardUnavailable, validationReport, survivors)
		if err != nil {
			return RunResult{}, err
		}
		receipt, err := BuildStageReceipt(r.Config, eventvalidation.CardBundle{}, nil, wf, mechanism, execution, map[string]string{
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

	bundle, err := eventvalidation.LoadCardBundle(r.Config.CardOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	if bundle.Manifest.ManifestHash != r.Config.CardManifestHash {
		return RunResult{}, fmt.Errorf("walkforward: card manifest hash mismatch")
	}

	cfg := r.Config
	cfg.ValidationReportHash = validationReport.ReportHash
	cfg.SurvivorsHash = survivors.SurvivorsHash

	mapManifestHashes := map[string]string{}
	mapBundles := make([]reverseevent.MapBundle, 0, len(r.Config.MapDirs))
	profilesByID := map[string]profileContext{}
	for _, dir := range r.Config.MapDirs {
		mapBundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		mapBundles = append(mapBundles, mapBundle)
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
	if err := eventvalidation.VerifyBindings(r.validationConfig(cfg), r.Catalog, bundle, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	if err := reverseevent.VerifyCatalogBinding(
		r.Catalog, r.Config.HistoryRoot, mapBundles,
		r.Config.MaxFiles, r.Config.MaxLines, r.Config.AllowTruncatedSmoke,
	); err != nil {
		return RunResult{}, err
	}

	cardsByHash := map[string]reverseevent.FrozenEventCard{}
	for _, card := range bundle.Cards {
		cardsByHash[card.EventCardHash] = card
	}

	startSurvivor := 0
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, cfg, bundle.Manifest.ManifestHash, survivors.SurvivorsHash, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted(bundle, validationReport, survivors, mapManifestHashes)
		}
		startSurvivor = cp.NextSurvivorIndex
	}

	var cardResults []CardWFResult
	var totalStats ReplayStats
	for i := startSurvivor; i < len(survivors.Survivors); i++ {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		default:
		}
		entry := survivors.Survivors[i]
		card, ok := cardsByHash[entry.EventCardHash]
		if !ok {
			return RunResult{}, fmt.Errorf("walkforward: missing card for survivor %s", entry.EventCardHash)
		}
		if err := reverseevent.ValidateEventCard(card); err != nil {
			return RunResult{}, fmt.Errorf("walkforward: invalid card %s: %w", card.EventCardHash, err)
		}
		profile, ok := profilesByID[card.ProfileID]
		if !ok {
			return RunResult{}, fmt.Errorf("walkforward: missing map profile %q for card %s", card.ProfileID, card.EventCardHash)
		}
		result, stats, err := r.runCardWF(ctx, card, profile, bundle.Ledger, entry)
		if err != nil {
			return RunResult{}, err
		}
		cardResults = append(cardResults, result)
		totalStats.add(stats)
		if err := r.saveCheckpoint(i+1, false, cfg, bundle, survivors, mapManifestHashes); err != nil {
			return RunResult{}, err
		}
		if r.OnProgress != nil {
			r.OnProgress(i, card.EventCardHash, card.ProfileID)
		}
	}

	overallStatus := StatusWFPass
	overallReason := ""
	for _, cr := range cardResults {
		if cr.Status != StatusWFPass {
			overallStatus = StatusWFFail
			if cr.FailReason != "" {
				overallReason = cr.FailReason
			}
			break
		}
	}
	wfReport, err := BuildWFReport(cfg, survivors, cardResults, overallStatus, overallReason)
	if err != nil {
		return RunResult{}, err
	}
	wfReport.EventCardHash = bundle.Manifest.EventCardHash
	wfReport.ExperimentHash = bundle.Manifest.ExperimentHash
	mechanism := BuildMechanismReport(wfReport)
	execution := BuildExecutionReport(wfReport, totalStats)
	summary := BuildWFSummary(wfReport, mechanism, execution)
	receipt, err := BuildStageReceipt(cfg, bundle, mapManifestHashes, wfReport, mechanism, execution, map[string]string{
		"catalog_hash":          r.Catalog.DataHash,
		"card_manifest_hash":    bundle.Manifest.ManifestHash,
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
	return RunResult{
		WFReport:        wfReport,
		Summary:         summary,
		MechanismReport: mechanism,
		ExecutionReport: execution,
		Receipt:         receipt,
		Validation:      validationReport,
		Survivors:       survivors,
	}, nil
}

func (r *Runner) runCardWF(
	ctx context.Context,
	card reverseevent.FrozenEventCard,
	profile profileContext,
	ledger reverseevent.ExperimentLedger,
	survivor eventvalidation.SurvivorEntry,
) (CardWFResult, ReplayStats, error) {
	embargo := PurgeEmbargoDuration(profile.Spec, card)
	var allEvents []WFEvent
	barsBySymbol := map[string][]profitmap.Bar{}
	fundingBySymbol := map[string][]profitmap.FundingPoint{}
	tracker := eventmap.NewWindowTracker()

	for _, symbol := range profile.Symbols {
		select {
		case <-ctx.Done():
			return CardWFResult{}, ReplayStats{}, ctx.Err()
		default:
		}
		rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
		bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return CardWFResult{}, ReplayStats{}, err
		}
		barsBySymbol[symbol] = bars
		var funding []profitmap.FundingPoint
		fundingRel := "okx_funding/" + symbol + ".jsonl"
		if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
			funding, err = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return CardWFResult{}, ReplayStats{}, fmt.Errorf("walkforward: funding %s: %w", symbol, err)
			}
		}
		fundingBySymbol[symbol] = funding
		events, err := ScanCardSymbolWalkForward(bars, symbol, card, ScanConfig{
			Context:        ctx,
			Spec:           profile.Spec,
			Boundaries:     profile.Boundaries,
			Thresholds:     profile.Thresholds,
			BTCBars:        profile.BTCBars,
			ShockClusterAt: profile.ShockCluster,
			Funding:        funding,
			MaxLines:       r.Config.MaxLines,
		}, tracker)
		if err != nil {
			return CardWFResult{}, ReplayStats{}, err
		}
		for _, ev := range events {
			if err := RejectNonWalkForwardPartition(eventmap.PartitionWalkFwd); err != nil {
				return CardWFResult{}, ReplayStats{}, err
			}
			allEvents = append(allEvents, ev)
		}
	}

	if len(allEvents) < protocol.WFFoldCount {
		return CardWFResult{
			Ordinal: card.Ordinal, EventCardHash: card.EventCardHash, ExperimentHash: card.ExperimentHash,
			ProfileID: card.ProfileID, EventFamily: card.EventFamily, Horizon: card.Horizon, Direction: card.Direction,
			Status: StatusInconclusive, FailReason: ReasonInsufficientEvents,
			PurgeEmbargoHours: embargo.Hours(),
		}, ReplayStats{}, nil
	}

	timestamps := make([]string, len(allEvents))
	for i, ev := range allEvents {
		timestamps[i] = ev.DecisionTime.UTC().Format(time.RFC3339Nano)
	}
	folds, err := BuildFolds(timestamps, protocol.WFFoldCount, embargo)
	if err != nil {
		return CardWFResult{}, ReplayStats{}, err
	}
	foldEvents := partitionEventsByFold(allEvents, folds)

	var allFoldResults []FoldResult
	var totalStats ReplayStats
	tierStatuses := map[int]string{}
	tierReasons := map[int]string{}
	var finalChecks WFGateChecks
	var finalAttempts []AttemptSignResult
	var finalLOFO []LOFOResult

	for _, tier := range protocol.CapitalTiersUSDT {
		equity := tier
		sizingInput := FrozenSizingFromSurvivor(survivor, int(equity))
		var tierFolds []FoldResult
		var tierDaily DailyEquityReplay
		for _, fold := range folds {
			events := foldEvents[fold.Index]
			scenarios, daily, stats := ReplayFoldEvents(events, barsBySymbol, fundingBySymbol, equity, sizingInput)
			totalStats.add(stats)
			tierFolds = append(tierFolds, SummarizeFoldResult(fold.Index, int(equity), scenarios))
			mergeDaily(&tierDaily, daily)
		}
		allFoldResults = append(allFoldResults, tierFolds...)
		lofo := BuildLOFOResults(folds, tierFolds)
		attempts := BuildAttemptSignResults(ledger, card.EventCardHash, tierDaily)
		status, checks, reason := EvaluateWFGates(tierFolds, lofo, tierDaily, attempts)
		tierStatuses[int(equity)] = status
		tierReasons[int(equity)] = reason
		if int(equity) == int(protocol.CapitalTiersUSDT[0]) {
			finalChecks = checks
			finalAttempts = attempts
			finalLOFO = lofo
		}
	}

	status := StatusWFPass
	failReason := ""
	for _, tier := range protocol.CapitalTiersUSDT {
		if tierStatuses[int(tier)] != StatusWFPass {
			status = StatusWFFail
			if r := tierReasons[int(tier)]; r != "" {
				failReason = r
			} else {
				failReason = ReasonFoldNetReturn
			}
			break
		}
	}

	return CardWFResult{
		Ordinal:            card.Ordinal,
		EventCardHash:        card.EventCardHash,
		ExperimentHash:       card.ExperimentHash,
		ProfileID:            card.ProfileID,
		EventFamily:          card.EventFamily,
		Horizon:              card.Horizon,
		Direction:            card.Direction,
		Status:               status,
		FailReason:           failReason,
		FoldCount:            len(folds),
		PurgeEmbargoHours:    embargo.Hours(),
		Folds:                folds,
		FoldResults:          allFoldResults,
		LOFOResults:          finalLOFO,
		GateChecks:           finalChecks,
		AttemptSignResults:   finalAttempts,
		TradeAllowed:         false,
		ExamEligible:         false,
	}, totalStats, nil
}

func partitionEventsByFold(events []WFEvent, folds []Fold) map[int][]WFEvent {
	out := map[int][]WFEvent{}
	foldTS := map[int]map[string]bool{}
	for _, fold := range folds {
		set := map[string]bool{}
		for _, ts := range fold.Timestamps {
			set[ts] = true
		}
		foldTS[fold.Index] = set
	}
	for _, ev := range events {
		ts := ev.DecisionTime.UTC().Format(time.RFC3339Nano)
		for idx, set := range foldTS {
			if set[ts] {
				out[idx] = append(out[idx], ev)
				break
			}
		}
	}
	return out
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

func (r *Runner) saveCheckpoint(
	nextSurvivor int,
	done bool,
	cfg RunConfig,
	bundle eventvalidation.CardBundle,
	survivors eventvalidation.ValidationSurvivors,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		HistoryDataHash:      protocol.HistoryDataHash,
		ConfigHash:           cfgHash,
		CardManifestHash:     bundle.Manifest.ManifestHash,
		EventCardHash:        bundle.Manifest.EventCardHash,
		ExperimentHash:       bundle.Manifest.ExperimentHash,
		ValidationReportHash: cfg.ValidationReportHash,
		SurvivorsHash:        survivors.SurvivorsHash,
		MapManifestHashes:    cloneStringMap(mapManifestHashes),
		OutputDir:            cfg.OutputDir,
		NextSurvivorIndex:    nextSurvivor,
		Done:                 done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

func (r *Runner) loadCompleted(
	bundle eventvalidation.CardBundle,
	validation eventvalidation.ValidationReport,
	survivors eventvalidation.ValidationSurvivors,
	mapManifestHashes map[string]string,
) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_wf_report.json"))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_event_mechanism_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var mechanism MechanismReport
	if err := json.Unmarshal(raw, &mechanism); err != nil {
		return RunResult{}, err
	}
	if err := ValidateMechanismReport(mechanism); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_execution_report.json"))
	if err != nil {
		return RunResult{}, err
	}
	var execution ExecutionReport
	if err := json.Unmarshal(raw, &execution); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_wf_summary.json"))
	if err != nil {
		return RunResult{}, err
	}
	var summary WFSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g6_wf_stage_receipt.json"))
	if err != nil {
		return RunResult{}, err
	}
	var receipt StageReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	_ = bundle
	_ = mapManifestHashes
	return RunResult{WFReport: wf, Summary: summary, MechanismReport: mechanism, ExecutionReport: execution, Receipt: receipt, Validation: validation, Survivors: survivors}, nil
}

func (r *Runner) validationConfig(cfg RunConfig) eventvalidation.RunConfig {
	return eventvalidation.RunConfig{
		SchemaVersion:       eventvalidation.SchemaVersionConfig,
		HistoryRoot:         cfg.HistoryRoot,
		CardOutputDir:       cfg.CardOutputDir,
		OutputDir:           cfg.OutputDir,
		MapDirs:             cfg.MapDirs,
		MaxFiles:            cfg.MaxFiles,
		MaxLines:            cfg.MaxLines,
		ProgressEvery:       cfg.ProgressEvery,
		AllowTruncatedSmoke: cfg.AllowTruncatedSmoke,
		Generation:          cfg.Generation,
		ProtocolHash:        cfg.ProtocolHash,
		HistoryDataHash:     cfg.HistoryDataHash,
		CardManifestHash:    cfg.CardManifestHash,
	}
}
