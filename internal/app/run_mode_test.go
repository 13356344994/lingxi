package app

import (
	"testing"

	"lingxi/internal/core"
)

func TestLiveBoolFromRunMode(t *testing.T) {
	if LiveBoolFromRunMode(core.RunModeOFF) {
		t.Fatal("OFF must not allow live orders")
	}
	if !LiveBoolFromRunMode(core.RunModeLIVE) {
		t.Fatal("LIVE must allow live orders")
	}
	if !LiveBoolFromRunMode(core.RunModeCANARYLive) {
		t.Fatal("CANARY_LIVE must allow live orders")
	}
}

func TestPerpPerpRunMode(t *testing.T) {
	if got := PerpPerpRunMode(false, true, core.StrategyModeCollectData); got != core.RunModeOFF {
		t.Fatalf("config off => OFF, got %q", got)
	}
	if got := PerpPerpRunMode(true, false, core.StrategyModeCollectData); got != core.RunModeOFF {
		t.Fatalf("control plane off => OFF, got %q", got)
	}
	if got := PerpPerpRunMode(true, true, core.StrategyModeCollectData); got != core.RunModeLIVE {
		t.Fatalf("both on => LIVE, got %q", got)
	}
}
