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

// crossVenueOpenFundingRefreshMinRetainFraction 下单前实时资费复核:最新目标腿资费相对快照预期至少需保留的比例,
// 低于此(即资费已大幅塌缩)则放弃开仓。0.5 = 允许缩水一半以内。
const crossVenueOpenFundingRefreshMinRetainFraction = 0.5

// crossVenueRefreshedFunding 下单前重拉的两腿最新资费。
type crossVenueRefreshedFunding struct {
	HighRate     float64
	LowRate      float64
	HighNext     time.Time
	LowNext      time.Time
	HighInterval int
	LowInterval  int
	Fetched      bool // 两腿都拉到才 true;否则 fail-open 沿用快照。
}

// refreshFundingBeforeOpen 进场窗内、真正下第一腿之前重拉两腿最新资费并复核目标腿资费是否仍成立。
// 离结算越近,交易所预测资费越收敛于真实结算值;用最新值挡住"用十几分钟前旧快照赌 3 分钟后结算的短周期腿翻盘"
// (LAB 即此:bybit 1h 腿在旧快照期内翻负→做空反付资费)。
// 返回 (fresh, ok, reason):ok=false 表示资费已翻向/大幅塌缩,应放弃本单;拉取失败时 fail-open(ok=true,Fetched=false)。
func (e *CrossVenueExecutor) refreshFundingBeforeOpen(ctx context.Context, plan crossVenueOpenPlan) (crossVenueRefreshedFunding, bool, string) {
	cfg := e.cfg.CrossVenueExecutor
	if !cfg.OpenFundingRefreshEnabled || !crossVenueUnifiedActiveForSymbol(cfg, plan.Opp.Symbol) {
		return crossVenueRefreshedFunding{}, true, ""
	}
	highV := e.venues[plan.Opp.HighVenue]
	lowV := e.venues[plan.Opp.LowVenue]
	if highV == nil || lowV == nil {
		return crossVenueRefreshedFunding{}, true, ""
	}
	highSnap, hok := fetchVenueFundingForSymbol(ctx, highV, venue.Symbol(crossVenueOppHighSymbol(plan.Opp)))
	lowSnap, lok := fetchVenueFundingForSymbol(ctx, lowV, venue.Symbol(crossVenueOppLowSymbol(plan.Opp)))
	if !hok || !lok {
		if e.logger != nil {
			e.logger.Printf("[跨所][进场资费复核] %s 实时资费拉取失败(high_ok=%v low_ok=%v),fail-open 沿用快照", plan.Opp.Symbol, hok, lok)
		}
		return crossVenueRefreshedFunding{}, true, ""
	}
	fresh := crossVenueRefreshedFunding{
		HighRate: highSnap.LastFundingRate, LowRate: lowSnap.LastFundingRate,
		HighNext: highSnap.NextFundingTime, LowNext: lowSnap.NextFundingTime,
		HighInterval: highSnap.IntervalHours, LowInterval: lowSnap.IntervalHours,
		Fetched: true,
	}
	freshOpp := plan.Opp
	freshOpp.HighFundingRate = fresh.HighRate
	freshOpp.LowFundingRate = fresh.LowRate
	if fresh.HighInterval > 0 {
		freshOpp.HighIntervalHours = fresh.HighInterval
	}
	if fresh.LowInterval > 0 {
		freshOpp.LowIntervalHours = fresh.LowInterval
	}
	if !fresh.HighNext.IsZero() {
		freshOpp.HighNextFundingTime = fresh.HighNext
	}
	if !fresh.LowNext.IsZero() {
		freshOpp.LowNextFundingTime = fresh.LowNext
	}
	freshFunding := crossVenueExpectedTargetFundingUSDT(cfg, freshOpp, plan.Gate.NotionalUSDT)
	if freshFunding <= 0 {
		return fresh, false, fmt.Sprintf("最新目标腿资费=%.4f≤0(快照费率 high=%.6f low=%.6f → 最新 high=%.6f low=%.6f),边缘机会已消失",
			freshFunding, plan.Opp.HighFundingRate, plan.Opp.LowFundingRate, fresh.HighRate, fresh.LowRate)
	}
	snapFunding := crossVenueExpectedTargetFundingUSDT(cfg, plan.Opp, plan.Gate.NotionalUSDT)
	if snapFunding > 0 && freshFunding < snapFunding*crossVenueOpenFundingRefreshMinRetainFraction {
		return fresh, false, fmt.Sprintf("最新目标腿资费 %.4f < 快照 %.4f × %.2f,资费已大幅塌缩",
			freshFunding, snapFunding, crossVenueOpenFundingRefreshMinRetainFraction)
	}
	return fresh, true, ""
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
	// 下单前实时资费复核(仅 live):进场窗内用最新资费挡住"旧快照赌短周期腿翻盘";达标则把最新费率写回仓位。
	if live {
		if fresh, ok, reason := e.refreshFundingBeforeOpen(ctx, plan); !ok {
			if e.logger != nil {
				e.logger.Printf("[跨所perp-perp] 放弃开仓 %s:进场资费复核未过——%s", plan.Opp.Symbol, reason)
			}
			return CrossVenuePosition{}, nil
		} else if fresh.Fetched {
			pos.HighFundingRate = fresh.HighRate
			pos.LowFundingRate = fresh.LowRate
			if fresh.HighInterval > 0 {
				pos.HighIntervalHours = fresh.HighInterval
			}
			if fresh.LowInterval > 0 {
				pos.LowIntervalHours = fresh.LowInterval
			}
			if !fresh.HighNext.IsZero() {
				pos.HighNextFundingTime = fresh.HighNext
			}
			if !fresh.LowNext.IsZero() {
				pos.LowNextFundingTime = fresh.LowNext
			}
			pos.TargetFirstFundingTime = rotationEarliestFundingAfterOpen(e.nowFn(), pos.HighNextFundingTime, pos.LowNextFundingTime)
		}
	}
	// 统一捕获模型:开仓即钉死本笔的捕获计划(目标腿/目标结算/计划离场/预期资费/基差预算);flag OFF → nil(回退旧行为)。
	pos.CapturePlan = crossVenueBuildCapturePlan(
		cfg, pos.Symbol, pos.HighFundingRate, pos.LowFundingRate, pos.NotionalUSDT,
		pos.HighIntervalHours, pos.HighNextFundingTime, pos.LowIntervalHours, pos.LowNextFundingTime, pos.OpenedAt, pos.StabilityScore,
	)
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
		// 查单未知不再仅告警:以交易所真实持仓判定地面真值,据此安全放弃 / 回滚裸腿 / HALT 新开。
		highPositions, perr := highV.FetchPositions(ctx)
		if perr != nil {
			// 无法判定真伪 → HALT 新开,留 opening 态待 manage/reconcile 复核(绝不在不确定时继续开新仓)。
			if highRes.ExecutedQty > executionQtyTolerance {
				pos.HighLeg.Qty = highRes.ExecutedQty
				pos.HighLeg.EntryPrice = highRes.AvgPrice
				pos.HighLeg.OrderIDs = []string{highID}
			}
			pos.Status = CrossVenueStatusOpeningSecondLeg
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			_ = e.SetOpeningsEnabled(false)
			e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第一腿查单失败且拉持仓核验失败,已 HALT 新开待复核: %v", plan.Opp.Symbol, perr), plan.Opp.Symbol)
			return pos, fmt.Errorf("第一腿状态未知且持仓核验失败,HALT 新开: %w", err)
		}
		liveQty := absQty(perpPositionQty(highPositions, highSym))
		if liveQty <= executionQtyTolerance {
			// 交易所确认未成交:无裸腿,安全放弃本次开仓。
			e.setSymbolCooldown(plan.Opp.Symbol)
			e.notifyWarn("跨所查单未知", fmt.Sprintf("%s 第一腿查单未知但交易所确认未成交,安全放弃", plan.Opp.Symbol), plan.Opp.Symbol)
			return pos, fmt.Errorf("第一腿状态未知,交易所核验未成交: %w", err)
		}
		// 交易所确认有裸腿 → 按真实数量回滚(带重试核验),仍失败由 rollbackFirstLeg 置 orphan 待 manage 续平。
		pos.HighLeg.Qty = liveQty
		pos.HighLeg.EntryPrice = highRes.AvgPrice
		pos.HighLeg.OrderIDs = []string{highID}
		pos.Status = CrossVenueStatusOpeningSecondLeg
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第一腿查单失败但交易所确认有裸腿(%.6f),执行回滚", plan.Opp.Symbol, liveQty), plan.Opp.Symbol)
		e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, liveQty, live)
		e.setSymbolCooldown(plan.Opp.Symbol)
		return pos, fmt.Errorf("第一腿状态未知,已按交易所真值回滚裸腿: %w", err)
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
		// 第一腿已在场。第二腿查单未知:以交易所真实持仓判定地面真值。
		lowPositions, lperr := lowV.FetchPositions(ctx)
		if lperr != nil {
			// 无法判定 → HALT 新开,留 opening_second_leg 态待 manage 续做核验(绝不在不确定时继续开新仓)。
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			_ = e.SetOpeningsEnabled(false)
			e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第二腿查单失败且拉持仓核验失败,已 HALT 新开待复核: %v", plan.Opp.Symbol, lperr), plan.Opp.Symbol)
			return pos, fmt.Errorf("第二腿状态未知且持仓核验失败,HALT 新开: %w", lowErr)
		}
		lowLiveQty := absQty(perpPositionQty(lowPositions, lowSym))
		if lowLiveQty <= executionQtyTolerance {
			// 交易所确认第二腿未成交 → 第一腿裸空,回滚第一腿(带重试核验)。
			e.notifyCritical("跨所查单未知", fmt.Sprintf("%s 第二腿查单失败且交易所确认未成交,回滚第一腿", plan.Opp.Symbol), plan.Opp.Symbol)
			e.rollbackFirstLeg(ctx, plan, highTrader, highSym, &pos, pos.HighLeg.Qty, live)
			e.setSymbolCooldown(plan.Opp.Symbol)
			e.recordOpenOutcome(plan.Opp.Symbol, false)
			return pos, fmt.Errorf("第二腿状态未知,交易所确认未成交,已回滚第一腿: %w", lowErr)
		}
		// 交易所确认第二腿已成交 → 双腿在场(已对冲),补全成交结果转入下方核验收尾,不再回滚。
		lowErr = nil
		if lowRes.ExecutedQty <= executionQtyTolerance {
			lowRes.ExecutedQty = lowLiveQty
		}
		lowRes.Symbol = lowSym
		if lowRes.AvgPrice <= 0 {
			lowRes.AvgPrice = plan.Gate.LowMid
		}
		e.notifyWarn("跨所查单未知", fmt.Sprintf("%s 第二腿查单未知但交易所确认已成交(%.6f),转核验收尾", plan.Opp.Symbol, lowLiveQty), plan.Opp.Symbol)
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
	highQty, lowQty, imbalanceQty, verifyErr := e.verifyAndFinalizeCrossVenueOpen(ctx, &pos, plan, live)
	if verifyErr != nil {
		e.recordOpenOutcome(plan.Opp.Symbol, false)
		e.notifyCritical("跨所开仓核验失败", fmt.Sprintf("%s 对冲核验失败: %v", plan.Opp.Symbol, verifyErr), plan.Opp.Symbol)
		return pos, verifyErr
	}
	pos.HighLeg.Qty = highQty
	pos.LowLeg.Qty = lowQty
	pos.LegImbalanceQty = imbalanceQty
	pos.LowLeg.EntryPrice = lowRes.AvgPrice
	pos.LowLeg.OrderIDs = []string{lowID}
	pos.NotionalUSDT = crossVenueBookNotionalUSDT(math.Min(highQty, lowQty), plan.RefPrice, highRes, lowRes)
	if crossVenueIsCrossDenomPair(plan.Opp) {
		pos.NotionalUSDT = math.Min(highQty*plan.Gate.HighMid, lowQty*plan.Gate.LowMid)
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
	openAlert := fmt.Sprintf("%s 空%s/多%s 高=%.6f/低=%.6f", plan.Opp.Symbol, plan.Opp.HighVenue, plan.Opp.LowVenue, highQty, lowQty)
	if imbalanceQty > executionQtyTolerance {
		openAlert += fmt.Sprintf(" 偏差=%.6f", imbalanceQty)
	} else {
		openAlert += fmt.Sprintf(" 价差年化%.1f%%", plan.Opp.SpreadAPR*100)
	}
	e.notifyTrade("跨所开仓", openAlert, plan.Opp.Symbol)
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

// crossVenueRollbackRetryAttempts 第一腿 reduce-only 回滚的最大重试轮数;每轮升级穿价滑点并以
// 交易所真实持仓核验是否真的平掉,绝不在腿可能仍存活时静默放弃。
const crossVenueRollbackRetryAttempts = 3

// rollbackFirstLeg 以 reduce-only 平掉裸露的第一腿(空头买回):逐轮升级滑点 + taker 兜底,每轮后
// 以 FetchPositions 核验是否真正平掉;取盘口/下单失败都不再静默返回(这会留下裸腿)。重试耗尽仍未
// 确认平掉时升级 critical 告警并把持仓置为显式 orphan/recover 态,交由 manage 主循环持续重试 flatten。
func (e *CrossVenueExecutor) rollbackFirstLeg(ctx context.Context, plan crossVenueOpenPlan, tr crossVenueTrader, sym venue.Symbol, pos *CrossVenuePosition, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	highV := e.venues[plan.Opp.HighVenue]
	baseSlip := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	_, _, deadlineBudget, _ := crossVenueMakerChaseParams(e.cfg.CrossVenueExecutor)
	remaining := qty
	var lastErr error
	for attempt := 1; attempt <= crossVenueRollbackRetryAttempts; attempt++ {
		// 下单前以交易所真实持仓为准(reduce-only 绝不超过剩余量);若已无裸腿则确认收尾。
		if highV != nil {
			if positions, perr := highV.FetchPositions(ctx); perr == nil {
				exch := absQty(perpPositionQty(positions, sym))
				if exch <= executionQtyTolerance {
					if pos != nil {
						pos.HighLeg.Qty = 0
					}
					return
				}
				remaining = exch
			} else {
				e.logger.Printf("[跨所] 回滚第一腿核验持仓失败 %s(第 %d 轮,继续兜底下单): %v", sym, attempt, perr)
			}
		}
		// 逐轮升级穿价滑点;clientID 带轮次后缀,跨轮重试不撞幂等去重。
		slip := baseSlip * float64(attempt)
		id := crossVenueLegClientID(plan.IdemPrefix, fmt.Sprintf("rb%d", attempt))
		book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
		res, err := e.placeCrossVenueLegExec(ctx, tr, sym, venue.SideBuy, remaining, id, true, live, true, slip, e.nowFn().Add(deadlineBudget), plan.Gate.HighInstr, true)
		if err != nil {
			lastErr = err
			e.logger.Printf("[跨所] 回滚第一腿失败 %s(第 %d 轮): %v", sym, attempt, err)
		} else if pos != nil && res.ExecutedQty > executionQtyTolerance {
			e.recordCrossVenueExecution(ctx, pos, cvExecRollbackHigh, res, crossVenueBookMid(book))
		}
		// 下单后再次以交易所真实持仓核验是否已平。
		if highV != nil {
			if positions, perr := highV.FetchPositions(ctx); perr == nil {
				if absQty(perpPositionQty(positions, sym)) <= executionQtyTolerance {
					if pos != nil {
						pos.HighLeg.Qty = 0
					}
					return
				}
			}
		} else if err == nil && res.ExecutedQty > executionQtyTolerance {
			// 无 venue 句柄无法核验:仅在本轮成交了全部剩余量时才视为平掉。
			if pos != nil {
				pos.HighLeg.Qty = 0
			}
			return
		}
	}
	// 重试耗尽仍可能有裸腿:升级 critical 告警 + 置显式 orphan/recover 态,交由 manage 循环持续重试 flatten。
	e.notifyCritical("跨所回滚持续失败",
		fmt.Sprintf("%s 第一腿 reduce-only 回滚 %d 轮仍未确认平掉(末次错误: %v),已置 orphan 待 manage 续平",
			sym, crossVenueRollbackRetryAttempts, lastErr),
		string(sym))
	if pos != nil {
		pos.Status = CrossVenueStatusOrphanSingleLeg
		pos.TransitionState = "recover"
		pos.RecoveryAction = crossVenueRecoveryActionFlatten
		pos.CloseReason = nonEmpty(pos.CloseReason, "rollback_first_leg_failed")
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(*pos)
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

// crossVenueMaxHedgedCloseAttempts 自愿平仓"保持对冲"最多容忍的轮数;超过后即使首腿仍卡住也升级为强平兜底
// (允许单边平),避免对冲腿被无限期持有。约 = N × 主循环周期(~60s)。
const crossVenueMaxHedgedCloseAttempts = 3

// crossVenueCloseForcesFlat 平仓原因是否属于"必须尽快变平"的强制场景:强平距离不足 / 紧急 flatten。
// 这类场景宁可单边裸腿也要尽快降敞口;其余(计划离场/换仓/基差止损/EV/回滚)优先保持对冲。
func crossVenueCloseForcesFlat(reason string) bool {
	r := strings.ToLower(strings.TrimSpace(reason))
	for _, kw := range []string{"强平", "紧急", "flatten", "liquidat", "emergency"} {
		if strings.Contains(reason, kw) || strings.Contains(r, kw) {
			return true
		}
	}
	return false
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
	// FIX(P1 平仓裸腿):计划离场/换仓/基差止损等"自愿"平仓,若首腿(高费)一张都没平掉、却照样平掉次腿(低费),
	// 会留下单边裸腿暴露到下一轮(~60s),在暴力币上直接吃方向性亏损(TAIKO 即此:gate 高腿未平、bybit 低腿先平,
	// 裸 55s → close_slip -0.545 / trade -0.80)。本轮若高腿无进展则跳过低腿、保持 delta 中性,下轮重试两腿。
	// 强平距离/紧急 flatten 等"必须尽快变平"场景维持原状(宁可单边也要快速降敞口);连续 N 轮仍卡住则升级强平兜底,
	// 避免对冲腿被无限期持有。
	forceFlat := crossVenueCloseForcesFlat(reason) || attempt >= crossVenueMaxHedgedCloseAttempts
	// 逐腿 reduce-only,按返回的 ExecutedQty 先行对账(clientID 带轮次后缀,跨轮重试不撞幂等去重)。
	highBeforeQty := pos.HighLeg.Qty
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
	// 高腿"本轮一张没平掉"且低腿仍在对冲 → 自愿平仓下保持对冲,跳过低腿。
	hedgePreserveSkipLow := !forceFlat &&
		highBeforeQty > executionQtyTolerance &&
		(highBeforeQty-pos.HighLeg.Qty) <= executionQtyTolerance &&
		pos.LowLeg.Qty > executionQtyTolerance
	switch {
	case hedgePreserveSkipLow:
		e.logger.Printf("[跨所] 平仓保持对冲 %s(第 %d 轮):高费腿未平动(剩 %.6f),本轮不平低费腿(剩 %.6f)以维持 delta 中性,下轮重试两腿",
			pos.Symbol, attempt, pos.HighLeg.Qty, pos.LowLeg.Qty)
	case pos.LowLeg.Qty > executionQtyTolerance:
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
		// 平仓终态:强制再拉一次真实资费(无视节流),抓取结算后实际入账值,避免超短持有把真实资费记成 0。
		e.forceSyncRealizedFunding(ctx, &pos)
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

// reduceCrossVenueRisk 强平距离不足时的同步降风险:两腿各减 50%。失败安全语义:
//  1. 下单前以交易所真实持仓刷新两腿数量(绝不用过期台账半量算 50%);
//  2. 先减「过对冲/较大」的那条腿(其减仓方向降低净敞口);
//  3. 若第一条腿减仓失败 → 立即中止第二条腿,绝不单边减仓制造更大失衡,并 critical 告警返回错误;
//  4. 第二条腿失败也 critical + 返回错误(剩余失衡由下轮 manage 的 rebalanceHedge 复核纠偏)。
//
// 净规则:失败的降风险绝不会让账面比降风险前更失衡——第一腿失败时两腿都不动(维持原平衡)。
func (e *CrossVenueExecutor) reduceCrossVenueRisk(ctx context.Context, pos CrossVenuePosition, live bool) error {
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	highTrader, _ := e.venues[pos.HighVenue].(crossVenueTrader)
	lowTrader, _ := e.venues[pos.LowVenue].(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return fmt.Errorf("交易接口缺失")
	}
	// 下单前先把两腿数量同步到交易所真实持仓,再算 50% 切量(避免用过期台账半量超卖/欠卖)。
	if live {
		e.syncCrossVenueCloseLegQtyFromExchange(ctx, e.venues[pos.HighVenue], highSym, &pos.HighLeg.Qty)
		e.syncCrossVenueCloseLegQtyFromExchange(ctx, e.venues[pos.LowVenue], lowSym, &pos.LowLeg.Qty)
	}
	halfHigh := pos.HighLeg.Qty / 2
	halfLow := pos.LowLeg.Qty / 2
	if halfHigh <= executionQtyTolerance || halfLow <= executionQtyTolerance {
		return e.closeCrossVenuePosition(ctx, pos, live, "强平距离不足,全平")
	}
	if !live {
		e.logger.Printf("[dry-run] 本应同步降风险 %s 各减 50%%", pos.Symbol)
		pos.HighLeg.Qty -= halfHigh
		pos.LowLeg.Qty -= halfLow
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyWarn("跨所同步降风险", fmt.Sprintf("%s 强平距离不足,两腿各减 50%%(高=%.6f 低=%.6f)", pos.Symbol, pos.HighLeg.Qty, pos.LowLeg.Qty), pos.Symbol)
		return nil
	}
	rebalDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	highInstr := e.lookupPerpInstrument(ctx, pos.HighVenue, highSym)
	lowInstr := e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym)
	highContract := e.venues[pos.HighVenue].Capabilities().ContractBasedPerp
	lowContract := e.venues[pos.LowVenue].Capabilities().ContractBasedPerp

	// reduceLeg 减一条腿(reduce-only),失败返回错误。
	reduceLeg := func(isHigh bool) error {
		if isHigh {
			bookH, _ := highTrader.FetchBookTicker(ctx, venue.MarketPerp, highSym)
			resH, errH := e.placeCrossVenueLegExec(ctx, highTrader, highSym, venue.SideBuy, halfHigh, crossVenueLegClientID(pos.IdemPrefix, "rh"), true, live, true, takerBps, rebalDeadline, highInstr, highContract)
			if errH != nil || resH.ExecutedQty <= executionQtyTolerance {
				rawErr := errH
				if rawErr == nil {
					rawErr = fmt.Errorf("executed_qty=%.6f", resH.ExecutedQty)
				}
				e.notifyCritical("跨所降风险失败", fmt.Sprintf("%s 高费腿 reduce-only 失败: %v", pos.Symbol, rawErr), pos.Symbol)
				return rawErr
			}
			e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceHigh, resH, crossVenueBookMid(bookH))
			return nil
		}
		bookL, _ := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, lowSym)
		resL, errL := e.placeCrossVenueLegExec(ctx, lowTrader, lowSym, venue.SideSell, halfLow, crossVenueLegClientID(pos.IdemPrefix, "rl"), true, live, true, takerBps, rebalDeadline, lowInstr, lowContract)
		if errL != nil || resL.ExecutedQty <= executionQtyTolerance {
			rawErr := errL
			if rawErr == nil {
				rawErr = fmt.Errorf("executed_qty=%.6f", resL.ExecutedQty)
			}
			e.notifyCritical("跨所降风险失败", fmt.Sprintf("%s 低费腿 reduce-only 失败: %v", pos.Symbol, rawErr), pos.Symbol)
			return rawErr
		}
		e.recordCrossVenueExecution(ctx, &pos, cvExecRebalanceLow, resL, crossVenueBookMid(bookL))
		return nil
	}

	// 先减「较大/过对冲」的那条腿(减它降低净方向敞口);相等时默认先减高费腿。
	firstIsHigh := pos.HighLeg.Qty >= pos.LowLeg.Qty
	var reduceErr error
	if err := reduceLeg(firstIsHigh); err != nil {
		// 第一条腿失败 → 立即中止第二条腿,绝不单边减仓制造更大失衡。
		e.syncCrossVenueBookLegQtysFromExchange(ctx, &pos, highSym, lowSym)
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所降风险中止",
			fmt.Sprintf("%s 首条腿 reduce-only 失败,已中止第二条腿以免加剧失衡: %v", pos.Symbol, err), pos.Symbol)
		return fmt.Errorf("降风险首腿失败,已中止第二腿: %w", err)
	}
	if err := reduceLeg(!firstIsHigh); err != nil {
		reduceErr = err
	}
	e.syncCrossVenueBookLegQtysFromExchange(ctx, &pos, highSym, lowSym)
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	if reduceErr != nil {
		return fmt.Errorf("降风险次腿失败(剩余失衡待下轮再平衡纠偏): %w", reduceErr)
	}
	e.notifyWarn("跨所同步降风险", fmt.Sprintf("%s 强平距离不足,两腿各减 50%%(高=%.6f 低=%.6f)", pos.Symbol, pos.HighLeg.Qty, pos.LowLeg.Qty), pos.Symbol)
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

func (e *CrossVenueExecutor) crossVenueOpenPlanFromPosition(ctx context.Context, pos CrossVenuePosition) crossVenueOpenPlan {
	highSym := venue.Symbol(crossVenuePositionHighSymbol(pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(pos))
	return crossVenueOpenPlan{
		Opp: CrossVenueOpportunity{Symbol: pos.Symbol, HighVenue: pos.HighVenue, LowVenue: pos.LowVenue},
		Gate: crossVenueGateResult{
			HighInstr: e.lookupPerpInstrument(ctx, pos.HighVenue, highSym),
			LowInstr:  e.lookupPerpInstrument(ctx, pos.LowVenue, lowSym),
		},
		IdemPrefix: pos.IdemPrefix,
	}
}

func (e *CrossVenueExecutor) fetchCrossVenueOpenLegCoinQtys(ctx context.Context, highV, lowV venue.Venue, highSym, lowSym venue.Symbol) (highQty, lowQty float64, err error) {
	if highV == nil || lowV == nil {
		return 0, 0, fmt.Errorf("venue 未就绪")
	}
	hp, errH := highV.FetchPositions(ctx)
	lp, errL := lowV.FetchPositions(ctx)
	if errH != nil || errL != nil {
		return 0, 0, fmt.Errorf("拉持仓失败 high=%v low=%v", errH, errL)
	}
	return absQty(perpPositionQty(hp, highSym)), absQty(perpPositionQty(lp, lowSym)), nil
}

func (e *CrossVenueExecutor) convergeCrossVenueOpenImbalance(ctx context.Context, pos *CrossVenuePosition, plan crossVenueOpenPlan, highQty, lowQty float64, live bool) error {
	if !live || pos == nil {
		return nil
	}
	rebalDeadline := crossVenueCloseLegDeadline(e.nowFn(), e.cfg.CrossVenueExecutor)
	takerBps := crossVenueTakerSlippageBps(e.cfg.CrossVenueExecutor)
	highSym := venue.Symbol(crossVenuePositionHighSymbol(*pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(*pos))
	highCaps := e.venues[pos.HighVenue].Capabilities()
	lowCaps := e.venues[pos.LowVenue].Capabilities()

	if highQty > lowQty+executionQtyTolerance {
		delta := normalizeVenueQty(highQty-lowQty, plan.Gate.HighInstr, highCaps.ContractBasedPerp)
		if delta <= executionQtyTolerance {
			return fmt.Errorf("收敛 delta 量化后为 0 (high=%.6f low=%.6f)", highQty, lowQty)
		}
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr == nil {
			return fmt.Errorf("高费腿交易接口缺失")
		}
		book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, highSym)
		res, err := e.placeCrossVenueLegExec(ctx, tr, highSym, venue.SideBuy, delta, crossVenueLegClientID(pos.IdemPrefix, "oc"), true, live, true, takerBps, rebalDeadline, plan.Gate.HighInstr, highCaps.ContractBasedPerp)
		if err != nil || res.ExecutedQty <= executionQtyTolerance {
			rawErr := err
			if rawErr == nil {
				rawErr = fmt.Errorf("executed_qty=%.6f", res.ExecutedQty)
			}
			return fmt.Errorf("收敛高费腿失败: %w", rawErr)
		}
		e.recordCrossVenueExecution(ctx, pos, cvExecRebalanceHigh, res, crossVenueBookMid(book))
		return nil
	}
	if lowQty > highQty+executionQtyTolerance {
		delta := normalizeVenueQty(lowQty-highQty, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
		if delta <= executionQtyTolerance {
			return fmt.Errorf("收敛 delta 量化后为 0 (high=%.6f low=%.6f)", highQty, lowQty)
		}
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr == nil {
			return fmt.Errorf("低费腿交易接口缺失")
		}
		book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, lowSym)
		res, err := e.placeCrossVenueLegExec(ctx, tr, lowSym, venue.SideSell, delta, crossVenueLegClientID(pos.IdemPrefix, "oc"), true, live, true, takerBps, rebalDeadline, plan.Gate.LowInstr, lowCaps.ContractBasedPerp)
		if err != nil || res.ExecutedQty <= executionQtyTolerance {
			rawErr := err
			if rawErr == nil {
				rawErr = fmt.Errorf("executed_qty=%.6f", res.ExecutedQty)
			}
			return fmt.Errorf("收敛低费腿失败: %w", rawErr)
		}
		e.recordCrossVenueExecution(ctx, pos, cvExecRebalanceLow, res, crossVenueBookMid(book))
	}
	return nil
}

func (e *CrossVenueExecutor) rollbackCrossVenueOpenImbalance(ctx context.Context, pos *CrossVenuePosition, highQty, lowQty float64, live bool, reason string) {
	if pos == nil {
		return
	}
	pos.HighLeg.Qty = highQty
	pos.LowLeg.Qty = lowQty
	if live {
		_ = e.closeCrossVenuePosition(ctx, *pos, live, reason)
	}
}

// verifyAndFinalizeCrossVenueOpen 以交易所真实持仓核验对冲;超阈值则 reduce-only 收敛,仍失败则回滚平仓。
func (e *CrossVenueExecutor) verifyAndFinalizeCrossVenueOpen(ctx context.Context, pos *CrossVenuePosition, plan crossVenueOpenPlan, live bool) (highQty, lowQty, imbalanceQty float64, err error) {
	if pos == nil {
		return 0, 0, 0, fmt.Errorf("position nil")
	}
	highSym := venue.Symbol(crossVenuePositionHighSymbol(*pos))
	lowSym := venue.Symbol(crossVenuePositionLowSymbol(*pos))
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]

	highQty, lowQty, err = e.fetchCrossVenueOpenLegCoinQtys(ctx, highV, lowV, highSym, lowSym)
	if err != nil {
		return 0, 0, 0, err
	}
	if highQty <= executionQtyTolerance || lowQty <= executionQtyTolerance {
		return highQty, lowQty, math.Abs(highQty - lowQty), fmt.Errorf("交易所一腿无持仓 high=%.6f low=%.6f", highQty, lowQty)
	}

	if live {
		for attempt := 0; attempt < 3 && qtyDeviation(highQty, lowQty) > crossVenueHedgeDeviationThreshold; attempt++ {
			if convErr := e.convergeCrossVenueOpenImbalance(ctx, pos, plan, highQty, lowQty, live); convErr != nil {
				e.rollbackCrossVenueOpenImbalance(ctx, pos, highQty, lowQty, live, "开仓对冲偏差收敛失败")
				return highQty, lowQty, math.Abs(highQty - lowQty), fmt.Errorf("开仓对冲偏差收敛失败: %w", convErr)
			}
			highQty, lowQty, err = e.fetchCrossVenueOpenLegCoinQtys(ctx, highV, lowV, highSym, lowSym)
			if err != nil {
				return 0, 0, 0, err
			}
		}
	}

	if qtyDeviation(highQty, lowQty) > crossVenueHedgeDeviationThreshold {
		if live {
			e.rollbackCrossVenueOpenImbalance(ctx, pos, highQty, lowQty, live, "开仓对冲偏差超阈值")
		}
		return highQty, lowQty, math.Abs(highQty - lowQty), fmt.Errorf("开仓两腿偏差 %.1f%% 超阈值", qtyDeviation(highQty, lowQty)*100)
	}
	return highQty, lowQty, math.Abs(highQty - lowQty), nil
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
