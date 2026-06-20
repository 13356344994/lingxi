package app

import (
	"strings"
	"time"
)

const (
	// perpSpotBorrowUnknownFailOpen 为借币状态 unknown 时扫描层 fail-open 的软诊断标记(非硬拒绝)。
	perpSpotBorrowUnknownFailOpen = "borrow_unknown_failopen"
)

// perpSpotBorrowState 判定 execution venue 上 base 的借币三态。
// borrowDisabled venue(如 gate)→Unavailable;registry fresh available→Available;fresh unavailable→Unavailable;其余→Unknown。
func perpSpotBorrowState(venue, baseAsset string, known *BorrowKnownAvailableRegistry, borrowDisabled map[string]struct{}, now time.Time) BorrowState {
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "" {
		return BorrowStateUnknown
	}
	if _, disabled := borrowDisabled[venue]; disabled {
		return BorrowStateUnavailable
	}
	if known == nil {
		return BorrowStateUnknown
	}
	return known.BorrowState(venue, baseAsset, now)
}

// perpSpotAssetBorrowable 布尔闸:仅 confirmed-unavailable(含 borrowDisabled)返回 false;unknown fail-open 返回 true。
// 新调用方优先使用 perpSpotBorrowState;本函数保留以兼容仍按 bool 消费的旧路径。
func perpSpotAssetBorrowable(venue, baseAsset string, known *BorrowKnownAvailableRegistry, borrowDisabled map[string]struct{}, now time.Time) bool {
	return perpSpotBorrowState(venue, baseAsset, known, borrowDisabled, now) != BorrowStateUnavailable
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
