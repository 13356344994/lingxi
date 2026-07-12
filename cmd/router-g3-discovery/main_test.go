package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
)

const testHMACKey = "0123456789abcdef0123456789abcdef"

const (
	testRuleSkyPattern = "ret=low/te=*/rv=high/dd=*/fund=mid"
	testFullSkyState   = "ret=low/te=mid/rv=high/dd=low/fund=mid"
)

func TestCheckpointRejectsBindingDrift(t *testing.T) {
	root := t.TempDir()
	binding := checkpointBinding{
		EventManifestHash: "em", SearchManifestHash: "sm", RulesHash: "rh",
		LedgerHash: "lh", InductionEventHash: "ih", RecheckEventHash: "rch",
		PartitionBoundariesHash: "pb", ConfigHash: "cfg", InputHash: "in",
		OutputDir: root, MaxLines: 1000,
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
}

func writeDiscoveryFixture(t *testing.T, root string) (bundleDir, inductionDir string) {
	t.Helper()
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", testHMACKey)
	key := []byte(testHMACKey)

	bundleDir = filepath.Join(root, "bundle")
	inductionDir = filepath.Join(root, "induction")
	for _, dir := range []string{
		filepath.Join(bundleDir, "g3_induction"),
		filepath.Join(bundleDir, "g3_recheck"),
		inductionDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rule := g3reverse.Rule{
		Ordinal: 1, SkyState: testRuleSkyPattern, SorterDomain: "liquid",
		Horizon: g3reverse.Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []g3reverse.RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: g3reverse.OpGTE, Threshold: 0.8, Quantile: 0.5,
		}},
	}
	hash, err := g3reverse.CanonicalRuleHash(rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.CanonicalHash = hash
	ledger := g3reverse.AlphaLedger{
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
	}
	ledger, err = g3reverse.SealLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, cluster string, offset time.Duration, feature, net float64, class g3reverse.OutcomeClass) g3reverse.Event {
		dt := base.Add(offset)
		return g3reverse.Event{
			ID: id, Symbol: "BTCUSDT", Cluster: cluster,
			SkyState: testFullSkyState, SorterDomain: "liquid", LiquidityBucket: "liq_6",
			Horizon: g3reverse.Horizon1h, DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
			EntryTime: dt, ExitTime: dt.Add(time.Hour), EntryPrice: 100, ExitPrice: 100,
			Direction:     mechanism.DirectionLong,
			Features:      map[string]float64{"symbol_return_1h": feature},
			NetOutcomeBPS: net, OutcomeClass: class,
		}
	}
	var inductionEvents []g3reverse.Event
	for i := 0; i < protocol.ReverseInductionExactPermMax+1; i++ {
		cluster := fmt.Sprintf("c%02d", i)
		off := time.Duration(i) * 12 * time.Hour
		inductionEvents = append(inductionEvents,
			mk("w"+cluster, cluster, off, 0.9, 40, g3reverse.OutcomeWinner),
			mk("tl"+cluster, cluster, off+30*time.Second, 0.9, -10, g3reverse.OutcomeLoss),
			mk("l"+cluster, cluster, off+time.Minute, 0.1, -30, g3reverse.OutcomeLoss),
			mk("n"+cluster, cluster, off+2*time.Minute, 0.3, 0, g3reverse.OutcomeNeutral),
		)
	}
	recheckEvents := make([]g3reverse.Event, len(inductionEvents))
	for i, event := range inductionEvents {
		event.DecisionTime = base.Add(569*time.Hour + time.Duration(i)*time.Minute)
		event.FeatureAsOf = event.DecisionTime.Add(-time.Minute)
		event.EntryTime = event.DecisionTime
		event.ExitTime = event.DecisionTime.Add(time.Hour)
		recheckEvents[i] = event
	}

	inductionHash, err := g3reverse.HashEvents(inductionEvents)
	if err != nil {
		t.Fatal(err)
	}
	recheckHash, err := g3reverse.HashEvents(recheckEvents)
	if err != nil {
		t.Fatal(err)
	}
	boundaries := g3reverse.Boundaries{
		Start: base, InductionEnd: base.Add(400 * time.Hour), RecheckStart: base.Add(568 * time.Hour),
		RecheckEnd: base.Add(600 * time.Hour), ForwardStart: base.Add(768 * time.Hour),
		ForwardEnd: base.Add(800 * time.Hour), WFStart: base.Add(968 * time.Hour), End: base.Add(1000 * time.Hour),
		EmbargoHours: 168,
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		t.Fatal(err)
	}
	configHash, err := g3reverse.ConfigHash(1, 100, 1000, 1000, "sky-test", boundariesHash)
	if err != nil {
		t.Fatal(err)
	}

	eventManifest := g3reverse.Manifest{
		SchemaVersion: g3reverse.SchemaVersion, GeneratorID: g3reverse.GeneratorID,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		SourceDataHash: protocol.HistoryDataHash, SourceSchemaHash: "schema-test",
		SourceDatasetID: "dataset-test", DerivationHash: "deriv-test",
		PartitionBoundariesHash: boundariesHash,
		InductionEventHash:      inductionHash, RecheckEventHash: recheckHash,
		ForwardEventHash: "forward-unused", WalkForwardEventHash: "wf-unused",
		WinnerIndexHash: "winner-test", EventCount: len(inductionEvents) + len(recheckEvents),
		InductionEventCount: len(inductionEvents), RecheckEventCount: len(recheckEvents),
		ForwardEventCount: 0, WalkForwardEventCount: 0,
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
		EventManifestHash: eventManifest.ManifestHash, InductionEventHash: inductionHash,
		WinnerIndexHash: "winner-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	searchManifest, err = g3reverse.SignSearchManifest(searchManifest, key, "test")
	if err != nil {
		t.Fatal(err)
	}

	writeJSONL(t, filepath.Join(bundleDir, "g3_induction", "events.jsonl"), inductionEvents)
	writeJSONL(t, filepath.Join(bundleDir, "g3_recheck", "events.jsonl"), recheckEvents)
	writeJSON(t, filepath.Join(bundleDir, "event_manifest.json"), eventManifest)
	writeJSON(t, filepath.Join(bundleDir, "partition_boundaries.json"), boundaries)
	writeJSON(t, filepath.Join(inductionDir, "search_manifest.json"), searchManifest)
	writeJSON(t, filepath.Join(inductionDir, "rules.json"), []g3reverse.Rule{rule})
	writeJSON(t, filepath.Join(inductionDir, "alpha_ledger.json"), ledger)
	return bundleDir, inductionDir
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

func TestDiscoveryCLIWritesArtifacts(t *testing.T) {
	root := t.TempDir()
	bundleDir, inductionDir := writeDiscoveryFixture(t, root)
	outputDir := filepath.Join(root, "out")
	checkpoint := filepath.Join(root, "checkpoint.json")
	if err := run([]string{
		"--bundle-dir", bundleDir,
		"--induction-dir", inductionDir,
		"--output-dir", outputDir,
		"--checkpoint", checkpoint,
		"--max-lines", "10000",
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"induction_evaluation.json", "recheck_report.json", "nominations.json",
		"control_summary.json", "discovery_receipt.json",
	} {
		if _, err := os.Stat(filepath.Join(outputDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	var evalReport g3reverse.InductionEvaluationReport
	if err := readJSON(filepath.Join(outputDir, "induction_evaluation.json"), &evalReport); err != nil {
		t.Fatal(err)
	}
	if evalReport.ExamActions != 0 {
		t.Fatalf("exam_actions=%d", evalReport.ExamActions)
	}
	if len(evalReport.Nominations) == 0 {
		t.Fatal("expected at least one nomination from fixture")
	}
	for _, n := range evalReport.Nominations {
		if n.Kind != g3reverse.KindCandidateNomination {
			t.Fatalf("kind=%s", n.Kind)
		}
	}
	var receipt g3reverse.DiscoveryReceipt
	if err := readJSON(filepath.Join(outputDir, "discovery_receipt.json"), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := g3reverse.VerifyDiscoveryReceipt(receipt, []byte(testHMACKey)); err != nil {
		t.Fatal(err)
	}
	expectedHashes := map[string]string{
		filepath.Join(bundleDir, "event_manifest.json"):       receipt.EventManifestHash,
		filepath.Join(inductionDir, "search_manifest.json"):   receipt.SearchManifestHash,
		filepath.Join(outputDir, "induction_evaluation.json"): receipt.InductionEvaluationHash,
		filepath.Join(outputDir, "recheck_report.json"):       receipt.RecheckReportHash,
		filepath.Join(outputDir, "nominations.json"):          receipt.NominationsHash,
		filepath.Join(outputDir, "control_summary.json"):      receipt.ControlSummaryHash,
	}
	for path, want := range expectedHashes {
		got, err := hashJSONFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("receipt hash for %s is not canonical JSON file hash: got=%s want=%s", path, got, want)
		}
	}
}

func TestValidateEventsInWindowRejectsWrongPartition(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	base := g3reverse.Event{ID: "event", DecisionTime: start}
	if err := validateEventsInWindow([]g3reverse.Event{base}, start, end, "induction"); err != nil {
		t.Fatalf("start boundary must be included: %v", err)
	}
	for name, decisionTime := range map[string]time.Time{
		"before_start": start.Add(-time.Nanosecond),
		"at_end":       end,
		"after_end":    end.Add(time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			event := base
			event.DecisionTime = decisionTime
			if err := validateEventsInWindow([]g3reverse.Event{event}, start, end, "induction"); err == nil {
				t.Fatal("expected wrong-partition event rejection")
			}
		})
	}
}

func TestDiscoveryCLIRejectsWrongPartitionEvent(t *testing.T) {
	tests := []struct {
		name      string
		eventDir  string
		partition string
		wrongTime func(g3reverse.Boundaries) time.Time
	}{
		{"induction", "g3_induction", "induction", func(b g3reverse.Boundaries) time.Time { return b.InductionEnd }},
		{"recheck", "g3_recheck", "recheck", func(b g3reverse.Boundaries) time.Time { return b.RecheckEnd }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bundleDir, inductionDir := writeDiscoveryFixture(t, root)
			eventsPath := filepath.Join(bundleDir, test.eventDir, "events.jsonl")
			events, err := readBoundedEventJSONL(eventsPath, 10000)
			if err != nil {
				t.Fatal(err)
			}
			var boundaries g3reverse.Boundaries
			if err := readJSON(filepath.Join(bundleDir, "partition_boundaries.json"), &boundaries); err != nil {
				t.Fatal(err)
			}
			events[0].DecisionTime = test.wrongTime(boundaries)
			writeJSONL(t, eventsPath, events)

			err = run([]string{
				"--bundle-dir", bundleDir,
				"--induction-dir", inductionDir,
				"--output-dir", filepath.Join(root, "out"),
				"--checkpoint", filepath.Join(root, "checkpoint.json"),
				"--max-lines", "10000",
			})
			if err == nil || !strings.Contains(err.Error(), "outside "+test.partition+" partition") {
				t.Fatalf("expected explicit wrong-partition rejection, got %v", err)
			}
		})
	}
}

func TestDiscoveryResumeRejectsArtifactAndReceiptTamper(t *testing.T) {
	tests := map[string]func(t *testing.T, outputDir string){
		"recheck_verdict": func(t *testing.T, outputDir string) {
			path := filepath.Join(outputDir, "recheck_report.json")
			var report g3reverse.RecheckReport
			if err := readJSON(path, &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Verdicts) == 0 {
				t.Fatal("fixture must produce a recheck verdict")
			}
			report.Verdicts[0].Status = g3reverse.RecheckVerdictClosed
			writeJSON(t, path, report)
		},
		"nominations": func(t *testing.T, outputDir string) {
			path := filepath.Join(outputDir, "nominations.json")
			var payload map[string]any
			if err := readJSON(path, &payload); err != nil {
				t.Fatal(err)
			}
			payload["nominations"] = []any{}
			writeJSON(t, path, payload)
		},
		"receipt_signature": func(t *testing.T, outputDir string) {
			path := filepath.Join(outputDir, "discovery_receipt.json")
			var receipt g3reverse.DiscoveryReceipt
			if err := readJSON(path, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.Signature = "00"
			writeJSON(t, path, receipt)
		},
		"exam_actions": func(t *testing.T, outputDir string) {
			path := filepath.Join(outputDir, "discovery_receipt.json")
			var receipt g3reverse.DiscoveryReceipt
			if err := readJSON(path, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt.ExamActions = 1
			writeJSON(t, path, receipt)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bundleDir, inductionDir := writeDiscoveryFixture(t, root)
			outputDir := filepath.Join(root, "out")
			checkpoint := filepath.Join(root, "checkpoint.json")
			args := []string{
				"--bundle-dir", bundleDir,
				"--induction-dir", inductionDir,
				"--output-dir", outputDir,
				"--checkpoint", checkpoint,
				"--max-lines", "10000",
			}
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			mutate(t, outputDir)
			if err := run(args); err == nil {
				t.Fatal("expected completed discovery tamper rejection")
			}
		})
	}
}
