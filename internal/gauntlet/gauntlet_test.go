package gauntlet

import (
	"context"
	"testing"
	"time"

	"lingxi/internal/exam"
)

var hmacKey = []byte("0123456789abcdef0123456789abcdef")

// ticketsFor builds a generation-scoped commitment pair so that challenge and
// data hashes stay globally unique. Cross-generation reuse is now rejected, so
// tests must not share commitments between generations even when they reuse a
// queue slot left untouched by an earlier failed generation.
func ticketsFor(gen string, first int) []Ticket {
	return []Ticket{
		{ID: gen + "-standard", Queue: first, Kind: KindStandard, Family: "liquidation", AlphaSpend: 1, ChallengeHash: gen + "-challenge-a", ProtocolHash: "virtual-protocol", DataHash: gen + "-data-a"},
		{ID: gen + "-golden", Queue: first + 1, Kind: KindGolden, Family: "liquidation", AlphaSpend: 1, ChallengeHash: gen + "-challenge-b", ProtocolHash: "virtual-protocol", DataHash: gen + "-data-b"},
	}
}

func TestFixedOrderBudgetGoldenAndResearchBoundary(t *testing.T) {
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	access := FixedAccess{}
	state, err := CreateGeneration(State{}, access, RoleCustodian, "g1", ticketsFor("g1", 1), map[string]int{"liquidation": 2}, "virtual-manifest", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateGeneration(state, access, RoleCustodian, "g1", ticketsFor("g1", 1), map[string]int{"liquidation": 2}, "changed", now); err != ErrGenerationLocked {
		t.Fatalf("generation mutation should be locked, got %v", err)
	}
	if _, _, err := UnsealNext(state, access, RoleResearcher, "g1", now); err != ErrForbidden {
		t.Fatalf("researcher should not unseal: %v", err)
	}
	state, opened, err := UnsealNext(state, access, RoleExaminer, "g1", now.Add(time.Minute))
	if err != nil || opened.ExamID != "g1-standard" {
		t.Fatalf("expected first standard paper: %+v, %v", opened, err)
	}
	state, result, err := Grade(context.Background(), state, access, RoleExaminer, "g1", true, "PASS", now.Add(2*time.Minute), exam.HMACSigner{KeyID: "test", Key: hmacKey})
	if err != nil || !result.Pass || !exam.VerifyHMACReceipt(*state.Generations[0].Exams[0].Receipt, hmacKey) {
		t.Fatalf("expected signed pass: %+v, %v", result, err)
	}
	state, opened, err = UnsealNext(state, access, RoleExaminer, "g1", now.Add(3*time.Minute))
	if err != nil || opened.ExamID != "g1-golden" {
		t.Fatalf("golden should be the final isolated paper: %+v, %v", opened, err)
	}
	state, result, err = Grade(context.Background(), state, access, RoleExaminer, "g1", false, "ROBUSTNESS_GATE", now.Add(4*time.Minute), exam.HMACSigner{KeyID: "test", Key: hmacKey})
	if err != nil || result.Pass || state.Generations[0].Status != GenerationFailed {
		t.Fatalf("golden failure must fail generation: %+v, %v", result, err)
	}
	view, err := ResearchReceipt(state, access, RoleResearcher, "g1")
	if err != nil || view.Reason != "ROBUSTNESS_GATE" || view.Signature == "" {
		t.Fatalf("researcher gets only the signed coarse result: %+v, %v", view, err)
	}
}

func TestFailedGenerationMustUseNextUntouchedPaper(t *testing.T) {
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	access := FixedAccess{}
	state, err := CreateGeneration(State{}, access, RoleCustodian, "g1", ticketsFor("g1", 10), map[string]int{"liquidation": 2}, "m1", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = UnsealNext(state, access, RoleExaminer, "g1", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = Grade(context.Background(), state, access, RoleExaminer, "g1", false, "FAMILY_MULTIPLE_TESTING", now, exam.HMACSigner{Key: hmacKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", ticketsFor("g2", 13), map[string]int{"liquidation": 2}, "m2", now); err == nil {
		t.Fatal("new generation must take queue 11, the next untouched paper")
	}
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", ticketsFor("g2", 11), map[string]int{"liquidation": 2}, "m2", now); err != nil {
		t.Fatalf("next untouched paper should be accepted: %v", err)
	}
}

func TestCrossGenerationCommitmentReuseRejected(t *testing.T) {
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	access := FixedAccess{}
	budget := map[string]int{"liquidation": 2}
	state, err := CreateGeneration(State{}, access, RoleCustodian, "g1", ticketsFor("g1", 1), budget, "m1", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = UnsealNext(state, access, RoleExaminer, "g1", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = Grade(context.Background(), state, access, RoleExaminer, "g1", false, "RISK_GATE", now, exam.HMACSigner{Key: hmacKey})
	if err != nil {
		t.Fatal(err)
	}
	reuseChallenge := ticketsFor("g2", 2)
	reuseChallenge[0].ChallengeHash = "g1-challenge-a"
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", reuseChallenge, budget, "m2", now); err == nil {
		t.Fatal("reusing a prior generation challenge hash must be rejected")
	}
	reuseData := ticketsFor("g2", 2)
	reuseData[0].DataHash = "g1-data-a"
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", reuseData, budget, "m2", now); err == nil {
		t.Fatal("reusing a prior generation data hash must be rejected")
	}
	dupWithin := ticketsFor("g2", 2)
	dupWithin[1].ChallengeHash = dupWithin[0].ChallengeHash
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", dupWithin, budget, "m2", now); err == nil {
		t.Fatal("duplicate challenge hash within a schedule must be rejected")
	}
	if _, err := CreateGeneration(state, access, RoleCustodian, "g2", ticketsFor("g2", 2), budget, "m2", now); err != nil {
		t.Fatalf("distinct commitments should be accepted: %v", err)
	}
}

func TestFamilyBudgetBlocksUnsealAndNoGoldenEarly(t *testing.T) {
	now := time.Now().UTC()
	access := FixedAccess{}
	budget := map[string]int{"liquidation": 1}
	state, err := CreateGeneration(State{}, access, RoleCustodian, "g", ticketsFor("g", 1), budget, "m", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = UnsealNext(state, access, RoleExaminer, "g", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := UnsealNext(state, access, RoleExaminer, "g", now); err == nil {
		t.Fatal("running paper must block another unseal")
	}
	state, _, err = Grade(context.Background(), state, access, RoleExaminer, "g", true, "PASS", now, exam.HMACSigner{Key: hmacKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := UnsealNext(state, access, RoleExaminer, "g", now); err == nil {
		t.Fatal("family alpha budget must block golden unseal")
	}
}

func TestCrashConsumesUnsealedPaper(t *testing.T) {
	now := time.Now().UTC()
	state, err := CreateGeneration(State{}, FixedAccess{}, RoleCustodian, "g", ticketsFor("g", 1), map[string]int{"liquidation": 2}, "m", now)
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = UnsealNext(state, FixedAccess{}, RoleExaminer, "g", now)
	if err != nil {
		t.Fatal(err)
	}
	state, result, err := Crash(context.Background(), state, FixedAccess{}, RoleExaminer, "g", now, exam.HMACSigner{Key: hmacKey})
	if err != nil || result.Pass || state.Generations[0].Exams[0].Status != exam.StatusConsumedCrashed {
		t.Fatalf("crash must permanently consume the unsealed paper: %+v, %v", result, err)
	}
}
