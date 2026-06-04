package core

import (
	"testing"
	"time"
)

func TestDiffFromDefaultsEmptyWhenUnchanged(t *testing.T) {
	cfg := DefaultEngineConfig()
	if diff := cfg.DiffFromDefaults(); len(diff) != 0 {
		t.Fatalf("default config should not diff from itself, got %d entries: %+v", len(diff), diff)
	}
}

func TestDiffFromDefaultsReportsOverrides(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MinMarginLevel = 1.5
	cfg.RiskConfig.FeedBlindnessGuardEnabled = true
	cfg.NetworkConfig.HTTPTimeout = 9 * time.Second

	diff := cfg.DiffFromDefaults()
	got := make(map[string]ConfigDiffEntry, len(diff))
	for _, e := range diff {
		got[e.Path] = e
	}

	if e, ok := got["RiskConfig.MinMarginLevel"]; !ok {
		t.Fatal("expected RiskConfig.MinMarginLevel in diff")
	} else if e.Default != "0" || e.Current != "1.5" {
		t.Fatalf("MinMarginLevel diff wrong: %+v", e)
	}
	if e, ok := got["RiskConfig.FeedBlindnessGuardEnabled"]; !ok {
		t.Fatal("expected RiskConfig.FeedBlindnessGuardEnabled in diff")
	} else if e.Default != "false" || e.Current != "true" {
		t.Fatalf("FeedBlindnessGuardEnabled diff wrong: %+v", e)
	}
	// time.Duration 走 Stringer 文本。
	if e, ok := got["NetworkConfig.HTTPTimeout"]; !ok {
		t.Fatal("expected NetworkConfig.HTTPTimeout in diff")
	} else if e.Current != "9s" {
		t.Fatalf("HTTPTimeout current should be 9s, got %q", e.Current)
	}
}

func TestDiffFromDefaultsSortedByPath(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MinMarginLevel = 1.5
	cfg.StrategyConfig.MaxTargets = 99
	cfg.ExecutionConfig.FuturesLeverage = 3

	diff := cfg.DiffFromDefaults()
	for i := 1; i < len(diff); i++ {
		if diff[i-1].Path > diff[i].Path {
			t.Fatalf("diff not sorted by path: %q before %q", diff[i-1].Path, diff[i].Path)
		}
	}
	if len(diff) != 3 {
		t.Fatalf("expected exactly 3 overrides, got %d: %+v", len(diff), diff)
	}
}
