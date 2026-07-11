package app

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

func TestBackfillRecentFirstBattleShadowReadsOnlyTail(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "okx_directional")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, fastExecShadowFile)
	data := ""
	for i := 0; i < 5; i++ {
		data += `{"recorded_at":"2026-07-11T00:00:00Z","venue":"okx","symbol":"X-USDT","snapshot_id":"d` +
			string(rune('0'+i)) + `","mid":1,"spread_bps":2}` + "\n"
	}
	if err := os.WriteFile(input, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := BackfillRecentFirstBattleShadow(root, input, 2, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("written=%d want 2", written)
	}
	f, err := os.Open(filepath.Join(dir, firstBattleShadowFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		count++
	}
	if count != 2 {
		t.Fatalf("output rows=%d want 2", count)
	}
}
