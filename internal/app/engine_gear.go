package app

import (
	"strings"
)

// EngineGear 描述系统当前的运行档位。
// 个人小资金长期运行，需要动态降级，而不是只有“跑”或“停”。
type EngineGear string

const (
	// GearObserve 只观察，不交易。用于系统预热、趋势变坏或完全熔断后的人工排查。
	GearObserve EngineGear = "observe"
	// GearConservative 极保守实盘。并发极低，开仓阈值极高，恢复立即升级。
	GearConservative EngineGear = "conservative"
	// GearNormal 标准实盘。正常配置运行。
	GearNormal EngineGear = "normal"
	// GearDefensive 防御模式。检测到环境变差时自动降级，减少仓位，降低频率。
	GearDefensive EngineGear = "defensive"
	// GearManualHold 人工接管。拒绝一切自动化操作，等待指令。
	GearManualHold EngineGear = "manual_hold"
	// GearHardStop 完全停机。系统不应再尝试任何循环。
	GearHardStop EngineGear = "hard_stop"
)

// NormalizeEngineGear 规范化档位枚举
func NormalizeEngineGear(gear string) EngineGear {
	switch strings.ToLower(strings.TrimSpace(gear)) {
	case "observe":
		return GearObserve
	case "conservative":
		return GearConservative
	case "normal":
		return GearNormal
	case "defensive":
		return GearDefensive
	case "manual_hold":
		return GearManualHold
	case "hard_stop":
		return GearHardStop
	default:
		return GearObserve // 默认退回观察模式，最安全
	}
}

// GearAllowsNewOrders 判断当前档位是否允许开启新仓位
func GearAllowsNewOrders(gear EngineGear) bool {
	switch gear {
	case GearNormal, GearConservative, GearDefensive:
		return true
	default:
		return false
	}
}

// GearAllowsRecovery 判断当前档位是否允许自动执行恢复动作
func GearAllowsRecovery(gear EngineGear) bool {
	switch gear {
	case GearNormal, GearConservative, GearDefensive:
		return true
	default:
		// Observe, ManualHold, HardStop 均不允许系统自动发单恢复
		return false
	}
}
