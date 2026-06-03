package execution

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestFloorAndCeilToStep(t *testing.T) {
	if got := FloorToStep(10.27, 0.05); !approxEqual(got, 10.25) {
		t.Fatalf("FloorToStep=%v want 10.25", got)
	}
	if got := CeilToStep(10.21, 0.05); !approxEqual(got, 10.25) {
		t.Fatalf("CeilToStep=%v want 10.25", got)
	}
	// step<=0 原样返回
	if got := FloorToStep(10.27, 0); !approxEqual(got, 10.27) {
		t.Fatalf("FloorToStep step=0 =%v want 10.27", got)
	}
	if got := CeilToStep(10.27, -1); !approxEqual(got, 10.27) {
		t.Fatalf("CeilToStep step<0 =%v want 10.27", got)
	}
}

func TestMakerLimitPrice(t *testing.T) {
	if got := MakerLimitPrice("buy", 100.07, 100.20, 0.05); !approxEqual(got, 100.05) {
		t.Fatalf("BUY maker=%v want 100.05", got)
	}
	if got := MakerLimitPrice("SELL", 100.07, 100.21, 0.05); !approxEqual(got, 100.25) {
		t.Fatalf("SELL maker=%v want 100.25", got)
	}
	if got := MakerLimitPrice("BUY", 0, 100, 0.05); got != 0 {
		t.Fatalf("BUY with no bid=%v want 0", got)
	}
	if got := MakerLimitPrice("HOLD", 1, 2, 0.05); got != 0 {
		t.Fatalf("invalid side=%v want 0", got)
	}
}

func TestProtectedLimitPriceDirectional(t *testing.T) {
	buy := ProtectedLimitPrice("BUY", 100, 0.01)
	if !approxEqual(buy, 101) {
		t.Fatalf("BUY protected=%v want 101", buy)
	}
	sell := ProtectedLimitPrice("SELL", 100, 0.01)
	if !approxEqual(sell, 99) {
		t.Fatalf("SELL protected=%v want 99", sell)
	}
	// SELL 极端滑点至少保留 0.0001 比例，不归零
	floor := ProtectedLimitPrice("SELL", 100, 5)
	if !approxEqual(floor, 100*0.0001) {
		t.Fatalf("SELL clamp floor=%v want 0.01", floor)
	}
	if got := ProtectedLimitPrice("BUY", 0, 0.01); got != 0 {
		t.Fatalf("zero reference=%v want 0", got)
	}
}

func TestNormalizeAndWidenedProtectedLimitPrice(t *testing.T) {
	// BUY 向上对齐
	if got := NormalizeProtectedLimitPrice("BUY", 100, 0.012, 0.05); !approxEqual(got, 101.20) {
		t.Fatalf("BUY normalized=%v want 101.20", got)
	}
	// SELL 向下对齐
	if got := NormalizeProtectedLimitPrice("SELL", 100, 0.012, 0.05); !approxEqual(got, 98.80) {
		t.Fatalf("SELL normalized=%v want 98.80", got)
	}
	// 扩宽倍数放大滑点
	widened := WidenedProtectedLimitPrice("BUY", 100, 0.01, 0.05, 3)
	normal := NormalizeProtectedLimitPrice("BUY", 100, 0.03, 0.05)
	if !approxEqual(widened, normal) {
		t.Fatalf("widened=%v want %v", widened, normal)
	}
}

func TestClampChaseLimitPrice(t *testing.T) {
	if got := ClampChaseLimitPrice("SELL", 98, 99); !approxEqual(got, 99) {
		t.Fatalf("SELL clamp=%v want 99 (not below hard limit)", got)
	}
	if got := ClampChaseLimitPrice("BUY", 102, 101); !approxEqual(got, 101) {
		t.Fatalf("BUY clamp=%v want 101 (not above hard limit)", got)
	}
	if got := ClampChaseLimitPrice("SELL", 100, 0); !approxEqual(got, 100) {
		t.Fatalf("no hard limit=%v want 100", got)
	}
}

func TestSlippageForAttempt(t *testing.T) {
	schedule := []float64{0.0010, 0.0025, 0.0020, 0.0035}
	if got := SlippageForAttempt(schedule, 1, 0.0010); !approxEqual(got, 0.0025) {
		t.Fatalf("attempt1=%v want 0.0025", got)
	}
	// 越界回退末位
	if got := SlippageForAttempt(schedule, 99, 0.0010); !approxEqual(got, 0.0035) {
		t.Fatalf("overflow=%v want 0.0035", got)
	}
	// 负数归零取首位
	if got := SlippageForAttempt(schedule, -5, 0.0010); !approxEqual(got, 0.0010) {
		t.Fatalf("negative=%v want 0.0010", got)
	}
	// 空表回退 fallback
	if got := SlippageForAttempt(nil, 0, 0.0042); !approxEqual(got, 0.0042) {
		t.Fatalf("empty=%v want 0.0042", got)
	}
}

func TestChaseReferencePriceReanchors(t *testing.T) {
	opts := ChaseOptions{AnchorPrice: 100, HardLimitPrice: 95, ReanchorAfter: 2}
	// 锚定期内固定用锚价
	if got := ChaseReferencePrice(98, 0, opts); !approxEqual(got, 100) {
		t.Fatalf("attempt0=%v want anchor 100", got)
	}
	if got := ChaseReferencePrice(98, 1, opts); !approxEqual(got, 100) {
		t.Fatalf("attempt1=%v want anchor 100", got)
	}
	// 达到 ReanchorAfter 后改用市场价
	if got := ChaseReferencePrice(97, 2, opts); !approxEqual(got, 97) {
		t.Fatalf("attempt2=%v want market 97", got)
	}
	// 市场价缺失回退锚价
	if got := ChaseReferencePrice(0, 2, opts); !approxEqual(got, 100) {
		t.Fatalf("no market=%v want anchor 100", got)
	}
}

func TestBuildAnchoredChaseOptions(t *testing.T) {
	schedule := []float64{0.0010, 0.0025, 0.0020, 0.0035}
	opts := BuildAnchoredChaseOptions("SELL", 100, 0.05, schedule, 4, 0.0010)
	if !approxEqual(opts.AnchorPrice, 100) {
		t.Fatalf("anchor=%v want 100", opts.AnchorPrice)
	}
	if opts.ReanchorAfter != 3 {
		t.Fatalf("reanchorAfter=%d want 3", opts.ReanchorAfter)
	}
	// 最大滑点 0.0035，SELL 向下对齐到 tick
	wantHard := NormalizeProtectedLimitPrice("SELL", 100, 0.0035, 0.05)
	if !approxEqual(opts.HardLimitPrice, wantHard) {
		t.Fatalf("hardLimit=%v want %v", opts.HardLimitPrice, wantHard)
	}
	// anchor<=0 返回零值
	if zero := BuildAnchoredChaseOptions("SELL", 0, 0.05, schedule, 4, 0.0010); zero != (ChaseOptions{}) {
		t.Fatalf("anchor<=0 should yield zero options, got %+v", zero)
	}
}
