package reversetapes

import (
	"testing"

	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
)

func TestCMLAllowedNotFillWithoutEvidence(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.Execution.SelectedAction = g10executionpolicy.ActionConditionalMarketableLimit
	hash, err := g10executionpolicy.CanonicalPolicyHash(in.Execution)
	if err != nil {
		t.Fatal(err)
	}
	in.Execution.ExecutionPolicyHash = hash
	in.Entry.CertificationOK = false
	in.Entry.EvidenceArtifactHash = ""
	result := Evaluate(in, nil)
	if result.Tape.RealizedFill {
		t.Fatalf("allowed CML without fill evidence must not realize fill: %+v", result)
	}
	if result.Tape.AbsoluteProfitUSDT != 0 {
		t.Fatalf("profit must be zero without fill evidence, got %v", result.Tape.AbsoluteProfitUSDT)
	}
}

func TestNoOutcomeLabelProfitBackfill(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.Execution.ActionsCompared = []g10executionpolicy.ActionEvaluation{
		{Action: g10executionpolicy.ActionBoundedMaker, ExpectedNetBPS: 0, ExpectedNetUSDT: 0, Allowed: true},
		{Action: g10executionpolicy.ActionConditionalMarketableLimit, Allowed: false},
		{Action: g10executionpolicy.ActionSkip, Allowed: true},
	}
	in.Execution.MakerQuadrant = g10executionpolicy.QuadrantFilledProfitable
	result := Evaluate(in, nil)
	if result.Tape.AbsoluteProfitUSDT != 0 {
		t.Fatalf("profit must not backfill from outcome label, got %v", result.Tape.AbsoluteProfitUSDT)
	}
}

func TestUnfilledProfitableCannotCertify(t *testing.T) {
	in := testEvalInput("touch_not_fill", true)
	in.Execution.SelectedAction = g10executionpolicy.ActionBoundedMaker
	in.Execution.MakerQuadrant = g10executionpolicy.QuadrantUnfilledSubsequentlyProfitable
	hash, err := g10executionpolicy.CanonicalPolicyHash(in.Execution)
	if err != nil {
		t.Fatal(err)
	}
	in.Execution.ExecutionPolicyHash = hash
	result := Evaluate(in, nil)
	if result.Status == TapeStatusExecutableCertified {
		t.Fatalf("unfilled profitable must not certify: %+v", result)
	}
	if result.Tape.AbsoluteProfitUSDT != 0 {
		t.Fatalf("unfilled profit must be 0, got %v", result.Tape.AbsoluteProfitUSDT)
	}
}

func TestFillOnlySurvivorshipRejected(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.Execution.MakerStats.Quadrants = g10executionpolicy.MakerQuadrantCounts{
		FilledProfitable: 3, TotalTriggers: 3,
	}
	in.AllTriggersDenominator = 3
	result := Evaluate(in, nil)
	if !containsStr(result.RejectReasons, RejectFillOnlySurvivorship) {
		t.Fatalf("fill-only denominator must reject: %+v", result.RejectReasons)
	}
}

func TestPrecommittedPolicyHashRequired(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.Execution.ExecutionPolicyHash = "tampered"
	result := Evaluate(in, nil)
	if !containsStr(result.RejectReasons, RejectProofHashMismatch) {
		t.Fatalf("tampered policy hash must reject: %+v", result.RejectReasons)
	}
}

func TestReversetapesNoOutcomeHindsight(t *testing.T) {
	in := testEvalInput("real_fill_grade_a", true)
	in.OutcomeLabel = OutcomeLabel{ConservativeNetBPS: -999, LabelOnly: true}
	in.Execution.MakerStats.Quadrants = g10executionpolicy.MakerQuadrantCounts{
		FilledProfitable: 3, TotalTriggers: 3,
	}
	in.AllTriggersDenominator = 3
	in.Execution.ActionsCompared = []g10executionpolicy.ActionEvaluation{
		{Action: g10executionpolicy.ActionBoundedMaker, ExpectedNetBPS: 5, ExpectedNetUSDT: 5, Allowed: true},
	}
	result := Evaluate(in, nil)
	if !containsStr(result.RejectReasons, RejectFillOnlySurvivorship) {
		t.Fatalf("fill-only must reject from execution stats even when outcome negative: %+v", result.RejectReasons)
	}
}

func TestCertifiedEntryRequiresArtifactHash(t *testing.T) {
	ep, err := g10entryproof.BuildEntryProof(g10entryproof.DefaultFixtureTriggers()[1].TriggerRecord)
	if err != nil {
		t.Fatal(err)
	}
	if ep.CertificationOK && ep.EvidenceArtifactHash == "" {
		t.Fatal("certified entry must carry artifact hash")
	}
}
