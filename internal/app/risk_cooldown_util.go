package app

import (
	"strings"
	"time"

	"lingxi/internal/core"
)

// SymbolCooldownForSeverity 按严重度返回 symbol 级冷却时长,语义对齐 CooldownForSeverity(平台级)。
func SymbolCooldownForSeverity(risk core.RiskConfig, severity string) time.Duration {
	switch strings.TrimSpace(severity) {
	case "critical":
		if risk.SymbolCriticalCooldown > 0 {
			return risk.SymbolCriticalCooldown
		}
		return risk.SymbolAbnormalCooldown
	case "minor":
		if risk.SymbolMinorCooldown > 0 {
			return risk.SymbolMinorCooldown
		}
		return risk.SymbolAbnormalCooldown
	default:
		return risk.SymbolAbnormalCooldown
	}
}

// CrossVenueSymbolCooldownForCategory 跨所执行器 symbol 冷却:按 category/severity 分级;
// bybit_agreement 保留独立长冷却(协议类,非一般异常)。
func CrossVenueSymbolCooldownForCategory(risk core.RiskConfig, crossVenue core.CrossVenueExecutorConfig, category, severity string) time.Duration {
	if strings.TrimSpace(category) == "bybit_agreement" {
		hours := crossVenue.BybitAgreementCooldownHours
		if hours <= 0 {
			hours = 6
		}
		return time.Duration(hours) * time.Hour
	}
	if d := SymbolCooldownForSeverity(risk, severity); d > 0 {
		return d
	}
	// legacy 回退:SymbolCooldownHours(默认 1h),仅当 RiskConfig symbol 冷却均未配置时生效。
	hours := crossVenue.SymbolCooldownHours
	if hours <= 0 {
		hours = 1
	}
	return time.Duration(hours) * time.Hour
}
