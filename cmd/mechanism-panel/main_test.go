package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunSmallFixtureWithProgress(t *testing.T) {
	dir := t.TempDir()
	liq := filepath.Join(dir, "liq.jsonl")
	features := filepath.Join(dir, "features.jsonl")
	labels := filepath.Join(dir, "labels.jsonl")
	checkpoint := filepath.Join(dir, "checkpoint.json")
	raw := "" +
		`{"recorded_at":"2026-07-11T00:00:00Z","symbol":"BTC-USDT","pos_side":"long","side":"sell","qty":1,"bk_px":100}` + "\n" +
		`{"recorded_at":"2026-07-11T00:00:30Z","symbol":"BTC-USDT","pos_side":"long","side":"sell","qty":1000,"bk_px":99}` + "\n"
	if err := os.WriteFile(liq, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	err := run(context.Background(), []string{
		"--liquidations", liq,
		"--features-out", features,
		"--labels-out", labels,
		"--checkpoint", checkpoint,
		"--progress-every", "1",
		"--finalize",
	}, &progress)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{features, labels, checkpoint} {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Fatalf("%s 未生成有效输出: info=%v err=%v", path, info, err)
		}
	}
	if progress.Len() == 0 {
		t.Fatal("应输出可见进度")
	}
}
