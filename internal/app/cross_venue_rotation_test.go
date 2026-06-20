package app

import (
	"testing"
	"time"
)

func TestCrossVenueCompareAllowedHeterogeneousIntervals(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		MinComparableWindowHours:   0.25,
		PostFundingCooldownSeconds: 180,
	}
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	highNext := now.Add(2 * time.Hour)   // 4h leg,距上次结算 >3min
	lowNext := now.Add(45 * time.Minute) // 1h leg,距上次结算 15min(>180s 冷却)
	window := crossVenueComparableWindowHours(now, highNext, lowNext)
	if window < 0.25 {
		t.Fatalf("comparable window=%.2fh want >=0.25 (min of 45m/2h legs)", window)
	}
	pos := &CrossVenuePosition{
		HighIntervalHours:            4,
		LowIntervalHours:             1,
		HighNextFundingTime:          highNext,
		LowNextFundingTime:           lowNext,
		CurrentComparableWindowHours: window,
	}
	if !crossVenueCompareAllowed(cfg, pos, now) {
		t.Fatal("expected compare allowed when window >= min and not in post-funding cooldown")
	}

	narrowWindow := 0.1
	pos.CurrentComparableWindowHours = narrowWindow
	if crossVenueCompareAllowed(cfg, pos, now) {
		t.Fatal("expected blocked when comparable window below min")
	}

	pos.CurrentComparableWindowHours = window
	// 1h 腿刚结算后 60s:仅该腿 interval 触发冷却,4h 腿不受影响。
	justAfter1hSettle := lowNext.Add(-1*time.Hour + 60*time.Second)
	pos.LowNextFundingTime = lowNext
	if crossVenueCompareAllowed(cfg, pos, justAfter1hSettle) {
		t.Fatal("expected blocked during 1h leg post-funding cooldown")
	}
	// 4h 腿结算后 60s:按 4h interval 独立判断,1h 腿不在冷却。
	fourHourNext := now.Add(3 * time.Hour)
	pos.HighNextFundingTime = fourHourNext
	pos.LowNextFundingTime = lowNext
	justAfter4hSettle := fourHourNext.Add(-4*time.Hour + 60*time.Second)
	if crossVenueCompareAllowed(cfg, pos, justAfter4hSettle) {
		t.Fatal("expected blocked during 4h leg post-funding cooldown")
	}
}
