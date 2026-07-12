package personalvalidation

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
	"lingxi/internal/routerg6/reverseevent"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalplaybook"
)

// Runner orchestrates personal validation screening with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	OnProgress       func(lines int64, playbookHash, symbol string)
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

// Run executes bounded personal validation replay for frozen personal wild playbooks.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if r.Catalog.DataHash != g10protocolv2.HistoryDataHash {
		return RunResult{}, fmt.Errorf("personalvalidation: catalog data hash mismatch")
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}

	bundle, err := LoadPlaybookBundle(r.Config.PlaybookOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	proofs, constHash, mapHashes, err := LoadProofBundle(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	if r.Config.ConstitutionHash == "" {
		r.Config.ConstitutionHash = constHash
	}
	if err := VerifyBindings(r.Config, r.Catalog, bundle, mapHashes); err != nil {
		return RunResult{}, err
	}
	if err := VerifyFormalHistoryScope(r.Catalog, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke, uniqueProfileIDs(bundle.Playbooks)); err != nil {
		return RunResult{}, err
	}

	profilesByID := map[string]profileContext{}
	thresholdsByProfile := map[string]eventmap.FrozenThresholds{}
	for _, dir := range r.Config.MapDirs {
		mapBundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		if _, ok := profilesByID[mapBundle.Manifest.ProfileID]; ok {
			return RunResult{}, fmt.Errorf("personalvalidation: duplicate profile map %q", mapBundle.Manifest.ProfileID)
		}
		thresholdsByProfile[mapBundle.Manifest.ProfileID] = mapBundle.Thresholds
	}
	for _, profileID := range uniqueProfileIDs(bundle.Playbooks) {
		thresholds, ok := thresholdsByProfile[profileID]
		if !ok {
			return RunResult{}, fmt.Errorf("personalvalidation: missing map for profile %q", profileID)
		}
		profile, err := r.buildProfileContext(profileID, thresholds)
		if err != nil {
			return RunResult{}, err
		}
		profilesByID[profileID] = profile
	}

	tierOutcomes := map[string][]TierResult{}
	degeneracyByPlaybook := map[string]DegeneracySummary{}
	makerByPlaybook := map[string]MakerPathStats{}
	pitByPlaybook := map[string]PITContaminationReport{}
	crowdingByPlaybook := map[string]CrowdingReport{}
	leaveOneByPlaybook := map[string]LeaveOneReport{}
	eventsByPlaybook := map[string][]TriggerEvent{}
	startPlaybook := 0
	nextSymbolByPlaybook := map[string]int{}
	var linesProcessed int64

	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, r.Catalog.DataHash, bundle, mapHashes); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return r.loadCompleted()
		}
		startPlaybook = cp.NextPlaybookIndex
		nextSymbolByPlaybook = cp.NextSymbolIndexByPlaybook
		linesProcessed = cp.LinesProcessed
	}

	for pbIdx := startPlaybook; pbIdx < len(bundle.Playbooks); pbIdx++ {
		pb := bundle.Playbooks[pbIdx]
		profile, ok := profilesByID[pb.ProfileID]
		if !ok {
			return RunResult{}, fmt.Errorf("personalvalidation: missing profile %q for playbook %s", pb.ProfileID, pb.PlaybookHash)
		}
		startSymbol := nextSymbolByPlaybook[pb.PlaybookHash]
		tracker := eventmap.NewWindowTracker()
		var allEvents []TriggerEvent
		for symIdx := startSymbol; symIdx < len(profile.Symbols); symIdx++ {
			select {
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			default:
			}
			symbol := profile.Symbols[symIdx]
			events, err := r.scanSymbol(ctx, pb, profile, symbol, tracker, proofs)
			if err != nil {
				return RunResult{}, err
			}
			allEvents = append(allEvents, events...)
			linesProcessed += int64(len(profile.BTCBars))
			nextSymbolByPlaybook[pb.PlaybookHash] = symIdx + 1
			if err := r.saveProgressCheckpoint(pbIdx, nextSymbolByPlaybook, linesProcessed, false, bundle, mapHashes); err != nil {
				return RunResult{}, err
			}
			if r.OnProgress != nil {
				r.OnProgress(linesProcessed, pb.PlaybookHash, symbol)
			}
		}

		makerByPlaybook[pb.PlaybookHash] = BuildMakerPathStats(allEvents)
		pitByPlaybook[pb.PlaybookHash] = BuildPITReport(allEvents)
		crowdingByPlaybook[pb.PlaybookHash] = BuildCrowdingReport(allEvents)

		degSummary, err := CheckBrainDegeneracy(allEvents, pb)
		if err != nil {
			return RunResult{}, err
		}
		degeneracyByPlaybook[pb.PlaybookHash] = degSummary

		barsBySymbol, fundingBySymbol, err := r.loadReplayData(profile)
		if err != nil {
			return RunResult{}, err
		}
		var tierResults []TierResult
		var tierDaily DailyEquityReplay
		for _, equity := range CapitalTiersUSDT {
			tier := int(equity)
			sizing := FrozenSizingFromEvents(allEvents, tier)
			scenarios, daily, _, replayStats := ReplayTierEvents(allEvents, barsBySymbol, fundingBySymbol, equity, sizing, pb, proofs)
			tierDaily = daily
			exitClear := true
			for _, ev := range allEvents {
				if !ev.ExitProofOK {
					exitClear = false
					break
				}
			}
			status, checks, reason := EvaluateTierGates(replayStats, scenarios, pitByPlaybook[pb.PlaybookHash], makerByPlaybook[pb.PlaybookHash], exitClear)
			tierResults = append(tierResults, TierResult{
				CapitalTier: tier, Status: status, FailReason: reason,
				FrozenConservativeMeanBPS: sizing.ConservativeMeanBPS,
				StressConservativeMeanBPS: sizing.StressConservativeMeanBPS,
				Replay: replayStats, Scenarios: scenarios, GateChecks: checks,
			})
		}
		leaveOneByPlaybook[pb.PlaybookHash] = BuildLeaveOneReport(tierDaily, allEvents)
		tierOutcomes[pb.PlaybookHash] = tierResults
	}

	results := BuildPlaybookResults(bundle.Playbooks, tierOutcomes, degeneracyByPlaybook, makerByPlaybook, pitByPlaybook, crowdingByPlaybook, leaveOneByPlaybook)
	historyScope := BuildHistoryScope(r.Catalog, uniqueProfileIDs(bundle.Playbooks), r.Config.MaxFiles, r.Config.AllowTruncatedSmoke)
	formalOK := FormalValidationAllowed(r.Config.AllowTruncatedSmoke, historyScope)
	if !formalOK {
		for i := range results {
			if results[i].Status == StatusValidationPass {
				results[i].Status = StatusInconclusive
				results[i].FailReason = ReasonInsufficientEvidence
				results[i].EligibleCapitalTiers = nil
			}
		}
	}
	report, err := BuildReport(r.Config, bundle, mapHashes, results, historyScope, formalOK)
	if err != nil {
		return RunResult{}, err
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		return RunResult{}, err
	}
	layers := BuildLayerSummary(report)
	receipt, err := BuildReceipt(r.Config, bundle, mapHashes, map[string]string{
		"catalog_hash": r.Catalog.DataHash, "playbook_manifest_hash": bundle.Manifest.ManifestHash,
	}, report, survivors)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, report, survivors, receipt, layers); err != nil {
		return RunResult{}, err
	}
	if err := r.saveProgressCheckpoint(len(bundle.Playbooks), nextSymbolByPlaybook, linesProcessed, true, bundle, mapHashes); err != nil {
		return RunResult{}, err
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt, Layers: layers}, nil
}

func (r *Runner) scanSymbol(
	ctx context.Context,
	pb personalplaybook.PersonalWildPlaybook,
	profile profileContext,
	symbol string,
	tracker *eventmap.WindowTracker,
	proofs ProofBundle,
) ([]TriggerEvent, error) {
	rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
	bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
	if err != nil {
		return nil, err
	}
	var funding []profitmap.FundingPoint
	fundingRel := "okx_funding/" + symbol + ".jsonl"
	if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
		funding, err = eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
		if err != nil {
			return nil, fmt.Errorf("personalvalidation: funding %s: %w", symbol, err)
		}
	}
	return ScanPlaybookSymbol(bars, symbol, pb, ScanConfig{
		Context: ctx, Spec: profile.Spec, ProfileID: profile.ProfileID, Boundaries: profile.Boundaries,
		Thresholds: profile.Thresholds, BTCBars: profile.BTCBars,
		ShockClusterAt: profile.ShockCluster, Funding: funding, MaxLines: r.Config.MaxLines,
		Proofs: proofs, RequireProofObservations: !r.Config.AllowTruncatedSmoke,
	}, tracker)
}

func (r *Runner) loadReplayData(profile profileContext) (map[string][]profitmap.Bar, map[string][]profitmap.FundingPoint, error) {
	barsBySymbol := map[string][]profitmap.Bar{}
	fundingBySymbol := map[string][]profitmap.FundingPoint{}
	for _, symbol := range profile.Symbols {
		rel := profile.Spec.PrimaryFamily + "/" + symbol + ".jsonl"
		bars, err := eventmap.LoadBars(filepath.Join(r.Config.HistoryRoot, rel), r.Config.MaxLines)
		if err != nil {
			return nil, nil, err
		}
		barsBySymbol[symbol] = bars
		fundingRel := "okx_funding/" + symbol + ".jsonl"
		if stats, declared := eventmap.CatalogFile(r.Catalog, fundingRel); declared && !stats.Empty {
			funding, err := eventmap.LoadSymbolFunding(r.Config.HistoryRoot, symbol, r.Config.MaxLines)
			if err != nil {
				return nil, nil, err
			}
			fundingBySymbol[symbol] = funding
		}
	}
	return barsBySymbol, fundingBySymbol, nil
}

func (r *Runner) buildProfileContext(profileID string, thresholds eventmap.FrozenThresholds) (profileContext, error) {
	spec, err := eventmap.ProfileSpecFor(profileID)
	if err != nil {
		return profileContext{}, err
	}
	selection, err := eventmap.SelectPrimaryFiles(r.Catalog, profileID, r.Config.MaxFiles, 0)
	if err != nil {
		return profileContext{}, err
	}
	if err := RequireFullCatalogSelection(r.Catalog, profileID, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke); err != nil {
		return profileContext{}, err
	}
	symbols := make([]string, len(selection.Files))
	for i := range selection.Files {
		symbols[i] = selection.Files[i].Symbol
	}
	btcRel := spec.PrimaryFamily + "/BTC-USDT-SWAP.jsonl"
	if profileID == eventmap.ProfileOKX1H {
		btcRel = "okx_1h/BTC-USDT-SWAP.jsonl"
	} else if profileID == eventmap.ProfileOKX5M {
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
	shockIndex := eventmap.BuildShockIndex(btcBars, spec, thresholds)
	return profileContext{
		ProfileID: profileID, Spec: spec, Boundaries: boundaries,
		Thresholds: thresholds, BTCBars: btcBars, ShockCluster: shockIndex.Lookup, Symbols: symbols,
	}, nil
}

func (r *Runner) saveProgressCheckpoint(
	nextPlaybook int,
	nextSymbolByPlaybook map[string]int,
	linesProcessed int64,
	done bool,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation: Generation, ProtocolHash: ProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash, ConfigHash: cfgHash, CatalogHash: r.Catalog.DataHash,
		PlaybookManifestHash: bundle.Manifest.ManifestHash, PlaybookHash: bundle.Manifest.PlaybookHash,
		ExperimentHash: bundle.Manifest.ExperimentHash, ConstitutionHash: r.Config.ConstitutionHash,
		ReverseTapesManifestHash: r.Config.ReverseTapesManifestHash,
		AnnouncementManifestHash: r.Config.AnnouncementManifestHash,
		MapManifestHashes: cloneStringMap(mapManifestHashes),
		OutputDir: r.Config.OutputDir, NextPlaybookIndex: nextPlaybook,
		NextSymbolIndexByPlaybook: nextSymbolByPlaybook, LinesProcessed: linesProcessed, Done: done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

func (r *Runner) loadCompleted() (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, OutputReportFile))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, OutputSurvivorsFile))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, OutputReceiptFile))
	if err != nil {
		return RunResult{}, err
	}
	var receipt ValidationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, OutputLayerFile))
	if err != nil {
		return RunResult{}, err
	}
	var layers LayerSummary
	if err := json.Unmarshal(raw, &layers); err != nil {
		return RunResult{}, err
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt, Layers: layers}, nil
}
