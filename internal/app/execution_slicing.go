package app

import (
	"math"
	"time"
)

// execution_slicing.go 实现 S8-B「TWAP/冰山执行切片」(R13)的纯规划层:把一笔大单按名义阈值/上限切成多片,
// 平滑大资金进出的流动性冲击。规划与执行解耦——本文件只产出确定性的切片计划(可单测),实际把计划提交到
// 交易所的双腿 lockstep 执行接线在 ExecutionSlicingEnabled flag 后分阶段灰度(切片间不得产生裸腿)。

// executionSlice 是一片切单:数量 + 相对上一片的等待时延(首片为 0)。
type executionSlice struct {
	Qty   float64
	Delay time.Duration
}

// sliceConfig 是切片规划入参(来自 ExecutionConfig)。
type sliceConfig struct {
	Enabled           bool
	ThresholdNotional float64       // 单腿名义超过此值才切片;否则单发
	MaxSliceNotional  float64       // 每片名义上限
	Interval          time.Duration // 片间间隔(TWAP 节奏)
	MaxSlices         int           // 最多切几片(防过度切片放大下单数/费用)
}

// planExecutionSlices 把总量 totalQty(对应名义 totalNotional)规划为若干切片。
// 规则(全部满足):
//   - 未启用 / 名义 ≤ 阈值 / 配置非法 → 单片(即不切,返回 [{totalQty,0}]),保证零行为变更;
//   - 否则按 ceil(totalNotional/MaxSliceNotional) 决定片数,封顶 MaxSlices;
//   - 各片数量等分(避免末片畸小),片间间隔 = Interval,首片 Delay=0;
//   - totalQty ≤ 0 → 返回空(无单可下)。
func planExecutionSlices(totalQty, totalNotional float64, cfg sliceConfig) []executionSlice {
	if totalQty <= 0 {
		return nil
	}
	single := []executionSlice{{Qty: totalQty, Delay: 0}}
	if !cfg.Enabled || cfg.MaxSliceNotional <= 0 || cfg.MaxSlices <= 1 {
		return single
	}
	if totalNotional <= cfg.ThresholdNotional {
		return single
	}
	n := int(math.Ceil(totalNotional / cfg.MaxSliceNotional))
	if n < 2 {
		return single
	}
	if n > cfg.MaxSlices {
		n = cfg.MaxSlices
	}
	per := totalQty / float64(n)
	slices := make([]executionSlice, 0, n)
	var allocated float64
	for i := 0; i < n; i++ {
		qty := per
		if i == n-1 {
			// 末片吃掉浮点余量,保证 Σ 切片 == totalQty(精确对齐,避免少下/多下)。
			qty = totalQty - allocated
		}
		delay := cfg.Interval
		if i == 0 {
			delay = 0
		}
		slices = append(slices, executionSlice{Qty: qty, Delay: delay})
		allocated += per
	}
	return slices
}

// sliceConfigFromExecution 从 ExecutionConfig 装配切片规划入参。
func sliceConfigFromExecution(cfg ExecutionConfig) sliceConfig {
	return sliceConfig{
		Enabled:           cfg.ExecutionSlicingEnabled,
		ThresholdNotional: cfg.SliceThresholdNotionalUSDT,
		MaxSliceNotional:  cfg.MaxSliceNotionalUSDT,
		Interval:          time.Duration(cfg.SliceIntervalMs) * time.Millisecond,
		MaxSlices:         cfg.MaxSlices,
	}
}
