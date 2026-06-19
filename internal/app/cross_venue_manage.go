package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

// cross_venue_manage.go 持仓期风控与平仓条件。

func estimateLiqDistance(pos venue.Position) float64 {
	mark := pos.MarkPrice
	if mark <= 0 {
		mark = pos.EntryPrice
	}
	if mark <= 0 {
		return 1.0
	}
	lev := float64(pos.Leverage)
	if lev <= 0 {
		lev = 1
	}
	// 无 liqPrice 时用杠杆保守估算:强平距离 ≈ 0.9/杠杆
	return 0.9 / lev
}

func (e *CrossVenueExecutor) manageCrossVenuePosition(ctx context.Context, pos CrossVenuePosition, live bool) (CrossVenuePosition, bool) {
	if !live {
		if reason := e.shouldCloseCrossVenue(ctx, &pos); reason != "" {
			logDryRunClose(e.logger, "跨所perp-perp", pos.Symbol, reason)
		}
		return pos, false
	}
	sym := venue.Symbol(pos.Symbol)
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]
	if highV == nil || lowV == nil {
		return pos, false
	}

	// closing 续平分支:上一轮平仓未完成(IOC 没吃干净)的持仓,直接重试平仓直到两腿都清零,
	// 不再走开仓期风控(孤腿/同步性都是「在场对冲」语义,半平状态会误判)。
	if pos.Status == CrossVenueStatusClosing {
		if crossVenueHasPendingSwitch(pos) && pos.TransitionState != "partial_switch" {
			e.markCrossVenuePositionPartialSwitch(&pos, "续平 pending switch")
		}
		err := e.closeCrossVenuePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, err == nil
	}

	highPos, errH := highV.FetchPositions(ctx)
	lowPos, errL := lowV.FetchPositions(ctx)
	if errH != nil || errL != nil {
		e.logger.Printf("[跨所] 拉持仓失败 %s: high=%v low=%v", pos.Symbol, errH, errL)
		return pos, false
	}

	highAmt := perpPositionQty(highPos, sym)
	lowAmt := perpPositionQty(lowPos, sym)
	highOpen := absQty(highAmt) > executionQtyTolerance
	lowOpen := absQty(lowAmt) > executionQtyTolerance

	if crossVenueIsOpeningStatus(pos.Status) && (crossVenueIsNewOpenPhase(pos.PartialSwitchPhase) || pos.PendingSwitchSequence != "") {
		return e.manageCrossVenueOpeningContinuation(ctx, pos, highOpen, lowOpen, highAmt, lowAmt, live)
	}

	// 孤腿检测
	if highOpen != lowOpen {
		e.handleOrphanLeg(ctx, pos, highOpen, lowOpen, live, highAmt, lowAmt)
		return pos, true
	}
	if !highOpen && !lowOpen {
		pos.Status = CrossVenueStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedCrossVenuePosition(pos, live)
		return pos, true
	}

	// 同步性核验
	dev := qtyDeviation(highAmt, lowAmt)
	if dev > 0.05 {
		e.rebalanceHedge(ctx, pos, highAmt, lowAmt, live)
		e.notifyWarn("跨所对冲偏差", fmt.Sprintf("%s 两腿数量偏差 %.1f%%", pos.Symbol, dev*100), pos.Symbol)
	}

	// 强平距离(单一来源: risk.min_liquidation_distance_rate,见 risk_liq_distance.go)
	volMult := 0.0
	if e.symbolVolMultiplier != nil {
		volMult = e.symbolVolMultiplier(pos.Symbol)
	}
	minDist := EffectiveMinLiquidationDistance(e.cfg.RiskConfig, e.cfg.CrossVenueExecutor, pos.Symbol, volMult)
	if minDist <= 0 {
		minDist = defaultMinLiquidationDistanceRate
	}
	for _, p := range highPos {
		if p.Symbol == sym && estimateLiqDistance(p) < minDist {
			_ = e.reduceCrossVenueRisk(ctx, pos, live)
			return pos, false
		}
	}
	for _, p := range lowPos {
		if p.Symbol == sym && estimateLiqDistance(p) < minDist {
			_ = e.reduceCrossVenueRisk(ctx, pos, live)
			return pos, false
		}
	}

	// 资金费记账:优先拉真实结算(节流),两腿权威值覆盖估算;轮间用费率估算补临时展示。
	e.syncRealizedFunding(ctx, &pos)
	interim := e.estimateFundingAccrual(&pos)
	pos.EstFundingUSDT = pos.HighLegFundingUSDT + pos.LowLegFundingUSDT + interim
	pos.EstimatedFundingUSDT = interim
	e.refreshCurrentPositionMetrics(&pos)
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	// dead-man 续期(okx/gate)
	e.armDeadMansForLegs(ctx, pos, live)

	// 平仓条件
	closeReason := e.shouldCloseCrossVenue(ctx, &pos)
	if closeReason != "" {
		_ = e.closeCrossVenuePosition(ctx, pos, live, closeReason)
		return pos, true
	}
	return pos, false
}

func (e *CrossVenueExecutor) manageCrossVenueOpeningContinuation(ctx context.Context, pos CrossVenuePosition, highOpen, lowOpen bool, highAmt, lowAmt float64, live bool) (CrossVenuePosition, bool) {
	now := e.nowFn()
	intent, hasIntent := e.switchIntentForTarget(pos.Symbol, pos.HighVenue, pos.LowVenue, now)
	if crossVenueIsStagedSwitch(pos.PendingSwitchSequence) && crossVenueIsStagedWaitPhase(pos.PartialSwitchPhase) {
		switch {
		case !highOpen && !lowOpen:
			pos.Status = CrossVenueStatusClosed
			pos.CloseReason = nonEmpty(pos.CloseReason, "open_new_retry")
			pos.RecoveryAction = crossVenueRecoveryActionObserve
			pos.PartialSwitchPhase = "open_new_retry"
			pos.UpdatedAt = now
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_retry", "staged switch target disappeared, waiting retry")
			}
			e.finalizeClosedCrossVenuePosition(pos, live)
			return pos, true
		case highOpen && !lowOpen:
			if hasIntent && crossVenueStagedSwitchReadyForSecondLeg(intent) {
				pos.PartialSwitchPhase = "open_new_second_leg"
				pos.TransitionState = "open"
				pos.UpdatedAt = now
				return e.continueCrossVenueOpeningSecondLeg(ctx, pos, highAmt, live, intent, hasIntent)
			}
			pos.Status = CrossVenueStatusOpeningSecondLeg
			pos.TransitionState = "staged"
			pos.UpdatedAt = now
			_ = e.persistPosition(pos)
			return pos, true
		case highOpen && lowOpen:
			pos.Status = CrossVenueStatusOpen
			pos.TransitionState = "hold"
			pos.RecoveryAction = crossVenueRecoveryActionObserve
			pos.PartialSwitchPhase = "consumed"
			pos.UpdatedAt = now
			_ = e.persistPosition(pos)
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "consumed", "staged switch target fully live")
				_ = e.removeSwitchIntent(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue)
			}
			return pos, true
		default:
			pos.Status = CrossVenueStatusOrphanSingleLeg
			pos.TransitionState = "recover"
			pos.RecoveryAction = crossVenueRecoveryActionFlatten
			pos.PartialSwitchPhase = "rollback_new"
			pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
			pos.UpdatedAt = now
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "staged switch target became unhedged, flattening")
			}
			e.handleOrphanLeg(ctx, pos, highOpen, lowOpen, live, highAmt, lowAmt)
			return pos, true
		}
	}
	switch {
	case !highOpen && !lowOpen:
		pos.Status = CrossVenueStatusClosed
		pos.CloseReason = nonEmpty(pos.CloseReason, "open_new_retry")
		pos.RecoveryAction = crossVenueRecoveryActionObserve
		pos.PartialSwitchPhase = "open_new_retry"
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_retry", "runtime opening target disappeared, waiting retry")
		}
		e.finalizeClosedCrossVenuePosition(pos, live)
		return pos, true
	case crossVenueCanPromoteToSecondLegContinuation(pos.Status, pos.PartialSwitchPhase, highOpen, lowOpen):
		pos.Status = CrossVenueStatusOpeningSecondLeg
		pos.TransitionState = "open"
		pos.RecoveryAction = crossVenueRecoveryActionObserve
		pos.PartialSwitchPhase = "open_new_second_leg"
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_second_leg", "runtime promoted target to second-leg continuation")
		}
		return e.continueCrossVenueOpeningSecondLeg(ctx, pos, highAmt, live, intent, hasIntent)
	case highOpen && !lowOpen && strings.TrimSpace(pos.PartialSwitchPhase) == "open_new_second_leg":
		return e.continueCrossVenueOpeningSecondLeg(ctx, pos, highAmt, live, intent, hasIntent)
	case highOpen && lowOpen:
		pos.Status = CrossVenueStatusOpen
		pos.TransitionState = "hold"
		pos.RecoveryAction = crossVenueRecoveryActionObserve
		pos.PartialSwitchPhase = "consumed"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "consumed", "runtime confirmed target position live")
			_ = e.removeSwitchIntent(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue)
		}
		return pos, true
	default:
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime opening target unhedged, flattening")
		}
		e.handleOrphanLeg(ctx, pos, highOpen, lowOpen, live, highAmt, lowAmt)
		return pos, true
	}
}

func (e *CrossVenueExecutor) continueCrossVenueOpeningSecondLeg(ctx context.Context, pos CrossVenuePosition, highAmt float64, live bool, intent crossVenueSwitchIntent, hasIntent bool) (CrossVenuePosition, bool) {
	now := e.nowFn()
	sym := venue.Symbol(pos.Symbol)
	lowV := e.venues[pos.LowVenue]
	highV := e.venues[pos.HighVenue]
	lowTrader, _ := lowV.(crossVenueTrader)
	if lowV == nil || highV == nil || lowTrader == nil {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime continuation missing trader, flattening")
		}
		e.handleOrphanLeg(ctx, pos, true, false, live, highAmt, 0)
		return pos, true
	}
	lowInstr, ok := lookupVenueInstrument(ctx, lowV, sym)
	if !ok {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime continuation missing instrument, flattening")
		}
		e.handleOrphanLeg(ctx, pos, true, false, live, highAmt, 0)
		return pos, true
	}
	lowCaps := lowV.Capabilities()
	highCaps := highV.Capabilities()
	hedgeQty := absQty(highAmt)
	if lowCaps.ContractBasedPerp {
		hedgeQty = normalizeVenueQty(hedgeQty, lowInstr, true)
	}
	if hedgeQty <= executionQtyTolerance {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime continuation normalized hedge qty to zero")
		}
		e.handleOrphanLeg(ctx, pos, true, false, live, highAmt, 0)
		return pos, true
	}
	_, _, deadlineBudget, _ := crossVenueMakerChaseParams(e.cfg.CrossVenueExecutor)
	lowID := crossVenueLegClientID(pos.IdemPrefix, fmt.Sprintf("l2%d", now.Unix()))
	if hasIntent {
		_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_second_leg", "runtime continuation retrying second leg")
	}
	lowRes, err := e.placeCrossVenueLegExec(ctx, lowTrader, sym, venue.SideBuy, hedgeQty, lowID, false, live, true, crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor), now.Add(deadlineBudget))
	if err != nil || lowRes.ExecutedQty <= executionQtyTolerance {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_new")
		pos.UpdatedAt = now
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime continuation second leg failed, flattening")
		}
		e.handleOrphanLeg(ctx, pos, true, false, live, highAmt, 0)
		return pos, true
	}
	book, _ := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
	e.recordCrossVenueExecution(ctx, &pos, cvExecOpenLow, lowRes, crossVenueBookMid(book))
	highCoin := absQty(pos.HighLeg.Qty)
	if highCoin <= executionQtyTolerance {
		highCoin = absQty(highAmt)
	}
	lowCoin := crossVenueLegExecutedCoinQty(lowCaps, lowInstr, lowRes, highCoin)
	bookQty := reconcileCrossVenueOpenBookQty(highCaps, lowCaps, highCoin, lowCoin)
	pos.HighLeg.Qty = bookQty
	pos.LowLeg.Qty = bookQty
	pos.LowLeg.EntryPrice = lowRes.AvgPrice
	pos.LowLeg.OrderIDs = append(pos.LowLeg.OrderIDs, lowID)
	if pos.NotionalUSDT <= 0 {
		refPrice := pos.HighLeg.EntryPrice
		if refPrice <= 0 {
			refPrice = lowRes.AvgPrice
		}
		pos.NotionalUSDT = bookQty * refPrice
	}
	pos.Status = CrossVenueStatusOpen
	pos.TransitionState = "hold"
	pos.RecoveryAction = crossVenueRecoveryActionObserve
	pos.PartialSwitchPhase = "consumed"
	pos.UpdatedAt = now
	_ = e.persistPosition(pos)
	e.recordCrossVenueTransition(pos, string(CrossVenueStatusOpeningSecondLeg), string(CrossVenueStatusOpen), "运行期续做第二腿完成")
	if hasIntent {
		_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "consumed", "runtime continuation completed second leg")
		_ = e.removeSwitchIntent(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue)
	}
	return pos, true
}

func (e *CrossVenueExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenuePosition, highOpen, lowOpen, live bool, highAmt, lowAmt float64) {
	pos.Status = CrossVenueStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	sym := venue.Symbol(pos.Symbol)
	orphDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	if highOpen && !lowOpen {
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(highAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if live {
				if res, _ := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideBuy, qty, crossVenueLegClientID(pos.IdemPrefix, "oh"), true, live, true, takerBps, orphDeadline); res.ExecutedQty > executionQtyTolerance {
					e.recordCrossVenueExecution(ctx, &pos, cvExecOrphanCloseHigh, res, crossVenueBookMid(book))
					_ = e.persistPosition(pos)
				}
			}
		}
	} else if lowOpen && !highOpen {
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(lowAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if live {
				if res, _ := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideSell, qty, crossVenueLegClientID(pos.IdemPrefix, "ol"), true, live, true, takerBps, orphDeadline); res.ExecutedQty > executionQtyTolerance {
					e.recordCrossVenueExecution(ctx, &pos, cvExecOrphanCloseLow, res, crossVenueBookMid(book))
					_ = e.persistPosition(pos)
				}
			}
		}
	}
	e.notifyCritical("跨所孤腿", fmt.Sprintf("%s 检测到孤腿,已平掉剩余腿", pos.Symbol), pos.Symbol)
}

func (e *CrossVenueExecutor) rebalanceHedge(ctx context.Context, pos CrossVenuePosition, highAmt, lowAmt float64, live bool) {
	sym := venue.Symbol(pos.Symbol)
	hAbs := absQty(highAmt)
	lAbs := absQty(lowAmt)
	if math.Abs(hAbs-lAbs)/math.Max(hAbs, lAbs) <= 0.05 {
		return
	}
	// 选风险小的方向:减大腿
	rebalDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	if hAbs > lAbs {
		delta := hAbs - lAbs
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil && live {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if res, _ := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideBuy, delta, crossVenueLegClientID(pos.IdemPrefix, "bh"), true, live, true, takerBps, rebalDeadline); res.ExecutedQty > executionQtyTolerance {
				e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceHigh, res, crossVenueBookMid(book))
			}
		}
		pos.HighLeg.Qty = lAbs
	} else {
		delta := lAbs - hAbs
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil && live {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if res, _ := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideSell, delta, crossVenueLegClientID(pos.IdemPrefix, "bl"), true, live, true, takerBps, rebalDeadline); res.ExecutedQty > executionQtyTolerance {
				e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceLow, res, crossVenueBookMid(book))
			}
		}
		pos.LowLeg.Qty = hAbs
	}
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
}

// estimateFundingAccrual 估算自上次记账以来的资金费**临时展示**增量:按 now−LastAccrualAt 的**真实小时数**
// 折算(主循环 60s 一轮时每轮只记 1/60 小时的量,绝不按「本轮=1小时」虚高)。费率从扫描器缓存查
// (绝不现场拉全量);缓存缺失时返回 0 且**不推进** LastAccrualAt,下轮拿到数据后把窗口补上。
// 权威腿资费由 syncRealizedFunding 覆盖写入,本函数**不修改** HighLegFundingUSDT/LowLegFundingUSDT。
func (e *CrossVenueExecutor) estimateFundingAccrual(pos *CrossVenuePosition) float64 {
	if pos == nil {
		return 0
	}
	now := e.nowFn()
	last := pos.LastAccrualAt
	if last.IsZero() {
		last = pos.OpenedAt
	}
	elapsedHours := now.Sub(last).Hours()
	if elapsedHours <= 0 {
		return 0
	}
	sym := venue.Symbol(pos.Symbol)
	hEntry, okH := e.fundingFromScanner(pos.HighVenue, sym)
	lEntry, okL := e.fundingFromScanner(pos.LowVenue, sym)
	if !okH || !okL {
		return 0
	}
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.HighLeg.Qty * pos.HighLeg.EntryPrice
	}
	// 空高费所收 funding,多低费所付 funding;按各自结算 interval 折算到每小时口径。
	hInterval := hEntry.IntervalHours
	lInterval := lEntry.IntervalHours
	if hInterval <= 0 {
		hInterval = 8
	}
	if lInterval <= 0 {
		lInterval = 8
	}
	hHourly := hEntry.Rate / float64(hInterval)
	lHourly := lEntry.Rate / float64(lInterval)
	hPart := notional * hHourly * elapsedHours
	lPart := -notional * lHourly * elapsedHours
	pos.LastAccrualAt = now
	return hPart + lPart
}

// syncRealizedFunding 按节流间隔拉两腿真实结算资费,覆盖写入腿字段(非叠加)。拉取失败保留上次值。
func (e *CrossVenueExecutor) syncRealizedFunding(ctx context.Context, pos *CrossVenuePosition) {
	if pos == nil || !e.shouldPollRealizedFunding(pos) {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	since := pos.OpenedAt

	highTotal, highOK := e.fetchLegRealizedFunding(ctx, pos.Symbol, pos.HighVenue, sym, since)
	lowTotal, lowOK := e.fetchLegRealizedFunding(ctx, pos.Symbol, pos.LowVenue, sym, since)
	if !highOK && !lowOK {
		pos.RealizedFundingComplete = false
		pos.FundingTruthStatus = "missing"
		return
	}
	if highOK {
		pos.HighLegFundingUSDT = highTotal
	}
	if lowOK {
		pos.LowLegFundingUSDT = lowTotal
	}
	pos.RealizedFundingComplete = highOK && lowOK
	switch {
	case highOK && lowOK:
		pos.FundingTruthStatus = "complete"
	case highOK || lowOK:
		pos.FundingTruthStatus = "partial"
	default:
		pos.FundingTruthStatus = "missing"
	}
	now := e.nowFn()
	pos.LastRealizedFundingAt = now
	pos.LastAccrualAt = now
	if e.logger != nil {
		e.logger.Printf("[跨所] %s 真实资费 sync: high=%.4f low=%.4f net=%.4f",
			pos.Symbol, pos.HighLegFundingUSDT, pos.LowLegFundingUSDT,
			pos.HighLegFundingUSDT+pos.LowLegFundingUSDT)
	}
}

func (e *CrossVenueExecutor) refreshCurrentPositionMetrics(pos *CrossVenuePosition) {
	if e == nil || pos == nil {
		return
	}
	now := e.nowFn()
	pos.DecisionCycleID = crossVenueDecisionCycleID(now)
	snap := CrossVenueScanSnapshot{}
	if e.scanner != nil {
		snap = e.scanner.Snapshot()
		pos.CurrentSnapshotID = snap.SnapshotID
		pos.FreshnessAgeSeconds = crossVenueSnapshotAgeSeconds(snap.ScannedAt, now)
	}
	sym := venue.Symbol(pos.Symbol)
	if hEntry, ok := e.fundingFromScanner(pos.HighVenue, sym); ok {
		pos.HighFundingRate = hEntry.Rate
		pos.HighIntervalHours = hEntry.IntervalHours
		pos.HighNextFundingTime = hEntry.NextFundingTime
	}
	if lEntry, ok := e.fundingFromScanner(pos.LowVenue, sym); ok {
		pos.LowFundingRate = lEntry.Rate
		pos.LowIntervalHours = lEntry.IntervalHours
		pos.LowNextFundingTime = lEntry.NextFundingTime
	}
	pos.CurrentComparableWindowHours = crossVenueComparableWindowHours(now, pos.HighNextFundingTime, pos.LowNextFundingTime)
	if pos.RecoveryQualityScore <= 0 {
		pos.RecoveryQualityScore = crossVenueRecoveryQualityForPair(pos.HighVenue, pos.LowVenue)
	}
	if pos.ExecutionScore <= 0 {
		pos.ExecutionScore = crossVenueExecutionScore(crossVenueExpectedSlippageUSDT(e.cfg.CrossVenueExecutor, crossVenuePositionNotionalUSDT(pos), 2), crossVenuePositionNotionalUSDT(pos))
	}
	futureEV, capEff, riskAdj, exitCost, switchCost, comparable, riskScore := crossVenueComputeCurrentFutureEV(e.cfg.CrossVenueExecutor, pos)
	pos.CurrentFutureEV = futureEV
	pos.CurrentCapitalEfficiencyEV = capEff
	pos.CurrentRiskAdjustedEVPerHour = riskAdj
	pos.EstimatedExitCost = exitCost
	pos.EstimatedSwitchCost = switchCost
	pos.CurrentComparableWindowHours = comparable
	pos.CurrentRiskScore = riskScore
	if pos.Priority == 0 {
		pos.Priority = pos.RankScore
	}
	if pos.TransitionState == "" {
		pos.TransitionState = "hold"
	}
	if pos.FundingTruthStatus == "" {
		if pos.RealizedFundingComplete {
			pos.FundingTruthStatus = "complete"
		} else {
			pos.FundingTruthStatus = "estimated"
		}
	}
}

func (e *CrossVenueExecutor) shouldPollRealizedFunding(pos *CrossVenuePosition) bool {
	cfg := e.cfg.CrossVenueExecutor
	pollMins := cfg.RealizedFundingPollMinutes
	if pollMins <= 0 {
		pollMins = 5
	}
	now := e.nowFn()
	last := pos.LastRealizedFundingAt
	if last.IsZero() {
		return true
	}
	elapsed := now.Sub(last)
	if elapsed >= time.Duration(pollMins)*time.Minute {
		return true
	}
	sym := venue.Symbol(pos.Symbol)
	for _, vname := range []string{pos.HighVenue, pos.LowVenue} {
		entry, ok := e.fundingFromScanner(vname, sym)
		if !ok {
			continue
		}
		intervalH := entry.IntervalHours
		if intervalH <= 0 {
			intervalH = 8
		}
		if elapsed >= time.Duration(intervalH)*time.Hour {
			return true
		}
	}
	return false
}

func (e *CrossVenueExecutor) fetchLegRealizedFunding(ctx context.Context, symbol, venueName string, sym venue.Symbol, since time.Time) (float64, bool) {
	v := e.venues[venueName]
	if v == nil {
		return 0, false
	}
	incomes, err := v.FetchRealizedFunding(ctx, sym, since)
	if err != nil {
		e.logRealFundingFetchFailed(symbol, venueName, err)
		return 0, false
	}
	total := 0.0
	for _, in := range incomes {
		total += in.Income
	}
	return total, true
}

const crossVenueRealFundingLogThrottle = 30 * time.Minute

func (e *CrossVenueExecutor) logRealFundingFetchFailed(symbol, venueName string, err error) {
	if e.logger == nil {
		return
	}
	key := symbol + "|" + venueName
	now := e.nowFn()
	e.mu.Lock()
	if e.realFundingLogLast == nil {
		e.realFundingLogLast = make(map[string]time.Time)
	}
	last := e.realFundingLogLast[key]
	if !last.IsZero() && now.Sub(last) < crossVenueRealFundingLogThrottle {
		e.mu.Unlock()
		return
	}
	e.realFundingLogLast[key] = now
	e.mu.Unlock()
	e.logger.Printf("[跨所] %s venue=%s 真实资费拉取失败(保留上次值): %v", symbol, venueName, err)
}

// crossVenueNetRealizedFundingUSDT 返回两腿真实已结算资费净值(不含轮间估算)。
func crossVenueNetRealizedFundingUSDT(pos *CrossVenuePosition) float64 {
	if pos == nil {
		return 0
	}
	return pos.HighLegFundingUSDT + pos.LowLegFundingUSDT
}

func crossVenuePositionNotionalUSDT(pos *CrossVenuePosition) float64 {
	if pos == nil {
		return 0
	}
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.HighLeg.Qty * pos.HighLeg.EntryPrice
	}
	return notional
}

// crossVenueEstCloseFeeUSDT 估算平仓两腿吃单手续费(高所+低所各一次),供面板展示。
func crossVenueEstCloseFeeUSDT(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition) float64 {
	notional := crossVenuePositionNotionalUSDT(pos)
	if notional <= 0 {
		return 0
	}
	cost := 0.0
	for _, legVenue := range []string{pos.HighVenue, pos.LowVenue} {
		cost += notional * crossVenueTakerFeeBps(cfg, legVenue) / 10000
	}
	return cost
}

// crossVenueRoundTripFeeUSDT 估算四腿往返吃单手续费(开+平 × 高/低所),供最小持有保护判断。
func crossVenueRoundTripFeeUSDT(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition) float64 {
	notional := crossVenuePositionNotionalUSDT(pos)
	if notional <= 0 {
		return 0
	}
	cost := 0.0
	for _, legVenue := range []string{pos.HighVenue, pos.LowVenue, pos.HighVenue, pos.LowVenue} {
		cost += notional * crossVenueTakerFeeBps(cfg, legVenue) / 10000
	}
	return cost
}

// crossVenueMinHoldBlocksSoftClose 最小持有保护:未满 MinHoldHours 且真实资费未覆盖往返手续费时不因软价差回落平仓。
func (e *CrossVenueExecutor) crossVenueMinHoldBlocksSoftClose(pos *CrossVenuePosition) bool {
	if pos == nil {
		return false
	}
	cfg := e.cfg.CrossVenueExecutor
	minHold := cfg.MinHoldHours
	if minHold <= 0 {
		minHold = 24
	}
	holdHours := e.nowFn().Sub(pos.OpenedAt).Hours()
	if holdHours >= minHold {
		return false
	}
	realized := crossVenueNetRealizedFundingUSDT(pos)
	required := crossVenueRoundTripFeeUSDT(cfg, pos)
	return realized < required
}

func (e *CrossVenueExecutor) shouldCloseCrossVenue(ctx context.Context, pos *CrossVenuePosition) string {
	if pos == nil {
		return ""
	}
	cfg := e.cfg.CrossVenueExecutor
	now := e.nowFn()
	e.refreshCurrentPositionMetrics(pos)
	maxHold := time.Duration(cfg.MaxHoldHours) * time.Hour
	if maxHold <= 0 {
		maxHold = 72 * time.Hour
	}
	if now.Sub(pos.OpenedAt) > maxHold {
		return fmt.Sprintf("持仓超过 %dh", cfg.MaxHoldHours)
	}
	if pos.FreshnessAgeSeconds > crossVenueMaxCompareAgeSeconds(cfg, e.cfg.CrossVenueScanner.MaxSnapshotAgeSeconds) {
		return ""
	}
	if !crossVenueCompareAllowed(cfg, pos, now) {
		return ""
	}
	if pos.CurrentComparableWindowHours > 0 && pos.CurrentComparableWindowHours < crossVenueMinComparableWindowHours(cfg) {
		return ""
	}
	if pos.CurrentComparableWindowHours >= crossVenueMinComparableWindowHours(cfg) && pos.CurrentFutureEV <= 0 {
		return fmt.Sprintf("未来净值<=0(%.4f)", pos.CurrentFutureEV)
	}
	if reason := e.crossVenueSwitchCloseReason(pos); reason != "" {
		return reason
	}

	spread := e.currentSpreadAPR(ctx, pos)
	need := cfg.CloseSpreadConsecutiveRounds
	if need <= 0 {
		need = 3
	}

	hardNegAPR := cfg.CloseHardNegativeSpreadAPR
	if hardNegAPR == 0 {
		hardNegAPR = -0.5
	}
	if spread < hardNegAPR {
		pos.CloseHardNegativeSpreadStreak++
		if pos.CloseHardNegativeSpreadStreak >= need {
			return fmt.Sprintf("价差深度转负 %.1f%% 连续 %d 轮低于 %.1f%%(硬止损)",
				spread*100, pos.CloseHardNegativeSpreadStreak, hardNegAPR*100)
		}
		_ = e.persistPosition(*pos)
	} else if pos.CloseHardNegativeSpreadStreak > 0 {
		pos.CloseHardNegativeSpreadStreak = 0
		_ = e.persistPosition(*pos)
	}

	closeAPR := cfg.CloseSpreadAPR
	if closeAPR <= 0 {
		closeAPR = 0.3
	}
	if spread < closeAPR {
		pos.CloseSpreadStreak++
		if pos.CloseSpreadStreak >= need {
			if e.crossVenueMinHoldBlocksSoftClose(pos) {
				realized := crossVenueNetRealizedFundingUSDT(pos)
				required := crossVenueRoundTripFeeUSDT(cfg, pos)
				holdHours := now.Sub(pos.OpenedAt).Hours()
				minHold := cfg.MinHoldHours
				if minHold <= 0 {
					minHold = 24
				}
				if e.logger != nil {
					e.logger.Printf("[跨所] %s 价差回落满足平仓条件但最小持有保护中: hold=%.1fh/%.0fh spread=%.1f%% 已收资费=%.4f 需覆盖手续费=%.4f",
						pos.Symbol, holdHours, minHold, spread*100, realized, required)
				}
				_ = e.persistPosition(*pos)
				return ""
			}
			return fmt.Sprintf("价差年化 %.1f%% 连续 %d 轮低于 %.1f%%", spread*100, pos.CloseSpreadStreak, closeAPR*100)
		}
		_ = e.persistPosition(*pos)
	} else {
		if pos.CloseSpreadStreak > 0 {
			pos.CloseSpreadStreak = 0
			_ = e.persistPosition(*pos)
		}
	}
	return ""
}

func crossVenueSwitchBufferUSDT(cfg CrossVenueExecutorConfig) float64 {
	if cfg.SwitchBufferUSDT > 0 {
		return cfg.SwitchBufferUSDT
	}
	return 0.5
}

func crossVenueSwitchCooldown(cfg CrossVenueExecutorConfig) time.Duration {
	if cfg.SwitchCooldownSeconds > 0 {
		return time.Duration(cfg.SwitchCooldownSeconds) * time.Second
	}
	return 5 * time.Minute
}

func crossVenueActiveSwitchCount(ps []CrossVenuePosition) int {
	n := 0
	for _, p := range activeCrossVenuePositions(ps) {
		if crossVenueHasPendingSwitch(p) || p.TransitionState == "switch_pending" || p.TransitionState == "partial_switch" {
			n++
		}
	}
	return n
}

func crossVenuePostFundingCooldown(cfg CrossVenueExecutorConfig) time.Duration {
	if cfg.PostFundingCooldownSeconds > 0 {
		return time.Duration(cfg.PostFundingCooldownSeconds) * time.Second
	}
	return 3 * time.Minute
}

func (e *CrossVenueExecutor) crossVenueSwitchCloseReason(pos *CrossVenuePosition) string {
	if e == nil || pos == nil || e.scanner == nil {
		return ""
	}
	// 统一 runner 接管跨策略换仓;仅保留已在途 staged/pending switch 续跑,不再主动触发内部换对。
	if e.RotationManaged() && !crossVenueHasPendingSwitch(*pos) {
		return ""
	}
	now := e.nowFn()
	if crossVenueHasPendingSwitch(*pos) {
		if crossVenueSwitchSequenceNeedsTargetFirst(pos.PendingSwitchSequence) {
			if crossVenueHasActivePositionTarget(e.snapshotPositions(), pos.Symbol, pos.PendingSwitchHighVenue, pos.PendingSwitchLowVenue, crossVenuePositionKey(*pos)) {
				return nonEmpty(pos.PendingSwitchReason, "open_then_close target live")
			}
			return ""
		}
		return nonEmpty(pos.PendingSwitchReason, "pending switch")
	}
	if !pos.SwitchCooldownUntil.IsZero() && now.Before(pos.SwitchCooldownUntil) {
		return ""
	}
	cfg := e.cfg.CrossVenueExecutor
	if cfg.MinHoldHours > 0 && now.Sub(pos.OpenedAt).Hours() < cfg.MinHoldHours {
		return ""
	}
	if crossVenueActiveSwitchCount(e.snapshotPositions()) >= crossVenueSwitchesAllowedPerCycle(cfg) {
		return ""
	}
	snap := e.scanner.Snapshot()
	if !e.snapshotFreshEnough(snap.ScannedAt) {
		return ""
	}
	buffer := crossVenueSwitchBufferUSDT(cfg)
	bestGain := 0.0
	best := CrossVenueOpportunity{}
	for _, raw := range snap.Opportunities {
		opp := e.enrichOpportunityFromSnapshot(raw, snap, now)
		if opp.Symbol == pos.Symbol && opp.HighVenue == pos.HighVenue && opp.LowVenue == pos.LowVenue {
			continue
		}
		if opp.NetEV <= 0 {
			continue
		}
		if opp.ComparableWindowHours < crossVenueMinComparableWindowHours(cfg) {
			continue
		}
		if crossVenuePostFundingCooldownActive(now, opp.HighNextFundingTime, opp.HighIntervalHours, crossVenuePostFundingCooldown(cfg)) {
			continue
		}
		if crossVenuePostFundingCooldownActive(now, opp.LowNextFundingTime, opp.LowIntervalHours, crossVenuePostFundingCooldown(cfg)) {
			continue
		}
		gain := opp.NetEV - pos.CurrentFutureEV - pos.EstimatedSwitchCost
		if gain > bestGain {
			bestGain = gain
			best = opp
		}
	}
	if bestGain <= buffer {
		return ""
	}
	sequence := crossVenueChooseSwitchSequence(cfg, pos, best)
	reason := fmt.Sprintf("更优候选触发换仓 %s/%s gain=%.4f new_ev=%.4f old_ev=%.4f", best.HighVenue, best.LowVenue, bestGain, best.NetEV, pos.CurrentFutureEV)
	pos.PendingSwitchDecisionCycleID = crossVenueDecisionCycleID(now)
	pos.PendingSwitchSnapshotID = best.SnapshotID
	pos.PendingSwitchHighVenue = best.HighVenue
	pos.PendingSwitchLowVenue = best.LowVenue
	pos.PendingSwitchSequence = sequence
	pos.PendingSwitchReason = reason
	pos.PendingSwitchExpectedGainUSDT = bestGain
	pos.PartialSwitchPhase = "pending"
	pos.SwitchCooldownUntil = now.Add(crossVenueSwitchCooldown(cfg))
	pos.ChurnStreak++
	pos.TransitionState = "switch_pending"
	pos.Priority = best.RankScore
	_ = e.upsertSwitchIntent(crossVenueSwitchIntent{
		Symbol:             pos.Symbol,
		FromHighVenue:      pos.HighVenue,
		FromLowVenue:       pos.LowVenue,
		TargetHighVenue:    best.HighVenue,
		TargetLowVenue:     best.LowVenue,
		DecisionCycleID:    pos.PendingSwitchDecisionCycleID,
		SnapshotID:         best.SnapshotID,
		SwitchSequence:     sequence,
		PartialSwitchPhase: "pending",
		Reason:             reason,
		ExpectedGainUSDT:   bestGain,
		TargetNetEV:        best.NetEV,
		TargetRankScore:    best.RankScore,
		TargetNotionalUSDT: best.TradableNotional,
		TargetSizeMult:     best.SizeMultiplier,
		CreatedAt:          now,
		ExpiresAt:          crossVenueSwitchIntentExpiry(now),
	})
	_ = e.persistPosition(*pos)
	if crossVenueSwitchSequenceNeedsTargetFirst(sequence) {
		if crossVenueIsStagedSwitch(sequence) {
			pos.PartialSwitchPhase = "pending"
		}
		return ""
	}
	return reason
}

// currentSpreadAPR 从扫描器缓存(最旧 10 分钟)计算当前跨所年化价差;缓存缺失沿用降级语义返回 0,
// 绝不现场拉全量快照。
func (e *CrossVenueExecutor) currentSpreadAPR(_ context.Context, pos *CrossVenuePosition) float64 {
	if pos == nil {
		return 0
	}
	sym := venue.Symbol(pos.Symbol)
	hEntry, okH := e.fundingFromScanner(pos.HighVenue, sym)
	lEntry, okL := e.fundingFromScanner(pos.LowVenue, sym)
	if !okH || !okL {
		return 0
	}
	hAPR := fundingAnnualizedAPR(hEntry.Rate, hEntry.IntervalHours)
	lAPR := fundingAnnualizedAPR(lEntry.Rate, lEntry.IntervalHours)
	return hAPR - lAPR
}

func isDeadManUnsupportedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, venue.ErrUnsupported) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "capability unsupported") || strings.Contains(msg, "unsupported")
}

func (e *CrossVenueExecutor) armDeadMansForLegs(ctx context.Context, pos CrossVenuePosition, live bool) {
	armDeadMansForVenueNames(
		ctx, e.cfg, e.venues, e.logger, e.nowFn,
		&e.mu, e.deadManUnsupportedVenues, e.deadManLastArm,
		[]string{pos.HighVenue, pos.LowVenue}, live,
	)
}

// armDeadMansForVenueNames 为指定 venue 续期 dead-man(账户级 cancel-all-after);不支持的所静默跳过。
func armDeadMansForVenueNames(
	ctx context.Context,
	cfg EngineConfig,
	venues map[string]venue.Venue,
	logger *log.Logger,
	nowFn func() time.Time,
	mu *sync.RWMutex,
	deadManUnsupportedVenues map[string]struct{},
	deadManLastArm map[string]time.Time,
	venueNames []string,
	live bool,
) {
	if !live {
		return
	}
	cd := time.Duration(cfg.CrossVenueExecutor.DeadManCountdownSeconds) * time.Second
	if cd <= 0 {
		cd = 60 * time.Second
	}
	now := time.Now()
	if nowFn != nil {
		now = nowFn()
	}
	for _, name := range venueNames {
		if strings.TrimSpace(name) == "" {
			continue
		}
		v := venues[name]
		if v == nil || !v.Capabilities().HasNativeDeadMan {
			continue
		}
		mu.RLock()
		_, skip := deadManUnsupportedVenues[name]
		lastArm := deadManLastArm[name]
		mu.RUnlock()
		if skip {
			continue
		}
		// 按所节流:一次成功续期已重置整账户倒计时(cd),同周期其余同所腿无需重复(否则撞 OKX 1次/2秒限频)。
		// 阈值取 cd/2,保证每个管理周期(≈cd)仍会续期一次、不让倒计时逼近到期。
		if !lastArm.IsZero() && now.Sub(lastArm) < cd/2 {
			continue
		}
		if err := v.ArmDeadMansSwitch(ctx, cd); err != nil {
			if isDeadManUnsupportedErr(err) {
				mu.Lock()
				deadManUnsupportedVenues[name] = struct{}{}
				mu.Unlock()
				continue
			}
			if logger != nil {
				logger.Printf("[dead-man] 续期失败 venue=%s: %v", name, err)
			}
			continue
		}
		mu.Lock()
		if deadManLastArm == nil {
			deadManLastArm = make(map[string]time.Time)
		}
		deadManLastArm[name] = now
		mu.Unlock()
	}
}
