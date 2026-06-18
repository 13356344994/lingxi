package app

import (
	"lingxi/internal/core"
)

// RotationRunMode 从部署闸 + 控制面 + 策略模式合成统一轮动运行模式。
// strategy_live_enabled=false(rotation_live_enabled) ≈ OFF; canary_profit 真钱时映射 CANARY_LIVE。
func RotationRunMode(configLiveEnabled bool, rotationLiveEnabled bool, strategyMode core.StrategyMode) core.RunMode {
	if !configLiveEnabled || !rotationLiveEnabled {
		return core.RunModeOFF
	}
	return strategyMode.RunModeWhenLiveEnabled()
}

// PerpPerpRunMode 跨所 perp-perp 运行模式(配置闸 + 控制面 perp_perp_live_enabled + 策略模式)。
func PerpPerpRunMode(configLiveEnabled bool, perpPerpLiveEnabled bool, strategyMode core.StrategyMode) core.RunMode {
	if !configLiveEnabled || !perpPerpLiveEnabled {
		return core.RunModeOFF
	}
	return strategyMode.RunModeWhenLiveEnabled()
}

// LiveBoolFromRunMode 执行器 live 参数:仅 CANARY_LIVE / LIVE 为 true。
func LiveBoolFromRunMode(mode core.RunMode) bool {
	return mode.IsLiveMoney()
}
