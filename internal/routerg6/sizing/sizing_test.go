package sizing_test

import (
	"math"
	"testing"

	g6protocol "lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/sizing"
)

func TestThreeCapitalTiersConcurrent(t *testing.T) {
	for _, tc := range []struct {
		equity float64
		want   int
	}{
		{30, g6protocol.MaxConcurrent30U},
		{100, g6protocol.MaxConcurrent100U},
		{1000, g6protocol.MaxConcurrent1000U},
	} {
		if got := sizing.MaxConcurrent(tc.equity); got != tc.want {
			t.Fatalf("equity %v concurrent=%d want %d", tc.equity, got, tc.want)
		}
	}
}

func TestStressBudgetSizing30_100_1000(t *testing.T) {
	for _, equity := range g6protocol.CapitalTiersUSDT {
		ref := 100.0
		stressAtRef := 5.0
		res := sizing.SizeEventNotional(sizing.Input{
			EquityUSDT:            equity,
			ConservativeEdgeUSDT:  1.0,
			StressLossUSDT:        stressAtRef,
			ReferenceNotionalUSDT: ref,
			MarginTimeSec:         3600,
		})
		if res.Skipped {
			t.Fatalf("equity %v skipped: %s", equity, res.SkipReason)
		}
		wantBudget := equity * g6protocol.MaxEventStressLossFraction
		if math.Abs(res.StressBudgetUSDT-wantBudget) > 1e-6 {
			t.Fatalf("equity %v stress budget=%v want %v", equity, res.StressBudgetUSDT, wantBudget)
		}
		wantNotional := wantBudget * ref / stressAtRef
		if math.Abs(res.NotionalUSDT-wantNotional) > 1e-6 {
			t.Fatalf("equity %v notional=%v want %v", equity, res.NotionalUSDT, wantNotional)
		}
		maxLev := equity * float64(g6protocol.MaxAccountLeverage)
		if res.NotionalUSDT > maxLev+1e-6 {
			t.Fatalf("equity %v exceeded leverage cap", equity)
		}
	}
}

func TestMinOrderSkipNoUpwardRounding(t *testing.T) {
	res := sizing.SizeEventNotional(sizing.Input{
		EquityUSDT:            30,
		ConservativeEdgeUSDT:  1,
		StressLossUSDT:        10,
		ReferenceNotionalUSDT: 100,
		MarginTimeSec:         3600,
		MinNotionalUSDT:       20,
		LotNotionalUSDT:       5,
	})
	if !res.Skipped || res.SkipReason != sizing.SkipBelowMinNotional {
		t.Fatalf("want skip below min notional got skipped=%v reason=%q notional=%v", res.Skipped, res.SkipReason, res.NotionalUSDT)
	}
	floored := sizing.ApplyLotFloorDown(17, 5, 0)
	if floored != 15 {
		t.Fatalf("lot floor down got %v want 15", floored)
	}
}

func TestAccountStressCapClipsNotional(t *testing.T) {
	res := sizing.SizeEventNotional(sizing.Input{
		EquityUSDT:            100,
		ConservativeEdgeUSDT:  2,
		StressLossUSDT:        1,
		ReferenceNotionalUSDT: 100,
		MarginTimeSec:         3600,
		UsedAccountStressUSDT: 100 * g6protocol.MaxAccountStressLossFraction,
	})
	if !res.Skipped || res.SkipReason != sizing.SkipAccountStressCap {
		t.Fatalf("want account stress cap skip got %v/%q", res.Skipped, res.SkipReason)
	}
}

func TestRankingScoreDescending(t *testing.T) {
	high := sizing.RankingScore(10, 1, 100)
	low := sizing.RankingScore(5, 1, 100)
	if high <= low {
		t.Fatal("higher edge must rank above lower edge")
	}
}
