package main

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultEngineConfigSectionsExposePromotedFields(t *testing.T) {
	cfg := DefaultEngineConfig()

	if cfg.NetworkConfig.HTTPTimeout != 10*time.Second || cfg.HTTPTimeout != cfg.NetworkConfig.HTTPTimeout {
		t.Fatalf("network config mismatch: cfg=%s grouped=%s", cfg.HTTPTimeout, cfg.NetworkConfig.HTTPTimeout)
	}
	if cfg.StrategyConfig.PerSymbolQuoteUSDT != 100 || cfg.PerSymbolQuoteUSDT != cfg.StrategyConfig.PerSymbolQuoteUSDT {
		t.Fatalf("strategy config mismatch: flat=%.2f grouped=%.2f", cfg.PerSymbolQuoteUSDT, cfg.StrategyConfig.PerSymbolQuoteUSDT)
	}
	if cfg.StrategyConfig.MaxVisibleDepthShare != 0.05 || cfg.MaxVisibleDepthShare != cfg.StrategyConfig.MaxVisibleDepthShare {
		t.Fatalf("strategy visible depth share mismatch: flat=%.2f grouped=%.2f", cfg.MaxVisibleDepthShare, cfg.StrategyConfig.MaxVisibleDepthShare)
	}
	if cfg.ExecutionConfig.ExecutionConfirmTimeout != 1500*time.Millisecond || cfg.ExecutionConfirmTimeout != cfg.ExecutionConfig.ExecutionConfirmTimeout {
		t.Fatalf("execution config mismatch: flat=%s grouped=%s", cfg.ExecutionConfirmTimeout, cfg.ExecutionConfig.ExecutionConfirmTimeout)
	}
	if cfg.RiskConfig.PlatformAbnormalCooldown != 15*time.Minute || cfg.PlatformAbnormalCooldown != cfg.RiskConfig.PlatformAbnormalCooldown {
		t.Fatalf("risk config mismatch: flat=%s grouped=%s", cfg.PlatformAbnormalCooldown, cfg.RiskConfig.PlatformAbnormalCooldown)
	}
	if cfg.HistoricalConfig.ReferenceKlineLookback != 60 || cfg.HistoricalConfig.CacheDir == "" {
		t.Fatalf("historical defaults not initialized: %+v", cfg.HistoricalConfig)
	}
	if !cfg.HistoricalConfig.BatchExperimentEnabled {
		t.Fatal("expected historical batch experiment enabled by default")
	}
	if cfg.HistoricalConfig.CrossStableQuoteMaxDeviationRate != 0.0010 {
		t.Fatalf("unexpected historical cross stable deviation default: %.4f", cfg.HistoricalConfig.CrossStableQuoteMaxDeviationRate)
	}
	if cfg.HistoricalConfig.BatchSpotTakerFeeRates != "0.0010,0.0000" || cfg.HistoricalConfig.BatchPerLegNotionalUSDT != "5,10,20" || cfg.HistoricalConfig.BatchMaxFundingCyclesHeld != "1,2" {
		t.Fatalf("unexpected historical batch defaults: %+v", cfg.HistoricalConfig)
	}
}

func TestRuntimeRiskConfigFromEngineConfigUsesGroupedSections(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.PerSymbolQuoteUSDT = 250
	cfg.RiskConfig.MinNetProfitRate = 0.0042
	cfg.ExecutionConfig.ExitDelayAfterFunding = 4 * time.Second
	cfg.ExecutionConfig.ExitMonitorInterval = 300 * time.Millisecond
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 3 * time.Second
	cfg.RiskConfig.PlatformAbnormalCooldown = 12 * time.Minute

	got := runtimeRiskConfigFromEngineConfig(cfg)
	if got.PerSymbolQuoteUSDT != 250 {
		t.Fatalf("expected grouped strategy value to be used, got %.2f", got.PerSymbolQuoteUSDT)
	}
	if got.MinNetProfitRate != 0.0042 {
		t.Fatalf("expected grouped risk value to be used, got %.4f", got.MinNetProfitRate)
	}
	if got.ExitDelayAfterFunding != "4s" {
		t.Fatalf("expected grouped exit delay, got %s", got.ExitDelayAfterFunding)
	}
	if got.ExitMonitorInterval != "300ms" {
		t.Fatalf("expected grouped exit monitor interval, got %s", got.ExitMonitorInterval)
	}
	if got.ExecutionConfirmTimeout != "3s" {
		t.Fatalf("expected grouped execution timeout, got %s", got.ExecutionConfirmTimeout)
	}
	if got.PlatformAbnormalCooldown != "12m0s" {
		t.Fatalf("expected grouped cooldown, got %s", got.PlatformAbnormalCooldown)
	}
}

func TestBacktestRunnerHistoricalConfigUsesGroupedFallbacks(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.NetworkConfig.RecordDir = "tmp-records"
	cfg.HistoricalConfig = HistoricalConfig{}

	runner := NewBacktestRunner(nil, nil, cfg)
	got := runner.historicalConfig()

	if got.CacheDir != "tmp-records/historical_cache" {
		t.Fatalf("expected cache dir fallback from network config, got %s", got.CacheDir)
	}
	if got.SpotTakerFeeRate != 0.0010 || got.FuturesTakerFeeRate != 0.0005 {
		t.Fatalf("unexpected fee fallbacks: spot=%.4f futures=%.4f", got.SpotTakerFeeRate, got.FuturesTakerFeeRate)
	}
	if got.MaxFundingCyclesHeld != 1 {
		t.Fatalf("expected max funding cycles fallback 1, got %d", got.MaxFundingCyclesHeld)
	}
	if got.ReferenceKlineLookback != 60 || got.EstimateKlineLookback != 180 {
		t.Fatalf("unexpected lookback fallbacks: ref=%d estimate=%d", got.ReferenceKlineLookback, got.EstimateKlineLookback)
	}
	if got.CrossStableQuoteMaxDeviationRate != 0.0010 {
		t.Fatalf("unexpected cross stable deviation fallback: %.4f", got.CrossStableQuoteMaxDeviationRate)
	}
}

func TestDefaultEngineConfigValidatePasses(t *testing.T) {
	cfg := DefaultEngineConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultEngineConfig().Validate() error = %v", err)
	}
}

func TestEngineConfigValidateRejectsUnsafeLimits(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxCycleOpenNotionalUSDT = cfg.StrategyConfig.PerSymbolQuoteUSDT - 1
	cfg.StrategyConfig.PreferredSpotQuoteAssets = ""
	cfg.StrategyConfig.LiveFundingAsset = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	message := err.Error()
	if !strings.Contains(message, "MaxCycleOpenNotionalUSDT") {
		t.Fatalf("expected notional validation message, got %s", message)
	}
	if !strings.Contains(message, "PreferredSpotQuoteAssets") {
		t.Fatalf("expected preferred spot quote validation message, got %s", message)
	}
	if !strings.Contains(message, "LiveFundingAsset") {
		t.Fatalf("expected live funding asset validation message, got %s", message)
	}
}

func TestEngineConfigConfigHashStableAndSensitiveToChange(t *testing.T) {
	cfg := DefaultEngineConfig()
	hashA := cfg.ConfigHash()
	hashB := cfg.ConfigHash()
	if hashA == "" || hashA != hashB {
		t.Fatalf("expected stable non-empty config hash, got %q and %q", hashA, hashB)
	}

	cfg.RiskConfig.MaxCycleLossUSDT++
	hashC := cfg.ConfigHash()
	if hashC == hashA {
		t.Fatalf("expected config hash to change after config mutation, got %q", hashC)
	}
}
