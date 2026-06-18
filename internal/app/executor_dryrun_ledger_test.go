package app

import (
	"path/filepath"
	"testing"
)

// assertRealLedgerUntouched 断言 dry-run 未写入真实 positions/settlements 台账。
func assertRealLedgerUntouched(t *testing.T, recordDir string, positionFiles ...string) {
	t.Helper()
	for _, name := range positionFiles {
		empty, err := ledgerFileEmpty(filepath.Join(recordDir, name))
		if err != nil {
			t.Fatalf("position ledger %s: %v", name, err)
		}
		if !empty {
			t.Fatalf("position ledger %s must stay empty in dry-run", name)
		}
	}
	for _, name := range []string{
		"singlevenuespotperp/settlements.jsonl",
		"singlevenueperpspot/settlements.jsonl",
		"crossvenuespotperp/settlements.jsonl",
		"crossvenueperpspot/settlements.jsonl",
		"crossvenue/settlements.jsonl",
	} {
		empty, err := ledgerFileEmpty(filepath.Join(recordDir, name))
		if err != nil {
			t.Fatalf("settlement ledger %s: %v", name, err)
		}
		if !empty {
			t.Fatalf("settlement ledger %s must stay empty in dry-run", name)
		}
	}
}
