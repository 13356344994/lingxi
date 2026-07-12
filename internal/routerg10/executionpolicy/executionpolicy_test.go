package executionpolicy

import (
	"context"
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
	if maker.ExpectedNetUSDT != 0 {
		t.Fatalf("expected 0 got %v", maker.ExpectedNetUSDT)
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
