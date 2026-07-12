package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSupportsCheckpointResume(t *testing.T) {
	root := t.TempDir()
	writeHistoryFixture(t, root, "okx_funding", "funding.jsonl",
		`{"venue":"okx","data":[["1710000000000","BTC-USDT-SWAP","0.0001"]]}`,
		`{"venue":"okx","data":[["1710003600000","BTC-USDT-SWAP","0.0002"]]}`,
	)
	checkpoint := filepath.Join(root, "inventory.checkpoint.json")

	var first bytes.Buffer
	if err := run(context.Background(), []string{
		"--root", root,
		"--family", "okx_funding",
		"--max-lines", "1",
		"--progress-every", "1",
		"--checkpoint", checkpoint,
	}, &first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var partial map[string]any
	if err := decodeLastJSON(first.String(), &partial); err != nil {
		t.Fatal(err)
	}
	if completed, _ := partial["completed"].(bool); completed {
		t.Fatalf("first run should be partial")
	}

	var second bytes.Buffer
	if err := run(context.Background(), []string{
		"--root", root,
		"--family", "okx_funding",
		"--progress-every", "1",
		"--checkpoint", checkpoint,
	}, &second); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var catalog map[string]any
	if err := decodeLastJSON(second.String(), &catalog); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dataset_id", "data_hash", "schema_hash"} {
		if _, ok := catalog[key].(string); !ok {
			t.Fatalf("missing %s in output %v", key, catalog)
		}
	}
}

func decodeLastJSON(output string, out any) error {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return os.ErrInvalid
	}
	return json.Unmarshal([]byte(lines[len(lines)-1]), out)
}

func writeHistoryFixture(t *testing.T, root, family, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, family)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".done", []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
