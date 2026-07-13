package executionpolicy

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestMakerTouchNotFillQuadrant(t *testing.T) {
	tr := TriggerRecord{
		MakerTouched: true, MakerFilled: false, SubsequentlyProfitable: true,
	}
	if ClassifyMakerQuadrant(tr) != QuadrantUnfilledSubsequentlyProfitable {
		t.Fatal("touch-not-fill with subsequent profit")
	}
	if MissedProfitablePnL(tr) != 0 {
		t.Fatal("missed profitable pnl must be zero for maker path")
	}
}

func TestFreezeAllActionsBeforeOutcome(t *testing.T) {
	triggers := DefaultFixtureTriggers()
	fp, err := FreezePolicy(triggers[0], triggers)
	if err != nil {
		t.Fatal(err)
	}
	if !fp.FreezeBeforeOutcomeKnown {
		t.Fatal("must freeze before outcome")
	}
	if err := ValidateFrozenPolicy(fp); err != nil {
		t.Fatal(err)
	}
	if len(fp.ActionsCompared) != 3 {
		t.Fatalf("actions=%d", len(fp.ActionsCompared))
	}
}

func TestMakerFillSelectionGateFillOnlyBias(t *testing.T) {
	triggers := DefaultFixtureTriggers()
	stats := BuildMakerStats(triggers)
	gate := EvaluateMakerFillSelectionGate(stats)
	if !gate.ForbidMaker {
		t.Fatalf("adverse selection must forbid maker: stats=%+v gate=%+v", stats, gate)
	}
	if stats.Quadrants.TotalTriggers != len(triggers) {
		t.Fatalf("denominator=%d want %d", stats.Quadrants.TotalTriggers, len(triggers))
	}
}

func TestMissedProfitableMovePnLZero(t *testing.T) {
	tr := TriggerRecord{MakerFilled: false, SubsequentlyProfitable: true, NetPnLUSDT: 99}
	maker := FreezeBoundedMaker(tr)
	if maker.ExpectedNetUSDT != 0 || maker.ExpectedNetBPS != 0 {
		t.Fatalf("expected 0 got usdt=%v bps=%v", maker.ExpectedNetUSDT, maker.ExpectedNetBPS)
	}
}

func TestConditionalMarketableLimitExpectedNetUnits(t *testing.T) {
	tr := TriggerRecord{
		Side: "long", Price: 100, ReferenceNotionalUSDT: 200,
		SpreadBPS: 3, FeeBPS: 2, LatencyMS: 50,
		RemainingEdgeBPS: 23.4, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
	}
	ev := FreezeConditionalMarketableLimit(tr)
	if !ev.Allowed {
		t.Fatalf("expected allowed: %+v", ev)
	}
	wantBPS := 23.4 - 3 - 2 - 50*0.01
	if math.Abs(ev.ExpectedNetBPS-wantBPS) > 1e-9 {
		t.Fatalf("bps=%v want %v", ev.ExpectedNetBPS, wantBPS)
	}
	wantUSDT := BpsToUSDT(wantBPS, 200)
	if math.Abs(ev.ExpectedNetUSDT-wantUSDT) > 1e-9 {
		t.Fatalf("usdt=%v want %v", ev.ExpectedNetUSDT, wantUSDT)
	}
	if math.Abs(ev.ExpectedNetUSDT-23.4) < 1 {
		t.Fatal("must not treat bps as usdt")
	}
}

func TestSelectFrozenActionComparesUSDT(t *testing.T) {
	gate := MakerFillSelectionGate{}
	actions := []ActionEvaluation{
		{Action: ActionBoundedMaker, ExpectedNetBPS: 50, ExpectedNetUSDT: 1.0, Allowed: true},
		{Action: ActionConditionalMarketableLimit, ExpectedNetBPS: 10, ExpectedNetUSDT: 2.5, Allowed: true},
		{Action: ActionSkip, Allowed: true},
	}
	if got := SelectFrozenAction(actions, gate); got != ActionConditionalMarketableLimit {
		t.Fatalf("selected=%s", got)
	}
}

func TestToxicFillExit(t *testing.T) {
	tr := DefaultFixtureTriggers()[4]
	if EvaluateToxicFill(tr) != ToxicFillExitStatus {
		t.Fatalf("toxic status=%s", EvaluateToxicFill(tr))
	}
}

func TestRunnerWithFixtures(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		OutputDir:     out,
		Sources:       SourcePaths{Triggers: filepath.Join(root, "triggers.jsonl")},
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Policies) != 5 {
		t.Fatalf("policies=%d", len(result.Policies))
	}
	if result.Manifest.AllTriggersDenominator != 5 {
		t.Fatal("all triggers must be denominator")
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFixturesBundle(t *testing.T) {
	bundle, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.BundleHash == "" || len(bundle.Policies) != 5 {
		t.Fatalf("bundle: %+v", bundle)
	}
}

func TestFreezeConditionalMarketableLimitShortFloor(t *testing.T) {
	tr := TriggerRecord{
		Side: "short", Price: 3500, MaxPriceCap: 3495.8,
		SpreadBPS: 4, FeeBPS: 2, LatencyMS: 60,
		RemainingEdgeBPS: 30, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
	}
	ev := FreezeConditionalMarketableLimit(tr)
	if !ev.Allowed {
		t.Fatalf("price above floor must allow CML: %+v", ev)
	}
	tr.Price = 3480
	ev = FreezeConditionalMarketableLimit(tr)
	if ev.Allowed || ev.BlockReason != "below_min_price_floor" {
		t.Fatalf("price below floor must block: %+v", ev)
	}
}

func TestFreezeConditionalMarketableLimitLongCap(t *testing.T) {
	tr := TriggerRecord{
		Side: "long", Price: 100500, MaxPriceCap: 100300,
		SpreadBPS: 3, FeeBPS: 2, LatencyMS: 45,
		RemainingEdgeBPS: 25, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
	}
	ev := FreezeConditionalMarketableLimit(tr)
	if ev.Allowed || ev.BlockReason != "above_max_price_cap" {
		t.Fatalf("price above cap must block long CML: %+v", ev)
	}
}

func TestSelectActionNotFromOutcome(t *testing.T) {
	tr := TriggerRecord{
		TriggerID: "x", DecisionTime: parseTime("2026-07-01T10:00:00Z"),
		MakerFilled: false, SubsequentlyProfitable: true, NetPnLUSDT: 100,
		RemainingEdgeBPS: 3, SpreadBPS: 2, FeeBPS: 1, LatencyMS: 10,
		MaxPriceCap: 100500, MaxSlippageBPS: 10, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
	}
	fp, err := FreezePolicy(tr, []TriggerRecord{tr})
	if err != nil {
		t.Fatal(err)
	}
	if fp.SelectedAction == ActionBoundedMaker && fp.MakerGate.ForbidMaker {
		t.Fatal("must not select forbidden maker")
	}
}
