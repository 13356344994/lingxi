package app

import (
	"math"
	"testing"
)

// TestParseVenuePlansNoSection 钉死后向兼容铁律:无 venues: 段 → nil 计划(单所旧路径)。
func TestParseVenuePlansNoSection(t *testing.T) {
	yaml := "active_profile: standard\nstrategy:\n  min_estimated_funding_rate: 0.0030\n"
	plans, err := parseVenuePlans([]byte(yaml), DefaultEngineConfig(), false)
	if err != nil {
		t.Fatalf("parseVenuePlans() error = %v", err)
	}
	if plans != nil {
		t.Fatalf("无 venues 段应返回 nil 计划, got %+v", plans)
	}
}

// TestParseVenuePlansBybitExecutorOverride 钉死 bybit_executor 嵌套段经 venues 覆盖能正确绑定到
// EngineConfig.BybitExecutor(主安全闸 + 各执行器参数),保证「写进 config 即生效」。
func TestParseVenuePlansBybitExecutorOverride(t *testing.T) {
	yaml := `
venues:
  bybit:
    enabled: true
    autostart: false
    allocated_capital_usdt: 300
    bybit_executor:
      live_trading_enabled: true
      top_n: 2
      min_funding_rate: 0.0008
      lead_seconds: 90
      max_concurrent_positions: 3
      max_position_notional_usdt: 200
`
	plans, err := parseVenuePlans([]byte(yaml), DefaultEngineConfig(), false)
	if err != nil {
		t.Fatalf("parseVenuePlans() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("应解析出 1 个 venue 计划, got %d", len(plans))
	}
	bx := plans[0].Config.BybitExecutor
	if !bx.LiveTradingEnabled {
		t.Fatal("live_trading_enabled 覆盖应为 true")
	}
	if bx.TopN != 2 {
		t.Fatalf("top_n 应为 2, got %d", bx.TopN)
	}
	if math.Abs(bx.MinFundingRate-0.0008) > 1e-12 {
		t.Fatalf("min_funding_rate 应为 0.0008, got %v", bx.MinFundingRate)
	}
	if bx.LeadSeconds != 90 {
		t.Fatalf("lead_seconds 应为 90, got %d", bx.LeadSeconds)
	}
	if bx.MaxConcurrentPositions != 3 {
		t.Fatalf("max_concurrent_positions 应为 3, got %d", bx.MaxConcurrentPositions)
	}
	if math.Abs(bx.MaxPositionNotionalUSDT-200) > 1e-9 {
		t.Fatalf("max_position_notional_usdt 应为 200, got %v", bx.MaxPositionNotionalUSDT)
	}
	if plans[0].Config.AllocatedCapitalUSDT != 300 {
		t.Fatalf("allocated_capital_usdt 应为 300, got %v", plans[0].Config.AllocatedCapitalUSDT)
	}
}

// TestParseVenuePlansMergeAndMeta 校验每所 = 全局 + 覆盖,且 enabled/autostart 元信息正确抽出。
func TestParseVenuePlansMergeAndMeta(t *testing.T) {
	yaml := `
strategy:
  min_estimated_funding_rate: 0.0030
venues:
  binance:
    enabled: true
    autostart: false
    allocated_capital_usdt: 500
    active_profile: research
    strategy:
      min_estimated_funding_rate: 0.0034
    execution:
      shadow_trading: true
  okx:
    enabled: false
    allocated_capital_usdt: 300
`
	global := DefaultEngineConfig()
	plans, err := parseVenuePlans([]byte(yaml), global, false)
	if err != nil {
		t.Fatalf("parseVenuePlans() error = %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("应解析出 2 个所, got %d", len(plans))
	}

	// 文档序:binance 在前。
	b := plans[0]
	if b.Name != "binance" || !b.Enabled || b.Autostart {
		t.Fatalf("binance 计划元信息错误: %+v", b)
	}
	if math.Abs(b.Config.AllocatedCapitalUSDT-500) > 1e-9 {
		t.Fatalf("binance allocated_capital 覆盖应为 500, got %v", b.Config.AllocatedCapitalUSDT)
	}
	if math.Abs(b.Config.StrategyConfig.MinEstimatedFundingRate-0.0034) > 1e-12 {
		t.Fatalf("binance strategy 覆盖应为 0.0034, got %v", b.Config.StrategyConfig.MinEstimatedFundingRate)
	}
	if !b.Config.ExecutionConfig.ShadowTrading {
		t.Fatalf("binance execution.shadow_trading 覆盖应为 true")
	}
	if string(b.Config.ActiveProfile) != "research" {
		t.Fatalf("binance active_profile 覆盖应为 research, got %s", b.Config.ActiveProfile)
	}

	// okx 禁用 + 未覆盖 strategy → 继承全局 0.0030。
	k := plans[1]
	if k.Name != "okx" || k.Enabled {
		t.Fatalf("okx 应为 disabled: %+v", k)
	}
	if math.Abs(k.Config.StrategyConfig.MinEstimatedFundingRate-global.StrategyConfig.MinEstimatedFundingRate) > 1e-12 {
		t.Fatalf("okx 未覆盖 strategy 应继承全局, got %v", k.Config.StrategyConfig.MinEstimatedFundingRate)
	}
	if math.Abs(k.Config.AllocatedCapitalUSDT-300) > 1e-9 {
		t.Fatalf("okx allocated_capital 应为 300, got %v", k.Config.AllocatedCapitalUSDT)
	}

	// enabledVenuePlans 仅留 enabled、按名排序。
	enabled := enabledVenuePlans(plans)
	if len(enabled) != 1 || enabled[0].Name != "binance" {
		t.Fatalf("enabledVenuePlans 应仅含 binance, got %+v", enabled)
	}
}

// TestParseVenuePlansDuplicateRejected 重复 venue 名应报错。
func TestParseVenuePlansDuplicateRejected(t *testing.T) {
	yaml := "venues:\n  binance: { enabled: true }\n  BINANCE: { enabled: false }\n"
	if _, err := parseVenuePlans([]byte(yaml), DefaultEngineConfig(), false); err == nil {
		t.Fatalf("重复 venue 名(大小写归一后)应报错")
	}
}

// TestParseVenuePlansBadBool enabled 非法布尔应报错。
func TestParseVenuePlansBadBool(t *testing.T) {
	yaml := "venues:\n  binance: { enabled: maybe }\n"
	if _, err := parseVenuePlans([]byte(yaml), DefaultEngineConfig(), false); err == nil {
		t.Fatalf("enabled 非法布尔应报错")
	}
}

// TestFlattenSkipsVenuesSubtree 校验全局 flatten 跳过 venues 子树(否则 venues.* 会被 BindConfig 当未知键)。
func TestFlattenSkipsVenuesSubtree(t *testing.T) {
	yaml := `
active_profile: standard
venues:
  binance:
    allocated_capital_usdt: 500
    strategy:
      min_estimated_funding_rate: 0.0034
`
	flat, err := flattenYAMLConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("flattenYAMLConfig() error = %v", err)
	}
	for k := range flat {
		if len(k) >= len(venuesConfigKey) && k[:len(venuesConfigKey)] == venuesConfigKey {
			t.Fatalf("全局扁平映射不应含 venues.* 键, got %q", k)
		}
	}
	if flat["active_profile"] != "standard" {
		t.Fatalf("非 venues 的全局键应正常扁平, got %q", flat["active_profile"])
	}
}
