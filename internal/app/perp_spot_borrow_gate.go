package app

import (
	"strings"
	"time"
)

// perpSpotAssetBorrowable 判定 execution venue 上 base 是否可借(仅 consult 历史 confirmed-available 集,fail-closed)。
// borrowDisabled 非 nil 时,listed venue 视为永久不可借(配置驱动,如 gate 单币种保证金无借币)。
func perpSpotAssetBorrowable(venue, baseAsset string, known *BorrowKnownAvailableRegistry, borrowDisabled map[string]struct{}, now time.Time) bool {
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "" {
		return false
	}
	if _, disabled := borrowDisabled[venue]; disabled {
		return false
	}
	if known == nil {
		return false
	}
	return known.IsBorrowable(venue, baseAsset, now)
}

func perpSpotBorrowCostUSDT(
	baseAsset string,
	notional, holdHours float64,
	borrowDailyRateByAsset map[string]float64,
	defaultDailyRateWhenUnknown float64,
) float64 {
	if notional <= 0 || holdHours <= 0 {
		return 0
	}
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	dailyRate, ok := borrowDailyRateByAsset[baseAsset]
	if !ok {
		if defaultDailyRateWhenUnknown > 0 {
			dailyRate = defaultDailyRateWhenUnknown
		} else {
			return 0
		}
	}
	if dailyRate <= 0 {
		return 0
	}
	holdDays := holdHours / 24.0
	return notional * dailyRate * holdDays
}
