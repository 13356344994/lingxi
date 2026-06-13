package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
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
	cfg := e.cfg.CrossVenueExecutor
	sym := venue.Symbol(pos.Symbol)
	highV := e.venues[pos.HighVenue]
	lowV := e.venues[pos.LowVenue]
	if highV == nil || lowV == nil {
		return pos, false
	}

	// closing 续平分支:上一轮平仓未完成(IOC 没吃干净)的持仓,直接重试平仓直到两腿都清零,
	// 不再走开仓期风控(孤腿/同步性都是「在场对冲」语义,半平状态会误判)。
	if pos.Status == CrossVenueStatusClosing {
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

	// 孤腿检测
	if highOpen != lowOpen {
		e.handleOrphanLeg(ctx, pos, highOpen, lowOpen, live, highAmt, lowAmt)
		return pos, true
	}
	if !highOpen && !lowOpen {
		pos.Status = CrossVenueStatusClosed
		pos.UpdatedAt = e.nowFn()
		_ = e.removeCrossVenuePosition(pos.Symbol)
		return pos, true
	}

	// 同步性核验
	dev := qtyDeviation(highAmt, lowAmt)
	if dev > 0.05 {
		e.rebalanceHedge(ctx, pos, highAmt, lowAmt, live)
		e.notifyWarn("跨所对冲偏差", fmt.Sprintf("%s 两腿数量偏差 %.1f%%", pos.Symbol, dev*100), pos.Symbol)
	}

	// 强平距离
	minDist := cfg.MinLiqDistance
	if minDist <= 0 {
		minDist = 0.15
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

	// 资金费记账(估计,按真实经过时长折算)
	pos.EstFundingUSDT += e.estimateFundingAccrual(&pos)
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

func (e *CrossVenueExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenuePosition, highOpen, lowOpen, live bool, highAmt, lowAmt float64) {
	pos.Status = CrossVenueStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	sym := venue.Symbol(pos.Symbol)
	if highOpen && !lowOpen {
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(highAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if live {
				_, _ = e.placeCrossVenueIOC(ctx, tr, sym, venue.SideBuy, qty, iocLimitPrice(venue.SideBuy, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps), crossVenueLegClientID(pos.IdemPrefix, "oh"), true, live)
			}
		}
	} else if lowOpen && !highOpen {
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil {
			qty := absQty(lowAmt)
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			if live {
				_, _ = e.placeCrossVenueIOC(ctx, tr, sym, venue.SideSell, qty, iocLimitPrice(venue.SideSell, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps), crossVenueLegClientID(pos.IdemPrefix, "ol"), true, live)
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
	if hAbs > lAbs {
		delta := hAbs - lAbs
		tr, _ := e.venues[pos.HighVenue].(crossVenueTrader)
		if tr != nil && live {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			_, _ = e.placeCrossVenueIOC(ctx, tr, sym, venue.SideBuy, delta, iocLimitPrice(venue.SideBuy, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps), crossVenueLegClientID(pos.IdemPrefix, "bh"), true, live)
		}
		pos.HighLeg.Qty = lAbs
	} else {
		delta := lAbs - hAbs
		tr, _ := e.venues[pos.LowVenue].(crossVenueTrader)
		if tr != nil && live {
			book, _ := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
			_, _ = e.placeCrossVenueIOC(ctx, tr, sym, venue.SideSell, delta, iocLimitPrice(venue.SideSell, book, e.cfg.CrossVenueExecutor.SlippageBudgetBps), crossVenueLegClientID(pos.IdemPrefix, "bl"), true, live)
		}
		pos.LowLeg.Qty = hAbs
	}
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
}

// estimateFundingAccrual 估算自上次记账以来的资金费收益增量:按 now−LastAccrualAt 的**真实小时数**
// 折算(主循环 60s 一轮时每轮只记 1/60 小时的量,绝不按「本轮=1小时」虚高)。费率从扫描器缓存查
// (绝不现场拉全量);缓存缺失时返回 0 且**不推进** LastAccrualAt,下轮拿到数据后把窗口补上。
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
	pos.LastAccrualAt = now
	return notional * (hHourly - lHourly) * elapsedHours
}

func (e *CrossVenueExecutor) shouldCloseCrossVenue(ctx context.Context, pos *CrossVenuePosition) string {
	if pos == nil {
		return ""
	}
	cfg := e.cfg.CrossVenueExecutor
	maxHold := time.Duration(cfg.MaxHoldHours) * time.Hour
	if maxHold <= 0 {
		maxHold = 72 * time.Hour
	}
	if e.nowFn().Sub(pos.OpenedAt) > maxHold {
		return fmt.Sprintf("持仓超过 %dh", cfg.MaxHoldHours)
	}
	spread := e.currentSpreadAPR(ctx, pos)
	closeAPR := cfg.CloseSpreadAPR
	if closeAPR <= 0 {
		closeAPR = 0.3
	}
	if spread < closeAPR {
		pos.CloseSpreadStreak++
		need := cfg.CloseSpreadConsecutiveRounds
		if need <= 0 {
			need = 3
		}
		if pos.CloseSpreadStreak >= need {
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
	if !live {
		return
	}
	cd := time.Duration(e.cfg.CrossVenueExecutor.DeadManCountdownSeconds) * time.Second
	if cd <= 0 {
		cd = 60 * time.Second
	}
	for _, name := range []string{pos.HighVenue, pos.LowVenue} {
		v := e.venues[name]
		if v == nil || !v.Capabilities().HasNativeDeadMan {
			continue
		}
		e.mu.RLock()
		_, skip := e.deadManUnsupportedVenues[name]
		e.mu.RUnlock()
		if skip {
			continue
		}
		if err := v.ArmDeadMansSwitch(ctx, cd); err != nil {
			if isDeadManUnsupportedErr(err) {
				e.mu.Lock()
				e.deadManUnsupportedVenues[name] = struct{}{}
				e.mu.Unlock()
				continue
			}
			e.logger.Printf("[跨所] dead-man 续期失败 venue=%s: %v", name, err)
		}
	}
}
