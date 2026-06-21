package app

import (
	"strings"
	"time"
)

const (
	defaultMinHoldToFirstSettlementBufferSeconds = 60
	defaultFundingCaptureIntervalHours           = 8
)

// rotationEarliestFundingAfterOpen 取开仓后最早一次 intended funding 结算时刻(多腿取 min)。
func rotationEarliestFundingAfterOpen(openedAt time.Time, fundingTimes ...time.Time) time.Time {
	if openedAt.IsZero() {
		return time.Time{}
	}
	var target time.Time
	for _, t := range fundingTimes {
		if t.IsZero() || !t.After(openedAt) {
			continue
		}
		if target.IsZero() || t.Before(target) {
			target = t
		}
	}
	return target
}

func rotationEffectiveTargetFirstFundingTime(openedAt, storedTarget time.Time, fundingTimes ...time.Time) time.Time {
	if !storedTarget.IsZero() {
		return storedTarget
	}
	return rotationEarliestFundingAfterOpen(openedAt, fundingTimes...)
}

func rotationMinHoldToFirstSettlementBuffer(cfg RotationConfig) time.Duration {
	sec := cfg.MinHoldToFirstSettlementBufferSeconds
	if sec <= 0 {
		sec = defaultMinHoldToFirstSettlementBufferSeconds
	}
	return time.Duration(sec) * time.Second
}

func crossVenueMinHoldToFirstSettlementBuffer(cfg CrossVenueExecutorConfig) time.Duration {
	sec := cfg.MinHoldToFirstSettlementBufferSeconds
	if sec <= 0 {
		sec = defaultMinHoldToFirstSettlementBufferSeconds
	}
	return time.Duration(sec) * time.Second
}

// rotationFirstSettlementHoldSatisfied 是否已持有至开仓目标的首个 funding 结算(+缓冲)。
func rotationFirstSettlementHoldSatisfied(pos HeldPosition, cfg RotationConfig, now time.Time) bool {
	if !cfg.MinHoldToFirstSettlementEnabled {
		return true
	}
	if pos.OpenedAt.IsZero() {
		return true
	}
	target := rotationEffectiveTargetFirstFundingTime(pos.OpenedAt, pos.TargetFirstFundingTime, pos.NextFundingTime)
	if target.IsZero() || !target.After(pos.OpenedAt) {
		return true
	}
	return !now.Before(target.Add(rotationMinHoldToFirstSettlementBuffer(cfg)))
}

func crossVenueFirstSettlementHoldSatisfied(pos *CrossVenuePosition, cfg CrossVenueExecutorConfig, now time.Time) bool {
	if !cfg.MinHoldToFirstSettlementEnabled {
		return true
	}
	if pos == nil || pos.OpenedAt.IsZero() {
		return true
	}
	target := rotationEffectiveTargetFirstFundingTime(
		pos.OpenedAt, pos.TargetFirstFundingTime, pos.HighNextFundingTime, pos.LowNextFundingTime,
	)
	if target.IsZero() || !target.After(pos.OpenedAt) {
		return true
	}
	return !now.Before(target.Add(crossVenueMinHoldToFirstSettlementBuffer(cfg)))
}

// crossVenueIsHardRiskCloseReason 硬风控平仓理由:须立即执行,不受 min-hold-to-settlement 限制。
func crossVenueIsHardRiskCloseReason(reason string) bool {
	if reason == "" {
		return false
	}
	if strings.Contains(reason, "硬止损") {
		return true
	}
	return false
}

func crossVenueFirstSettlementHoldBlocksVoluntaryClose(pos *CrossVenuePosition, cfg CrossVenueExecutorConfig, now time.Time) bool {
	if !cfg.MinHoldToFirstSettlementEnabled {
		return false
	}
	return !crossVenueFirstSettlementHoldSatisfied(pos, cfg, now)
}

func (e *CrossVenueExecutor) crossVenueFilterVoluntaryCloseReason(pos *CrossVenuePosition, reason string) string {
	if reason == "" || crossVenueIsHardRiskCloseReason(reason) {
		return reason
	}
	if crossVenueFirstSettlementHoldBlocksVoluntaryClose(pos, e.cfg.CrossVenueExecutor, e.nowFn()) {
		return ""
	}
	return reason
}

func settlementHeldThroughFirstFunding(openTime, closeTime time.Time, fundingIntervalHours int) bool {
	if openTime.IsZero() || closeTime.IsZero() || !closeTime.After(openTime) {
		return false
	}
	if fundingIntervalHours <= 0 {
		fundingIntervalHours = defaultFundingCaptureIntervalHours
	}
	return closeTime.Sub(openTime) >= time.Duration(fundingIntervalHours)*time.Hour
}
