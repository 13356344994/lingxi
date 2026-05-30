package main

import (
	"testing"
	"time"
)

func TestApplyEngineConfigOverrides(t *testing.T) {
	cfg := DefaultEngineConfig()
	err := applyEngineConfigOverrides(&cfg, map[string]string{
		"network.http_timeout":                  "12s",
		"network.max_idle_conns":                "320",
		"network.max_idle_conns_per_host":       "96",
		"network.idle_conn_timeout":             "75s",
		"network.tls_handshake_timeout":         "4s",
		"network.response_header_timeout":       "6s",
		"network.expect_continue_timeout":       "1500ms",
		"network.dashboard_addr":                "127.0.0.1:9090",
		"network.public_get_base_slot":          "250ms",
		"network.public_get_cooldown_403":       "7s",
		"network.public_get_use_apikey":         "true",
		"network.public_get_apikey_whitelist":   "premiumIndex,openInterest",
		"strategy.live_funding_asset":           "USDT",
		"strategy.exit_spread_reserve_rate":     "0.0008",
		"strategy.preferred_spot_quote_assets":  "USDC,FDUSD,USDT",
		"execution.exit_monitor_interval":       "350ms",
		"execution.auto_balance_usdt":           "false",
		"execution.open_hedge_max_concurrency":  "3",
		"execution.parallel_open_symbols":       "BTCUSDT,ETH",
		"execution.parallel_close_symbols":      "BTCUSDT",
		"execution.use_protected_ioc":           "false",
		"execution.spot_limit_slippage_rate":    "0.0025",
		"execution.spot_stp_mode":               "EXPIRE_BOTH",
		"execution.futures_stp_mode":            "EXPIRE_MAKER",
		"execution.execution_confirm_timeout":   "2500ms",
		"risk.protected_ioc_min_attempts":       "9",
		"risk.protected_ioc_max_unfilled_ratio": "0.55",
		"risk.protected_ioc_max_cancel_rate":    "0.45",
		"risk.max_daily_loss_usdt":              "55.5",
	})
	if err != nil {
		t.Fatalf("applyEngineConfigOverrides() error = %v", err)
	}
	if cfg.NetworkConfig.DashboardAddr != "127.0.0.1:9090" {
		t.Fatalf("unexpected dashboard addr: %s", cfg.NetworkConfig.DashboardAddr)
	}
	if cfg.NetworkConfig.HTTPTimeout != 12*time.Second {
		t.Fatalf("unexpected http timeout: %s", cfg.NetworkConfig.HTTPTimeout)
	}
	if cfg.NetworkConfig.MaxIdleConns != 320 {
		t.Fatalf("unexpected max idle conns: %d", cfg.NetworkConfig.MaxIdleConns)
	}
	if cfg.NetworkConfig.MaxIdleConnsPerHost != 96 {
		t.Fatalf("unexpected max idle conns per host: %d", cfg.NetworkConfig.MaxIdleConnsPerHost)
	}
	if cfg.NetworkConfig.IdleConnTimeout != 75*time.Second {
		t.Fatalf("unexpected idle conn timeout: %s", cfg.NetworkConfig.IdleConnTimeout)
	}
	if cfg.NetworkConfig.TLSHandshakeTimeout != 4*time.Second {
		t.Fatalf("unexpected tls handshake timeout: %s", cfg.NetworkConfig.TLSHandshakeTimeout)
	}
	if cfg.NetworkConfig.ResponseHeaderTimeout != 6*time.Second {
		t.Fatalf("unexpected response header timeout: %s", cfg.NetworkConfig.ResponseHeaderTimeout)
	}
	if cfg.NetworkConfig.ExpectContinueTimeout != 1500*time.Millisecond {
		t.Fatalf("unexpected expect continue timeout: %s", cfg.NetworkConfig.ExpectContinueTimeout)
	}
	if cfg.NetworkConfig.PublicGETBaseSlot != 250*time.Millisecond {
		t.Fatalf("unexpected public get base slot: %s", cfg.NetworkConfig.PublicGETBaseSlot)
	}
	if cfg.NetworkConfig.PublicGETCooldown403 != 7*time.Second {
		t.Fatalf("unexpected public get 403 cooldown: %s", cfg.NetworkConfig.PublicGETCooldown403)
	}
	if !cfg.NetworkConfig.PublicGETUseAPIKey {
		t.Fatal("expected public get apikey switch enabled by override")
	}
	if cfg.NetworkConfig.PublicGETAPIKeyWhitelist != "premiumIndex,openInterest" {
		t.Fatalf("unexpected public get whitelist: %s", cfg.NetworkConfig.PublicGETAPIKeyWhitelist)
	}
	if cfg.StrategyConfig.ExitSpreadReserveRate != 0.0008 {
		t.Fatalf("unexpected exit spread reserve rate: %v", cfg.StrategyConfig.ExitSpreadReserveRate)
	}
	if cfg.StrategyConfig.LiveFundingAsset != "USDT" {
		t.Fatalf("unexpected live funding asset: %s", cfg.StrategyConfig.LiveFundingAsset)
	}
	if cfg.StrategyConfig.PreferredSpotQuoteAssets != "USDC,FDUSD,USDT" {
		t.Fatalf("unexpected preferred spot quote assets: %s", cfg.StrategyConfig.PreferredSpotQuoteAssets)
	}
	if cfg.ExecutionConfig.UseProtectedIOC {
		t.Fatal("expected protected IOC to be disabled by override")
	}
	if cfg.ExecutionConfig.SpotLimitSlippageRate != 0.0025 {
		t.Fatalf("unexpected spot slippage rate: %v", cfg.ExecutionConfig.SpotLimitSlippageRate)
	}
	if cfg.ExecutionConfig.SpotSTPMode != "EXPIRE_BOTH" {
		t.Fatalf("unexpected spot stp mode: %s", cfg.ExecutionConfig.SpotSTPMode)
	}
	if cfg.ExecutionConfig.FuturesSTPMode != "EXPIRE_MAKER" {
		t.Fatalf("unexpected futures stp mode: %s", cfg.ExecutionConfig.FuturesSTPMode)
	}
	if cfg.ExecutionConfig.ExecutionConfirmTimeout != 2500*time.Millisecond {
		t.Fatalf("unexpected confirm timeout: %s", cfg.ExecutionConfig.ExecutionConfirmTimeout)
	}
	if cfg.ExecutionConfig.ExitMonitorInterval != 350*time.Millisecond {
		t.Fatalf("unexpected exit monitor interval: %s", cfg.ExecutionConfig.ExitMonitorInterval)
	}
	if cfg.ExecutionConfig.AutoBalanceUSDT {
		t.Fatal("expected auto balance usdt to be disabled by override")
	}
	if cfg.ExecutionConfig.OpenHedgeMaxConcurrency != 3 {
		t.Fatalf("unexpected open hedge max concurrency: %d", cfg.ExecutionConfig.OpenHedgeMaxConcurrency)
	}
	if cfg.ExecutionConfig.ParallelOpenSymbols != "BTCUSDT,ETH" {
		t.Fatalf("unexpected parallel open symbols: %s", cfg.ExecutionConfig.ParallelOpenSymbols)
	}
	if cfg.ExecutionConfig.ParallelCloseSymbols != "BTCUSDT" {
		t.Fatalf("unexpected parallel close symbols: %s", cfg.ExecutionConfig.ParallelCloseSymbols)
	}
	if cfg.RiskConfig.MaxDailyLossUSDT != 55.5 {
		t.Fatalf("unexpected daily loss limit: %v", cfg.RiskConfig.MaxDailyLossUSDT)
	}
	if cfg.RiskConfig.ProtectedIOCMinAttempts != 9 {
		t.Fatalf("unexpected protected ioc min attempts: %d", cfg.RiskConfig.ProtectedIOCMinAttempts)
	}
	if cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio != 0.55 {
		t.Fatalf("unexpected protected ioc max unfilled ratio: %v", cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio)
	}
	if cfg.RiskConfig.ProtectedIOCMaxCancelRate != 0.45 {
		t.Fatalf("unexpected protected ioc max cancel rate: %v", cfg.RiskConfig.ProtectedIOCMaxCancelRate)
	}
}
