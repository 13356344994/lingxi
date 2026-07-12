package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg3/protocol"
)

const testHMACKey = "0123456789abcdef0123456789abcdef"
const testRuleSkyPattern = "ret=low/te=*/rv=mid/dd=*/fund=high"
const testEventFullSky = "ret=low/te=high/rv=mid/dd=low/fund=high"

func TestMain(m *testing.M) {
	verifyCanonicalForwardWF = func(
		_ context.Context,
		_ string,
		_ historydata.HistoryCatalog,
		_ string,
		_ g3reverse.Boundaries,
		_ sky.Cutpoints,
		_ g3reverse.Manifest,
	) error {
		return nil
	}
	validateCatalog = func(historydata.HistoryCatalog) error { return nil }
	os.Exit(m.Run())
}

func TestCheckpointRejectsProvenanceTamper(t *testing.T) {
	root := t.TempDir()
	binding := checkpointBinding{
		EventManifestHash: "em", SearchManifestHash: "sm", RulesHash: "rh",
		DiscoveryReceiptHash: "dr",
		LedgerHash:           "lh", RecheckReportHash: "rr", NominationsHash: "nh",
		ForwardEventHash: "fh", WalkForwardEventHash: "wh",
		PartitionBoundariesHash: "pb", CatalogDataHash: protocol.HistoryDataHash,
		ConfigHash: "cfg", InputHash: "in", OutputDir: root, MaxLines: 1000,
	}
	cp := newCheckpoint(binding)
	path := filepath.Join(root, "checkpoint.json")
	if err := saveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := loadCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	drift := binding
	drift.RulesHash = "tampered"
	if err := loaded.validateBinding(drift); err == nil {
		t.Fatal("expected checkpoint binding drift rejection")
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["checkpoint_hash"] = "tampered"
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCheckpoint(path); err == nil {
		t.Fatal("checkpoint hash tamper must fail")
	}
}

func TestPartitionFilesRejectMisplacedEvents(t *testing.T) {
	start := time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	valid := []g3reverse.Event{{ID: "ok", DecisionTime: start.Add(time.Hour)}}
	if err := validateEventsInWindow(valid, start, end, "forward"); err != nil {
		t.Fatal(err)
	}
	misplaced := []g3reverse.Event{{ID: "bad", DecisionTime: end}}
	if err := validateEventsInWindow(misplaced, start, end, "forward"); err == nil {
		t.Fatal("event on/after partition end must fail closed")
	}
}

func TestZeroCandidatesOutputsNOGO(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{
		includeRecheckPass: false,
	})
	outputDir := filepath.Join(root, "out")
	checkpoint := filepath.Join(root, "checkpoint.json")
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir,
		"--discovery-dir", discoveryDir,
		"--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"),
		"--history-catalog", catalogPath,
		"--output-dir", outputDir,
		"--checkpoint", checkpoint,
		"--max-lines", "10000",
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := readStageSummary(filepath.Join(outputDir, "g3_stage_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RegisteredCandidates != 0 || summary.SystemStatus != "NO_GO" || summary.ExamActions != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	emptyRegistry, err := registry.Load(filepath.Join(outputDir, "g3_candidate_registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if emptyRegistry.Hash == "" || emptyRegistry.Candidates == nil || len(emptyRegistry.Candidates) != 0 {
		t.Fatalf("empty registry must be sealed with [] candidates: %+v", emptyRegistry)
	}
}

func TestDiscoveryReceiptTamperHardFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{})
	var receipt g3reverse.DiscoveryReceipt
	receiptPath := filepath.Join(discoveryDir, "discovery_receipt.json")
	if err := readJSON(receiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Signature = "00"
	writeJSON(t, receiptPath, receipt)
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
		"--output-dir", filepath.Join(root, "out"), "--checkpoint", filepath.Join(root, "checkpoint.json"),
		"--max-lines", "10000",
	}); err == nil {
		t.Fatal("tampered discovery receipt must hard fail")
	}
}

func TestDiscoveryArtifactDriftAgainstReceiptHardFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{})
	writeJSON(t, filepath.Join(discoveryDir, "control_summary.json"), map[string]any{
		"schema_version": "tampered", "exam_actions": 0,
	})
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
		"--output-dir", filepath.Join(root, "out"), "--checkpoint", filepath.Join(root, "checkpoint.json"),
		"--max-lines", "10000",
	}); err == nil {
		t.Fatal("discovery artifact drift against signed receipt must hard fail")
	}
}

func TestNarrativeAndAmbiguousAttributionRecorded(t *testing.T) {
	tests := []struct {
		name       string
		features   []string
		wantStatus string
	}{
		{"narrative", []string{"symbol_return_1h"}, "NARRATIVE_ONLY"},
		{"ambiguous", []string{"rubik_lsratio_1h", "rubik_taker_imbalance_1h"}, "AMBIGUOUS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
			bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{
				includeRecheckPass: true, recheckPass: true, features: tc.features,
			})
			outputDir := filepath.Join(root, "out")
			if err := run(context.Background(), []string{
				"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
				"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
				"--output-dir", outputDir, "--checkpoint", filepath.Join(root, "checkpoint.json"),
				"--max-lines", "10000",
			}); err != nil {
				t.Fatal(err)
			}
			var report attributionReportFile
			if err := readJSON(filepath.Join(outputDir, "g3_attribution_report.json"), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Decisions) != 1 || report.Decisions[0].Status != tc.wantStatus ||
				report.Closed != 1 || report.Attributed != 0 {
				t.Fatalf("attribution report=%+v", report)
			}
			summary, err := readStageSummary(filepath.Join(outputDir, "g3_stage_summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			if summary.ClosedCount != 1 || summary.RegisteredCandidates != 0 ||
				(tc.wantStatus == "NARRATIVE_ONLY" && summary.NarrativeOnlyCount != 1) ||
				(tc.wantStatus == "AMBIGUOUS" && summary.AmbiguousCount != 1) {
				t.Fatalf("stage summary attribution counts=%+v", summary)
			}
		})
	}
}

func TestRecheckFailNominationDoesNotRegister(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{
		includeRecheckPass: true, recheckPass: false,
	})
	outputDir := filepath.Join(root, "out")
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
		"--output-dir", outputDir, "--checkpoint", filepath.Join(root, "checkpoint.json"),
		"--max-lines", "10000",
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := readStageSummary(filepath.Join(outputDir, "g3_stage_summary.json"))
	if summary.RegisteredCandidates != 0 {
		t.Fatalf("recheck fail must not register: %+v", summary)
	}
}

func TestForwardFailureClosesRegisteredCandidate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{
		includeRecheckPass: true, recheckPass: true, forwardNet: -5, wfNet: 20,
	})
	outputDir := filepath.Join(root, "out")
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
		"--output-dir", outputDir, "--checkpoint", filepath.Join(root, "checkpoint.json"),
		"--max-lines", "10000",
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := readStageSummary(filepath.Join(outputDir, "g3_stage_summary.json"))
	if summary.RegisteredCandidates != 1 || summary.ResearchPassLeaves != 0 || summary.SystemStatus != "NO_GO" {
		t.Fatalf("forward fail must close: %+v", summary)
	}
}

func TestResearchPassWhenForwardAndWFPass(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	bundleDir, discoveryDir, inductionDir, catalogPath := writeResearchFixture(t, root, fixtureOptions{
		includeRecheckPass: true, recheckPass: true, forwardNet: 20, wfNet: 20,
	})
	outputDir := filepath.Join(root, "out")
	if err := run(context.Background(), []string{
		"--bundle-dir", bundleDir, "--discovery-dir", discoveryDir, "--induction-dir", inductionDir,
		"--history-root", filepath.Join(root, "history"), "--history-catalog", catalogPath,
		"--output-dir", outputDir, "--checkpoint", filepath.Join(root, "checkpoint.json"),
		"--max-lines", "10000",
	}); err != nil {
		t.Fatal(err)
	}
	summary, _ := readStageSummary(filepath.Join(outputDir, "g3_stage_summary.json"))
	if summary.RegisteredCandidates != 1 || summary.ResearchPassLeaves != 1 || summary.SystemStatus != "RESEARCH_PASS" {
		t.Fatalf("expected research pass: %+v", summary)
	}
}

type fixtureOptions struct {
	includeRecheckPass bool
	recheckPass        bool
	forwardNet         float64
	wfNet              float64
	features           []string
}

func writeResearchFixture(t *testing.T, root string, opts fixtureOptions) (bundleDir, discoveryDir, inductionDir, catalogPath string) {
	t.Helper()
	if !opts.includeRecheckPass {
		opts.recheckPass = false
	}
	if opts.recheckPass && opts.forwardNet == 0 {
		opts.forwardNet = 20
		opts.wfNet = 20
	}
	if opts.forwardNet == 0 {
		opts.forwardNet = -10
	}
	if opts.wfNet == 0 {
		opts.wfNet = -10
	}

	key := []byte(testHMACKey)
	bundleDir = filepath.Join(root, "bundle")
	discoveryDir = filepath.Join(root, "discovery")
	inductionDir = filepath.Join(root, "induction")
	for _, dir := range []string{
		filepath.Join(bundleDir, "g3_forward"),
		filepath.Join(bundleDir, "g3_wf"),
		discoveryDir, inductionDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if len(opts.features) == 0 {
		opts.features = []string{"rubik_lsratio_1h"}
	}
	conditions := make([]g3reverse.RuleCondition, 0, len(opts.features))
	for _, feature := range opts.features {
		conditions = append(conditions, g3reverse.RuleCondition{
			FeatureKey: feature, Op: g3reverse.OpGTE, Threshold: 0.5, Quantile: 0.5,
		})
	}
	rule := g3reverse.Rule{
		Ordinal: 1, SkyState: testRuleSkyPattern, SorterDomain: "liquid",
		Horizon: g3reverse.Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: conditions,
	}
	hash, err := g3reverse.CanonicalRuleHash(rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.CanonicalHash = hash

	ledger, err := g3reverse.SealLedger(g3reverse.AlphaLedger{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Entries: []g3reverse.LedgerEntry{{
			Ordinal: 1, CanonicalRuleHash: hash,
			DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
			AlphaStage:                    "reverse_induction", Status: g3reverse.LedgerStatusRegistered,
			SkyState: rule.SkyState, SorterDomain: rule.SorterDomain,
			Horizon: rule.Horizon, Direction: rule.Direction, RuleKind: "single",
			Conditions: rule.Conditions, SourceWinnerEventIDs: []string{"w1"},
			AvailableFeatureSampleCount: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	boundaries := g3reverse.Boundaries{
		Start: base, InductionEnd: base.Add(400 * time.Hour), RecheckStart: base.Add(568 * time.Hour),
		RecheckEnd: base.Add(600 * time.Hour), ForwardStart: base.Add(768 * time.Hour),
		ForwardEnd: base.Add(900 * time.Hour), WFStart: base.Add(1068 * time.Hour), End: base.Add(6100 * time.Hour),
		EmbargoHours: 168,
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		t.Fatal(err)
	}

	mkEvent := func(id, cluster string, when time.Time, net float64) g3reverse.Event {
		return g3reverse.Event{
			ID: id, Symbol: "BTCUSDT", Cluster: cluster,
			SkyState: testEventFullSky, SorterDomain: "liquid", LiquidityBucket: "liq_6",
			Horizon: g3reverse.Horizon1h, DecisionTime: when, FeatureAsOf: when.Add(-time.Minute),
			EntryTime: when, ExitTime: when.Add(time.Hour), EntryPrice: 100, ExitPrice: 100,
			Direction: mechanism.DirectionLong,
			Features: func() map[string]float64 {
				values := map[string]float64{}
				for _, feature := range opts.features {
					values[feature] = 1.0
				}
				return values
			}(),
			NetOutcomeBPS: net, OutcomeClass: g3reverse.OutcomeWinner,
		}
	}
	var forwardEvents, wfEvents []g3reverse.Event
	for i := 0; i < 35; i++ {
		cluster := fmt.Sprintf("f%02d", i)
		forwardEvents = append(forwardEvents, mkEvent(
			"f-"+cluster, cluster, boundaries.ForwardStart.Add(time.Duration(i)*time.Hour), opts.forwardNet,
		))
	}
	for i := 0; i < 40; i++ {
		cluster := fmt.Sprintf("w%02d", i)
		wfEvents = append(wfEvents, mkEvent(
			"w-"+cluster, cluster, boundaries.WFStart.Add(time.Duration(i)*120*time.Hour), opts.wfNet,
		))
	}
	forwardHash, err := g3reverse.HashEvents(forwardEvents)
	if err != nil {
		t.Fatal(err)
	}
	wfHash, err := g3reverse.HashEvents(wfEvents)
	if err != nil {
		t.Fatal(err)
	}

	catalog := historydata.HistoryCatalog{
		DatasetID: "fixture", SchemaHash: "schema-fixture", DataHash: protocol.HistoryDataHash,
	}
	catalogPath = filepath.Join(root, "catalog.json")
	writeJSON(t, catalogPath, catalog)

	cutpoints := sky.Cutpoints{Frozen: true, Quantiles: protocol.SkyQuantiles, Partition: "reverse_induction"}
	configHash, err := g3reverse.ConfigHash(1, 100, 1000, 1000, "sky-test", boundariesHash)
	if err != nil {
		t.Fatal(err)
	}

	eventManifest := g3reverse.Manifest{
		SchemaVersion: g3reverse.SchemaVersion, GeneratorID: g3reverse.GeneratorID,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		SourceDataHash: protocol.HistoryDataHash, SourceSchemaHash: catalog.SchemaHash,
		SourceDatasetID: catalog.DatasetID, DerivationHash: "deriv-test",
		PartitionBoundariesHash: boundariesHash,
		InductionEventHash:      "induction-unused", RecheckEventHash: "recheck-unused",
		ForwardEventHash: forwardHash, WalkForwardEventHash: wfHash,
		WinnerIndexHash: "winner-test", EventCount: len(forwardEvents) + len(wfEvents),
		ForwardEventCount: len(forwardEvents), WalkForwardEventCount: len(wfEvents),
		MaxSymbols: 1, MaxFundingLines: 100, MaxPriceLines: 1000, MaxFamilyLines: 1000,
		ConfigHash: configHash, SkyCutpointsHash: "sky-test",
	}
	eventManifest, err = g3reverse.SealManifest(eventManifest)
	if err != nil {
		t.Fatal(err)
	}
	eventManifest, err = g3reverse.SignManifest(eventManifest, key, "test")
	if err != nil {
		t.Fatal(err)
	}

	searchManifest, err := g3reverse.NewSearchManifest([]g3reverse.Rule{rule}, ledger, g3reverse.SearchManifestBindings{
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "deriv-test",
		EventManifestHash: eventManifest.ManifestHash, InductionEventHash: "induction-unused",
		WinnerIndexHash: "winner-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	searchManifest, err = g3reverse.SignSearchManifest(searchManifest, key, "test")
	if err != nil {
		t.Fatal(err)
	}

	nomination := g3reverse.Nomination{
		RuleOrdinal: 1, CanonicalRuleHash: hash,
		Origin: g3reverse.OriginReverseInduction, Kind: g3reverse.KindCandidateNomination,
		SearchManifestHash: searchManifest.ManifestHash, SearchSpaceID: protocol.ReverseInductionSearchSpaceID,
		ActualDerivedRuleCount: 1, Rank: 1, MatchingRule: protocol.ReverseInductionMatchRule,
		SkyState: rule.SkyState, SorterDomain: rule.SorterDomain, Horizon: rule.Horizon,
		Direction: string(rule.Direction), ControlCount: 10,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
	recheckStatus := g3reverse.RecheckVerdictClosed
	if opts.includeRecheckPass && opts.recheckPass {
		recheckStatus = g3reverse.RecheckVerdictPass
	}
	recheckReport := g3reverse.RecheckReport{
		Partition: string(g3reverse.PartitionRecheck), RecheckDataHash: "recheck-data",
		SearchManifestHash: searchManifest.ManifestHash, RecheckAlpha: protocol.RecheckAlpha,
		MinSamples: protocol.G3MinRecheckSamples, MinClusters: protocol.G3MinRecheckClusters,
		NominationCount: 1, ExamActions: protocol.ExamActions,
		Verdicts: []g3reverse.RecheckVerdict{{
			CanonicalRuleHash: hash, Status: recheckStatus, RecheckDataHash: "recheck-data",
			SearchManifestHash: searchManifest.ManifestHash,
			Generation:         protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		}},
	}

	writeJSONL(t, filepath.Join(bundleDir, "g3_forward", "events.jsonl"), forwardEvents)
	writeJSONL(t, filepath.Join(bundleDir, "g3_wf", "events.jsonl"), wfEvents)
	writeJSON(t, filepath.Join(bundleDir, "event_manifest.json"), eventManifest)
	writeJSON(t, filepath.Join(bundleDir, "partition_boundaries.json"), boundaries)
	writeJSON(t, filepath.Join(bundleDir, "sky_cutpoints.json"), cutpoints)
	writeJSON(t, filepath.Join(inductionDir, "search_manifest.json"), searchManifest)
	writeJSON(t, filepath.Join(inductionDir, "rules.json"), []g3reverse.Rule{rule})
	writeJSON(t, filepath.Join(inductionDir, "alpha_ledger.json"), ledger)
	writeJSON(t, filepath.Join(discoveryDir, "induction_evaluation.json"), map[string]any{
		"schema_version": "fixture", "exam_actions": 0,
	})
	writeJSON(t, filepath.Join(discoveryDir, "recheck_report.json"), recheckReport)
	writeJSON(t, filepath.Join(discoveryDir, "nominations.json"), struct {
		Nominations []g3reverse.Nomination `json:"nominations"`
		ExamActions int                    `json:"exam_actions"`
	}{[]g3reverse.Nomination{nomination}, protocol.ExamActions})
	writeJSON(t, filepath.Join(discoveryDir, "control_summary.json"), map[string]any{
		"schema_version": "fixture", "exam_actions": 0,
	})
	eventManifestFileHash, err := hashJSONFile(filepath.Join(bundleDir, "event_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	searchManifestFileHash, err := hashJSONFile(filepath.Join(inductionDir, "search_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	inductionEvaluationHash, _ := hashJSONFile(filepath.Join(discoveryDir, "induction_evaluation.json"))
	recheckReportHash, _ := hashJSONFile(filepath.Join(discoveryDir, "recheck_report.json"))
	nominationsHash, _ := hashJSONFile(filepath.Join(discoveryDir, "nominations.json"))
	controlSummaryHash, _ := hashJSONFile(filepath.Join(discoveryDir, "control_summary.json"))
	receipt, err := g3reverse.NewDiscoveryReceipt(g3reverse.DiscoveryReceiptBindings{
		EventManifestHash: eventManifestFileHash, SearchManifestHash: searchManifestFileHash,
		InductionEvaluationHash: inductionEvaluationHash, RecheckReportHash: recheckReportHash,
		NominationsHash: nominationsHash, ControlSummaryHash: controlSummaryHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = g3reverse.SignDiscoveryReceipt(receipt, key, "test")
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(discoveryDir, "discovery_receipt.json"), receipt)
	if err := os.MkdirAll(filepath.Join(root, "history"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bundleDir, discoveryDir, inductionDir, catalogPath
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSONL(t *testing.T, path string, events []g3reverse.Event) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
