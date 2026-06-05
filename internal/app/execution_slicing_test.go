package app

import (
	"math"
	"testing"
	"time"
)

func sumSliceQty(slices []executionSlice) float64 {
	var s float64
	for _, sl := range slices {
		s += sl.Qty
	}
	return s
}

func TestPlanExecutionSlicesDisabledOrBelowThreshold(t *testing.T) {
	disabled := sliceConfig{Enabled: false, MaxSliceNotional: 1000, MaxSlices: 8, ThresholdNotional: 2000}
	if got := planExecutionSlices(10, 5000, disabled); len(got) != 1 || got[0].Qty != 10 {
		t.Fatalf("未启用应单发, got %+v", got)
	}
	enabled := sliceConfig{Enabled: true, MaxSliceNotional: 1000, MaxSlices: 8, ThresholdNotional: 2000, Interval: time.Second}
	// 名义 1500 ≤ 阈值 2000 → 单发。
	if got := planExecutionSlices(10, 1500, enabled); len(got) != 1 {
		t.Fatalf("名义低于阈值应单发, got %+v", got)
	}
}

func TestPlanExecutionSlicesTWAP(t *testing.T) {
	cfg := sliceConfig{Enabled: true, ThresholdNotional: 2000, MaxSliceNotional: 1000, Interval: 800 * time.Millisecond, MaxSlices: 8}
	// 名义 5000,每片 ≤1000 → ceil(5)=5 片。
	slices := planExecutionSlices(50, 5000, cfg)
	if len(slices) != 5 {
		t.Fatalf("5000/1000 应切 5 片, got %d", len(slices))
	}
	// Σ 切片精确等于总量。
	if math.Abs(sumSliceQty(slices)-50) > 1e-9 {
		t.Fatalf("切片数量之和应等于总量 50, got %.9f", sumSliceQty(slices))
	}
	// 首片无延迟,其余片有间隔。
	if slices[0].Delay != 0 {
		t.Fatalf("首片应无延迟, got %v", slices[0].Delay)
	}
	for i := 1; i < len(slices); i++ {
		if slices[i].Delay != 800*time.Millisecond {
			t.Fatalf("片 %d 间隔应为 800ms, got %v", i, slices[i].Delay)
		}
	}
}

func TestPlanExecutionSlicesCapsAtMaxSlices(t *testing.T) {
	cfg := sliceConfig{Enabled: true, ThresholdNotional: 1000, MaxSliceNotional: 1000, Interval: time.Second, MaxSlices: 3}
	// 名义 100000 → ceil(100) 片,但封顶 3 片。
	slices := planExecutionSlices(1000, 100000, cfg)
	if len(slices) != 3 {
		t.Fatalf("应封顶 MaxSlices=3, got %d", len(slices))
	}
	if math.Abs(sumSliceQty(slices)-1000) > 1e-9 {
		t.Fatalf("封顶后切片之和仍应等于总量, got %.9f", sumSliceQty(slices))
	}
}

func TestPlanExecutionSlicesZeroQty(t *testing.T) {
	cfg := sliceConfig{Enabled: true, ThresholdNotional: 0, MaxSliceNotional: 1000, Interval: time.Second, MaxSlices: 8}
	if got := planExecutionSlices(0, 5000, cfg); got != nil {
		t.Fatalf("零数量应返回空, got %+v", got)
	}
}
