package app

import (
	"math"
	"testing"

	"lingxi/internal/core"
	"lingxi/internal/exchange"
)

func TestCapPinnedFundingEstimate(t *testing.T) {
	const eps = 1e-12
	cases := []struct {
		name        string
		last        float64
		estimate    float64
		cap         float64
		floor       float64
		proximity   float64
		factor      float64
		wantApplied bool
		wantRate    float64
	}{
		{
			name:        "FIDA 负费率撞底被R7归零_全额豁免",
			last:        -0.02,
			estimate:    0, // R7 fundingSignConflict 归零
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      1.0,
			wantApplied: true,
			wantRate:    -0.02,
		},
		{
			name:        "正费率撞顶被归零_全额豁免",
			last:        0.02,
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      1.0,
			wantApplied: true,
			wantRate:    0.02,
		},
		{
			name:        "撞底但factor打折_按比例修正",
			last:        -0.02,
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      0.7,
			wantApplied: true,
			wantRate:    -0.014,
		},
		{
			name:        "未贴限_不豁免",
			last:        -0.005,
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      1.0,
			wantApplied: false,
			wantRate:    0,
		},
		{
			name:        "已是全额估计_无需豁免",
			last:        -0.02,
			estimate:    -0.02,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      1.0,
			wantApplied: false,
			wantRate:    -0.02,
		},
		{
			name:        "proximity非法_不豁免",
			last:        -0.02,
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0,
			factor:      1.0,
			wantApplied: false,
			wantRate:    0,
		},
		{
			name:        "factor非法_不豁免",
			last:        -0.02,
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      0,
			wantApplied: false,
			wantRate:    0,
		},
		{
			name:        "越过proximity阈值_豁免",
			last:        -0.0185, // 已越过 floor(-0.02) * proximity(0.9) = -0.018
			estimate:    0,
			cap:         0.02,
			floor:       -0.02,
			proximity:   0.9,
			factor:      1.0,
			wantApplied: true,
			wantRate:    -0.0185,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := capPinnedFundingEstimate(tc.last, tc.estimate, tc.cap, tc.floor, tc.proximity, tc.factor)
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			if math.Abs(got-tc.wantRate) > eps {
				t.Fatalf("rate = %.6f, want %.6f", got, tc.wantRate)
			}
		})
	}
}

func TestLoadFundingRateCaps(t *testing.T) {
	caps := loadFundingRateCaps([]exchange.FundingInfoItem{
		{Symbol: "fidausdt", AdjustedFundingRateCap: "0.02", AdjustedFundingRateFloor: "-0.02"},
		{Symbol: "  ", AdjustedFundingRateCap: "0.02"},
		{Symbol: "EMPTYUSDT", AdjustedFundingRateCap: "0", AdjustedFundingRateFloor: "0"},
	})
	if len(caps) != 1 {
		t.Fatalf("应只收录 1 个有效条目, got %d", len(caps))
	}
	info, ok := caps["FIDAUSDT"]
	if !ok {
		t.Fatalf("FIDAUSDT 应被收录(且 symbol 归一为大写)")
	}
	if info.Cap != 0.02 || info.Floor != -0.02 {
		t.Fatalf("FIDAUSDT 上下限解析错误: %+v", info)
	}
}

func TestFundingCapFloorForFallback(t *testing.T) {
	e := &FundingRateSniperEngine{
		fundingRateCapBySymbol: map[string]fundingRateCapFloor{
			"FIDAUSDT": {Cap: 0.025, Floor: -0.025},
		},
	}
	if got := e.fundingCapFloorFor("fidausdt"); got.Cap != 0.025 || got.Floor != -0.025 {
		t.Fatalf("已知 symbol 应返回交易所值: %+v", got)
	}
	fallback := e.fundingCapFloorFor("UNKNOWNUSDT")
	if fallback.Cap != defaultFundingRateCapAbs || fallback.Floor != -defaultFundingRateCapAbs {
		t.Fatalf("未知 symbol 应回退默认 ±%.2f: %+v", defaultFundingRateCapAbs, fallback)
	}
}

func TestApplyCapPinnedFundingOverrideToggle(t *testing.T) {
	newEngine := func(enabled bool) *FundingRateSniperEngine {
		cfg := core.DefaultEngineConfig()
		cfg.StrategyConfig.CapPinnedFundingOverrideEnabled = enabled
		return &FundingRateSniperEngine{
			cfg: cfg,
			fundingRateCapBySymbol: map[string]fundingRateCapFloor{
				"FIDAUSDT": {Cap: 0.02, Floor: -0.02},
			},
		}
	}

	// 关闭时:原样返回,跳变不变。
	off := newEngine(false)
	rate, jump, applied := off.applyCapPinnedFundingOverride("FIDAUSDT", -0.02, 0, 0.02)
	if applied || rate != 0 || jump != 0.02 {
		t.Fatalf("开关关闭时不应修正: rate=%.6f jump=%.6f applied=%v", rate, jump, applied)
	}

	// 开启时:修正为 last,跳变归零。
	on := newEngine(true)
	rate, jump, applied = on.applyCapPinnedFundingOverride("FIDAUSDT", -0.02, 0, 0.02)
	if !applied || math.Abs(rate-(-0.02)) > 1e-12 || math.Abs(jump) > 1e-12 {
		t.Fatalf("开关开启且撞限时应修正: rate=%.6f jump=%.6f applied=%v", rate, jump, applied)
	}
}
