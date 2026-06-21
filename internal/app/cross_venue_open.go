package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

// cross_venue_open.go 跨所开仓/平仓/回滚执行协议。

type crossVenueTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

const crossVenueConfirmTimeout = 8 * time.Second
const crossVenueConfirmPoll = 400 * time.Millisecond

// crossVenueOpenPlan 是通过闸门后的开仓计划。
type crossVenueOpenPlan struct {
	Opp             CrossVenueOpportunity
	Gate            crossVenueGateResult
	Qty             float64
	RefPrice        float64
	IdemPrefix      string
	DecisionCycleID string
	SnapshotID      string
	SnapshotTime    time.Time
	SwitchIntent    *crossVenueSwitchIntent
	SelectionMode   crossVenueOpenSelectionMode
}

func crossVenueIdemPrefix(sym venue.Symbol, highVenue string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("cv-%s-%s-%d", base, highVenue, time.Now().UTC().Unix())
}

func crossVenueLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

func iocLimitPrice(side venue.Side, book venue.BookTicker, slippageBps float64) float64 {
	bps := slippageBps / 10000
	if side == venue.SideBuy {
		return book.AskPrice * (1 + bps)
	}
	return book.BidPrice * (1 - bps)
}

func crossVenueTakerSlippageBps(cfg core.CrossVenueExecutorConfig) float64 {
	return crossVenueResolvedTakerSlippageBps(cfg)
}

func (e *CrossVenueExecutor) confirmCrossVenueFill(ctx context.Context, tr crossVenueTrader, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(crossVenueConfirmTimeout)
	var last venue.OrderResult
	for {
		res, err := tr.QueryOrder(ctx, venue.MarketPerp, sym, clientID)
		if err == nil {
			last = res
			st := strings.ToLower(res.Status)
			if st == "filled" {
				return res, nil
			}
			if st == "canceled" || st == "rejected" {
				return res, fmt.Errorf("订单终态=%s", res.Status)
			}
		}
		if e.nowFn().After(deadline) {
			if last.ExecutedQty > executionQtyTolerance {
				return last, nil
			}
			return last, fmt.Errorf("确认成交超时")
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(crossVenueConfirmPoll):
		}
	}
}

func (e *CrossVenueExecutor) placeCrossVenueIOC(
	ctx context.Context,
	tr crossVenueTrader,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	limitPrice float64,
	clientID string,
	reduceOnly bool,
	live bool,
	instr venue.Instrument,
	contractBased bool,
) (venue.OrderResult, error) {
	if !live {
		return venue.OrderResult{
			Symbol:        sym,
			Market:        venue.MarketPerp,
			ClientOrderID: clientID,
			Status:        "filled",
			ExecutedQty:   qty,
			AvgPrice:      limitPrice,
		}, nil
	}
	limitPrice, qty = quantizeOrderPriceQty(limitPrice, qty, side, instr, contractBased, e.logger, string(sym))
	logLiveOrderLeg(e.logger, traderVenueName(tr), venue.MarketPerp, string(sym), side, limitPrice, instr.TickSize, qty, instr.StepSize)
	res, err := tr.PlaceOrder(ctx, venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        sym,
		Side:          side,
		Type:          venue.OrderTypeIOC,
		Qty:           qty,
		Price:         limitPrice,
		ClientOrderID: clientID,
		ReduceOnly:    reduceOnly,
	})
	if err != nil {
		return res, err
	}
	if res.ExecutedQty <= executionQtyTolerance {
		confirmed, cerr := e.confirmCrossVenueFill(ctx, tr, sym, clientID)
		if cerr == nil && confirmed.ExecutedQty > executionQtyTolerance {
			return confirmed, nil
		}
		return res, fmt.Errorf("IOC 未成交")
	}
	return res, nil
}

// executeCrossVenueOpen 极端所(高费)先腿 → 低费所对冲;第二腿失败立即回滚。
func (e *CrossVenueExecutor) executeCrossVenueOpen(ctx context.Context, plan crossVenueOpenPlan, live bool) (CrossVenuePosition, error) {
	cfg := e.cfg.CrossVenueExecutor
	highSym := venue.Symbol(crossVenueOppHighSymbol(plan.Opp))
	lowSym := venue.Symbol(crossVenueOppLowSymbol(plan.Opp))
	highV := e.venues[plan.Opp.HighVenue]
	lowV := e.venues[plan.Opp.LowVenue]
	highTrader, _ := highV.(crossVenueTrader)
	lowTrader, _ := lowV.(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return CrossVenuePosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	if plan.SwitchIntent != nil {
		_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_first_leg", "switch open attempt")
	}

	pos := CrossVenuePosition{
		Symbol:               plan.Opp.Symbol,
		HighVenue:            plan.Opp.HighVenue,
		LowVenue:             plan.Opp.LowVenue,
		DecisionCycleID:      plan.DecisionCycleID,
		EntryDecisionCycleID: plan.DecisionCycleID,
		EntrySnapshotID:      plan.SnapshotID,
		CurrentSnapshotID:    plan.SnapshotID,
		OpenSpreadAPR:        plan.Opp.SpreadAPR,
		EstimatedFundingUSDT: plan.Opp.SpreadEV,
		HighFundingRate:      plan.Opp.HighFundingRate,
		LowFundingRate:       plan.Opp.LowFundingRate,
		HighIntervalHours:    plan.Opp.HighIntervalHours,
		LowIntervalHours:     plan.Opp.LowIntervalHours,
		HighNextFundingTime:  plan.Opp.HighNextFundingTime,
		LowNextFundingTime:   plan.Opp.LowNextFundingTime,
		TargetFirstFundingTime: rotationEarliestFundingAfterOpen(
			e.nowFn(), plan.Opp.HighNextFundingTime, plan.Opp.LowNextFundingTime,
		),
		FreshnessAgeSeconds:  plan.Opp.FreshnessAgeSeconds,
		HistoryQualityScore:  plan.Opp.HistoryQualityScore,
		LiquidityScore:       plan.Opp.LiquidityScore,
		StabilityScore:       plan.Opp.StabilityScore,
		ExecutionScore:       plan.Opp.ExecutionScore,
		RecoveryQualityScore: plan.Opp.RecoveryQualityScore,
		RankScore:            plan.Opp.RankScore,
		SizeMultiplier:       plan.Opp.SizeMultiplier,
		EntryOpenMode:        string(plan.SelectionMode),
		UsedFallback:         plan.SelectionMode == crossVenueOpenFallback,
		Priority:             plan.Opp.RankScore,
		TransitionState:      "open",
		Status:               CrossVenueStatusOpeningFirstLeg,
		OpenedAt:             e.nowFn(),
		UpdatedAt:            e.nowFn(),
		IdemPrefix:           plan.IdemPrefix,
		NotionalUSDT:         plan.Gate.NotionalUSDT,
		HighLeg:              CrossVenueLegState{Venue: plan.Opp.HighVenue, Symbol: string(highSym), Side: "sell"},
		LowLeg:               CrossVenueLegState{Venue: plan.Opp.LowVenue, Symbol: string(lowSym), Side: "buy"},
	}
	if plan.SwitchIntent != nil {
		pos.PendingSwitchSequence = nonEmpty(plan.SwitchIntent.SwitchSequence, crossVenueSwitchSequenceCloseThenOpen)
		pos.PartialSwitchPhase = "open_new_first_leg"
	}
	pos.CurrentComparableWindowHours = plan.Opp.ComparableWindowHours
	pos.CurrentFutureEV = plan.Opp.NetEV
	pos.CurrentCapitalEfficiencyEV = plan.Opp.CapitalEfficiencyEV
	pos.CurrentRiskAdjustedEVPerHour = plan.Opp.RiskAdjustedEVPerHour
	pos.CurrentRiskScore = clamp01(1 - (plan.Opp.ExecutionScore+plan.Opp.RecoveryQualityScore)/2)
	pos.EstimatedExitCost = plan.Opp.CloseCost + crossVenueExpectedSlippageUSDT(cfg, pos.NotionalUSDT, 2)
	pos.EstimatedSwitchCost = pos.EstimatedExitCost + plan.Opp.OpenCost + crossVenueExpectedSlippageUSDT(cfg, pos.NotionalUSDT, 2)
	e.recordCrossVenueTransition(pos, "", string(CrossVenueStatusOpeningFirstLeg), "开仓计划已创建")
	if live {
		_ = e.persistPosition(pos)
	}

	highID := crossVenueLegClientID(plan.IdemPrefix, "h1")
	if !live {
		logDryRunOpen(e.logger, "跨所perp-perp", plan.Opp.Symbol,
			fmt.Sprintf("空%s/多%s 各≈%.6f 名义≈%.2fU 价差年化%.1f%%",
				plan.Opp.HighVenue, plan.Opp.LowVenue, plan.Qty, plan.Gate.NotionalUSDT, plan.Opp.SpreadAPR*100))
		appendDryRunEvent(e.recordDir, "cvpp_open", map[string]any{
			"symbol": plan.Opp.Symbol, "high": plan.Opp.HighVenue, "low": plan.Opp.LowVenue, "qty": plan.Qty,
		})
		return CrossVenuePosition{}, nil
	}
	highCaps := highV.Capabilities()
	lowCaps := lowV.Capabilities()

	// 把两条永续腿杠杆设到 sizing 口径(MaxEffectiveLeverage),否则继承交易所账户默认高杠杆
	// (如币安 20x)会让 estimateLiqDistance=0.9/lev 误判「强平距离不足」→ 每轮无谓减仓空转。
	legLev := int(cfg.MaxEffectiveLeverage)
	if legLev < 1 {
		legLev = 2
	}
	openQtyHigh := plan.Gate.HighQty
	if openQtyHigh <= 0 {
		openQtyHigh = plan.Qty
	}
	openQtyLow := plan.Gate.LowQty
	if openQtyLow <= 0 {
		openQtyLow = plan.Qty
	}
	if crossVenueIsCrossDenomPair(plan.Opp) {
		openQtyHigh = normalizeVenueQty(openQtyHigh, plan.Gate.HighInstr, highCaps.ContractBasedPerp)
		openQtyLow = normalizeVenueQty(openQtyLow, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
	} else {
		unified := alignOpenQtyFromInstruments(plan.Qty, plan.Gate.HighInstr, plan.Gate.LowInstr, highCaps, lowCaps)
		openQtyHigh = unified
		openQtyLow = unified
	}
	if plan.Gate.HighInstr.StepSize <= 0 && plan.Gate.HighInstr.ContractSize <= 0 {
		if instr, ok := lookupVenueInstrumentForMarket(ctx, highV, highSym, venue.MarketPerp); ok {
			plan.Gate.HighInstr = instr
			openQtyHigh = alignOpenQtyFromInstruments(openQtyHigh, plan.Gate.HighInstr, plan.Gate.LowInstr, highCaps, lowCaps)
		}
	}
	if plan.Gate.LowInstr.StepSize <= 0 && plan.Gate.LowInstr.ContractSize <= 0 {
		if instr, ok := lookupVenueInstrumentForMarket(ctx, lowV, lowSym, venue.MarketPerp); ok {
			plan.Gate.LowInstr = instr
			if crossVenueIsCrossDenomPair(plan.Opp) {
				openQtyLow = normalizeVenueQty(openQtyLow, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
			} else {
				openQtyLow = alignOpenQtyFromInstruments(plan.Qty, plan.Gate.HighInstr, plan.Gate.LowInstr, highCaps, lowCaps)
			}
		}
	}
	refPrice := plan.RefPrice
	if refPrice <= 0 {
		refPrice = plan.Gate.HighMid
	}
	if instrumentHasQtyRules(plan.Gate.HighInstr) && !openLegTradableAfterQuantize(openQtyHigh, plan.Gate.HighMid, plan.Gate.HighInstr) {
		return CrossVenuePosition{}, fmt.Errorf("开仓数量低于最小可交易量 qty=%.8f", openQtyHigh)
	}
	lowRef := plan.Gate.LowMid
	if lowRef <= 0 {
		lowRef = refPrice
	}
	if instrumentHasQtyRules(plan.Gate.LowInstr) && !openLegTradableAfterQuantize(openQtyLow, lowRef, plan.Gate.LowInstr) {
		return CrossVenuePosition{}, fmt.Errorf("开仓数量低于最小可交易量 qty=%.8f", openQtyLow)
	}
	plan.Qty = openQtyHigh

	if lerr := highV.SetLeverage(ctx, highSym, legLev); lerr != nil && lerr != venue.ErrUnsupported {
		e.logger.Printf("[跨所] 设高费腿杠杆失败 %s %s lev=%d: %v", plan.Opp.HighVenue, highSym, legLev, lerr)
	}
	if lerr := lowV.SetLeverage(ctx, lowSym, legLev); lerr != nil && lerr != venue.ErrUnsupported {
		e.logger.Printf("[跨所] 设低费腿杠杆失败 %s %s lev=%d: %v", plan.Opp.LowVenue, lowSym, legLev, lerr)
	}

	_, _, deadlineBudget, _ := crossVenueMakerChaseParams(cfg)
	firstLegDeadline := e.nowFn().Add(deadlineBudget)
	// 极端价差(资费收益远超 taker 费)时第一腿允许 taker 兜底:H 这类高波动极端币 maker 挂单几乎吃不到,
	// 不兜底就永远"第一腿 maker 未成交"开不出仓;EV 本就按 taker 计,故兜底不改变盈利预期。普通机会仍 maker-only。
	firstLegAllowTakerFallback := crossVenueBypassesConfidenceDownsize(plan.Opp)
	highRes, err := e.placeCrossVenueLegExec(ctx, highTrader, highSym, venue.SideSell, openQtyHigh, highID, false, live, firstLegAllowTakerFallback, crossVenueResolvedIOCSlippageBps(cfg), firstLegDeadline, plan.Gate.HighInstr, highCaps.ContractBasedPerp)
	if orderStatusIsUnknown(highRes) || isUnknownOrderStateError(err) {
		if highRes.ExecutedQty > executionQtyTolerance {
			pos.HighLeg.Qty = highRes.ExecutedQty
			pos.HighLeg.EntryPrice = highRes.AvgPrice
			pos.HighLeg.OrderIDs = []string{highID}
			pos.Status = CrossVenueStatusOpeningSecondLeg
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第一腿查单失败,禁止自动回滚", plan.Opp.Symbol), plan.Opp.Symbol)
		return pos, fmt.Errorf("第一腿状态未知,禁止自动回滚: %w", err)
	}
	if err != nil || highRes.ExecutedQty <= executionQtyTolerance {
		if highRes.ExecutedQty > executionQtyTolerance {
			if plan.SwitchIntent != nil {
				_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "rollback_new", "switch first leg partial fill, rolling back")
			}
			e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, highRes.ExecutedQty, live)
		}
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_retry", "switch first leg failed")
		}
		return pos, fmt.Errorf("第一腿失败: %w", err)
	}
	e.recordCrossVenueExecution(ctx, &pos, cvExecOpenHigh, highRes, plan.Gate.HighMid)
	pos.HighLeg.Qty = highRes.ExecutedQty
	pos.HighLeg.EntryPrice = highRes.AvgPrice
	pos.HighLeg.OrderIDs = []string{highID}
	pos.Status = CrossVenueStatusOpeningSecondLeg
	pos.UpdatedAt = e.nowFn()
	if plan.SwitchIntent != nil {
		pos.PartialSwitchPhase = "open_new_second_leg"
	}
	if plan.SwitchIntent != nil {
		_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_second_leg", "switch first leg filled")
	}
	e.recordCrossVenueTransition(pos, string(CrossVenueStatusOpeningFirstLeg), string(CrossVenueStatusOpeningSecondLeg), "第一腿成交")
	_ = e.persistPosition(pos)
	if plan.SwitchIntent != nil && crossVenueIsStagedSwitch(plan.SwitchIntent.SwitchSequence) {
		pos.PartialSwitchPhase = "staged_close_old"
		pos.TransitionState = "staged"
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "close_old", "staged switch target first leg live, waiting old position close")
		e.recordCrossVenueTransition(pos, string(CrossVenueStatusOpeningSecondLeg), "staged_wait_close_old", "staged switch 第一腿已开,等待旧仓 close_old")
		return pos, nil
	}

	nakedStart := e.nowFn()
	if _, err := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, lowSym); err != nil {
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "rollback_new", "switch second leg precheck failed")
		}
		e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, pos.HighLeg.Qty, live)
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_retry", "switch second leg precheck failed")
		}
		e.setSymbolCooldown(plan.Opp.Symbol)
		return pos, err
	}
	lowID := crossVenueLegClientID(plan.IdemPrefix, "l1")
	hedgeQty := crossVenueOpenHedgeLowQty(plan, highRes, openQtyHigh, openQtyLow, lowCaps)
	if hedgeQty <= executionQtyTolerance {
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "rollback_new", "switch hedge qty normalized to zero")
		}
		e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, pos.HighLeg.Qty, live)
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_retry", "switch hedge qty normalized to zero")
		}
		e.setSymbolCooldown(plan.Opp.Symbol)
		return pos, fmt.Errorf("第二腿对冲数量经步进后为 0(第一腿 %.6f)", highRes.ExecutedQty)
	}
	secondLegDeadline := nakedStart.Add(deadlineBudget)
	lowRes, lowErr := e.placeCrossVenueLegExec(ctx, lowTrader, lowSym, venue.SideBuy, hedgeQty, lowID, false, live, true, crossVenueTakerSlippageBps(cfg), secondLegDeadline, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
	if orderStatusIsUnknown(lowRes) || isUnknownOrderStateError(lowErr) {
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第二腿查单失败,禁止自动回滚", plan.Opp.Symbol), plan.Opp.Symbol)
		return pos, fmt.Errorf("第二腿状态未知,禁止自动回滚: %w", lowErr)
	}
	if lowErr != nil || lowRes.ExecutedQty <= executionQtyTolerance {
		elapsed := e.nowFn().Sub(nakedStart).Seconds()
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "rollback_new", "switch second leg failed, rolling back")
		}
		e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, pos.HighLeg.Qty, live)
		if plan.SwitchIntent != nil {
			_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "open_new_retry", "switch rollback complete, waiting retry")
		}
		e.setSymbolCooldown(plan.Opp.Symbol)
		e.recordOpenOutcome(plan.Opp.Symbol, false)
		msg := fmt.Sprintf("%s 第二腿失败,已回滚第一腿(裸腿 %.0fs)", plan.Opp.Symbol, elapsed)
		e.notifyWarn("跨所开仓回滚", msg, plan.Opp.Symbol)
		if elapsed > float64(cfg.MaxNakedSeconds) {
			e.notifyCritical("跨所裸腿超时", fmt.Sprintf("%s 裸腿暴露 %.0fs 超过阈值 %ds", plan.Opp.Symbol, elapsed, cfg.MaxNakedSeconds), plan.Opp.Symbol)
		}
		return pos, fmt.Errorf("第二腿失败: %w", lowErr)
	}
	e.recordCrossVenueExecution(ctx, &pos, cvExecOpenLow, lowRes, plan.Gate.LowMid)
	highCoin := crossVenueLegExecutedCoinQty(highCaps, plan.Gate.HighInstr, highRes, 0)
	lowCoin := crossVenueLegExecutedCoinQty(lowCaps, plan.Gate.LowInstr, lowRes, highCoin)
	highCoin = crossVenueLegExecutedCoinQty(highCaps, plan.Gate.HighInstr, highRes, lowCoin)
	lowCoin = crossVenueLegExecutedCoinQty(lowCaps, plan.Gate.LowInstr, lowRes, highCoin)
	var bookHigh, bookLow float64
	if crossVenueIsCrossDenomPair(plan.Opp) {
		bookHigh, bookLow = reconcileCrossVenueOpenBookQtyPerLeg(highCoin, lowCoin, crossVenueOppHighMult(plan.Opp), crossVenueOppLowMult(plan.Opp))
	} else {
		bookQty := reconcileCrossVenueOpenBookQty(highCaps, lowCaps, highCoin, lowCoin)
		bookHigh, bookLow = bookQty, bookQty
	}
	pos.HighLeg.Qty = bookHigh
	pos.LowLeg.Qty = bookLow
	pos.LowLeg.EntryPrice = lowRes.AvgPrice
	pos.LowLeg.OrderIDs = []string{lowID}
	pos.NotionalUSDT = crossVenueBookNotionalUSDT(math.Min(bookHigh, bookLow), plan.RefPrice, highRes, lowRes)
	if crossVenueIsCrossDenomPair(plan.Opp) {
		pos.NotionalUSDT = math.Min(bookHigh*plan.Gate.HighMid, bookLow*plan.Gate.LowMid)
	}
	pos.Status = CrossVenueStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	if plan.SwitchIntent != nil {
		pos.PartialSwitchPhase = "consumed"
	}
	if plan.SwitchIntent != nil {
		_ = e.updateSwitchIntentAttempt(plan.SwitchIntent.Symbol, plan.SwitchIntent.TargetHighVenue, plan.SwitchIntent.TargetLowVenue, "consumed", "switch target opened")
	}
	e.recordCrossVenueTransition(pos, string(CrossVenueStatusOpeningSecondLeg), string(CrossVenueStatusOpen), "双腿开仓完成")
	_ = e.persistPosition(pos)
	e.recordOpenOutcome(plan.Opp.Symbol, true)
	e.notifyTrade("跨所开仓", fmt.Sprintf("%s 空%s/多%s 高≈%.6f/低≈%.6f 价差年化%.1f%%",
		plan.Opp.Symbol, plan.Opp.HighVenue, plan.Opp.LowVenue, bookHigh, bookLow, plan.Opp.SpreadAPR*100), plan.Opp.Symbol)
	return pos, nil
}

func crossVenueOpenHedgeLowQty(plan crossVenueOpenPlan, highRes venue.OrderResult, openQtyHigh, openQtyLow float64, lowCaps venue.Capabilities) float64 {
	hedgeQty := openQtyLow
	if openQtyHigh > executionQtyTolerance && highRes.ExecutedQty > 0 &&
		math.Abs(highRes.ExecutedQty-openQtyHigh) > executionQtyTolerance {
		hedgeQty = openQtyLow * (highRes.ExecutedQty / openQtyHigh)
	}
	if !crossVenueIsCrossDenomPair(plan.Opp) && lowCaps.ContractBasedPerp {
		hedgeQty = normalizeVenueQty(highRes.ExecutedQty, plan.Gate.LowInstr, true)
		return hedgeQty
	}
	return normalizeVenueQty(hedgeQty, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
}

func (e *CrossVenueExecutor) rollbackFirstLeg(ctx context.Context, plan crossVenueOpenPlan, tr crossVenueTrader, sym venue.Symbol, pos *CrossVenuePosition, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	book, err := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		e.logger.Printf("[跨所] 回滚第一腿取盘口失败 %s: %v", sym, err)
		return
	}
	id := crossVenueLegClientID(plan.IdemPrefix, "rb")
	_, _, deadlineBudget, _ := crossVenueMakerChaseParams(e.cfg.CrossVenueExecutor)
	res, err := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideBuy, qty, id, true, live, true, crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor), e.nowFn().Add(deadlineBudget), plan.Gate.HighInstr, true)
	if err != nil {
		e.logger.Printf("[跨所] 回滚第一腿失败 %s: %v", sym, err)
		e.notifyCritical("跨所回滚失败", fmt.Sprintf("%s 第一腿 reduce-only 回滚失败: %v", sym, err), string(sym))
		return
	}
	if pos != nil {
		e.recordCrossVenueExecution(ctx, pos, cvExecRollbackHigh, res, crossVenueBookMid(book))
	}
}

// closeCrossVenuePosition 平仓协议。live 模式下逐腿 IOC 平仓后**核验真实成交**(ExecutedQty 对账 +
// FetchPositions 复核):任一腿未平干净 → 保持 closing 状态、更新剩余 Qty、**不删台账**,下轮主循环
// 重试(manageCrossVenuePosition 的 closing 分支);CloseAttempts 驱动递进滑点/穿价兜底;≥3 轮 critical 告警(退避)。
// OFF 模式无真仓,记日志后直接收尾删台账(行为不变)。
func (e *CrossVenueExecutor) lookupPerpInstrument(ctx context.Context, venueName string, sym venue.Symbol) venue.Instrument {
	v := e.venues[venueName]
	if v == nil {
		return venue.Instrument{}
	}
	if instr, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketPerp); ok {
		return instr
	}
	return venue.Instrument{}
}

func (e *CrossVenueExecutor) closeCrossVenuePosition(ctx context.Context, pos CrossVenuePosition, live bool, reason string) error {
	if !live {
		if pos.HighLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 高费腿 %s 买回 %.6f 原因:%s", pos.Symbol, pos.HighVenue, pos.HighLeg.Qty, reason)
		}
		if pos.LowLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 低费腿 %s 卖出 %.6f 原因:%s", pos.Symbol, pos.LowVenue, pos.LowLeg.Qty, reason)
		}
		appendDryRunEvent(e.recordDir, "cvpp_close", map[string]any{"symbol": pos.Symbol, "reason": reason})
		return nil
	}
	fromStatus := pos.Status
	pos.Status = CrossVenueStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	pos.TransitionState = "close"
	if crossVenueHasPendingSwitch(pos) && (pos.PartialSwitchPhase == "" || pos.PartialSwitchPhase == "pending") {
		pos.PartialSwitchPhase = "close_old"
	}
	e.recordCrossVenueTransition(pos, string(fromStatus), string(CrossVenueStatusClosing), reason)
	_ = e.persistPosition(pos)
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]
	highTrader, _ := highV.(crossVenueTrader)
	lowTrader, _ := lowV.(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	closeDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	baseTakerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	// 下单前以交易所真实持仓为准(reduce-only 绝不超过剩余量)。
	e.syncCrossVenueCloseLegQtyFromExchange(ctx, highV, highSym, &pos.HighLeg.Qty)
	e.syncCrossVenueCloseLegQtyFromExchange(ctx, lowV, lowSym, &pos.LowLeg.Qty)
	// 逐腿 reduce-only,按返回的 ExecutedQty 先行对账(clientID 带轮次后缀,跨轮重试不撞幂等去重)。
	if pos.HighLeg.Qty > executionQtyTolerance {
		id := crossVenueLegClientID(pos.IdemPrefix, fmt.Sprintf("ch%d", attempt))
		highInstr := e.lookupPerpInstrument(ctx, pos.HighVenue, highSym)
		if res, book, perr := e.placeCrossVenueCloseLeg(ctx, highTrader, highSym, venue.SideBuy, pos.HighLeg.Qty, id, live, attempt, baseTakerBps, closeDeadline, highInstr, highV.Capabilities().ContractBasedPerp); perr == nil {
			coinQty := e.recordCrossVenueExecution(ctx, &pos, cvExecCloseHigh, res, crossVenueCloseSlippageRefMid(book))
			pos.HighLeg.Qty = math.Max(pos.HighLeg.Qty-coinQty, 0)
		} else {
			e.logger.Printf("[跨所] 平仓高费腿失败 %s(第 %d 轮,下轮重试): %v", pos.Symbol, attempt, perr)
		}
	}
	if pos.LowLeg.Qty > executionQtyTolerance {
		id := crossVenueLegClientID(pos.IdemPrefix, fmt.Sprintf("cl%d", attempt))
		lowInstr := e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym)
		if res, book, perr := e.placeCrossVenueCloseLeg(ctx, lowTrader, lowSym, venue.SideSell, pos.LowLeg.Qty, id, live, attempt, baseTakerBps, closeDeadline, lowInstr, lowV.Capabilities().ContractBasedPerp); perr == nil {
			coinQty := e.recordCrossVenueExecution(ctx, &pos, cvExecCloseLow, res, crossVenueCloseSlippageRefMid(book))
			pos.LowLeg.Qty = math.Max(pos.LowLeg.Qty-coinQty, 0)
		} else {
			e.logger.Printf("[跨所] 平仓低费腿失败 %s(第 %d 轮,下轮重试): %v", pos.Symbol, attempt, perr)
		}
	}
	// 权威复核:以交易所真实仓位为准刷新剩余量(IOC 回执可能不完整;拉失败保留本地对账值,保守)。
	if hp, herr := highV.FetchPositions(ctx); herr == nil {
		pos.HighLeg.Qty = absQty(perpPositionQty(hp, highSym))
	}
	if lp, lerr := lowV.FetchPositions(ctx); lerr == nil {
		pos.LowLeg.Qty = absQty(perpPositionQty(lp, lowSym))
	}

	if pos.HighLeg.Qty <= executionQtyTolerance && pos.LowLeg.Qty <= executionQtyTolerance {
		if crossVenueHasPendingSwitch(pos) {
			pos.PartialSwitchPhase = "closed_old"
			_ = e.updateSwitchIntentPhase(pos.Symbol, pos.PendingSwitchHighVenue, pos.PendingSwitchLowVenue, "open_new")
		}
		pos.Status = CrossVenueStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.recordCrossVenueTransition(pos, string(CrossVenueStatusClosing), string(CrossVenueStatusClosed), pos.CloseReason)
		e.finalizeClosedCrossVenuePosition(pos, live)
		e.notifyTrade("跨所平仓", fmt.Sprintf("%s %s(第 %d 轮平仓完成)", pos.Symbol, pos.CloseReason, attempt), pos.Symbol)
		return nil
	}

	// 未平干净:保持 closing、保留台账,下轮主循环重试。
	pos.UpdatedAt = e.nowFn()
	if crossVenueHasPendingSwitch(pos) {
		e.markCrossVenuePositionPartialSwitch(&pos, "pending switch 未完成,进入 partial switch 恢复")
	} else {
		pos.TransitionState = "close"
		_ = e.persistPosition(pos)
	}
	e.logger.Printf("[跨所] 平仓未完成 %s(第 %d 轮): 高费腿剩 %.6f 低费腿剩 %.6f,保留台账下轮重试",
		pos.Symbol, attempt, pos.HighLeg.Qty, pos.LowLeg.Qty)
	if crossVenueCloseCriticalShouldAlert(attempt) {
		e.notifyCritical("跨所平仓持续失败",
			fmt.Sprintf("%s 已连续 %d 轮平不掉(高费腿剩 %.6f / 低费腿剩 %.6f),请人工核对", pos.Symbol, attempt, pos.HighLeg.Qty, pos.LowLeg.Qty),
			pos.Symbol)
	}
	return fmt.Errorf("平仓未完成: %s 第 %d 轮仍有剩余腿", pos.Symbol, attempt)
}

func (e *CrossVenueExecutor) reduceCrossVenueRisk(ctx context.Context, pos CrossVenuePosition, live bool) error {
	// 两腿等比例各减 50%
	halfHigh := pos.HighLeg.Qty / 2
	halfLow := pos.LowLeg.Qty / 2
	if halfHigh <= executionQtyTolerance || halfLow <= executionQtyTolerance {
		return e.closeCrossVenuePosition(ctx, pos, live, "强平距离不足,全平")
	}
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	highTrader, _ := e.venues[pos.HighVenue].(crossVenueTrader)
	lowTrader, _ := e.venues[pos.LowVenue].(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return fmt.Errorf("交易接口缺失")
	}
	bookH, _ := highTrader.FetchBookTicker(ctx, venue.MarketPerp, highSym)
	bookL, _ := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, lowSym)
	idH := crossVenueLegClientID(pos.IdemPrefix, "rh")
	idL := crossVenueLegClientID(pos.IdemPrefix, "rl")
	rebalDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	highInstr := e.lookupPerpInstrument(ctx, pos.HighVenue, highSym)
	lowInstr := e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym)
	highContract := e.venues[pos.HighVenue].Capabilities().ContractBasedPerp
	lowContract := e.venues[pos.LowVenue].Capabilities().ContractBasedPerp
	if live {
		if res, _ := e.placeCrossVenueLegExec(ctx, highTrader, highSym, venue.SideBuy, halfHigh, idH, true, live, true, takerBps, rebalDeadline, highInstr, highContract); res.ExecutedQty > executionQtyTolerance {
			e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceHigh, res, crossVenueBookMid(bookH))
		}
		if res, _ := e.placeCrossVenueLegExec(ctx, lowTrader, lowSym, venue.SideSell, halfLow, idL, true, live, true, takerBps, rebalDeadline, lowInstr, lowContract); res.ExecutedQty > executionQtyTolerance {
			e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceLow, res, crossVenueBookMid(bookL))
		}
	} else {
		e.logger.Printf("[dry-run] 本应同步降风险 %s 各减 50%%", pos.Symbol)
	}
	pos.HighLeg.Qty -= halfHigh
	pos.LowLeg.Qty -= halfLow
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.notifyWarn("跨所同步降风险", fmt.Sprintf("%s 强平距离不足,两腿各减 50%%", pos.Symbol), pos.Symbol)
	return nil
}

func perpPositionQty(positions []venue.Position, sym venue.Symbol) float64 {
	for _, p := range positions {
		if venueSymbolsMatch(p.Symbol, sym) {
			return p.PositionAmt
		}
	}
	return 0
}

func absQty(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func qtyDeviation(a, b float64) float64 {
	if a == 0 && b == 0 {
		return 0
	}
	max := math.Max(absQty(a), absQty(b))
	if max <= executionQtyTolerance {
		return 0
	}
	return math.Abs(absQty(a)-absQty(b)) / max
}
