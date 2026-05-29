package main

import (
	"testing"
	"time"
)

func TestApplyEngineConfigOverrides(t *testing.T) {
	cfg := DefaultEngineConfig()
	err := applyEngineConfigOverrides(&cfg, map[string]string{
		"network.dashboard_addr":              "127.0.0.1:9090",
		"execution.use_protected_ioc":         "false",
		"execution.spot_limit_slippage_rate":  "0.0025",
		"execution.execution_confirm_timeout": "2500ms",
		"risk.max_daily_loss_usdt":            "55.5",
	})
	if err != nil {
		t.Fatalf("applyEngineConfigOverrides() error = %v", err)
	}
	if cfg.NetworkConfig.DashboardAddr != "127.0.0.1:9090" {
		t.Fatalf("unexpected dashboard addr: %s", cfg.NetworkConfig.DashboardAddr)
	}
	if cfg.ExecutionConfig.UseProtectedIOC {
		t.Fatal("expected protected IOC to be disabled by override")
	}
	if cfg.ExecutionConfig.SpotLimitSlippageRate != 0.0025 {
		t.Fatalf("unexpected spot slippage rate: %v", cfg.ExecutionConfig.SpotLimitSlippageRate)
	}
	if cfg.ExecutionConfig.ExecutionConfirmTimeout != 2500*time.Millisecond {
		t.Fatalf("unexpected confirm timeout: %s", cfg.ExecutionConfig.ExecutionConfirmTimeout)
	}
	if cfg.RiskConfig.MaxDailyLossUSDT != 55.5 {
		t.Fatalf("unexpected daily loss limit: %v", cfg.RiskConfig.MaxDailyLossUSDT)
	}
}
