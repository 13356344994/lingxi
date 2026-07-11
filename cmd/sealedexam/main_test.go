package main

import (
	"path/filepath"
	"testing"

	"lingxi/internal/exam"
)

func TestSealedExamCLIUsesExplicitState(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	t.Setenv("LINGXI_EXAM_SIGNING_KEY", key)
	state := filepath.Join(t.TempDir(), "sealed_exam_state.json")
	if err := run([]string{"draft", "--state", state, "--id", "exam", "--generation", "gen", "--order", "1", "--challenge-hash", "challenge", "--protocol-hash", "protocol", "--data-hash", "data"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"seal", "--state", state, "--id", "exam"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"start-next", "--state", state}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"consume", "--state", state, "--id", "exam", "--passed", "--reason", "ok", "--decision-id", "decision-1"}); err != nil {
		t.Fatal(err)
	}
	r, err := exam.LoadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("exam")
	if !ok || got.Status != exam.StatusConsumedPassed {
		t.Fatalf("expected consumed_passed in explicit state file, got %+v ok=%v", got, ok)
	}
	if got.Receipt == nil || !got.Receipt.Signed || !exam.VerifyHMACReceipt(*got.Receipt, []byte(key)) {
		t.Fatalf("cli 应生成可验证签名receipt: %+v", got.Receipt)
	}
}
