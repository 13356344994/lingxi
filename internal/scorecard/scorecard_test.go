package scorecard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFailsClosedWithoutSignedExam(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "okx_directional")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settlements.jsonl"), []byte(
		"{\"side\":\"long\",\"realized_usdt\":1}\n"+
			"{\"side\":\"short\",\"realized_usdt\":-2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fast_maker_attempts.jsonl"), []byte(
		"{\"maker_filled\":true,\"maker_wait_ms\":500}\n"+
			"{\"maker_filled\":false,\"maker_wait_ms\":2000}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Build(Options{RecordDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusNoGo || report.Settlements.NetUSDT != -1 {
		t.Fatalf("report=%+v", report)
	}
	if report.Maker.Attempts != 2 || report.Maker.Filled != 1 || report.Maker.FastFilled != 1 {
		t.Fatalf("maker=%+v", report.Maker)
	}
}
