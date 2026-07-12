package validation

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
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
)

// Runner orchestrates validation screening with checkpoint resume.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	Fingerprint      FingerprintBinding
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

// Run executes bounded validation replay for frozen Event Playbooks.
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

	bundle, err := LoadPlaybookBundle(r.Config.PlaybookOutputDir)
	if err != nil {
		return RunResult{}, err
	}
	for _, pb := range bundle.Playbooks {
		if err := playbook.ValidatePlaybook(pb); err != nil {
			return RunResult{}, fmt.Errorf("frozen playbook %s: %w", pb.PlaybookHash, err)
		}
	}

	mapManifestHashes := map[string]string{}
	mapArtifactHashes := map[string]string{}
	residualBundles := make([]playbook.ResidualMapBundle, 0, len(r.Config.ResidualMapDirs))
	profilesByID := map[string]profileContext{}
	for _, dir := range r.Config.ResidualMapDirs {
		mapBundle, err := playbook.LoadResidualMapBundle(dir)
		if err != nil {
			return RunResult{}, err
		}
		residualBundles = append(residualBundles, mapBundle)
		mapManifestHashes[mapBundle.Manifest.ProfileID] = mapBundle.Manifest.ManifestHash
		for name, hash := range mapBundle.Manifest.ArtifactHashes {
			mapArtifactHashes[mapBundle.Manifest.ProfileID+"/"+name] = hash
		}
		if _, ok := profilesByID[mapBundle.Manifest.ProfileID]; ok {
			return RunResult{}, fmt.Errorf("validation: duplicate profile map %q", mapBundle.Manifest.ProfileID)
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
	if err := playbook.VerifyCatalogBinding(
		r.Catalog, r.Config.HistoryRoot, residualBundles,
		r.Config.MaxFiles, r.Config.MaxLines, r.Config.AllowTruncatedSmoke,
	); err != nil {
		return RunResult{}, err
	}
	if err := VerifyFormalHistoryScope(r.Catalog, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke, residualBundles); err != nil {
		return RunResult{}, err
	}

	fpBinding, err := LoadFingerprintBinding(r.Config.FingerprintPath)
	if err != nil {
		return RunResult{}, err
	}
	if err := VerifyFingerprintBinding(fpBinding); err != nil {
		return RunResult{}, err
	}
	if r.Config.FingerprintHash != fpBinding.Hash {
		return RunResult{}, fmt.Errorf("validation: fingerprint hash binding mismatch")
	}
	r.Fingerprint = fpBinding

	tierOutcomes := map[string][]TierResult{}
	degeneracyByPlaybook := map[string]DegeneracySummary{}
	startPlaybook := 0
	nextSymbolByPlaybook := map[string]int{}
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
		startPlaybook = cp.NextPlaybookIndex
		nextSymbolByPlaybook = cp.NextSymbolIndexByPlaybook
		linesProcessed = cp.LinesProcessed
	}

	canonicalDataHashes := map[string]string{
		"catalog_hash":            r.Catalog.DataHash,
		"playbook_hash":           bundle.Manifest.PlaybookHash,
		"experiment_hash":         bundle.Manifest.ExperimentHash,
		"playbook_manifest_hash":  bundle.Manifest.ManifestHash,
	}

	for pbIdx := startPlaybook; pbIdx < len(bundle.Playbooks); pbIdx++ {
		pb := bundle.Playbooks[pbIdx]
		profile, ok := profilesByID[pb.ProfileID]
		if !ok {
			return RunResult{}, fmt.Errorf("validation: missing map profile %q for playbook %s", pb.ProfileID, pb.PlaybookHash)
		}
		startSymbol := nextSymbolByPlaybook[pb.PlaybookHash]
		tracker := eventmap.NewWindowTracker()
		var allEvents []ReplayEvent
		for symIdx := startSymbol; symIdx < len(profile.Symbols); symIdx++ {
			select {
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			default:
			}
			symbol := profile.Symbols[symIdx]
			events, err := r.scanSymbol(ctx, pb, profile, symbol, tracker)
			if err != nil {
				return RunResult{}, err
			}
			allEvents = append(allEvents, events...)
			linesProcessed += int64(len(profile.BTCBars))
			nextSymbolByPlaybook[pb.PlaybookHash] = symIdx + 1
			if err := r.saveProgressCheckpoint(pbIdx, nextSymbolByPlaybook, linesProcessed, false, bundle, mapManifestHashes); err != nil {
				return RunResult{}, err
			}
			if r.OnProgress != nil {
				r.OnProgress(linesProcessed, pb.PlaybookHash, symbol)
			}
		}

		degSummary, err := CheckBrainDegeneracy(allEvents, r.Fingerprint.Fingerprint, pb.ProfileID)
		if err != nil {
			return RunResult{}, err
		}
		degeneracyByPlaybook[pb.PlaybookHash] = degSummary

		barsBySymbol, fundingBySymbol, err := r.loadReplayData(profile)
		if err != nil {
			return RunResult{}, err
		}
		var tierResults []TierResult
		for _, equity := range protocol.ValidationInitialEquities {
			tier := int(equity)
			sizing := FrozenSizingFromEvents(allEvents, tier)
			if sizing.ConservativeMeanBPS <= 0 {
				sizing.ReferenceNotional = equity
			}
			scenarios, daily, _, replayStats := ReplayTierEvents(allEvents, barsBySymbol, fundingBySymbol, equity, sizing, pb, r.Fingerprint.Fingerprint)
			nonzero, positive := shockBlockSigns(daily.ShockDayPnL)
			status, checks, reason := EvaluateTierGatesWithShockSign(replayStats, scenarios, nonzero, positive)
			tierResults = append(tierResults, TierResult{
				CapitalTier: tier, Status: status, FailReason: reason,
				Replay: replayStats, Scenarios: scenarios, GateChecks: checks,
				FrozenConservativeMeanBPS: sizing.ConservativeMeanBPS,
				StressConservativeMeanBPS: sizing.StressConservativeMeanBPS,
			})
		}
		tierOutcomes[pb.PlaybookHash] = tierResults
	}

	results := BuildPlaybookResults(bundle.Playbooks, tierOutcomes, bundle.Ledger, degeneracyByPlaybook)
	report, err := BuildReport(r.Config, bundle, mapManifestHashes, results, bundle.Ledger)
	if err != nil {
		return RunResult{}, err
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(r.Config, bundle, mapManifestHashes, mapArtifactHashes, canonicalDataHashes, report, survivors)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(r.Config.OutputDir, report, survivors, receipt); err != nil {
		return RunResult{}, err
	}
	if err := r.saveProgressCheckpoint(len(bundle.Playbooks), nextSymbolByPlaybook, linesProcessed, true, bundle, mapManifestHashes); err != nil {
		return RunResult{}, err
	}
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}

func (r *Runner) scanSymbol(
	ctx context.Context,
	pb playbook.FrozenEventPlaybook,
	profile profileContext,
	symbol string,
	tracker *eventmap.WindowTracker,
) ([]ReplayEvent, error) {
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
			return nil, fmt.Errorf("validation: funding %s: %w", symbol, err)
		}
	}
	return ScanPlaybookSymbol(bars, symbol, pb, ScanConfig{
		Context: ctx, Spec: profile.Spec, ProfileID: profile.ProfileID, Boundaries: profile.Boundaries,
		Thresholds: profile.Thresholds, BTCBars: profile.BTCBars,
		ShockClusterAt: profile.ShockCluster, Funding: funding, MaxLines: r.Config.MaxLines,
		Fingerprint: r.Fingerprint.Fingerprint,
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

func (r *Runner) buildProfileContext(bundle playbook.ResidualMapBundle) (profileContext, error) {
	spec, err := eventmap.ProfileSpecFor(bundle.Manifest.ProfileID)
	if err != nil {
		return profileContext{}, err
	}
	selection, err := eventmap.SelectPrimaryFiles(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, 0)
	if err != nil {
		return profileContext{}, err
	}
	if err := RequireFullCatalogSelection(r.Catalog, bundle.Manifest.ProfileID, r.Config.MaxFiles, r.Config.AllowTruncatedSmoke); err != nil {
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
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash, CatalogHash: r.Catalog.DataHash,
		PlaybookManifestHash: bundle.Manifest.ManifestHash, PlaybookHash: bundle.Manifest.PlaybookHash,
		ExperimentHash: bundle.Manifest.ExperimentHash, MapManifestHashes: cloneStringMap(mapManifestHashes),
		OutputDir: r.Config.OutputDir, NextPlaybookIndex: nextPlaybook,
		NextSymbolIndexByPlaybook: nextSymbolByPlaybook, LinesProcessed: linesProcessed, Done: done,
	}
	return SaveCheckpoint(r.CheckpointPath, cp)
}

func (r *Runner) loadCompleted(
	bundle PlaybookBundle,
	mapManifestHashes, mapArtifactHashes map[string]string,
) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_validation_report.json"))
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
	raw, err = os.ReadFile(filepath.Join(r.Config.OutputDir, "g10_validation_survivors.json"))
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
	_ = bundle
	_ = mapManifestHashes
	_ = mapArtifactHashes
	return RunResult{Report: report, Survivors: survivors, Receipt: receipt}, nil
}
