package app

import (
	"context"
	"fmt"
	"strings"

	"lingxi/internal/venue"
)

func crossVenueHasPendingSwitch(pos CrossVenuePosition) bool {
	return pos.PendingSwitchHighVenue != "" && pos.PendingSwitchLowVenue != ""
}

func crossVenuePendingSwitchTarget(pos CrossVenuePosition) string {
	if !crossVenueHasPendingSwitch(pos) {
		return ""
	}
	return pos.PendingSwitchHighVenue + "/" + pos.PendingSwitchLowVenue
}

func crossVenueIsOpeningStatus(status CrossVenuePositionStatus) bool {
	return status == CrossVenueStatusOpeningFirstLeg || status == CrossVenueStatusOpeningSecondLeg
}

func crossVenueIsNewOpenPhase(phase string) bool {
	return strings.HasPrefix(strings.TrimSpace(phase), "open_new")
}

func crossVenueCanPromoteToSecondLegContinuation(status CrossVenuePositionStatus, phase string, hOpen, lOpen bool) bool {
	if !hOpen || lOpen {
		return false
	}
	phase = strings.TrimSpace(phase)
	return status == CrossVenueStatusOpeningFirstLeg ||
		status == CrossVenueStatusOpeningSecondLeg ||
		phase == "open_new_first_leg" ||
		phase == "open_new_retry" ||
		phase == "open_new_second_leg"
}

func (e *CrossVenueExecutor) reconcileOpeningPositionOnStart(ctx context.Context, p CrossVenuePosition, hOpen, lOpen bool) {
	_ = ctx
	now := e.nowFn()
	intent, hasIntent := e.switchIntentForTarget(p.Symbol, p.HighVenue, p.LowVenue, now)
	if crossVenueIsStagedSwitch(p.PendingSwitchSequence) && crossVenueIsStagedWaitPhase(p.PartialSwitchPhase) {
		switch {
		case !hOpen && !lOpen:
			p.Status = CrossVenueStatusClosed
			p.CloseReason = nonEmpty(p.CloseReason, "reconcile_opening_empty")
			p.RecoveryAction = crossVenueRecoveryActionObserve
			p.PartialSwitchPhase = "open_new_retry"
			e.finalizeClosedCrossVenuePosition(p)
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_retry", "reconcile staged target empty, waiting retry")
			}
		case hOpen && !lOpen:
			from := p.TransitionState
			if hasIntent && crossVenueStagedSwitchReadyForSecondLeg(intent) {
				p.Status = CrossVenueStatusOpeningSecondLeg
				p.TransitionState = "open"
				p.RecoveryAction = crossVenueRecoveryActionObserve
				p.PartialSwitchPhase = "open_new_second_leg"
			} else {
				p.Status = CrossVenueStatusOpeningSecondLeg
				p.TransitionState = "staged"
				p.RecoveryAction = crossVenueRecoveryActionObserve
				p.PartialSwitchPhase = "staged_close_old"
			}
			p.UpdatedAt = now
			_ = e.persistPosition(p)
			e.recordCrossVenueTransition(p, from, p.TransitionState, "reconcile 保留 staged switch 过渡态")
		case hOpen && lOpen:
			from := p.TransitionState
			p.Status = CrossVenueStatusOpen
			p.TransitionState = "hold"
			p.RecoveryAction = crossVenueRecoveryActionObserve
			p.PartialSwitchPhase = "consumed"
			p.UpdatedAt = now
			_ = e.persistPosition(p)
			e.recordCrossVenueTransition(p, from, "hold", "reconcile 确认 staged target 双腿均在场")
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "consumed", "reconcile confirmed staged target live")
				_ = e.removeSwitchIntent(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue)
			}
		case hOpen != lOpen:
			from := p.TransitionState
			p.Status = CrossVenueStatusOrphanSingleLeg
			p.TransitionState = "recover"
			p.RecoveryAction = crossVenueRecoveryActionFlatten
			p.CloseReason = nonEmpty(p.CloseReason, "reconcile_open_new_unhedged")
			p.PartialSwitchPhase = "rollback_new"
			p.UpdatedAt = now
			_ = e.persistPosition(p)
			e.recordCrossVenueTransition(p, from, "recover", "reconcile 发现 staged target 单腿在场,进入 rollback_new/flatten")
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "reconcile found staged target unhedged, flattening")
			}
			e.notifyCritical("跨所 reconcile staged 裸腿", fmt.Sprintf("%s staged target 只剩单腿,进入 flatten 恢复", p.Symbol), p.Symbol)
		}
		return
	}
	switch {
	case !hOpen && !lOpen:
		p.Status = CrossVenueStatusClosed
		p.CloseReason = nonEmpty(p.CloseReason, "reconcile_opening_empty")
		p.RecoveryAction = crossVenueRecoveryActionObserve
		p.PartialSwitchPhase = nonEmpty(p.PartialSwitchPhase, "open_new_retry")
		e.finalizeClosedCrossVenuePosition(p)
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_retry", "reconcile opening position empty, waiting retry")
		}
	case crossVenueCanPromoteToSecondLegContinuation(p.Status, p.PartialSwitchPhase, hOpen, lOpen):
		from := p.TransitionState
		p.Status = CrossVenueStatusOpeningSecondLeg
		p.TransitionState = "open"
		p.RecoveryAction = crossVenueRecoveryActionObserve
		p.PartialSwitchPhase = "open_new_second_leg"
		p.UpdatedAt = now
		_ = e.persistPosition(p)
		e.recordCrossVenueTransition(p, from, "open", "reconcile 保留第一腿并升级为 second-leg continuation")
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_second_leg", "reconcile promoted target to second-leg continuation")
		}
	case hOpen && lOpen:
		from := p.TransitionState
		p.Status = CrossVenueStatusOpen
		p.TransitionState = "hold"
		p.RecoveryAction = crossVenueRecoveryActionObserve
		if crossVenueIsNewOpenPhase(p.PartialSwitchPhase) || p.PartialSwitchPhase == "" {
			p.PartialSwitchPhase = "consumed"
		}
		p.UpdatedAt = now
		_ = e.persistPosition(p)
		e.recordCrossVenueTransition(p, from, "hold", "reconcile 确认新仓双腿均在场")
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "consumed", "reconcile confirmed target position live")
			_ = e.removeSwitchIntent(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue)
		}
	case hOpen != lOpen:
		from := p.TransitionState
		p.Status = CrossVenueStatusOrphanSingleLeg
		p.TransitionState = "recover"
		p.RecoveryAction = crossVenueRecoveryActionFlatten
		p.CloseReason = nonEmpty(p.CloseReason, "reconcile_open_new_unhedged")
		p.PartialSwitchPhase = "rollback_new"
		p.UpdatedAt = now
		_ = e.persistPosition(p)
		e.recordCrossVenueTransition(p, from, "recover", "reconcile 发现新仓仅单腿在场,进入 rollback_new/flatten")
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "reconcile found unhedged target leg, flattening")
		}
		e.notifyCritical("跨所 reconcile 新仓裸腿", fmt.Sprintf("%s 新仓只开出单腿,进入 flatten 恢复", p.Symbol), p.Symbol)
	}
}

func (e *CrossVenueExecutor) reconcileTrackedPositionOnStart(ctx context.Context, p CrossVenuePosition, highV, lowV venue.Venue) {
	_ = ctx
	highPos, errH := highV.FetchPositions(ctx)
	lowPos, errL := lowV.FetchPositions(ctx)
	if errH != nil || errL != nil {
		e.logger.Printf("[跨所执行] reconcile:%s 拉持仓失败,保留台账", p.Symbol)
		return
	}
	sym := venue.Symbol(p.Symbol)
	hAmt := perpPositionQty(highPos, sym)
	lAmt := perpPositionQty(lowPos, sym)
	hOpen := absQty(hAmt) > executionQtyTolerance
	lOpen := absQty(lAmt) > executionQtyTolerance
	if crossVenueIsOpeningStatus(p.Status) {
		e.reconcileOpeningPositionOnStart(ctx, p, hOpen, lOpen)
		return
	}
	switch {
	case !hOpen && !lOpen:
		e.logger.Printf("[跨所执行] reconcile:%s 两所均无持仓,orphan 清除台账", p.Symbol)
		p.Status = CrossVenueStatusClosed
		p.CloseReason = "reconcile_orphan"
		p.RecoveryAction = crossVenueRecoveryActionObserve
		e.finalizeClosedCrossVenuePosition(p)
		e.notifyWarn("跨所 orphan", fmt.Sprintf("%s 台账有但两所均无持仓", p.Symbol), p.Symbol)
	case hOpen != lOpen:
		p.Status = CrossVenueStatusOrphanSingleLeg
		p.TransitionState = "recover"
		p.RecoveryAction = crossVenueRecoveryActionFlatten
		p.CloseReason = nonEmpty(p.CloseReason, "reconcile_orphan_leg")
		p.UpdatedAt = e.nowFn()
		_ = e.persistPosition(p)
		e.recordCrossVenueTransition(p, "", "recover", "reconcile 发现 orphan,进入 flatten 恢复")
		e.notifyCritical("跨所 reconcile 孤腿", fmt.Sprintf("%s 台账与所内不一致", p.Symbol), p.Symbol)
	case p.Status == CrossVenueStatusClosing:
		e.markCrossVenuePositionRecovering(&p, "重启恢复续平")
	case crossVenueHasPendingSwitch(p):
		if p.Status != CrossVenueStatusClosing {
			p.Status = CrossVenueStatusClosing
		}
		if p.CloseReason == "" {
			p.CloseReason = nonEmpty(p.PendingSwitchReason, "pending switch recovery")
		}
		e.markCrossVenuePositionPartialSwitch(&p, "重启恢复 pending switch")
	default:
		p.TransitionState = "hold"
		p.RecoveryAction = crossVenueRecoveryActionObserve
		e.logger.Printf("[跨所执行] reconcile:%s 恢复监控", p.Symbol)
	}
}

func (e *CrossVenueExecutor) markCrossVenuePositionRecovering(pos *CrossVenuePosition, reason string) {
	if e == nil || pos == nil {
		return
	}
	from := pos.TransitionState
	pos.TransitionState = "recover"
	pos.RecoveryAction = crossVenueRecoveryActionFlatten
	pos.PartialSwitchPhase = ""
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(*pos)
	e.recordCrossVenueTransition(*pos, from, "recover", reason)
}

func (e *CrossVenueExecutor) markCrossVenuePositionPartialSwitch(pos *CrossVenuePosition, reason string) {
	if e == nil || pos == nil {
		return
	}
	from := pos.TransitionState
	pos.TransitionState = "partial_switch"
	pos.RecoveryAction = crossVenueRecoveryActionFlatten
	if pos.PendingSwitchSequence == "" {
		pos.PendingSwitchSequence = crossVenueSwitchSequenceCloseThenOpen
	}
	if pos.PartialSwitchPhase == "" {
		pos.PartialSwitchPhase = "close_old"
	}
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(*pos)
	e.recordCrossVenueTransition(*pos, from, "recover", nonEmpty(reason, "partial switch 恢复")+" seq="+nonEmpty(pos.PendingSwitchSequence, crossVenueSwitchSequenceCloseThenOpen)+" phase="+pos.PartialSwitchPhase)
	e.notifyWarn("跨所 partial switch", fmt.Sprintf("%s 保留过渡态等待续平后再切到 %s", pos.Symbol, crossVenuePendingSwitchTarget(*pos)), pos.Symbol)
}
