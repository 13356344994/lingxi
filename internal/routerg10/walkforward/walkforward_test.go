package walkforward

import (
	"testing"

	g10validation "lingxi/internal/routerg10/validation"
	g10protocol "lingxi/internal/routerg10/protocol"
)

func TestEvaluateWFGatesEmptyFolds(t *testing.T) {
	status, _, reason := EvaluateWFGates(nil, nil, DailyEquityReplay{DailyPnL: map[string]float64{}}, nil)
	if status != StatusInconclusive || reason != ReasonFoldNetReturn {
		t.Fatalf("expected inconclusive empty folds, got %s %s", status, reason)
	}
}

func TestCollapseTierStatusesPartialPass(t *testing.T) {
	status, eligible := CollapseTierStatuses(map[int]string{
		30:  StatusWFPass,
		100: StatusWFFail,
		1000: StatusWFFail,
	})
	if status != StatusWFPass {
		t.Fatalf("expected WF_PASS with one tier, got %s", status)
	}
	if len(eligible) != 1 || eligible[0] != "30" {
		t.Fatalf("expected eligible [30], got %v", eligible)
	}
}

func TestEvaluateRobustnessShockLeaveOneOut(t *testing.T) {
	daily := DailyEquityReplay{
		DailyPnL:    map[string]float64{"d1": 10, "d2": 5},
		ShockDayPnL: map[string]float64{"shock-a": 8, "shock-b": -2},
		SymbolPnL:   map[string]float64{"BTC": 6, "ETH": 4},
	}
	checks := evaluateRobustness(daily)
	if !checks.DayLeaveOneOutOK {
		t.Fatal("day LOO should pass")
	}
	if checks.ShockDayPositiveOK {
		t.Fatal("shock LOO must fail when excluding negative block turns total negative")
	}
	if !checks.SymbolClusterLOO_OK {
		t.Fatal("symbol LOO should pass")
	}
}

func TestFrozenSizingFromSurvivorNoFloorRollback(t *testing.T) {
	entry := g10validation.SurvivorEntry{FrozenConservativeMeanBPS: -0.5, FrozenStressConservativeMeanBPS: 0.2}
	sizing := FrozenSizingFromSurvivor(entry, 30)
	if sizing.ConservativeMeanBPS != -0.5 {
		t.Fatalf("negative edge must not rollback, got %v", sizing.ConservativeMeanBPS)
	}
}

func TestBuildMechanismReportUnexplained(t *testing.T) {
	wf := WFReport{Status: StatusWFPass, ReportHash: "abc", BrainDegeneracyClear: true}
	m := BuildMechanismReport(wf)
	if m.MechanismStatus != MechanismUnexplainedPersonalEdge {
		t.Fatalf("expected UNEXPLAINED_PERSONAL_EDGE, got %s", m.MechanismStatus)
	}
	if !m.BrainDegeneracyClear {
		t.Fatal("mechanism must propagate brain degeneracy clear")
	}
	if m.TradeAllowed || m.ExamEligible || m.Certified {
		t.Fatal("mechanism must forbid trade/exam/certified")
	}
	if m.ExamActions != g10protocol.ExamActions {
		t.Fatal("exam_actions must be zero")
	}
}

func TestMaxDrawdownOK(t *testing.T) {
	if !MaxDrawdownOK(0.29) {
		t.Fatal("0.29 should pass")
	}
	if MaxDrawdownOK(0.31) {
		t.Fatal("0.31 should fail")
	}
}

func TestDefaultRunConfigProtocolBinding(t *testing.T) {
	cfg := DefaultRunConfig("/h", "/p", "/v", "/o", []string{"/m"})
	if cfg.ProtocolHash != g10protocol.RouterProtocolHash {
		t.Fatal("protocol hash mismatch")
	}
	if cfg.SchemaVersion != SchemaVersionConfig {
		t.Fatal("schema mismatch")
	}
}
