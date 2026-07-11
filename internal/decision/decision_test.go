package decision

import (
	"math"
	"testing"

	"lingxi/internal/mechanism"
	"lingxi/internal/money"
)

func TestWeakestLinkConfidence(t *testing.T) {
	if got := WeakestLinkConfidence(); got != 0 {
		t.Fatalf("empty links should be 0, got %v", got)
	}
	if got := WeakestLinkConfidence(0.9, 0.3, 0.7); got != 0.3 {
		t.Fatalf("weakest link should be 0.3, got %v", got)
	}
	if got := WeakestLinkConfidence(0.9, 0, 0.7); got != 0 {
		t.Fatalf("a zero link should collapse confidence to 0, got %v", got)
	}
	if got := WeakestLinkConfidence(0.9, -0.1); got != 0 {
		t.Fatalf("a negative link should collapse to 0, got %v", got)
	}
}

func TestNHardTakesMinimumOfSetCaps(t *testing.T) {
	h := HardCaps{
		MaintenanceMargin: money.FromUSDT(1000),
		LiquidationRoot:   money.FromUSDT(600),
		ExtremeScenario:   money.FromUSDT(900),
		Liquidity:         money.FromUSDT(800),
		Beta:              money.FromUSDT(750),
		MechanismCluster:  money.FromUSDT(700),
		OrderGrid:         money.FromUSDT(650),
		AccountTotalRisk:  money.FromUSDT(950),
	}
	if got := h.NHard(); got != money.FromUSDT(600) {
		t.Fatalf("N_hard should be min of set caps (600), got %s", got)
	}

	if got := (HardCaps{}).NHard(); got != 0 {
		t.Fatalf("all-unset caps should give N_hard 0, got %s", got)
	}

	h.OrderGrid = 0
	if got := h.NHard(); got != 0 {
		t.Fatalf("an unknown/zero hard cap must force N_hard 0, got %s", got)
	}
}

// TestNTargetCannotBreachHardCap 钉死信心只能缩放、不能突破硬上限。
func TestNTargetCannotBreachHardCap(t *testing.T) {
	nHard := money.FromUSDT(1000)

	full := NTarget(nHard, TargetInputs{1, 1, 1, 1, 1})
	if full != nHard {
		t.Fatalf("all-ones factors should give exactly N_hard, got %s", full)
	}

	// 即便传入越界 >1 的因子，也被夹到 N_hard 以内。
	over := NTarget(nHard, TargetInputs{2, 2, 2, 2, 2})
	if over != nHard {
		t.Fatalf("out-of-range factors must not breach N_hard, got %s", over)
	}

	scaled := NTarget(nHard, TargetInputs{0.5, 1, 1, 1, 1})
	if scaled != money.FromUSDT(500) {
		t.Fatalf("0.5 confidence should halve target, got %s", scaled)
	}

	if got := NTarget(0, TargetInputs{1, 1, 1, 1, 1}); got != 0 {
		t.Fatalf("zero N_hard should give zero target, got %s", got)
	}
}

// TestAllocateLotsFloorsAndSkips 钉死整数 lot 只向下取整，不足最小单位则跳过（绝不向上取整）。
func TestAllocateLotsFloorsAndSkips(t *testing.T) {
	lot := money.FromUSDT(10)

	// 25 USDT / 10 = 2 lot（floor），实际 20 USDT。
	plan := AllocateLots(money.FromUSDT(25), lot, 0)
	if plan.Skipped || plan.Lots != 2 || plan.Notional != money.FromUSDT(20) {
		t.Fatalf("expected 2 lots / 20 USDT, got %+v", plan)
	}

	// 理论仓位不足一个 lot → 跳过，绝不向上取整成 1 lot。
	plan = AllocateLots(money.FromUSDT(7), lot, 0)
	if !plan.Skipped || plan.SkipReason != SkipBelowMinLot {
		t.Fatalf("sub-lot target must skip, got %+v", plan)
	}

	// 取整后名义低于交易所最小名义 → 跳过。
	plan = AllocateLots(money.FromUSDT(25), lot, money.FromUSDT(50))
	if !plan.Skipped || plan.SkipReason != SkipBelowMinNotional {
		t.Fatalf("below min notional must skip, got %+v", plan)
	}

	plan = AllocateLots(0, lot, 0)
	if !plan.Skipped || plan.SkipReason != SkipNonPositiveTarget {
		t.Fatalf("non-positive target must skip, got %+v", plan)
	}
}

func TestAssessOODForcesFlat(t *testing.T) {
	cfg := DefaultOODConfig()

	known := mechanism.MechanismEventCard{
		Regime:  mechanism.Regime{Kind: mechanism.RegimePanicLiquidation},
		Quality: mechanism.Quality{ObservationConfidence: 0.9, DataAgeMs: 100},
	}
	if v := AssessOOD(known, cfg); v.ForceFlat || v.OutOfDistribution {
		t.Fatalf("clean known card should not force flat: %+v", v)
	}

	unknown := known
	unknown.Regime.Kind = mechanism.RegimeUnknown
	if v := AssessOOD(unknown, cfg); !v.ForceFlat || !v.OutOfDistribution {
		t.Fatalf("unknown regime should force flat + OOD: %+v", v)
	}

	stale := known
	stale.Quality.DataAgeMs = 999_999
	if v := AssessOOD(stale, cfg); !v.ForceFlat {
		t.Fatalf("stale data should force flat: %+v", v)
	}

	flagged := known
	flagged.Quality.OutOfDistribution = true
	if v := AssessOOD(flagged, cfg); !v.OutOfDistribution || !v.ForceFlat {
		t.Fatalf("explicit OOD flag should force flat: %+v", v)
	}

	novel := known
	novel.Regime.Kind = mechanism.RegimeKind("future_unregistered_regime")
	if v := AssessOOD(novel, cfg); !v.OutOfDistribution || !v.ForceFlat {
		t.Fatalf("unregistered regime should be open-world OOD: %+v", v)
	}

	invalidConfidence := known
	invalidConfidence.Quality.ObservationConfidence = math.NaN()
	if v := AssessOOD(invalidConfidence, cfg); !v.ForceFlat {
		t.Fatalf("NaN confidence should force flat: %+v", v)
	}
}
