package marketdata

import (
	"math"
	"testing"
	"time"
)

func approxEqual(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

func TestComputeAverageFillPrice(t *testing.T) {
	levels := []DepthLevel{{Price: 100, Quantity: 1}, {Price: 101, Quantity: 2}, {Price: 102, Quantity: 5}}
	// 需要 2 个：1@100 + 1@101 → 均价 100.5，最优 100，完全成交
	avg, best, filled, complete := ComputeAverageFillPrice(levels, 2)
	if !approxEqual(avg, 100.5) || !approxEqual(best, 100) || !approxEqual(filled, 2) || !complete {
		t.Fatalf("avg=%v best=%v filled=%v complete=%v", avg, best, filled, complete)
	}
	// 需求超过可见深度 → 部分成交，complete=false
	_, _, filled, complete = ComputeAverageFillPrice(levels, 100)
	if complete || !approxEqual(filled, 8) {
		t.Fatalf("partial fill expected: filled=%v complete=%v", filled, complete)
	}
	// 非法输入
	if _, _, _, ok := ComputeAverageFillPrice(nil, 1); ok {
		t.Fatalf("empty levels should be incomplete")
	}
	if _, _, _, ok := ComputeAverageFillPrice(levels, 0); ok {
		t.Fatalf("zero qty should be incomplete")
	}
}

func TestEstimateVisibleDepthNotional(t *testing.T) {
	levels := []DepthLevel{{Price: 10, Quantity: 2}, {Price: 0, Quantity: 5}, {Price: 5, Quantity: 4}}
	if got := EstimateVisibleDepthNotional(levels); !approxEqual(got, 40) {
		t.Fatalf("notional=%v want 40", got)
	}
}

func TestMinPositiveFloat(t *testing.T) {
	if got := MinPositiveFloat(0, -3, 7, 2, 9); !approxEqual(got, 2) {
		t.Fatalf("min=%v want 2", got)
	}
	if got := MinPositiveFloat(0, -1); got != 0 {
		t.Fatalf("no positive should yield 0, got %v", got)
	}
}

func TestEntrySpreadRate(t *testing.T) {
	// 正向：现货贵于合约 → 正价差
	if got := EntrySpreadRate(false, 100, 99); !approxEqual(got, 0.01) {
		t.Fatalf("positive carry spread=%v want 0.01", got)
	}
	// 负向：合约贵于现货 → 正价差
	if got := EntrySpreadRate(true, 100, 101); !approxEqual(got, 0.01) {
		t.Fatalf("negative carry spread=%v want 0.01", got)
	}
	if got := EntrySpreadRate(false, 0, 100); got != 0 {
		t.Fatalf("zero spot should yield 0, got %v", got)
	}
}

func TestPriceMovedAdversely(t *testing.T) {
	drift := 0.0008
	// 正向：现货上行超过阈值算不利
	if !PriceMovedAdversely(false, 100, 100.2, 100, 100, drift) {
		t.Fatalf("expected adverse on spot up-move (positive carry)")
	}
	// 正向：合约下行超过阈值算不利
	if !PriceMovedAdversely(false, 100, 100, 100, 99.8, drift) {
		t.Fatalf("expected adverse on futures down-move (positive carry)")
	}
	// 微小漂移不触发
	if PriceMovedAdversely(false, 100, 100.0001, 100, 100, drift) {
		t.Fatalf("tiny drift should not be adverse")
	}
	// 负向反向
	if !PriceMovedAdversely(true, 100, 99.8, 100, 100, drift) {
		t.Fatalf("expected adverse on spot down-move (negative carry)")
	}
}

func TestStaleness(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	if got := Staleness(now, 0); got != 0 {
		t.Fatalf("no timestamp should yield 0, got %v", got)
	}
	if got := Staleness(now, 1_000_500); got != 0 {
		t.Fatalf("future event (clock skew) should yield 0, got %v", got)
	}
	if got := Staleness(now, 999_000); got != time.Second {
		t.Fatalf("staleness=%v want 1s", got)
	}
}
