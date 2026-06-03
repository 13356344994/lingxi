package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// 本文件从 runtime_controller.go 拆出：仅承载「页面在线风控参数」的读取、保守校验
// （只允许收紧、不允许放松/在线改不可变项）、与时长解析/格式化（同包搬移，行为不变），
// 让 runtime_controller.go 收敛到「控制器编排」职责并满足单文件 ≤ 800 行门禁。

func (c *RuntimeController) CurrentRiskConfig() RuntimeRiskConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return runtimeRiskConfigFromEngineConfig(c.cfg)
}

func (c *RuntimeController) UpdateRiskConfig(req RuntimeRiskConfig) (RuntimeRiskConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	window, err := parseRuntimeWindowDuration(req.RecentTradeVolumeWindow, c.cfg.StrategyConfig.RecentTradeVolumeWindow)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if req.PerSymbolQuoteUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("单标的名义资金必须大于 0")
	}
	if req.MaxCycleOpenNotionalUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("单周期总敞口上限必须大于 0")
	}
	if req.MaxRecentVolumeShare <= 0 || req.MaxRecentVolumeShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("最近成交额占比必须在 (0, 1] 之间")
	}
	if req.MaxOpenInterestShare <= 0 || req.MaxOpenInterestShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("未平仓名义占比必须在 (0, 1] 之间")
	}
	if req.MaxVisibleDepthShare <= 0 || req.MaxVisibleDepthShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("可见深度占比必须在 (0, 1] 之间")
	}
	if req.MinNetProfitRate <= 0 || req.MinNetProfitRate > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("最低净收益率必须在 (0, 1] 之间")
	}
	if req.ImmediateInvalidateRate <= 0 || req.ImmediateInvalidateRate > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("立即作废阈值必须在 (0, 1] 之间")
	}
	if req.ImmediateInvalidateRate >= req.MinNetProfitRate {
		return RuntimeRiskConfig{}, fmt.Errorf("立即作废阈值必须小于最低净收益率")
	}
	if req.MonitorMaxConsecutiveBreach <= 0 || req.MonitorMaxConsecutiveBreach > 20 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续观察 breach 必须在 1 到 20 之间")
	}
	if req.MaxCycleLossUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大单周期亏损必须大于 0")
	}
	if req.MaxExecutionIncidents <= 0 || req.MaxExecutionIncidents > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大执行事故数必须在 1 到 100 之间")
	}
	if req.MaxOrderUncertaintyPerCycle <= 0 || req.MaxOrderUncertaintyPerCycle > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大不确定成交数必须在 1 到 100 之间")
	}
	if req.MaxConsecutivePlatformRejects <= 0 || req.MaxConsecutivePlatformRejects > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续平台拒绝数必须在 1 到 100 之间")
	}
	if req.MaxConsecutiveRequestErrors <= 0 || req.MaxConsecutiveRequestErrors > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续异常请求数必须在 1 到 100 之间")
	}
	if req.ProtectedIOCMinAttempts <= 0 || req.ProtectedIOCMinAttempts > 1000 {
		return RuntimeRiskConfig{}, fmt.Errorf("IOC 风控最小观察次数必须在 1 到 1000 之间")
	}
	if req.ProtectedIOCMaxUnfilledRatio <= 0 || req.ProtectedIOCMaxUnfilledRatio > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("IOC 最大未成交比例必须在 (0, 1] 之间")
	}
	if req.ProtectedIOCMaxCancelRate <= 0 || req.ProtectedIOCMaxCancelRate > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("IOC 最大撤单比例必须在 (0, 1] 之间")
	}
	exitDelayAfterFunding, err := parseRuntimeFlexibleDuration(req.ExitDelayAfterFunding, c.cfg.ExecutionConfig.ExitDelayAfterFunding, "结算后最晚退出时限", 0, 30*time.Second)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	exitMonitorInterval, err := parseRuntimeFlexibleDuration(req.ExitMonitorInterval, c.cfg.ExecutionConfig.ExitMonitorInterval, "退出监控轮询间隔", 50*time.Millisecond, 5*time.Second)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	confirmTimeout, err := parseRuntimeFlexibleDuration(req.ExecutionConfirmTimeout, c.cfg.ExecutionConfig.ExecutionConfirmTimeout, "开平仓确认超时", 100*time.Millisecond, 30*time.Second)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	symbolCooldown, err := parseRuntimeFlexibleDuration(req.SymbolAbnormalCooldown, c.cfg.RiskConfig.SymbolAbnormalCooldown, "symbol 异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	minorCooldown, err := parseRuntimeFlexibleDuration(req.PlatformMinorCooldown, c.cfg.RiskConfig.PlatformMinorCooldown, "轻微异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if req.MaxRecoveryActionsPerCycle <= 0 || req.MaxRecoveryActionsPerCycle > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大回补次数必须在 1 到 100 之间")
	}
	if req.MaxDailyLossUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日亏损必须大于 0")
	}
	if req.MaxDailyOrderCount <= 0 || req.MaxDailyOrderCount > 10000 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日下单次数必须在 1 到 10000 之间")
	}
	if req.MaxDailyOpenNotionalUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日开仓名义必须大于 0")
	}
	cooldown, err := parseRuntimeCooldownDuration(req.PlatformAbnormalCooldown, c.cfg.RiskConfig.PlatformAbnormalCooldown)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	criticalCooldown, err := parseRuntimeFlexibleDuration(req.PlatformCriticalCooldown, c.cfg.RiskConfig.PlatformCriticalCooldown, "严重异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if minorCooldown > cooldown {
		return RuntimeRiskConfig{}, fmt.Errorf("轻微异常冷却时间不能大于平台异常冷却时间")
	}
	if criticalCooldown < cooldown {
		return RuntimeRiskConfig{}, fmt.Errorf("严重异常冷却时间不能小于平台异常冷却时间")
	}
	if err := validateConservativeRuntimeRiskUpdate(c.cfg, req, window, exitDelayAfterFunding, exitMonitorInterval, confirmTimeout, symbolCooldown, minorCooldown, cooldown, criticalCooldown); err != nil {
		return RuntimeRiskConfig{}, err
	}

	updated := c.cfg
	updated.StrategyConfig.PerSymbolQuoteUSDT = req.PerSymbolQuoteUSDT
	updated.RiskConfig.MaxCycleOpenNotionalUSDT = req.MaxCycleOpenNotionalUSDT
	updated.StrategyConfig.RecentTradeVolumeWindow = window
	updated.StrategyConfig.MaxRecentVolumeShare = req.MaxRecentVolumeShare
	updated.StrategyConfig.MaxOpenInterestShare = req.MaxOpenInterestShare
	updated.StrategyConfig.MaxVisibleDepthShare = req.MaxVisibleDepthShare
	updated.RiskConfig.MinNetProfitRate = req.MinNetProfitRate
	updated.RiskConfig.ImmediateInvalidateRate = req.ImmediateInvalidateRate
	updated.RiskConfig.MonitorMaxConsecutiveBreach = req.MonitorMaxConsecutiveBreach
	updated.RiskConfig.MaxCycleLossUSDT = req.MaxCycleLossUSDT
	updated.RiskConfig.MaxExecutionIncidents = req.MaxExecutionIncidents
	updated.RiskConfig.MaxOrderUncertaintyPerCycle = req.MaxOrderUncertaintyPerCycle
	updated.RiskConfig.MaxConsecutivePlatformRejects = req.MaxConsecutivePlatformRejects
	updated.RiskConfig.MaxConsecutiveRequestErrors = req.MaxConsecutiveRequestErrors
	updated.RiskConfig.ProtectedIOCMinAttempts = req.ProtectedIOCMinAttempts
	updated.RiskConfig.ProtectedIOCMaxUnfilledRatio = req.ProtectedIOCMaxUnfilledRatio
	updated.RiskConfig.ProtectedIOCMaxCancelRate = req.ProtectedIOCMaxCancelRate
	updated.ExecutionConfig.ExitDelayAfterFunding = exitDelayAfterFunding
	updated.ExecutionConfig.ExitMonitorInterval = exitMonitorInterval
	updated.ExecutionConfig.ExecutionConfirmTimeout = confirmTimeout
	updated.RiskConfig.SymbolAbnormalCooldown = symbolCooldown
	updated.RiskConfig.PlatformMinorCooldown = minorCooldown
	updated.RiskConfig.PlatformAbnormalCooldown = cooldown
	updated.RiskConfig.PlatformCriticalCooldown = criticalCooldown
	updated.RiskConfig.MaxRecoveryActionsPerCycle = req.MaxRecoveryActionsPerCycle
	updated.RiskConfig.MaxDailyLossUSDT = req.MaxDailyLossUSDT
	updated.RiskConfig.MaxDailyOrderCount = req.MaxDailyOrderCount
	updated.RiskConfig.MaxDailyOpenNotionalUSDT = req.MaxDailyOpenNotionalUSDT
	if err := updated.Validate(); err != nil {
		return RuntimeRiskConfig{}, err
	}
	c.cfg = updated

	config := runtimeRiskConfigFromEngineConfig(updated)
	c.status.LastMessage = "风控参数已收紧，将在下一次启动任务时生效"
	return config, nil
}

func validateConservativeRuntimeRiskUpdate(current EngineConfig, req RuntimeRiskConfig, recentWindow time.Duration, exitDelayAfterFunding time.Duration, exitMonitorInterval time.Duration, confirmTimeout time.Duration, symbolCooldown time.Duration, minorCooldown time.Duration, abnormalCooldown time.Duration, criticalCooldown time.Duration) error {
	immutableViolations := make([]string, 0, 12)
	tighteningViolations := make([]string, 0, 12)

	requireEqualFloat := func(label string, currentValue float64, proposedValue float64) {
		if !runtimeFloatEqual(currentValue, proposedValue) {
			immutableViolations = append(immutableViolations, label)
		}
	}
	requireEqualInt := func(label string, currentValue int, proposedValue int) {
		if currentValue != proposedValue {
			immutableViolations = append(immutableViolations, label)
		}
	}
	requireEqualDuration := func(label string, currentValue time.Duration, proposedValue time.Duration) {
		if currentValue != proposedValue {
			immutableViolations = append(immutableViolations, label)
		}
	}
	requireMax := func(label string, currentValue float64, proposedValue float64) {
		if proposedValue-currentValue > 1e-12 {
			tighteningViolations = append(tighteningViolations, label)
		}
	}
	requireMaxInt := func(label string, currentValue int, proposedValue int) {
		if proposedValue > currentValue {
			tighteningViolations = append(tighteningViolations, label)
		}
	}
	requireMinDuration := func(label string, currentValue time.Duration, proposedValue time.Duration) {
		if proposedValue < currentValue {
			tighteningViolations = append(tighteningViolations, label)
		}
	}

	requireMax("单标的名义资金", current.StrategyConfig.PerSymbolQuoteUSDT, req.PerSymbolQuoteUSDT)
	requireMax("单周期总敞口上限", current.RiskConfig.MaxCycleOpenNotionalUSDT, req.MaxCycleOpenNotionalUSDT)
	requireMax("最大单周期亏损", current.RiskConfig.MaxCycleLossUSDT, req.MaxCycleLossUSDT)
	requireMinDuration("symbol 异常冷却时间", current.RiskConfig.SymbolAbnormalCooldown, symbolCooldown)
	requireMinDuration("轻微异常冷却时间", current.RiskConfig.PlatformMinorCooldown, minorCooldown)
	requireMinDuration("平台异常冷却时间", current.RiskConfig.PlatformAbnormalCooldown, abnormalCooldown)
	requireMinDuration("严重异常冷却时间", current.RiskConfig.PlatformCriticalCooldown, criticalCooldown)
	requireMax("最大日亏损", current.RiskConfig.MaxDailyLossUSDT, req.MaxDailyLossUSDT)
	requireMaxInt("最大日下单次数", current.RiskConfig.MaxDailyOrderCount, req.MaxDailyOrderCount)
	requireMax("最大日开仓名义", current.RiskConfig.MaxDailyOpenNotionalUSDT, req.MaxDailyOpenNotionalUSDT)

	requireEqualDuration("最近成交额窗口", current.StrategyConfig.RecentTradeVolumeWindow, recentWindow)
	requireEqualFloat("最近成交额占比上限", current.StrategyConfig.MaxRecentVolumeShare, req.MaxRecentVolumeShare)
	requireEqualFloat("未平仓名义占比上限", current.StrategyConfig.MaxOpenInterestShare, req.MaxOpenInterestShare)
	requireEqualFloat("可见深度占比上限", current.StrategyConfig.MaxVisibleDepthShare, req.MaxVisibleDepthShare)
	requireEqualFloat("最低净收益率", current.RiskConfig.MinNetProfitRate, req.MinNetProfitRate)
	requireEqualFloat("立即作废阈值", current.RiskConfig.ImmediateInvalidateRate, req.ImmediateInvalidateRate)
	requireEqualInt("最大连续观察 breach", current.RiskConfig.MonitorMaxConsecutiveBreach, req.MonitorMaxConsecutiveBreach)
	requireEqualInt("最大执行事故数", current.RiskConfig.MaxExecutionIncidents, req.MaxExecutionIncidents)
	requireEqualInt("最大不确定成交数", current.RiskConfig.MaxOrderUncertaintyPerCycle, req.MaxOrderUncertaintyPerCycle)
	requireEqualInt("最大连续平台拒绝", current.RiskConfig.MaxConsecutivePlatformRejects, req.MaxConsecutivePlatformRejects)
	requireEqualInt("最大连续异常请求", current.RiskConfig.MaxConsecutiveRequestErrors, req.MaxConsecutiveRequestErrors)
	requireEqualDuration("结算后最晚退出时限", current.ExecutionConfig.ExitDelayAfterFunding, exitDelayAfterFunding)
	requireEqualDuration("退出监控轮询间隔", current.ExecutionConfig.ExitMonitorInterval, exitMonitorInterval)
	requireEqualDuration("开平仓确认超时", current.ExecutionConfig.ExecutionConfirmTimeout, confirmTimeout)
	requireEqualInt("最大回补次数", current.RiskConfig.MaxRecoveryActionsPerCycle, req.MaxRecoveryActionsPerCycle)

	if len(immutableViolations) > 0 {
		sort.Strings(immutableViolations)
		return fmt.Errorf("以下参数不允许通过页面在线修改，请改配置文件并重启: %s", strings.Join(immutableViolations, "、"))
	}
	if len(tighteningViolations) > 0 {
		sort.Strings(tighteningViolations)
		return fmt.Errorf("页面在线风控只允许收紧，不允许放松: %s", strings.Join(tighteningViolations, "、"))
	}
	return nil
}

func runtimeFloatEqual(left float64, right float64) bool {
	return math.Abs(left-right) <= 1e-12
}

func runtimeRiskConfigFromEngineConfig(cfg EngineConfig) RuntimeRiskConfig {
	return RuntimeRiskConfig{
		PerSymbolQuoteUSDT:            cfg.StrategyConfig.PerSymbolQuoteUSDT,
		MaxCycleOpenNotionalUSDT:      cfg.RiskConfig.MaxCycleOpenNotionalUSDT,
		RecentTradeVolumeWindow:       formatRuntimeRiskDuration(cfg.StrategyConfig.RecentTradeVolumeWindow),
		MaxRecentVolumeShare:          cfg.StrategyConfig.MaxRecentVolumeShare,
		MaxOpenInterestShare:          cfg.StrategyConfig.MaxOpenInterestShare,
		MaxVisibleDepthShare:          cfg.StrategyConfig.MaxVisibleDepthShare,
		MinNetProfitRate:              cfg.RiskConfig.MinNetProfitRate,
		ImmediateInvalidateRate:       cfg.RiskConfig.ImmediateInvalidateRate,
		MonitorMaxConsecutiveBreach:   cfg.RiskConfig.MonitorMaxConsecutiveBreach,
		MaxCycleLossUSDT:              cfg.RiskConfig.MaxCycleLossUSDT,
		MaxExecutionIncidents:         cfg.RiskConfig.MaxExecutionIncidents,
		MaxOrderUncertaintyPerCycle:   cfg.RiskConfig.MaxOrderUncertaintyPerCycle,
		MaxConsecutivePlatformRejects: cfg.RiskConfig.MaxConsecutivePlatformRejects,
		MaxConsecutiveRequestErrors:   cfg.RiskConfig.MaxConsecutiveRequestErrors,
		ProtectedIOCMinAttempts:       cfg.RiskConfig.ProtectedIOCMinAttempts,
		ProtectedIOCMaxUnfilledRatio:  cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio,
		ProtectedIOCMaxCancelRate:     cfg.RiskConfig.ProtectedIOCMaxCancelRate,
		ExitDelayAfterFunding:         formatRuntimeRiskDuration(cfg.ExecutionConfig.ExitDelayAfterFunding),
		ExitMonitorInterval:           formatRuntimeRiskDuration(cfg.ExecutionConfig.ExitMonitorInterval),
		ExecutionConfirmTimeout:       formatRuntimeRiskDuration(cfg.ExecutionConfig.ExecutionConfirmTimeout),
		SymbolAbnormalCooldown:        formatRuntimeRiskDuration(cfg.RiskConfig.SymbolAbnormalCooldown),
		PlatformMinorCooldown:         formatRuntimeRiskDuration(cfg.RiskConfig.PlatformMinorCooldown),
		PlatformAbnormalCooldown:      formatRuntimeRiskDuration(cfg.RiskConfig.PlatformAbnormalCooldown),
		PlatformCriticalCooldown:      formatRuntimeRiskDuration(cfg.RiskConfig.PlatformCriticalCooldown),
		MaxRecoveryActionsPerCycle:    cfg.RiskConfig.MaxRecoveryActionsPerCycle,
		MaxDailyLossUSDT:              cfg.RiskConfig.MaxDailyLossUSDT,
		MaxDailyOrderCount:            cfg.RiskConfig.MaxDailyOrderCount,
		MaxDailyOpenNotionalUSDT:      cfg.RiskConfig.MaxDailyOpenNotionalUSDT,
	}
}

func parseRuntimeWindowDuration(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if fallback > 0 {
			return fallback, nil
		}
		return 0, fmt.Errorf("最近成交额窗口不能为空")
	}
	window, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("最近成交额窗口格式错误: %w", err)
	}
	if window < time.Minute || window > time.Hour {
		return 0, fmt.Errorf("最近成交额窗口必须在 1m 到 1h 之间")
	}
	return window, nil
}

func parseRuntimeCooldownDuration(raw string, fallback time.Duration) (time.Duration, error) {
	return parseRuntimeFlexibleDuration(raw, fallback, "平台异常冷却时间", time.Second, 24*time.Hour)
}

func parseRuntimeFlexibleDuration(raw string, fallback time.Duration, fieldName string, min time.Duration, max time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if fallback > 0 {
			return fallback, nil
		}
		return 0, fmt.Errorf("%s不能为空", fieldName)
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s格式错误: %w", fieldName, err)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s必须在 %s 到 %s 之间", fieldName, min, max)
	}
	return value, nil
}

func formatRuntimeRiskDuration(window time.Duration) string {
	if window <= 0 {
		return ""
	}
	return window.String()
}
