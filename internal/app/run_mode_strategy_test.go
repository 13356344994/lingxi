package app

import (
	"testing"

	"lingxi/internal/core"
)

func TestRotationRunModeOFF(t *testing.T) {
	if got := RotationRunMode(true, false, core.StrategyModeCollectData); got != core.RunModeOFF {
		t.Fatalf("RotationRunMode(true,false)=%q want OFF", got)
	}
}

func TestRotationRunModeLiveModes(t *testing.T) {
	if got := RotationRunMode(true, true, core.StrategyModeCollectData); got != core.RunModeLIVE {
		t.Fatalf("collect_data live=%q want LIVE", got)
	}
	if got := RotationRunMode(true, true, core.StrategyModeProfitSeeking); got != core.RunModeLIVE {
		t.Fatalf("profit_seeking live=%q want LIVE", got)
	}
	if got := RotationRunMode(true, true, core.StrategyModeCanaryProfit); got != core.RunModeCANARYLive {
		t.Fatalf("canary_profit live=%q want CANARY_LIVE", got)
	}
}

func TestPerpPerpRunModeCanaryMapsCANARYLive(t *testing.T) {
	if got := PerpPerpRunMode(true, true, core.StrategyModeCanaryProfit); got != core.RunModeCANARYLive {
		t.Fatalf("canary_perp live=%q want CANARY_LIVE", got)
	}
}
