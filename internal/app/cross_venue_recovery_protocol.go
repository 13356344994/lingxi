package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// crossVenueReconcileFetchAttempts 启动对账拉两腿持仓的最大尝试次数(含首次);用于吸收瞬时 API 抖动。
const crossVenueReconcileFetchAttempts = 4

// crossVenueReconcileFetchBackoff 重试间的基础退避(线性递增 ×attempt);设为 var 便于测试压缩。
var crossVenueReconcileFetchBackoff = time.Second

// fetchReconcilePositionsWithRetry 以有限线性退避重试拉两腿持仓,吸收启动瞬时 API 抖动。
// 任一腿失败即视为本轮失败并退避重试;尊重 ctx 取消;全部尝试仍失败才返回错误(交由调用方 fail-closed)。
func (e *CrossVenueExecutor) fetchReconcilePositionsWithRetry(ctx context.Context, symbol string, highV, lowV venue.Venue) (highPos, lowPos []venue.Position, err error) {
	var errH, errL error
	for attempt := 1; attempt <= crossVenueReconcileFetchAttempts; attempt++ {
		highPos, errH = highV.FetchPositions(ctx)
		lowPos, errL = lowV.FetchPositions(ctx)
		if errH == nil && errL == nil {
			if attempt > 1 && e.logger != nil {
				e.logger.Printf("[跨所执行] reconcile:%s 拉持仓第 %d 次重试成功(瞬时抖动已吸收)", symbol, attempt)
			}
			return highPos, lowPos, nil
		}
		if attempt < crossVenueReconcileFetchAttempts {
			if e.logger != nil {
				e.logger.Printf("[跨所执行] reconcile:%s 拉持仓失败(第 %d/%d 次, high=%v low=%v),退避重试", symbol, attempt, crossVenueReconcileFetchAttempts, errH, errL)
			}
			select {
			case <-ctx.Done():
				return highPos, lowPos, fmt.Errorf("reconcile 拉持仓被取消: %w", ctx.Err())
			case <-time.After(crossVenueReconcileFetchBackoff * time.Duration(attempt)):
			}
		}
	}
	return highPos, lowPos, fmt.Errorf("high=%v low=%v", errH, errL)
}

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
			e.finalizeClosedCrossVenuePosition(p, true)
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
		e.finalizeClosedCrossVenuePosition(p, true)
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
	// 启动对账先以有限退避重试拉持仓,吸收瞬时 API 抖动(避免一次启动抖动就永久 HALT 开仓)。
	highPos, lowPos, ferr := e.fetchReconcilePositionsWithRetry(ctx, p.Symbol, highV, lowV)
	if ferr != nil {
		// 持续(非瞬时)拉持仓失败:fail-closed。绝不在未经交易所核验的台账上开新仓。
		// HALT 新开(在场仓仍由 manage 循环 FetchPositions 复核并平仓/管理)。
		// 注意:本进程无周期性 reconcile,不存在自动解除路径——这是真实交易所断连时的预期 fail-closed 行为:
		// 待运维确认交易所连通后,经控制面策略总控(strategy_control.go SetOpeningsEnabled(true))手动重新开启开仓。
		e.logger.Printf("[跨所执行] reconcile:%s 重试 %d 次后仍拉持仓失败(%v),HALT 新开待人工核验后由控制面手动重开,保留台账继续管理", p.Symbol, crossVenueReconcileFetchAttempts, ferr)
		_ = e.SetOpeningsEnabled(false)
		e.notifyCritical("跨所 reconcile 核验失败",
			fmt.Sprintf("%s 启动对账重试 %d 次仍拉持仓失败,已 HALT 新开仓(在场仓仍管理/平仓);确认交易所连通后须经控制面手动重新开启开仓", p.Symbol, crossVenueReconcileFetchAttempts), p.Symbol)
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
		e.finalizeClosedCrossVenuePosition(p, true)
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
