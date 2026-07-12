package personalwalkforward

import (
	"testing"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

func TestProtocolNoDrift(t *testing.T) {
	if Generation != g10protocolv2.Generation {
		t.Fatal("generation drift")
	}
	if StatusWFPass != "WF_PASS" {
		t.Fatalf("wf pass status=%s", StatusWFPass)
	}
}

func TestEvaluateWFGatesEmptyInconclusive(t *testing.T) {
	status, _, reason := EvaluateWFGates(nil, nil, g10personalvalidation.DailyEquityReplay{}, g10personalvalidation.LeaveOneReport{})
	if status != StatusInconclusive || reason != ReasonInsufficientEvents {
		t.Fatalf("got %s %s", status, reason)
	}
}

func TestBuildLOFOResults(t *testing.T) {
	folds := []Fold{{Index: 0}, {Index: 1}}
	foldResults := []FoldResult{
		{FoldIndex: 0, CapitalTier: 30, BaseNetReturnFraction: 0.1, NetReturnNonNegative: true},
		{FoldIndex: 1, CapitalTier: 30, BaseNetReturnFraction: 0.05, NetReturnNonNegative: true},
	}
	lofo := BuildLOFOResults(folds, foldResults)
	if len(lofo) != 2 {
		t.Fatalf("lofo=%d", len(lofo))
	}
}

func TestBuildFoldsRequiresMinimumEvents(t *testing.T) {
	_, err := BuildFolds([]string{"2020-01-01T00:00:00Z"}, WFFoldCount, 0)
	if err == nil {
		t.Fatal("expected insufficient folds error")
	}
}

func TestRunConfigValidate(t *testing.T) {
	if (RunConfig{}).Validate() == nil {
		t.Fatal("expected validate error")
	}
}
