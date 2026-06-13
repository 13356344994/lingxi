package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// cross_venue_open.go 跨所开仓/平仓/回滚执行协议。

type crossVenueTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

const crossVenueConfirmTimeout = 8 * time.Second
const crossVenueConfirmPoll = 400 * time.Millisecond

// crossVenueOpenPlan 是通过闸门后的开仓计划。
type crossVenueOpenPlan struct {
	Opp        CrossVenueOpportunity
	Gate       crossVenueGateResult
	Qty        float64
	RefPrice   float64
	IdemPrefix string
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
	sym := venue.Symbol(plan.Opp.Symbol)
	highV := e.venues[plan.Opp.HighVenue]
	lowV := e.venues[plan.Opp.LowVenue]
	highTrader, _ := highV.(crossVenueTrader)
	lowTrader, _ := lowV.(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return CrossVenuePosition{}, fmt.Errorf("venue 不满足交易接口")
	}

	pos := CrossVenuePosition{
		Symbol:        plan.Opp.Symbol,
		HighVenue:     plan.Opp.HighVenue,
		LowVenue:      plan.Opp.LowVenue,
		OpenSpreadAPR: plan.Opp.SpreadAPR,
		Status:        CrossVenueStatusOpeningFirstLeg,
		OpenedAt:      e.nowFn(),
		UpdatedAt:     e.nowFn(),
		IdemPrefix:    plan.IdemPrefix,
		NotionalUSDT:  plan.Gate.NotionalUSDT,
		HighLeg:       CrossVenueLegState{Venue: plan.Opp.HighVenue, Side: "sell"},
		LowLeg:        CrossVenueLegState{Venue: plan.Opp.LowVenue, Side: "buy"},
	}
	if live {
		_ = e.persistPosition(pos)
	}

	highBook, err := highTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		return pos, err
	}
	highPrice := iocLimitPrice(venue.SideSell, highBook, cfg.SlippageBudgetBps)
	highID := crossVenueLegClientID(plan.IdemPrefix, "h1")
	if !live {
		e.logger.Printf("[影子] 本应开仓 %s: 空%s/多%s 各≈%.6f 名义≈%.2fU 价差年化%.1f%%",
			plan.Opp.Symbol, plan.Opp.HighVenue, plan.Opp.LowVenue, plan.Qty, plan.Gate.NotionalUSDT, plan.Opp.SpreadAPR*100)
		return CrossVenuePosition{}, nil
	}
	highCaps := highV.Capabilities()
	lowCaps := lowV.Capabilities()

	highRes, err := e.placeCrossVenueIOC(ctx, highTrader, sym, venue.SideSell, plan.Qty, highPrice, highID, false, live)
	if err != nil || highRes.ExecutedQty <= executionQtyTolerance {
		return pos, fmt.Errorf("第一腿失败: %w", err)
	}
	pos.HighLeg.Qty = highRes.ExecutedQty
	pos.HighLeg.EntryPrice = highRes.AvgPrice
	pos.HighLeg.OrderIDs = []string{highID}
	pos.Status = CrossVenueStatusOpeningSecondLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	nakedStart := e.nowFn()
	lowBook, err := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		e.rollbackFirstLeg(ctx, plan, highTrader, sym, pos.HighLeg.Qty, live)
		e.setSymbolCooldown(plan.Opp.Symbol)
		return pos, err
	}
	lowPrice := iocLimitPrice(venue.SideBuy, lowBook, cfg.SlippageBudgetBps)
	lowID := crossVenueLegClientID(plan.IdemPrefix, "l1")
	hedgeQty := highRes.ExecutedQty
	if lowCaps.ContractBasedPerp {
		hedgeQty = normalizeVenueQty(highRes.ExecutedQty, plan.Gate.LowInstr, true)
		if hedgeQty <= executionQtyTolerance {
			e.rollbackFirstLeg(ctx, plan, highTrader, sym, pos.HighLeg.Qty, live)
			e.setSymbolCooldown(plan.Opp.Symbol)
			return pos, fmt.Errorf("第二腿对冲数量经张数步进后为 0(第一腿 %.6f)", highRes.ExecutedQty)
		}
	}
	lowRes, lowErr := e.placeCrossVenueIOC(ctx, lowTrader, sym, venue.SideBuy, hedgeQty, lowPrice, lowID, false, live)
	if lowErr != nil || lowRes.ExecutedQty <= executionQtyTolerance {
		elapsed := e.nowFn().Sub(nakedStart).Seconds()
		e.rollbackFirstLeg(ctx, plan, highTrader, sym, pos.HighLeg.Qty, live)
		e.setSymbolCooldown(plan.Opp.Symbol)
		msg := fmt.Sprintf("%s 第二腿失败,已回滚第一腿(裸腿 %.0fs)", plan.Opp.Symbol, elapsed)
		e.notifyWarn("跨所开仓回滚", msg, plan.Opp.Symbol)
		if elapsed > float64(cfg.MaxNakedSeconds) {
			e.notifyCritical("跨所裸腿超时", fmt.Sprintf("%s 裸腿暴露 %.0fs 超过阈值 %ds", plan.Opp.Symbol, elapsed, cfg.MaxNakedSeconds), plan.Opp.Symbol)
		}
		return pos, fmt.Errorf("第二腿失败: %w", lowErr)
	}
	highCoin := crossVenueLegExecutedCoinQty(highCaps, plan.Gate.HighInstr, highRes, 0)
	lowCoin := crossVenueLegExecutedCoinQty(lowCaps, plan.Gate.LowInstr, lowRes, highCoin)
	highCoin = crossVenueLegExecutedCoinQty(highCaps, plan.Gate.HighInstr, highRes, lowCoin)
	lowCoin = crossVenueLegExecutedCoinQty(lowCaps, plan.Gate.LowInstr, lowRes, highCoin)
	bookQty := reconcileCrossVenueOpenBookQty(highCaps, lowCaps, highCoin, lowCoin)
	pos.HighLeg.Qty = bookQty
	pos.LowLeg.Qty = bookQty
	pos.LowLeg.EntryPrice = lowRes.AvgPrice
	pos.LowLeg.OrderIDs = []string{lowID}
	pos.NotionalUSDT = crossVenueBookNotionalUSDT(bookQty, plan.RefPrice, highRes, lowRes)
	pos.Status = CrossVenueStatusOpen
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.notifyTrade("跨所开仓", fmt.Sprintf("%s 空%s/多%s 各≈%.6f 价差年化%.1f%%",
		plan.Opp.Symbol, plan.Opp.HighVenue, plan.Opp.LowVenue, bookQty, plan.Opp.SpreadAPR*100), plan.Opp.Symbol)
	return pos, nil
}

func (e *CrossVenueExecutor) rollbackFirstLeg(ctx context.Context, plan crossVenueOpenPlan, tr crossVenueTrader, sym venue.Symbol, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	book, err := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		e.logger.Printf("[跨所] 回滚第一腿取盘口失败 %s: %v", sym, err)
		return
	}
	price := iocLimitPrice(venue.SideBuy, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps)
	id := crossVenueLegClientID(plan.IdemPrefix, "rb")
	_, err = e.placeCrossVenueIOC(ctx, tr, sym, venue.SideBuy, qty, price, id, true, live)
	if err != nil {
		e.logger.Printf("[跨所] 回滚第一腿失败 %s: %v", sym, err)
		e.notifyCritical("跨所回滚失败", fmt.Sprintf("%s 第一腿 reduce-only 回滚失败: %v", sym, err), string(sym))
	}
}

// crossVenueCloseCriticalAttempts 平仓连续未完成多少轮后升级 critical 告警。
const crossVenueCloseCriticalAttempts = 3

// closeCrossVenuePosition 平仓协议。live 模式下逐腿 IOC 平仓后**核验真实成交**(ExecutedQty 对账 +
// FetchPositions 复核):任一腿未平干净 → 保持 closing 状态、更新剩余 Qty、**不删台账**,下轮主循环
// 重试(manageCrossVenuePosition 的 closing 分支);连续 ≥3 轮平不掉升级 critical 告警。
// 影子模式无真仓,记日志后直接收尾删台账(行为不变)。
func (e *CrossVenueExecutor) closeCrossVenuePosition(ctx context.Context, pos CrossVenuePosition, live bool, reason string) error {
	pos.Status = CrossVenueStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	sym := venue.Symbol(pos.Symbol)
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]
	highTrader, _ := highV.(crossVenueTrader)
	lowTrader, _ := lowV.(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}

	if !live {
		// 影子:没有真仓,日志后直接收尾。
		if pos.HighLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 高费腿 %s 买回 %.6f 原因:%s", pos.Symbol, pos.HighVenue, pos.HighLeg.Qty, reason)
		}
		if pos.LowLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 低费腿 %s 卖出 %.6f 原因:%s", pos.Symbol, pos.LowVenue, pos.LowLeg.Qty, reason)
		}
		pos.Status = CrossVenueStatusClosed
		pos.UpdatedAt = e.nowFn()
		_ = e.removeCrossVenuePosition(pos.Symbol)
		e.notifyTrade("跨所平仓", fmt.Sprintf("%s %s", pos.Symbol, reason), pos.Symbol)
		return nil
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	// 逐腿 reduce-only IOC,按返回的 ExecutedQty 先行对账(clientID 带轮次后缀,跨轮重试不撞幂等去重)。
	if pos.HighLeg.Qty > executionQtyTolerance {
		book, berr := highTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if berr == nil {
			price := iocLimitPrice(venue.SideBuy, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps)
			id := crossVenueLegClientID(pos.IdemPrefix, fmt.Sprintf("ch%d", attempt))
			if res, perr := e.placeCrossVenueIOC(ctx, highTrader, sym, venue.SideBuy, pos.HighLeg.Qty, price, id, true, live); perr == nil {
				pos.HighLeg.Qty = math.Max(pos.HighLeg.Qty-res.ExecutedQty, 0)
			} else {
				e.logger.Printf("[跨所] 平仓高费腿 IOC 失败 %s(下轮重试): %v", pos.Symbol, perr)
			}
		}
	}
	if pos.LowLeg.Qty > executionQtyTolerance {
		book, berr := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if berr == nil {
			price := iocLimitPrice(venue.SideSell, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps)
			id := crossVenueLegClientID(pos.IdemPrefix, fmt.Sprintf("cl%d", attempt))
			if res, perr := e.placeCrossVenueIOC(ctx, lowTrader, sym, venue.SideSell, pos.LowLeg.Qty, price, id, true, live); perr == nil {
				pos.LowLeg.Qty = math.Max(pos.LowLeg.Qty-res.ExecutedQty, 0)
			} else {
				e.logger.Printf("[跨所] 平仓低费腿 IOC 失败 %s(下轮重试): %v", pos.Symbol, perr)
			}
		}
	}
	// 权威复核:以交易所真实仓位为准刷新剩余量(IOC 回执可能不完整;拉失败保留本地对账值,保守)。
	if hp, herr := highV.FetchPositions(ctx); herr == nil {
		pos.HighLeg.Qty = absQty(perpPositionQty(hp, sym))
	}
	if lp, lerr := lowV.FetchPositions(ctx); lerr == nil {
		pos.LowLeg.Qty = absQty(perpPositionQty(lp, sym))
	}

	if pos.HighLeg.Qty <= executionQtyTolerance && pos.LowLeg.Qty <= executionQtyTolerance {
		pos.Status = CrossVenueStatusClosed
		pos.UpdatedAt = e.nowFn()
		_ = e.removeCrossVenuePosition(pos.Symbol)
		e.notifyTrade("跨所平仓", fmt.Sprintf("%s %s(第 %d 轮平仓完成)", pos.Symbol, pos.CloseReason, attempt), pos.Symbol)
		return nil
	}

	// 未平干净:保持 closing、保留台账,下轮主循环重试。
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[跨所] 平仓未完成 %s(第 %d 轮): 高费腿剩 %.6f 低费腿剩 %.6f,保留台账下轮重试",
		pos.Symbol, attempt, pos.HighLeg.Qty, pos.LowLeg.Qty)
	if attempt >= crossVenueCloseCriticalAttempts {
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
	sym := venue.Symbol(pos.Symbol)
	highTrader, _ := e.venues[pos.HighVenue].(crossVenueTrader)
	lowTrader, _ := e.venues[pos.LowVenue].(crossVenueTrader)
	if highTrader == nil || lowTrader == nil {
		return fmt.Errorf("交易接口缺失")
	}
	bookH, _ := highTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
	bookL, _ := lowTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
	idH := crossVenueLegClientID(pos.IdemPrefix, "rh")
	idL := crossVenueLegClientID(pos.IdemPrefix, "rl")
	if live {
		_, _ = e.placeCrossVenueIOC(ctx, highTrader, sym, venue.SideBuy, halfHigh, iocLimitPrice(venue.SideBuy, bookH, e.cfg.CrossVenueExecutor.SlippageBudgetBps), idH, true, live)
		_, _ = e.placeCrossVenueIOC(ctx, lowTrader, sym, venue.SideSell, halfLow, iocLimitPrice(venue.SideSell, bookL, e.cfg.CrossVenueExecutor.SlippageBudgetBps), idL, true, live)
	} else {
		e.logger.Printf("[影子] 本应同步降风险 %s 各减 50%%", pos.Symbol)
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
		if p.Symbol == sym {
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
