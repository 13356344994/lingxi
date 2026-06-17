package core

import (
	"strings"
	"testing"
)

func TestCapitalAllocatorAndDataCollectionDefaults(t *testing.T) {
	cfg := DefaultEngineConfig()

	if cfg.CapitalAllocator.Enabled {
		t.Fatal("CapitalAllocator.Enabled 默认应为 false")
	}
	if cfg.CapitalAllocator.Mode != "shadow" {
		t.Fatalf("CapitalAllocator.Mode 默认应为 shadow, got %q", cfg.CapitalAllocator.Mode)
	}
	if cfg.CapitalAllocator.MaxTotalCapitalUsageRatio != 0.2 {
		t.Fatalf("CapitalAllocator.MaxTotalCapitalUsageRatio 默认应为 0.2, got %v", cfg.CapitalAllocator.MaxTotalCapitalUsageRatio)
	}
	if cfg.CapitalAllocator.MaxDailyLossUSDT != 0.3 {
		t.Fatalf("CapitalAllocator.MaxDailyLossUSDT 默认应为 0.3, got %v", cfg.CapitalAllocator.MaxDailyLossUSDT)
	}

	if cfg.DataCollection.Enabled {
		t.Fatal("DataCollection.Enabled 默认应为 false")
	}
	if cfg.DataCollection.Mode != "shadow" {
		t.Fatalf("DataCollection.Mode 默认应为 shadow, got %q", cfg.DataCollection.Mode)
	}
	if cfg.DataCollection.StrategyType != "" {
		t.Fatalf("DataCollection.StrategyType 默认应为空, got %q", cfg.DataCollection.StrategyType)
	}
	if cfg.DataCollection.MaxPositions != 1 {
		t.Fatalf("DataCollection.MaxPositions 默认应为 1, got %d", cfg.DataCollection.MaxPositions)
	}
	if cfg.DataCollection.MaxNotionalUSDT != 10 {
		t.Fatalf("DataCollection.MaxNotionalUSDT 默认应为 10, got %v", cfg.DataCollection.MaxNotionalUSDT)
	}
	if cfg.DataCollection.MaxExpectedLossUSDTPerTrade != 0.03 {
		t.Fatalf("DataCollection.MaxExpectedLossUSDTPerTrade 默认应为 0.03, got %v", cfg.DataCollection.MaxExpectedLossUSDTPerTrade)
	}
}

func TestCapitalAllocatorAndDataCollectionBindConfig(t *testing.T) {
	cfg := DefaultEngineConfig()
	values := map[string]string{
		"capital_allocator.enabled":                    "true",
		"capital_allocator.mode":                         "profit",
		"capital_allocator.max_total_capital_usage_ratio": "0.35",
		"capital_allocator.max_daily_loss_usdt":        "0.5",
		"data_collection.enabled":                      "true",
		"data_collection.mode":                         "data_collection",
		"data_collection.strategy_type":                "perp_probe",
		"data_collection.max_positions":                "3",
		"data_collection.max_notional_usdt":            "25",
		"data_collection.max_expected_loss_usdt_per_trade": "0.05",
	}
	if err := BindConfig(&cfg, values, false); err != nil {
		t.Fatalf("BindConfig error: %v", err)
	}

	if !cfg.CapitalAllocator.Enabled {
		t.Fatal("capital_allocator.enabled 未绑定")
	}
	if cfg.CapitalAllocator.Mode != "profit" {
		t.Fatalf("capital_allocator.mode 未绑定, got %q", cfg.CapitalAllocator.Mode)
	}
	if cfg.CapitalAllocator.MaxTotalCapitalUsageRatio != 0.35 {
		t.Fatalf("capital_allocator.max_total_capital_usage_ratio 未绑定, got %v", cfg.CapitalAllocator.MaxTotalCapitalUsageRatio)
	}
	if cfg.CapitalAllocator.MaxDailyLossUSDT != 0.5 {
		t.Fatalf("capital_allocator.max_daily_loss_usdt 未绑定, got %v", cfg.CapitalAllocator.MaxDailyLossUSDT)
	}

	if !cfg.DataCollection.Enabled {
		t.Fatal("data_collection.enabled 未绑定")
	}
	if cfg.DataCollection.Mode != "data_collection" {
		t.Fatalf("data_collection.mode 未绑定, got %q", cfg.DataCollection.Mode)
	}
	if cfg.DataCollection.StrategyType != "perp_probe" {
		t.Fatalf("data_collection.strategy_type 未绑定, got %q", cfg.DataCollection.StrategyType)
	}
	if cfg.DataCollection.MaxPositions != 3 {
		t.Fatalf("data_collection.max_positions 未绑定, got %d", cfg.DataCollection.MaxPositions)
	}
	if cfg.DataCollection.MaxNotionalUSDT != 25 {
		t.Fatalf("data_collection.max_notional_usdt 未绑定, got %v", cfg.DataCollection.MaxNotionalUSDT)
	}
	if cfg.DataCollection.MaxExpectedLossUSDTPerTrade != 0.05 {
		t.Fatalf("data_collection.max_expected_loss_usdt_per_trade 未绑定, got %v", cfg.DataCollection.MaxExpectedLossUSDTPerTrade)
	}
}

func TestSampleConfigYAMLIncludesNewSections(t *testing.T) {
	yaml := SampleConfigYAML()
	if !strings.Contains(yaml, "capital_allocator") {
		t.Fatal("SampleConfigYAML 应包含 capital_allocator")
	}
	if !strings.Contains(yaml, "data_collection") {
		t.Fatal("SampleConfigYAML 应包含 data_collection")
	}
}

func TestCapitalAllocatorValidateGatesWhenEnabled(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.CapitalAllocator.Enabled = true
	cfg.CapitalAllocator.MaxTotalCapitalUsageRatio = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("启用 CapitalAllocator 且 MaxTotalCapitalUsageRatio=0 应校验失败")
	}

	cfg = DefaultEngineConfig()
	cfg.CapitalAllocator.MaxTotalCapitalUsageRatio = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("禁用 CapitalAllocator 时 MaxTotalCapitalUsageRatio=0 应通过校验, got %v", err)
	}
}

func TestDataCollectionValidateGatesWhenEnabled(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.DataCollection.Enabled = true
	cfg.DataCollection.MaxNotionalUSDT = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("启用 DataCollection 且 MaxNotionalUSDT=0 应校验失败")
	}

	cfg = DefaultEngineConfig()
	cfg.DataCollection.MaxNotionalUSDT = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("禁用 DataCollection 时 MaxNotionalUSDT=0 应通过校验, got %v", err)
	}
}
