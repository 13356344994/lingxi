package personalwalkforward

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
	"lingxi/internal/routerg10/personalplaybook"
)

// ScanPlaybookSymbolWalkForward evaluates walk_forward-partition triggers for one playbook.
func ScanPlaybookSymbolWalkForward(
	bars []eventmap.Bar,
	symbol string,
	pb personalplaybook.PersonalWildPlaybook,
	cfg g10personalvalidation.ScanConfig,
	tracker *eventmap.WindowTracker,
) ([]WFEvent, error) {
	cfg.Partition = eventmap.PartitionWalkFwd
	return g10personalvalidation.ScanPlaybookSymbol(bars, symbol, pb, cfg, tracker)
}

// ReplayFoldEvents replays one fold's triggers through personal account replay.
func ReplayFoldEvents(
	events []WFEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	equity float64,
	sizing g10personalvalidation.FrozenSizingInput,
	pb personalplaybook.PersonalWildPlaybook,
	proofs g10personalvalidation.ProofBundle,
) ([]TierScenarioResult, DailyEquityReplay, ReplayStats) {
	scenarios, daily, stats, _ := g10personalvalidation.ReplayTierEvents(events, barsBySymbol, fundingBySymbol, equity, sizing, pb, proofs)
	return scenarios, daily, stats
}

// ScanConfig aliases personal validation scan config for WF partition.
type ScanConfig = g10personalvalidation.ScanConfig

type profileContext struct {
	ProfileID    string
	Spec         eventmap.ProfileSpec
	Boundaries   eventmap.Boundaries
	Thresholds   eventmap.FrozenThresholds
	BTCBars      []eventmap.Bar
	ShockCluster func(time.Time) string
	Symbols      []string
}

// Runner orchestrates bounded G10P personal playbook walk-forward replay.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	Proofs           g10personalvalidation.ProofBundle
	OnProgress       func(survivorIndex int, playbookHash, symbol string)
	CatalogValidator func(historydata.HistoryCatalog) error
}

// Run executes walk-forward folds for validation survivors and writes artifacts.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	validationReport, survivors, valErr := g10personalvalidation.VerifyValidationInputs(r.Config.ValidationDir, r.Config.PlaybookManifestHash)
	if valErr != nil {
		return r.buildSkipped(ReasonValidationNotPass, validationReport, survivors)
	}
	if len(survivors.Survivors) == 0 {
		return r.buildSkipped(ReasonPlaybookUnavailable, validationReport, survivors)
	}
	bundle, err := g10personalvalidation.LoadPlaybookBundle(r.Config.PlaybookOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	proofs, constHash, mapHashes, err := g10personalvalidation.LoadProofBundle(g10personalvalidation.RunConfig{
		ReverseTapesDir: r.Config.ReverseTapesDir, ReverseTapesManifestHash: r.Config.ReverseTapesManifestHash,
		AnnouncementDir: r.Config.AnnouncementDir, AnnouncementManifestHash: r.Config.AnnouncementManifestHash,
		ConstitutionHash: r.Config.ConstitutionHash,
		EntryProofDir: r.Config.EntryProofDir, ExecutionPolicyDir: r.Config.ExecutionPolicyDir,
		ExitProofPath: r.Config.ExitProofPath, ExclusionMapDir: r.Config.ExclusionMapDir,
		MapDirs: r.Config.MapDirs,
	})
	if err != nil {
		return RunResult{}, err
	}
	r.Config.ConstitutionHash = constHash
	r.Proofs = proofs
	if err := g10personalvalidation.VerifyBindings(g10personalvalidation.RunConfig{
		ProtocolHash: g10protocolv2.RouterProtocolHash, HistoryDataHash: g10protocolv2.HistoryDataHash,
		PlaybookManifestHash: bundle.Manifest.ManifestHash, ConstitutionHash: constHash,
		MapDirs: r.Config.MapDirs,
	}, r.Catalog, bundle, mapHashes); err != nil {
		return RunResult{}, err
	}
	if err := g10personalvalidation.VerifyFormalHistoryScope(r.Catalog, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke, profileIDsFromSurvivors(survivors)); err != nil {
		return RunResult{}, err
	}
	profilesByID := map[string]profileContext{}
	for _, dir := range r.Config.MapDirs {
		mapBundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		profile, err := r.buildProfileContext(mapBundle)
		if err != nil {
			return RunResult{}, err
		}
		profilesByID[mapBundle.Manifest.ProfileID] = profile
	}

	startSurvivor := 0
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, bundle.Manifest.ManifestHash, survivors.SurvivorsHash); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return loadCompleted(r.Config.OutputDir, validationReport, survivors)
		}
		startSurvivor = cp.NextSurvivorIndex
	}

	var playbookResults []PlaybookWFResult
	for i := startSurvivor; i < len(survivors.Survivors); i++ {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		default:
		}
		entry := survivors.Survivors[i]
		pb, ok := g10personalvalidation.LoadPlaybookByHash(bundle, entry.PlaybookHash)
		if !ok {
			return RunResult{}, fmt.Errorf("personalwalkforward: missing playbook for survivor %s", entry.PlaybookHash)
		}
		profile := profilesByID[pb.ProfileID]
		result, err := r.runPlaybookWF(ctx, pb, profile, entry)
		if err != nil {
			return RunResult{}, err
		}
		playbookResults = append(playbookResults, result)
		if err := saveCheckpoint(r.CheckpointPath, i+1, false, r.Config, bundle, survivors); err != nil {
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
			overallReason = pr.FailReason
			break
		}
	}
	wfReport, summary, receipt, err := BuildOutputs(r.Config, bundle, survivors, validationReport, playbookResults, overallStatus, overallReason)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, wfReport, summary, receipt); err != nil {
		return RunResult{}, err
	}
	if err := saveCheckpoint(r.CheckpointPath, len(survivors.Survivors), true, r.Config, bundle, survivors); err != nil {
		return RunResult{}, err
	}
	return RunResult{WFReport: wfReport, Summary: summary, Receipt: receipt, Validation: validationReport, Survivors: survivors}, nil
}

func (r *Runner) runPlaybookWF(
	ctx context.Context,
	pb personalplaybook.PersonalWildPlaybook,
	profile profileContext,
	survivor g10personalvalidation.SurvivorEntry,
) (PlaybookWFResult, error) {
	embargo := PurgeEmbargoDuration(pb.ProfileID, pb.Horizon)
	var allEvents []WFEvent
	barsBySymbol := map[string][]profitmap.Bar{}
	fundingBySymbol := map[string][]profitmap.FundingPoint{}
	tracker := eventmap.NewWindowTracker()
	proofs := g10personalvalidation.ProofBundle{Announcements: r.Proofs.Announcements, ConstitutionHash: r.Config.ConstitutionHash}

	for _, symbol := range profile.Symbols {
		rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
		bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return PlaybookWFResult{}, err
		}
		barsBySymbol[symbol] = bars
		var funding []profitmap.FundingPoint
		fundingRel := "okx_funding/" + symbol + ".jsonl"
		if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
			funding, err = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return PlaybookWFResult{}, err
			}
		}
		fundingBySymbol[symbol] = funding
		events, err := ScanPlaybookSymbolWalkForward(bars, symbol, pb, g10personalvalidation.ScanConfig{
			Context: ctx, Spec: profile.Spec, ProfileID: profile.ProfileID, Boundaries: profile.Boundaries,
			Thresholds: profile.Thresholds, BTCBars: profile.BTCBars, ShockClusterAt: profile.ShockCluster,
			Funding: funding, MaxLines: r.Config.MaxLines, Proofs: proofs,
		}, tracker)
		if err != nil {
			return PlaybookWFResult{}, err
		}
		allEvents = append(allEvents, events...)
	}

	if len(allEvents) < WFFoldCount {
		layers := g10personalvalidation.UpdateWFLayer(survivor.Layers, false)
		return PlaybookWFResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
			ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, PersonalTapeFamily: pb.PersonalTapeFamily,
			Horizon: pb.Horizon, Status: StatusInconclusive, FailReason: ReasonInsufficientEvents,
			Layers: layers, PurgeEmbargoHours: embargo.Hours(), TradeAllowed: false, ExamEligible: false,
			ExamActions: g10protocolv2.ExamActions,
		}, nil
	}

	degSummary, err := g10personalvalidation.CheckBrainDegeneracy(allEvents, pb)
	if err != nil {
		return PlaybookWFResult{}, err
	}
	if degSummary.Degenerate {
		layers := g10personalvalidation.UpdateWFLayer(survivor.Layers, false)
		return PlaybookWFResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ProfileID: pb.ProfileID,
			EventFamily: pb.EventFamily, PersonalTapeFamily: pb.PersonalTapeFamily, Horizon: pb.Horizon,
			Status: StatusWFFail, FailReason: ReasonBrainDegeneracy, Layers: layers,
			BrainDegeneracy: degSummary, TradeAllowed: false, ExamEligible: false,
			ExamActions: g10protocolv2.ExamActions,
		}, nil
	}

	timestamps := make([]string, len(allEvents))
	for i, ev := range allEvents {
		timestamps[i] = ev.DecisionTime.UTC().Format(time.RFC3339Nano)
	}
	folds, err := BuildFolds(timestamps, WFFoldCount, embargo)
	if err != nil {
		return PlaybookWFResult{}, err
	}
	foldEvents := PartitionEventsByFold(allEvents, folds)

	var allFoldResults []FoldResult
	var tierDaily DailyEquityReplay
	equity := g10personalvalidation.CapitalTiersUSDT[0]
	if len(survivor.EligibleCapitalTiers) > 0 {
		for _, tier := range g10personalvalidation.CapitalTiersUSDT {
			equity = tier
			break
		}
	}
	sizing := g10personalvalidation.FrozenSizingFromEvents(allEvents, int(equity))
	for _, fold := range folds {
		events := foldEvents[fold.Index]
		scenarios, daily, _ := ReplayFoldEvents(events, barsBySymbol, fundingBySymbol, equity, sizing, pb, r.Proofs)
		baseNet := 0.0
		nonNeg := true
		for _, s := range scenarios {
			if s.DelayBars == 0 && !s.P90SlippageStress && s.CostStressMultiplier <= 1.0+1e-9 {
				baseNet = s.NetReturnFraction
				nonNeg = s.NetReturnNonNegative
			}
		}
		allFoldResults = append(allFoldResults, FoldResult{
			FoldIndex: fold.Index, CapitalTier: int(equity), Scenarios: scenarios,
			BaseNetReturnFraction: baseNet, NetReturnNonNegative: nonNeg,
		})
		tierDaily = daily
	}
	lofo := BuildLOFOResults(folds, allFoldResults)
	leaveOne := g10personalvalidation.BuildLeaveOneReport(tierDaily, allEvents)
	status, checks, reason := EvaluateWFGates(allFoldResults, lofo, tierDaily, leaveOne)
	layers := g10personalvalidation.UpdateWFLayer(survivor.Layers, status == StatusWFPass)
	eligible := []string{}
	if status == StatusWFPass {
		eligible = append(eligible, survivor.EligibleCapitalTiers...)
	}
	return PlaybookWFResult{
		Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
		ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, PersonalTapeFamily: pb.PersonalTapeFamily,
		Horizon: pb.Horizon, Status: status, FailReason: reason, Layers: layers,
		FoldCount: len(folds), PurgeEmbargoHours: embargo.Hours(), Folds: folds,
		FoldResults: allFoldResults, LOFOResults: lofo, GateChecks: checks,
		EligibleCapitalTiers: eligible, MaxCapacityUSDT: survivor.MaxCapacityUSDT,
		BrainDegeneracy: degSummary, TradeAllowed: false, ExamEligible: false,
		ExamActions: g10protocolv2.ExamActions,
	}, nil
}

func (r *Runner) buildProfileContext(bundle reverseevent.MapBundle) (profileContext, error) {
	spec, err := eventmap.ProfileSpecFor(bundle.Manifest.ProfileID)
	if err != nil {
		return profileContext{}, err
	}
	selection, err := eventmap.SelectPrimaryFiles(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, 0)
	if err != nil {
		return profileContext{}, err
	}
	if err := g10personalvalidation.RequireFullCatalogSelection(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke); err != nil {
		return profileContext{}, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if bundle.Manifest.ProfileID == eventmap.ProfileOKX1H {
		btcRel = "okx_1h/BTC-USDT-SWAP.jsonl"
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

func (r *Runner) buildSkipped(reason string, validation g10personalvalidation.ValidationReport, survivors g10personalvalidation.ValidationSurvivors) (RunResult, error) {
	wf, summary, receipt, err := BuildSkippedReport(r.Config, reason, validation, survivors)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, wf, summary, receipt); err != nil {
		return RunResult{}, err
	}
	return RunResult{WFReport: wf, Summary: summary, Receipt: receipt, Validation: validation, Survivors: survivors}, nil
}

func profileIDsFromSurvivors(survivors g10personalvalidation.ValidationSurvivors) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range survivors.Survivors {
		if s.ProfileID == "" || seen[s.ProfileID] {
			continue
		}
		seen[s.ProfileID] = true
		out = append(out, s.ProfileID)
	}
	return out
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.PlaybookOutputDir == "" || c.ValidationDir == "" || c.OutputDir == "" {
		return fmt.Errorf("history_root, playbook_output_dir, validation_dir, output_dir required")
	}
	if c.PlaybookManifestHash == "" {
		return fmt.Errorf("playbook_manifest_hash required")
	}
	if len(c.MapDirs) == 0 {
		return fmt.Errorf("at least one map-dir required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return fmt.Errorf("max_files and max_lines must be positive")
	}
	return nil
}
