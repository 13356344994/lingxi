package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultEngineConfigPath = "config.yaml"

func loadEngineConfig() (EngineConfig, error) {
	cfg := DefaultEngineConfig()
	configPath := strings.TrimSpace(os.Getenv("LINGXI_CONFIG"))
	if configPath == "" {
		configPath = defaultEngineConfigPath
	}
	if _, err := os.Stat(configPath); err == nil {
		values, err := readSimpleYAML(configPath)
		if err != nil {
			return EngineConfig{}, err
		}
		if err := applyEngineConfigOverrides(&cfg, values); err != nil {
			return EngineConfig{}, fmt.Errorf("加载 %s 失败: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return EngineConfig{}, fmt.Errorf("检查配置文件失败: %w", err)
	}
	if err := applyEngineConfigOverrides(&cfg, envConfigOverrides()); err != nil {
		return EngineConfig{}, fmt.Errorf("加载环境变量配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return EngineConfig{}, err
	}
	return cfg, nil
}

func envConfigOverrides() map[string]string {
	pairs := map[string]string{}
	for _, key := range []string{
		"network.http_timeout",
		"network.max_idle_conns",
		"network.max_idle_conns_per_host",
		"network.idle_conn_timeout",
		"network.tls_handshake_timeout",
		"network.response_header_timeout",
		"network.expect_continue_timeout",
		"network.dashboard_addr",
		"network.record_dir",
		"network.emergency_stop_file",
		"network.public_get_base_slot",
		"network.public_get_cooldown_403",
		"network.public_get_cooldown_418",
		"network.public_get_use_apikey",
		"network.public_get_apikey_whitelist",
		"strategy.per_symbol_quote_usdt",
		"strategy.max_targets",
		"strategy.min_estimated_funding_rate",
		"strategy.max_estimated_funding_rate",
		"strategy.max_scan_basis_rate",
		"strategy.fee_reserve_rate",
		"execution.execution_confirm_timeout",
		"execution.execution_recovery_timeout",
		"execution.exit_delay_after_funding",
		"execution.open_hedge_max_concurrency",
		"execution.use_protected_ioc",
		"execution.spot_limit_slippage_rate",
		"execution.futures_limit_slippage_rate",
		"execution.futures_price_protect",
		"execution.spot_stp_mode",
		"execution.futures_stp_mode",
		"risk.min_net_profit_rate",
		"risk.immediate_invalidate_rate",
		"risk.max_cycle_loss_usdt",
		"risk.max_cycle_open_notional_usdt",
		"risk.max_execution_incidents",
		"risk.max_order_uncertainty_per_cycle",
		"risk.protected_ioc_min_attempts",
		"risk.protected_ioc_max_unfilled_ratio",
		"risk.protected_ioc_max_cancel_rate",
		"risk.max_daily_loss_usdt",
		"risk.max_daily_order_count",
		"risk.max_daily_open_notional_usdt",
		"historical.default_lookback_days",
		"historical.initial_equity_usdt",
		"historical.per_leg_notional_usdt",
	} {
		envKey := "LINGXI_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, ".", "_"), "-", "_"))
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			pairs[key] = value
		}
	}
	return pairs
}

func applyEngineConfigOverrides(cfg *EngineConfig, values map[string]string) error {
	for rawKey, rawValue := range values {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		value := strings.TrimSpace(rawValue)
		if key == "" || value == "" {
			continue
		}
		switch key {
		case "network.http_timeout", "http_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.HTTPTimeout = parsed
		case "network.max_idle_conns", "max_idle_conns":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.MaxIdleConns = parsed
		case "network.max_idle_conns_per_host", "max_idle_conns_per_host":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.MaxIdleConnsPerHost = parsed
		case "network.idle_conn_timeout", "idle_conn_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.IdleConnTimeout = parsed
		case "network.tls_handshake_timeout", "tls_handshake_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.TLSHandshakeTimeout = parsed
		case "network.response_header_timeout", "response_header_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.ResponseHeaderTimeout = parsed
		case "network.expect_continue_timeout", "expect_continue_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.ExpectContinueTimeout = parsed
		case "network.dashboard_addr", "dashboard_addr":
			cfg.NetworkConfig.DashboardAddr = value
		case "network.record_dir", "record_dir":
			cfg.NetworkConfig.RecordDir = value
		case "network.emergency_stop_file", "emergency_stop_file":
			cfg.NetworkConfig.EmergencyStopFile = value
		case "network.public_get_base_slot", "public_get_base_slot":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.PublicGETBaseSlot = parsed
		case "network.public_get_cooldown_403", "public_get_cooldown_403":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.PublicGETCooldown403 = parsed
		case "network.public_get_cooldown_418", "public_get_cooldown_418":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.PublicGETCooldown418 = parsed
		case "network.public_get_use_apikey", "public_get_use_apikey":
			parsed, err := parseConfigBool(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.NetworkConfig.PublicGETUseAPIKey = parsed
		case "network.public_get_apikey_whitelist", "public_get_apikey_whitelist":
			cfg.NetworkConfig.PublicGETAPIKeyWhitelist = value
		case "strategy.per_symbol_quote_usdt", "per_symbol_quote_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.PerSymbolQuoteUSDT = parsed
		case "strategy.max_targets", "max_targets":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.MaxTargets = parsed
		case "strategy.min_estimated_funding_rate", "min_estimated_funding_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.MinEstimatedFundingRate = parsed
		case "strategy.max_estimated_funding_rate", "max_estimated_funding_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.MaxEstimatedFundingRate = parsed
		case "strategy.max_scan_basis_rate", "max_scan_basis_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.MaxScanBasisRate = parsed
		case "strategy.live_funding_asset", "live_funding_asset":
			cfg.StrategyConfig.LiveFundingAsset = value
		case "strategy.fee_reserve_rate", "fee_reserve_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.FeeReserveRate = parsed
		case "strategy.exit_spread_reserve_rate", "exit_spread_reserve_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.StrategyConfig.ExitSpreadReserveRate = parsed
		case "strategy.preferred_spot_quote_assets", "preferred_spot_quote_assets":
			cfg.StrategyConfig.PreferredSpotQuoteAssets = value
		case "execution.execution_confirm_timeout", "execution_confirm_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.ExecutionConfirmTimeout = parsed
		case "execution.execution_recovery_timeout", "execution_recovery_timeout":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.ExecutionRecoveryTimeout = parsed
		case "execution.exit_delay_after_funding", "exit_delay_after_funding":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.ExitDelayAfterFunding = parsed
		case "execution.exit_monitor_interval", "exit_monitor_interval":
			parsed, err := parseConfigDuration(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.ExitMonitorInterval = parsed
		case "execution.auto_balance_usdt", "auto_balance_usdt":
			parsed, err := parseConfigBool(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.AutoBalanceUSDT = parsed
		case "execution.open_hedge_max_concurrency", "open_hedge_max_concurrency":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.OpenHedgeMaxConcurrency = parsed
		case "execution.parallel_open_symbols", "parallel_open_symbols":
			cfg.ExecutionConfig.ParallelOpenSymbols = value
		case "execution.parallel_close_symbols", "parallel_close_symbols":
			cfg.ExecutionConfig.ParallelCloseSymbols = value
		case "execution.use_protected_ioc", "use_protected_ioc":
			parsed, err := parseConfigBool(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.UseProtectedIOC = parsed
		case "execution.spot_limit_slippage_rate", "spot_limit_slippage_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.SpotLimitSlippageRate = parsed
		case "execution.futures_limit_slippage_rate", "futures_limit_slippage_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.FuturesLimitSlippageRate = parsed
		case "execution.futures_price_protect", "futures_price_protect":
			parsed, err := parseConfigBool(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.ExecutionConfig.FuturesPriceProtect = parsed
		case "execution.spot_stp_mode", "spot_stp_mode":
			cfg.ExecutionConfig.SpotSTPMode = value
		case "execution.futures_stp_mode", "futures_stp_mode":
			cfg.ExecutionConfig.FuturesSTPMode = value
		case "risk.min_net_profit_rate", "min_net_profit_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MinNetProfitRate = parsed
		case "risk.immediate_invalidate_rate", "immediate_invalidate_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.ImmediateInvalidateRate = parsed
		case "risk.max_cycle_loss_usdt", "max_cycle_loss_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxCycleLossUSDT = parsed
		case "risk.max_cycle_open_notional_usdt", "max_cycle_open_notional_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxCycleOpenNotionalUSDT = parsed
		case "risk.max_execution_incidents", "max_execution_incidents":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxExecutionIncidents = parsed
		case "risk.max_order_uncertainty_per_cycle", "max_order_uncertainty_per_cycle":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxOrderUncertaintyPerCycle = parsed
		case "risk.protected_ioc_min_attempts", "protected_ioc_min_attempts":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.ProtectedIOCMinAttempts = parsed
		case "risk.protected_ioc_max_unfilled_ratio", "protected_ioc_max_unfilled_ratio":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio = parsed
		case "risk.protected_ioc_max_cancel_rate", "protected_ioc_max_cancel_rate":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.ProtectedIOCMaxCancelRate = parsed
		case "risk.max_daily_loss_usdt", "max_daily_loss_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxDailyLossUSDT = parsed
		case "risk.max_daily_order_count", "max_daily_order_count":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxDailyOrderCount = parsed
		case "risk.max_daily_open_notional_usdt", "max_daily_open_notional_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.RiskConfig.MaxDailyOpenNotionalUSDT = parsed
		case "historical.default_lookback_days", "default_lookback_days":
			parsed, err := parseConfigInt(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.HistoricalConfig.DefaultLookbackDays = parsed
		case "historical.initial_equity_usdt", "initial_equity_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.HistoricalConfig.InitialEquityUSDT = parsed
		case "historical.per_leg_notional_usdt", "per_leg_notional_usdt":
			parsed, err := parseConfigFloat(value)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			cfg.HistoricalConfig.PerLegNotionalUSDT = parsed
		default:
			continue
		}
	}
	return nil
}

func parseConfigDuration(raw string) (time.Duration, error) {
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("非法 duration 配置: %s", raw)
	}
	return value, nil
}

func parseConfigFloat(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("非法 float 配置: %s", raw)
	}
	return value, nil
}

func parseConfigInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("非法 int 配置: %s", raw)
	}
	return value, nil
}

func parseConfigBool(raw string) (bool, error) {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("非法 bool 配置: %s", raw)
	}
	return value, nil
}
