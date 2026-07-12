package reversetapes

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/announcements"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

func TestProtocolNoDrift(t *testing.T) {
	if DiscoveryMode != g10protocolv2.DiscoveryMode {
		t.Fatalf("discovery mode drift: %s", DiscoveryMode)
	}
	if len(ExecutionActionsCompared) != 3 {
		t.Fatal("execution actions must be triple compared")
	}
}

func TestUnfilledProfitZero(t *testing.T) {
	in := testEvalInput("touch_not_fill", false)
	in.Execution.SelectedAction = g10executionpolicy.ActionBoundedMaker
	in.Execution.MakerQuadrant = g10executionpolicy.QuadrantUnfilledSubsequentlyProfitable
	result := Evaluate(in, nil)
	if result.Tape.AbsoluteProfitUSDT != 0 {
		t.Fatalf("unfilled profit must be 0, got %v", result.Tape.AbsoluteProfitUSDT)
	}
}

func TestCertifiedExecutableTape(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	result := Evaluate(in, nil)
	if !result.Accepted {
		t.Fatalf("expected accepted: %+v", result.RejectReasons)
	}
	if result.Status != TapeStatusExecutableCertified {
		t.Fatalf("status=%s", result.Status)
	}
	if err := ValidateTape(result.Tape); err != nil {
		t.Fatal(err)
	}
	if result.Tape.ProofBinding.ExclusionProofHash == "" {
		t.Fatal("proof binding required")
	}
	if result.Tape.DecisionBefore.FrozenExecutionAction == g10executionpolicy.ActionSkip {
		t.Fatal("executable tape cannot be SKIP")
	}
}

func TestDiscoveryOnlyGradeC(t *testing.T) {
	in := testEvalInput("shadow_discovery_c", true)
	result := Evaluate(in, nil)
	if !result.Accepted {
		t.Fatalf("discovery should accept: %+v", result.RejectReasons)
	}
	if result.Status != TapeStatusDiscoveryOnly {
		t.Fatalf("status=%s want DISCOVERY_ONLY", result.Status)
	}
	if GradeDiscoveryOnly(in.Entry) && result.Status == TapeStatusExecutableCertified {
		t.Fatal("C/D grade cannot certify")
	}
}

func TestCannotEnterDeleted(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.Entry.ExecutableDepth = 0
	result := Evaluate(in, nil)
	if result.Accepted {
		t.Fatal("cannot enter must reject certified path")
	}
	if !containsStr(result.RejectReasons, RejectCannotEnter) {
		t.Fatalf("want CANNOT_ENTER got %+v", result.RejectReasons)
	}
}

func TestAnnouncementLookaheadReject(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	future := in.Exclusion.DecisionTime.Add(time.Hour)
	in.Announcements.Items = append(in.Announcements.Items, announcements.Announcement{
		ID: "future-ann", Symbol: "BTC",
		PublishedAt: future, FirstSeenAt: future,
	})
	result := Evaluate(in, nil)
	if !containsStr(result.RejectReasons, RejectAnnouncementLookahead) {
		t.Fatalf("lookahead must reject: %+v", result.RejectReasons)
	}
}

func TestShockClusterNonOverlap(t *testing.T) {
	used := map[string]bool{shockOverlapKey("BTC-USDT-SWAP", "shock-a"): true}
	in := testEvalInput("real_fill_grade_a", true)
	in.Exclusion.ShockCluster = "shock-a"
	result := Evaluate(in, used)
	if !containsStr(result.RejectReasons, RejectShockOverlap) {
		t.Fatalf("overlap must reject: %+v", result.RejectReasons)
	}
}

func TestOutcomeLabelOnly(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.OutcomeLabel = OutcomeLabel{ConservativeNetBPS: 50, LabelOnly: true}
	result := Evaluate(in, nil)
	if !result.Tape.OutcomeLabel.LabelOnly {
		t.Fatal("outcome must remain label_only")
	}
}

func TestLedgerAndTapeHashes(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	result := Evaluate(in, nil)
	ledger, err := BuildLedger(
		"const", "map", "ex-manifest", "entry-manifest", "exec-manifest", "tape", "ann-manifest",
		[]ExperimentAttempt{NewAttempt(1, in.Exclusion, result)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTape(result.Tape); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInputBundleFromFixtures(t *testing.T) {
	root := t.TempDir()
	fix, err := WriteFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		ExclusionMapDir: fix.ExclusionMapDir, EntryProofDir: fix.EntryProofDir,
		ExecutionPolicyDir: fix.ExecutionPolicyDir, ExitProofPath: fix.ExitProofPath,
		AnnouncementDir: fix.AnnouncementDir, OutputDir: filepath.Join(root, "out"),
		CandidateLimit: 10,
	}
	bundle, err := LoadInputBundle(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seeds := CollectAcceptedSeeds(bundle)
	if len(seeds) != 3 {
		t.Fatalf("seeds=%d", len(seeds))
	}
}

func TestWriteFixturesBundle(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if fix.ExclusionMapDir == "" || fix.EntryProofDir == "" {
		t.Fatalf("fixture: %+v", fix)
	}
}

var execTriggerAlias = map[string]string{
	"touch_not_fill":     "touch_not_fill_missed_profit",
	"real_fill_grade_a":  "filled_profitable_slow",
	"shadow_discovery_c": "unfilled_not_profitable",
}

func testEvalInput(triggerID string, withOutcome bool) EvaluationInput {
	triggers := g10entryproof.DefaultFixtureTriggers()
	execTriggers := g10executionpolicy.DefaultFixtureTriggers()
	var tr g10entryproof.FixtureTrigger
	for _, f := range triggers {
		if f.TriggerID == triggerID {
			tr = f
			break
		}
	}
	ep, err := g10entryproof.BuildEntryProof(tr.TriggerRecord)
	if err != nil {
		panic(err)
	}
	xp, err := g10exitproof.BuildProof(g10exitproof.Input{
		TriggerID: tr.TriggerID, DecisionTime: tr.DecisionTime,
		ExitDepthUSDT: tr.ExitDepthUSDT, CanExitBeforeMaintenance: tr.CanExitBeforeMaintenance,
		ExtremeExitSlippageBPS: tr.ExtremeExitSlippageBPS, PartialCloseFeasible: tr.PartialCloseFeasible,
	})
	if err != nil {
		panic(err)
	}
	var execTr g10executionpolicy.TriggerRecord
	execID := triggerID
	if mapped, ok := execTriggerAlias[triggerID]; ok {
		execID = mapped
	}
	for _, e := range execTriggers {
		if e.TriggerID == execID {
			execTr = e
			break
		}
	}
	fp, err := g10executionpolicy.FreezePolicy(execTr, execTriggers)
	if err != nil {
		panic(err)
	}
	exclusion := buildFixtureExclusionProof(triggerID, tr.Symbol+"-SWAP", tr.Side, 1, "shock-x")
	label := OutcomeLabel{}
	if withOutcome {
		label = OutcomeLabel{ConservativeNetBPS: 25, LabelOnly: true}
	}
	return EvaluationInput{
		Exclusion: exclusion, Entry: ep, Execution: fp, Exit: xp,
		Announcements: AnnouncementContext{TapeHash: "tape", ManifestHash: "ann"},
		OutcomeLabel: label, ConstitutionHash: "const", CapitalUSDT: 30,
		AllTriggersDenominator: len(execTriggers),
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func historyCatalogFixture() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{DataHash: g10protocolv2.HistoryDataHash}
}

func TestRunnerSmokeWithFixtures(t *testing.T) {
	root := t.TempDir()
	fix, err := WriteFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, OutputDir: out,
		ExclusionMapDir: fix.ExclusionMapDir, EntryProofDir: fix.EntryProofDir,
		ExecutionPolicyDir: fix.ExecutionPolicyDir, ExitProofPath: fix.ExitProofPath,
		AnnouncementDir: fix.AnnouncementDir,
		CandidateLimit: 10, ProgressEvery: 1,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg,
		Catalog: historyCatalogFixture(),
		CheckpointPath: filepath.Join(out, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ManifestHash == "" {
		t.Fatal("manifest required")
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
}
