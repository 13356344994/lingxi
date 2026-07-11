package exam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

type testSigner struct{}

func (testSigner) SignReceipt(_ context.Context, payload ReceiptPayload) (Receipt, error) {
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return Receipt{
		Payload:          payload,
		KeyID:            "test-key",
		Algorithm:        "test-sha256-not-secure",
		Signature:        hex.EncodeToString(sum[:]),
		Signed:           true,
		SecurityBoundary: "TEST_ONLY: deterministic unit-test signer, not a production key service",
	}, nil
}

func TestExamStateMachineConsumesOnce(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	draft := NewDraft("exam-1", "gen-1", 1, "challenge", "protocol", "data", now)
	sealed, err := Seal(draft, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	running, err := Start(sealed, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if running.AttemptID == "" || running.Status != StatusRunning {
		t.Fatalf("start should unseal into a single running attempt: %+v", running)
	}
	if _, err := Start(running, now.Add(3*time.Minute)); err == nil {
		t.Fatal("running exam must not be startable again")
	}
	consumed, err := Consume(context.Background(), running, Verdict{
		Passed:     true,
		Reason:     "thresholds passed",
		DecisionID: "decision:exam-1",
		Metrics:    map[string]float64{"pf": 1.4},
	}, now.Add(4*time.Minute), testSigner{})
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Status != StatusConsumedPassed || !consumed.Consumed() {
		t.Fatalf("expected consumed_passed: %+v", consumed)
	}
	if consumed.Receipt == nil || !consumed.Receipt.Signed || consumed.Receipt.Signature == "" {
		t.Fatalf("expected signed receipt: %+v", consumed.Receipt)
	}
	if _, err := Consume(context.Background(), consumed, Verdict{Passed: false}, now.Add(5*time.Minute), testSigner{}); err == nil {
		t.Fatal("consumed exam must not be reusable")
	}
}

func TestCrashConsumesAndCannotRerun(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	sealed, err := Seal(NewDraft("exam-crash", "gen", 1, "challenge", "protocol", "data", now), now)
	if err != nil {
		t.Fatal(err)
	}
	running, err := Start(sealed, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	crashed, err := Crash(context.Background(), running, "worker died", now.Add(2*time.Minute), UnsignedBoundarySigner{})
	if err != nil {
		t.Fatal(err)
	}
	if crashed.Status != StatusConsumedCrashed {
		t.Fatalf("expected consumed_crashed, got %s", crashed.Status)
	}
	if crashed.Receipt == nil || crashed.Receipt.Signed {
		t.Fatalf("unsigned boundary signer must be explicit and unsigned: %+v", crashed.Receipt)
	}
	if _, err := Start(crashed, now.Add(3*time.Minute)); err == nil {
		t.Fatal("crashed exam must not be runnable again")
	}
}

func TestNextSealedUsesFixedOrder(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	a, _ := Seal(NewDraft("b", "gen", 2, "challenge", "protocol", "data", now), now)
	b, _ := Seal(NewDraft("a", "gen", 1, "challenge", "protocol", "data", now.Add(time.Second)), now.Add(time.Second))
	c, _ := Seal(NewDraft("c", "gen", 1, "challenge", "protocol", "data", now), now)
	next, ok := NextSealed([]Exam{a, b, c})
	if !ok || next.ExamID != "c" {
		t.Fatalf("expected lowest order then oldest sealed then id, got %+v ok=%v", next, ok)
	}
}

func TestRegistryRoundTripAndStartNext(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "exam_state.json")
	r := Registry{Version: 1}
	sealed, err := Seal(NewDraft("exam", "gen", 1, "challenge", "protocol", "data", now), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistry(path, r.Upsert(sealed)); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	updated, started, ok, err := loaded.StartNext(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || started.Status != StatusRunning {
		t.Fatalf("expected start next, got %+v ok=%v", started, ok)
	}
	if got, ok := updated.Get("exam"); !ok || got.AttemptID == "" {
		t.Fatalf("registry not updated with running attempt: %+v ok=%v", got, ok)
	}
}
