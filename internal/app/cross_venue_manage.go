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

const (
	crossVenueHedgeDeviationThreshold        = 0.05
	crossVenueRebalanceFailCriticalThreshold = 3
)

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

// crossVenueLegEconomicQty 把单腿 native 持仓量按其下单 symbol 解析出的计价倍数归一为经济币量,
// 用于跨计价两腿(如 1000PEPE vs PEPE)偏差比较;同计价(倍数相同)时归一不改变偏差结果。
// 复用既有 crossVenueEconomicBase 解析倍数(10 的整数次幂白名单),不新造倍数表。
func crossVenueLegEconomicQty(legSym venue.Symbol, nativeQty float64) float64 {
	mult := 1.0
	if _, m, ok := crossVenueEconomicBase(string(legSym)); ok && m > 0 {
		mult = m
	}
	return absQty(nativeQty) * mult
}

func (e *CrossVenueExecutor) manageCrossVenuePosition(ctx context.Context, pos CrossVenuePosition, live bool) (CrossVenuePosition, bool) {
	if !live {
		if reason := e.shouldCloseCrossVenue(ctx, &pos); reason != "" {
			logDryRunClose(e.logger, "跨所perp-perp", pos.Symbol, reason)
		}
		return pos, false
	}
	// 两腿可能跨计价(如 binance 1000PEPE-USDT vs gate PEPE-USDT),必须各用自己的下单 symbol,
	// 否则 perpPositionQty 用错 symbol 会把在场腿误判为 0 → 误孤腿/误平仓。
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
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

	highAmt := perpPositionQty(highPos, highSym)
	lowAmt := perpPositionQty(lowPos, lowSym)
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

	// 同步性核验:跨计价时按经济币量(native×倍数)归一后再比偏差,避免 1000x 计价差被误判为巨大偏差。
	dev := qtyDeviation(crossVenueLegEconomicQty(highSym, highAmt), crossVenueLegEconomicQty(lowSym, lowAmt))
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
		if venueSymbolsMatch(p.Symbol, highSym) && estimateLiqDistance(p) < minDist {
			if rerr := e.reduceCrossVenueRisk(ctx, pos, live); rerr != nil {
				e.logger.Printf("[跨所] %s 强平距离不足降风险失败(高费腿触发): %v", pos.Symbol, rerr)
			}
			return pos, false
		}
	}
	for _, p := range lowPos {
		if venueSymbolsMatch(p.Symbol, lowSym) && estimateLiqDistance(p) < minDist {
			if rerr := e.reduceCrossVenueRisk(ctx, pos, live); rerr != nil {
				e.logger.Printf("[跨所] %s 强平距离不足降风险失败(低费腿触发): %v", pos.Symbol, rerr)
			}
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
	lowRes, err := e.placeCrossVenueLegExec(ctx, lowTrader, sym, venue.SideBuy, hedgeQty, lowID, false, live, true, crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor), now.Add(deadlineBudget), lowInstr, lowCaps.ContractBasedPerp)
	if orderStatusIsUnknown(lowRes) || isUnknownOrderStateError(err) {
		// 续做第二腿查单未知:以交易所真实持仓判定地面真值,绝不仅告警了事。
		lowSymTruth := venue.Symbol(crossVenuePositionLowSymbol(pos))
		lowPositions, lperr := lowV.FetchPositions(ctx)
		if lperr != nil {
			// 无法判定 → HALT 新开,保留 opening_second_leg 待下轮续做核验。
			pos.Status = CrossVenueStatusOpeningSecondLeg
			pos.TransitionState = "open"
			pos.RecoveryAction = crossVenueRecoveryActionObserve
			pos.PartialSwitchPhase = "open_new_second_leg"
			pos.UpdatedAt = now
			_ = e.persistPosition(pos)
			_ = e.SetOpeningsEnabled(false)
			e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第二腿续做查单失败且拉持仓核验失败,已 HALT 新开待复核", pos.Symbol), pos.Symbol)
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_second_leg", "runtime continuation second leg unknown + positions fetch failed, halting opens")
			}
			return pos, true
		}
		if absQty(perpPositionQty(lowPositions, lowSymTruth)) <= executionQtyTolerance {
			// 交易所确认第二腿未成交:保留 opening_second_leg,下轮续做重试(不平掉在场的第一腿,完成对冲优先)。
			pos.Status = CrossVenueStatusOpeningSecondLeg
			pos.TransitionState = "open"
			pos.RecoveryAction = crossVenueRecoveryActionObserve
			pos.PartialSwitchPhase = "open_new_second_leg"
			pos.UpdatedAt = now
			_ = e.persistPosition(pos)
			e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第二腿续做查单失败但交易所确认未成交,保留续做重试", pos.Symbol), pos.Symbol)
			if hasIntent {
				_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new_second_leg", "runtime continuation second leg unknown, awaiting retry")
			}
			return pos, true
		}
		// 交易所确认第二腿已成交 → 补全成交结果,转入下方核验收尾(双腿在场,不回滚)。
		err = nil
		if lowRes.ExecutedQty <= executionQtyTolerance {
			lowRes.ExecutedQty = absQty(perpPositionQty(lowPositions, lowSymTruth))
		}
		lowRes.Symbol = lowSymTruth
		e.notifyWarn("跨所查单未知", fmt.Sprintf("%s 第二腿续做查单未知但交易所确认已成交,转核验收尾", pos.Symbol), pos.Symbol)
	}
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
	openPlan := e.crossVenueOpenPlanFromPosition(ctx, pos)
	highQty, lowQty, imbalanceQty, verifyErr := e.verifyAndFinalizeCrossVenueOpen(ctx, &pos, openPlan, live)
	if verifyErr != nil {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.PartialSwitchPhase = "rollback_new"
		pos.CloseReason = nonEmpty(pos.CloseReason, "open_verify_failed")
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		if hasIntent {
			_ = e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "rollback_new", "runtime continuation open verify failed")
		}
		e.notifyCritical("跨所开仓核验失败", fmt.Sprintf("%s 续做第二腿后核验失败: %v", pos.Symbol, verifyErr), pos.Symbol)
		return pos, true
	}
	pos.HighLeg.Qty = highQty
	pos.LowLeg.Qty = lowQty
	pos.LegImbalanceQty = imbalanceQty
	pos.LowLeg.EntryPrice = lowRes.AvgPrice
	pos.LowLeg.OrderIDs = append(pos.LowLeg.OrderIDs, lowID)
	if pos.NotionalUSDT <= 0 {
		refPrice := pos.HighLeg.EntryPrice
		if refPrice <= 0 {
			refPrice = lowRes.AvgPrice
		}
		pos.NotionalUSDT = math.Min(highQty, lowQty) * refPrice
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
	// 跨计价时两腿下单 symbol 不同,平孤腿必须用该腿自己的 symbol/合约口径。
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	orphDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	flattened := false
	if highOpen && !lowOpen {
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(highAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, highSym)
			highInstr := e.lookupPerpInstrument(ctx, pos.HighVenue, highSym)
			if live {
				res, err := e.placeCrossVenueLegExec(ctx, tr, highSym, venue.SideBuy, qty, crossVenueLegClientID(pos.IdemPrefix, "oh"), true, live, true, takerBps, orphDeadline, highInstr, e.venues[pos.HighVenue].Capabilities().ContractBasedPerp)
				if res.ExecutedQty > executionQtyTolerance {
					e.recordCrossVenueExecution(ctx, &pos, cvExecOrphanCloseHigh, res, crossVenueBookMid(book))
					_ = e.persistPosition(pos)
					flattened = true
				} else if err != nil && e.logger != nil {
					e.logger.Printf("[跨所] %s 孤腿平高费腿失败: %v", pos.Symbol, err)
				}
			}
		}
	} else if lowOpen && !highOpen {
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(lowAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, lowSym)
			lowInstr := e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym)
			if live {
				res, err := e.placeCrossVenueLegExec(ctx, tr, lowSym, venue.SideSell, qty, crossVenueLegClientID(pos.IdemPrefix, "ol"), true, live, true, takerBps, orphDeadline, lowInstr, e.venues[pos.LowVenue].Capabilities().ContractBasedPerp)
				if res.ExecutedQty > executionQtyTolerance {
					e.recordCrossVenueExecution(ctx, &pos, cvExecOrphanCloseLow, res, crossVenueBookMid(book))
					_ = e.persistPosition(pos)
					flattened = true
				} else if err != nil && e.logger != nil {
					e.logger.Printf("[跨所] %s 孤腿平低费腿失败: %v", pos.Symbol, err)
				}
			}
		}
	}
	if flattened {
		e.notifyCritical("跨所孤腿", fmt.Sprintf("%s 检测到孤腿,已平掉剩余腿", pos.Symbol), pos.Symbol)
	} else {
		e.notifyCritical("跨所孤腿", fmt.Sprintf("%s 检测到孤腿,平掉剩余腿未成功", pos.Symbol), pos.Symbol)
	}
}

func (e *CrossVenueExecutor) recordRebalanceFailure(pos *CrossVenuePosition, sym string, err error) {
	if pos == nil || err == nil {
		return
	}
	pos.RebalanceFailStreak++
	title := "跨所再平衡失败"
	detail := fmt.Sprintf("%s 对冲再平衡失败(连续 %d 次): %v", pos.Symbol, pos.RebalanceFailStreak, err)
	if pos.RebalanceFailStreak >= crossVenueRebalanceFailCriticalThreshold {
		title = "跨所再平衡持续失败"
		detail = fmt.Sprintf("%s 已连续 %d 次再平衡失败,交易所仍偏差: %v", pos.Symbol, pos.RebalanceFailStreak, err)
	}
	e.notifyCritical(title, detail, sym)
}

func (e *CrossVenueExecutor) syncCrossVenueBookLegQtysFromExchange(ctx context.Context, pos *CrossVenuePosition, highSym, lowSym venue.Symbol) {
	if pos == nil {
		return
	}
	if highV := e.venues[pos.HighVenue]; highV != nil {
		if hp, err := highV.FetchPositions(ctx); err == nil {
			pos.HighLeg.Qty = absQty(perpPositionQty(hp, highSym))
		}
	}
	if lowV := e.venues[pos.LowVenue]; lowV != nil {
		if lp, err := lowV.FetchPositions(ctx); err == nil {
			pos.LowLeg.Qty = absQty(perpPositionQty(lp, lowSym))
		}
	}
}

func (e *CrossVenueExecutor) rebalanceHedge(ctx context.Context, pos CrossVenuePosition, highAmt, lowAmt float64, live bool) {
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	hAbs := absQty(highAmt)
	lAbs := absQty(lowAmt)
	if qtyDeviation(hAbs, lAbs) <= crossVenueHedgeDeviationThreshold {
		return
	}
	if !live {
		return
	}
	rebalDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	sym := venue.Symbol(pos.Symbol)

	rebalanced := false
	if hAbs > lAbs {
		highInstr := e.lookupPerpInstrument(ctx, pos.HighVenue, highSym)
		delta := normalizeVenueQty(hAbs-lAbs, highInstr, e.venues[pos.HighVenue].Capabilities().ContractBasedPerp)
		if delta <= executionQtyTolerance {
			e.recordRebalanceFailure(&pos, string(sym), fmt.Errorf("rebalance delta quantized to zero (high=%.6f low=%.6f)", hAbs, lAbs))
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return
		}
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, highSym)
			res, err := e.placeCrossVenueLegExec(ctx, tr, highSym, venue.SideBuy, delta, crossVenueLegClientID(pos.IdemPrefix, "bh"), true, live, true, takerBps, rebalDeadline, highInstr, e.venues[pos.HighVenue].Capabilities().ContractBasedPerp)
			if err != nil || res.ExecutedQty <= executionQtyTolerance {
				rawErr := err
				if rawErr == nil {
					rawErr = fmt.Errorf("executed_qty=%.6f", res.ExecutedQty)
				}
				e.recordRebalanceFailure(&pos, string(sym), rawErr)
				pos.UpdatedAt = e.nowFn()
				_ = e.persistPosition(pos)
				return
			}
			e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceHigh, res, crossVenueBookMid(book))
			rebalanced = true
		}
	} else {
		lowInstr := e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym)
		delta := normalizeVenueQty(lAbs-hAbs, lowInstr, e.venues[pos.LowVenue].Capabilities().ContractBasedPerp)
		if delta <= executionQtyTolerance {
			e.recordRebalanceFailure(&pos, string(sym), fmt.Errorf("rebalance delta quantized to zero (high=%.6f low=%.6f)", hAbs, lAbs))
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return
		}
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, lowSym)
			res, err := e.placeCrossVenueLegExec(ctx, tr, lowSym, venue.SideSell, delta, crossVenueLegClientID(pos.IdemPrefix, "bl"), true, live, true, takerBps, rebalDeadline, lowInstr, e.venues[pos.LowVenue].Capabilities().ContractBasedPerp)
			if err != nil || res.ExecutedQty <= executionQtyTolerance {
				rawErr := err
				if rawErr == nil {
					rawErr = fmt.Errorf("executed_qty=%.6f", res.ExecutedQty)
				}
				e.recordRebalanceFailure(&pos, string(sym), rawErr)
				pos.UpdatedAt = e.nowFn()
				_ = e.persistPosition(pos)
				return
			}
			e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceLow, res, crossVenueBookMid(book))
			rebalanced = true
		}
	}
	if rebalanced {
		e.syncCrossVenueBookLegQtysFromExchange(ctx, &pos, highSym, lowSym)
		pos.RebalanceFailStreak = 0
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
	futureEV, capEff, riskAdj, exitCost, switchCost, comparable, riskScore := crossVenueComputeCurrentFutureEV(e.cfg.CrossVenueExecutor, pos, now)
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

// crossVenueEffectiveFundingUSDT 最小持有/软平仓经济学用资费:优先真实值;FetchRealizedFunding 不可用时用 EstFundingUSDT。
func crossVenueEffectiveFundingUSDT(pos *CrossVenuePosition) float64 {
	if pos == nil {
		return 0
	}
	realized := crossVenueNetRealizedFundingUSDT(pos)
	switch strings.TrimSpace(pos.FundingTruthStatus) {
	case "complete", "partial":
		return realized
	}
	if pos.RealizedFundingComplete && realized != 0 {
		return realized
	}
	if pos.EstFundingUSDT != 0 {
		return pos.EstFundingUSDT
	}
	return realized
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
	realized := crossVenueEffectiveFundingUSDT(pos)
	required := crossVenueRoundTripFeeUSDT(cfg, pos)
	return realized < required
}

// crossVenueAdverseBasisPnLUSDT 计算开仓以来两腿基差走阔造成的不利 PnL(USDT,>0=净亏)。
// 用 per-leg 相对收益度量(免合约乘数差异):空高腿不利=high 涨,多低腿不利=low 跌。
func crossVenueAdverseBasisPnLUSDT(notional, entryHigh, highMid, entryLow, lowMid float64) float64 {
	if notional <= 0 || entryHigh <= 0 || entryLow <= 0 || highMid <= 0 || lowMid <= 0 {
		return 0
	}
	highRet := (highMid - entryHigh) / entryHigh
	lowRet := (lowMid - entryLow) / entryLow
	return notional * (highRet - lowRet)
}

// crossVenueLegMid 取某腿当前 perp 中间价(book ticker 优先,回退 depth);不可用返回 0。
func (e *CrossVenueExecutor) crossVenueLegMid(ctx context.Context, venueName, sym string) float64 {
	if e == nil || ctx == nil {
		return 0
	}
	v := e.venues[venueName]
	if v == nil {
		return 0
	}
	return crossVenuePerpPerpMidFromDepthOrBook(ctx, v, venue.Symbol(sym))
}

// crossVenueBasisStopCloseReason 统一捕获模型的基差止损(真正的风控):跟踪开仓以来两腿基差走阔造成的不利 PnL,
// 超过 BasisBudgetUSDT(= 系数 × 预期目标资费)即平,把单笔最大亏损钉死在"原本要赚的资费"量级。
// 用相对收益(per-leg 自身入场价归一)度量,免受合约乘数差异影响:
//
//	空高腿不利 = high 上涨;多低腿不利 = low 下跌;不利基差 PnL = notional×(highRet − lowRet),>0 即净亏。
func (e *CrossVenueExecutor) crossVenueBasisStopCloseReason(ctx context.Context, pos *CrossVenuePosition, now time.Time) string {
	if e == nil || pos == nil || pos.CapturePlan == nil {
		return ""
	}
	budget := pos.CapturePlan.BasisBudgetUSDT
	if budget <= 0 {
		return ""
	}
	entryHigh := pos.HighLeg.EntryPrice
	entryLow := pos.LowLeg.EntryPrice
	if entryHigh <= 0 || entryLow <= 0 {
		return ""
	}
	notional := crossVenuePositionNotionalUSDT(pos)
	if notional <= 0 {
		return ""
	}
	highMid := e.crossVenueLegMid(ctx, pos.HighVenue, crossVenuePositionHighSymbol(*pos))
	lowMid := e.crossVenueLegMid(ctx, pos.LowVenue, crossVenuePositionLowSymbol(*pos))
	if highMid <= 0 || lowMid <= 0 {
		return ""
	}
	adverse := crossVenueAdverseBasisPnLUSDT(notional, entryHigh, highMid, entryLow, lowMid) // >0 = 基差走阔致亏(不利)
	if adverse > pos.CapturePlan.PeakAdverseBasisUSDT {
		pos.CapturePlan.PeakAdverseBasisUSDT = adverse
		_ = e.persistPosition(*pos)
	}
	if adverse > budget {
		return fmt.Sprintf("基差止损:累计不利基差 %.4f USDT 超过预算 %.4f USDT", adverse, budget)
	}
	return ""
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
		return e.crossVenueFilterVoluntaryCloseReason(pos, fmt.Sprintf("持仓超过 %dh", cfg.MaxHoldHours))
	}
	unified := cfg.UnifiedCaptureModelEnabled && pos.CapturePlan != nil
	if unified {
		// 统一捕获模型主离场:持有到计划离场时刻(目标结算 + 缓冲)。纯时间驱动,先于快照新鲜度短路检查,确保到点必离。
		if reason := crossVenuePlannedExitCloseReason(pos, now); reason != "" {
			return e.crossVenueFilterVoluntaryCloseReason(pos, reason)
		}
		// 基差止损(统一模型真正的风控):累计不利基差 PnL 超过预算即平。也先于新鲜度检查,基差跟踪用持仓内最新价。
		if reason := e.crossVenueBasisStopCloseReason(ctx, pos, now); reason != "" {
			return e.crossVenueFilterVoluntaryCloseReason(pos, reason)
		}
	}
	if pos.FreshnessAgeSeconds > crossVenueMaxCompareAgeSeconds(cfg, e.cfg.CrossVenueScanner.MaxSnapshotAgeSeconds) {
		return ""
	}
	if reason := crossVenuePreFundingAdverseCloseReason(cfg, pos, now); reason != "" {
		return e.crossVenueFilterVoluntaryCloseReason(pos, reason)
	}
	if !crossVenueCompareAllowed(cfg, pos, now) {
		return ""
	}
	if pos.CurrentComparableWindowHours > 0 && pos.CurrentComparableWindowHours < crossVenueMinComparableWindowHours(cfg) {
		return ""
	}
	if !unified && pos.CurrentComparableWindowHours >= crossVenueMinComparableWindowHours(cfg) && pos.CurrentFutureEV <= 0 {
		// 统一模型下退化为 backstop(默认关):FutureEV<=0 不再触发离场,改由计划离场 + 基差止损治理。
		if e.crossVenueMinHoldBlocksSoftClose(pos) {
			_ = e.persistPosition(*pos)
			return ""
		}
		return e.crossVenueFilterVoluntaryCloseReason(pos, fmt.Sprintf("未来净值<=0(%.4f)", pos.CurrentFutureEV))
	}
	if reason := e.crossVenueSwitchCloseReason(ctx, pos); reason != "" {
		return e.crossVenueFilterVoluntaryCloseReason(pos, reason)
	}

	if unified {
		// 统一模型:资费价差(spread)硬/软止损退化为 backstop(默认关)。价差噪声不再触发离场,
		// 真正风控交给"基差止损"(Phase 4)+ 计划离场。彻底消除"为 okx 开仓却被 bybit 价差噪声平掉"的抖动churn。
		return ""
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
				realized := crossVenueEffectiveFundingUSDT(pos)
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
			return e.crossVenueFilterVoluntaryCloseReason(pos, fmt.Sprintf("价差年化 %.1f%% 连续 %d 轮低于 %.1f%%", spread*100, pos.CloseSpreadStreak, closeAPR*100))
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

func (e *CrossVenueExecutor) crossVenueSwitchTargetOpenGateOK(ctx context.Context, pos *CrossVenuePosition, now time.Time) bool {
	if e == nil || pos == nil || e.scanner == nil {
		return false
	}
	highVenue := strings.TrimSpace(pos.PendingSwitchHighVenue)
	lowVenue := strings.TrimSpace(pos.PendingSwitchLowVenue)
	if highVenue == "" || lowVenue == "" {
		return false
	}
	snap := e.scanner.Snapshot()
	if !e.snapshotFreshEnough(snap.ScannedAt) {
		return false
	}
	var target CrossVenueOpportunity
	found := false
	for _, raw := range snap.Opportunities {
		opp := e.enrichOpportunityFromSnapshot(raw, snap, now)
		if opp.Symbol == pos.Symbol && opp.HighVenue == highVenue && opp.LowVenue == lowVenue {
			target = opp
			found = true
			break
		}
	}
	if !found {
		return false
	}
	cfg := e.cfg.CrossVenueExecutor
	deps := e.gateDeps(snap)
	recordDir := e.cfg.NetworkConfig.RecordDir
	gateRes := evaluateCrossVenueOpenGate(ctx, cfg, deps, e.venues, crossVenueGateInput{
		Opp:                 target,
		Positions:           e.snapshotPositions(),
		AllowExistingSymbol: true,
		BybitHeld:           func(sym venue.Symbol) bool { return isBybitSymbolHeld(recordDir, sym) },
		BinanceHeld:         func(sym venue.Symbol) bool { return isBinanceSymbolHeld(recordDir, sym) },
		FundingHeld:         func(sym venue.Symbol) bool { return isFundingSymbolHeldAnyVenue(recordDir, sym) },
		HaltActive:          false,
		SymbolCooling:       e.isSymbolCooling,
		Now:                 now,
	})
	return gateRes.OK
}

func (e *CrossVenueExecutor) crossVenueSwitchCloseReason(ctx context.Context, pos *CrossVenuePosition) string {
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
		// close_then_open: 平旧前确认替换目标仍过 open gate(含进场窗),避免平掉旧仓后开新被拒而空仓。
		if !e.crossVenueSwitchTargetOpenGateOK(ctx, pos, now) {
			return ""
		}
		return nonEmpty(pos.PendingSwitchReason, "pending switch")
	}
	if !pos.SwitchCooldownUntil.IsZero() && now.Before(pos.SwitchCooldownUntil) {
		return ""
	}
	cfg := e.cfg.CrossVenueExecutor
	if crossVenueFirstSettlementHoldBlocksVoluntaryClose(pos, cfg, now) {
		return ""
	}
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
