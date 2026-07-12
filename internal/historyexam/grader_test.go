package historyexam

import (
	"context"
	"testing"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
)

var testHMACKey = []byte("0123456789abcdef0123456789abcdef")

// newSealedGeneration builds a gauntlet generation whose public tickets are the
// commitment projection of the hidden manifest, mirroring the custodian flow.
func newSealedGeneration(t *testing.T, manifest HiddenManifest) gauntlet.State {
	t.Helper()
	tickets := manifest.Commitments()
	budgets := map[string]int{}
	for _, tk := range tickets {
		budgets[tk.Family] += tk.AlphaSpend
	}
	state, err := gauntlet.CreateGeneration(gauntlet.State{}, gauntlet.FixedAccess{}, gauntlet.RoleCustodian, manifest.Generation, tickets, budgets, "manifest-hash", now())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func now() time.Time { return time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC) }

func TestGraderRejectsCallerSuppliedVerdict(t *testing.T) {
	// The Grade signature has no pass boolean; the only way to grade is through
	// a protocol-bound evaluator. This test documents that fact by driving a
	// full pass/fail solely from fixture content.
	manifest := examManifest(t) // standard obs {1,2,3} mean 2; golden obs {4,5,6} mean 5
	state := newSealedGeneration(t, manifest)
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 3, MinSample: 1}
	access := gauntlet.FixedAccess{}
	signer := exam.HMACSigner{KeyID: "test", Key: testHMACKey}

	// Standard paper: mean 2 < 3 -> fail, generation fails.
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	state, res, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || res.Reason != "ROBUSTNESS_GATE" || res.Signature == "" {
		t.Fatalf("evaluator should fail the low-mean standard paper with a signed result: %+v", res)
	}
	if state.Generations[0].Status != gauntlet.GenerationFailed {
		t.Fatalf("a failed standard paper must fail the generation")
	}
}

func TestGraderPassesThenReachesGolden(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1.5, MinSample: 1}
	access := gauntlet.FixedAccess{}
	signer := exam.HMACSigner{KeyID: "test", Key: testHMACKey}

	// Standard paper mean 2 >= 1.5 -> pass.
	state, opened, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil || opened.ExamID != "g1-standard" {
		t.Fatalf("fixed order should open the standard paper first: %+v %v", opened, err)
	}
	state, res, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer)
	if err != nil || !res.Pass || res.Reason != "PASS" {
		t.Fatalf("standard paper should pass: %+v %v", res, err)
	}
	// Golden opens only after the standard passed, and is the final paper.
	state, opened, err = gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil || opened.ExamID != "g1-golden" {
		t.Fatalf("golden must be the final isolated paper: %+v %v", opened, err)
	}
	state, res, err = Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer)
	if err != nil || !res.Pass || state.Generations[0].Status != gauntlet.GenerationPassed {
		t.Fatalf("golden pass must pass the generation: %+v %v", res, err)
	}
	if !exam.VerifyHMACReceipt(*state.Generations[0].Exams[1].Receipt, testHMACKey) {
		t.Fatal("golden receipt must verify")
	}
}

func TestGraderRequiresRunningPaper(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1, MinSample: 1}
	if _, _, err := Grade(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), exam.HMACSigner{Key: testHMACKey}); err != ErrNoRunningPaper {
		t.Fatalf("grading without an unsealed paper must fail: %v", err)
	}
}

func TestGraderVerifiesCommitmentsAgainstStore(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	access := gauntlet.FixedAccess{}
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the hidden store so its committed data hash no longer matches the
	// running exam. Grading must fail closed rather than grade the wrong paper.
	tampered := manifest
	tampered.Papers = append([]HiddenPaper(nil), manifest.Papers...)
	tampered.Papers[0].DataHash = "not-the-committed-hash"
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1, MinSample: 1}
	if _, _, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", tampered, evaluator, now(), exam.HMACSigner{Key: testHMACKey}); err != ErrCommitmentMismatch {
		t.Fatalf("commitment mismatch must fail closed: %v", err)
	}
}

func TestGraderRequiresProtocolBoundEvaluator(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	access := gauntlet.FixedAccess{}
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	wrong := ThresholdEvaluator{Protocol: "some-other-protocol", MinMean: 1, MinSample: 1}
	if _, _, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, wrong, now(), exam.HMACSigner{Key: testHMACKey}); err != ErrProtocolUnbound {
		t.Fatalf("an evaluator bound to a different protocol must be rejected: %v", err)
	}
}

func TestGraderFixtureIntegrityChecked(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	access := gauntlet.FixedAccess{}
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt fixture bytes while leaving the committed DataHash intact.
	corrupt := manifest
	corrupt.Papers = append([]HiddenPaper(nil), manifest.Papers...)
	corrupt.Papers[0].Fixture = syntheticFixture(99, 99)
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1, MinSample: 1}
	if _, _, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", corrupt, evaluator, now(), exam.HMACSigner{Key: testHMACKey}); err != ErrFixtureCorrupt {
		t.Fatalf("fixture bytes that do not match DataHash must fail closed: %v", err)
	}
}

func TestGraderOneTimeConsumption(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	access := gauntlet.FixedAccess{}
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1.5, MinSample: 1}
	signer := exam.HMACSigner{Key: testHMACKey}
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer)
	if err != nil {
		t.Fatal(err)
	}
	// A second grade with no freshly unsealed paper must be refused: the paper
	// was permanently consumed.
	if _, _, err := Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer); err != ErrNoRunningPaper {
		t.Fatalf("a consumed paper must not be regradable: %v", err)
	}
}

func TestResearcherOnlyReceivesSignedResult(t *testing.T) {
	manifest := examManifest(t)
	state := newSealedGeneration(t, manifest)
	access := gauntlet.FixedAccess{}
	evaluator := ThresholdEvaluator{Protocol: testProtocol, MinMean: 1.5, MinSample: 1}
	signer := exam.HMACSigner{Key: testHMACKey}
	state, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleExaminer, "g1", now())
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = Grade(context.Background(), state, access, gauntlet.RoleExaminer, "g1", manifest, evaluator, now(), signer)
	if err != nil {
		t.Fatal(err)
	}
	// Researcher may not unseal or grade.
	if _, _, err := gauntlet.UnsealNext(state, access, gauntlet.RoleResearcher, "g1", now()); err != gauntlet.ErrForbidden {
		t.Fatalf("researcher must not unseal: %v", err)
	}
	view, err := gauntlet.ResearchReceipt(state, access, gauntlet.RoleResearcher, "g1")
	if err != nil || view.Signature == "" || view.Reason != "PASS" {
		t.Fatalf("researcher should receive only the signed coarse result: %+v %v", view, err)
	}
}
