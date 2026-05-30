package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

const realtimeDrawdownMonitorInterval = 100 * time.Millisecond

type managedExposureTarget struct {
	Symbol    string
	Pair      string
	BaseAsset string
}

func (e *FundingRateSniperEngine) startRealtimeDrawdownMonitor(ctx context.Context) {
	if e == nil {
		return
	}
	e.realtimeRiskWorkerOnce.Do(func() {
		go e.runRealtimeDrawdownMonitor(ctx)
	})
}

func (e *FundingRateSniperEngine) wakeRealtimeDrawdownMonitor() {
	if e == nil || e.realtimeRiskWakeCh == nil {
		return
	}
	select {
	case e.realtimeRiskWakeCh <- struct{}{}:
	default:
	}
}

func (e *FundingRateSniperEngine) runRealtimeDrawdownMonitor(ctx context.Context) {
	ticker := time.NewTicker(realtimeDrawdownMonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-e.realtimeRiskWakeCh:
		}
		if shouldPanic, reason := e.shouldTriggerRealtimeDrawdownPanic(); shouldPanic {
			go e.panicFlattenManagedExposure(reason)
		}
	}
}

func (e *FundingRateSniperEngine) noteSpotAccountPositionUpdate(balances map[string]float64) {
	if e == nil {
		return
	}
	e.platformRiskMu.Lock()
	if e.spotRealtimeBalances == nil {
		e.spotRealtimeBalances = make(map[string]float64, len(balances))
	}
	for asset, balance := range balances {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" {
			continue
		}
		if balance <= executionQtyTolerance {
			delete(e.spotRealtimeBalances, asset)
			continue
		}
		e.spotRealtimeBalances[asset] = balance
	}
	e.lastSpotAccountUpdateAt = time.Now().UTC()
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
}

func (e *FundingRateSniperEngine) noteFuturesAccountUpdate(walletUSDT float64, positions []futuresRealtimePosition) {
	if e == nil {
		return
	}
	e.platformRiskMu.Lock()
	if walletUSDT > 0 {
		e.futuresRealtimeWalletUSDT = walletUSDT
	}
	if e.futuresRealtimePositions == nil {
		e.futuresRealtimePositions = make(map[string]futuresRealtimePosition, len(positions))
	}
	for _, position := range positions {
		symbol := strings.ToUpper(strings.TrimSpace(position.Symbol))
		if symbol == "" {
			continue
		}
		position.Symbol = symbol
		if math.Abs(position.PositionAmt) <= executionQtyTolerance && math.Abs(position.UnrealizedProfit) <= executionQtyTolerance {
			delete(e.futuresRealtimePositions, symbol)
			continue
		}
		e.futuresRealtimePositions[symbol] = position
	}
	e.lastFuturesAccountUpdateAt = time.Now().UTC()
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
}

func (e *FundingRateSniperEngine) shouldTriggerRealtimeDrawdownPanic() (bool, string) {
	if e == nil {
		return false, ""
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if e.panicFlattenInFlight {
		return false, ""
	}
	symbols := e.managedExposureSymbolsLocked()
	if len(symbols) == 0 {
		return false, ""
	}
	managedUnrealizedLoss := 0.0
	worst := make([]string, 0, len(symbols))
	for symbol := range symbols {
		position, ok := e.futuresRealtimePositions[symbol]
		if !ok {
			continue
		}
		if position.UnrealizedProfit < 0 {
			managedUnrealizedLoss += -position.UnrealizedProfit
			worst = append(worst, fmt.Sprintf("%s upnl=%.6f qty=%.8f", symbol, position.UnrealizedProfit, position.PositionAmt))
		}
	}
	if managedUnrealizedLoss <= 0 {
		return false, ""
	}
	if limit := e.cfg.RiskConfig.MaxCycleLossUSDT; limit > 0 && managedUnrealizedLoss+1e-9 >= limit {
		e.panicFlattenInFlight = true
		return true, fmt.Sprintf("实时浮亏触发单周期熔断: managedUnrealizedLoss=%.6f limit=%.6f details=%s", managedUnrealizedLoss, limit, strings.Join(worst, " | "))
	}
	if limit := e.cfg.RiskConfig.MaxDailyLossUSDT; limit > 0 {
		totalLoss := maxFloat(-e.dailyRealizedPnL, 0) + managedUnrealizedLoss
		if totalLoss+1e-9 >= limit {
			e.panicFlattenInFlight = true
			return true, fmt.Sprintf("实时浮亏触发日级熔断: realizedLoss=%.6f managedUnrealizedLoss=%.6f limit=%.6f details=%s", maxFloat(-e.dailyRealizedPnL, 0), managedUnrealizedLoss, limit, strings.Join(worst, " | "))
		}
	}
	return false, ""
}

func (e *FundingRateSniperEngine) managedExposureSymbolsLocked() map[string]struct{} {
	symbols := make(map[string]struct{}, len(e.activeOpenHedges)+len(e.exposureLocks))
	for _, hedge := range e.activeOpenHedges {
		if strings.TrimSpace(hedge.Target.Symbol) == "" {
			continue
		}
		symbols[strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol))] = struct{}{}
	}
	for symbol := range e.exposureLocks {
		if strings.TrimSpace(symbol) == "" {
			continue
		}
		symbols[strings.ToUpper(strings.TrimSpace(symbol))] = struct{}{}
	}
	return symbols
}

func (e *FundingRateSniperEngine) managedExposureTargets() []managedExposureTarget {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	targetsBySymbol := make(map[string]managedExposureTarget, len(e.activeOpenHedges)+len(e.exposureLocks))
	for _, hedge := range e.activeOpenHedges {
		symbol := strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol))
		if symbol == "" {
			continue
		}
		targetsBySymbol[symbol] = managedExposureTarget{
			Symbol:    symbol,
			Pair:      strings.ToUpper(strings.TrimSpace(hedge.Target.Pair)),
			BaseAsset: strings.ToUpper(strings.TrimSpace(hedge.Target.SpotBaseAsset)),
		}
	}
	for symbol := range e.exposureLocks {
		normalizedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
		if normalizedSymbol == "" {
			continue
		}
		target := targetsBySymbol[normalizedSymbol]
		target.Symbol = normalizedSymbol
		if target.Pair == "" {
			target.Pair = strings.ToUpper(strings.TrimSpace(e.goldenSpotPairByFuture[normalizedSymbol]))
		}
		if target.BaseAsset == "" && target.Pair != "" {
			if meta, ok := e.spotMeta[target.Pair]; ok {
				target.BaseAsset = strings.ToUpper(strings.TrimSpace(meta.BaseAsset))
			}
		}
		targetsBySymbol[normalizedSymbol] = target
	}
	targets := make([]managedExposureTarget, 0, len(targetsBySymbol))
	for _, target := range targetsBySymbol {
		targets = append(targets, target)
	}
	return targets
}

func (e *FundingRateSniperEngine) panicFlattenManagedExposure(reason string) {
	if e == nil {
		return
	}
	defer func() {
		e.platformRiskMu.Lock()
		e.panicFlattenInFlight = false
		e.platformRiskMu.Unlock()
		e.wakeRealtimeDrawdownMonitor()
	}()
	_ = e.tripPlatformFuse("realtime_drawdown", "critical", reason)
	targets := e.managedExposureTargets()
	if len(targets) == 0 {
		return
	}
	timeout := e.cfg.ExecutionConfig.ExecutionRecoveryTimeout
	if timeout < 12*time.Second {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		if e.logger != nil {
			e.logger.Printf("实时浮亏 panic flatten 获取合约仓位失败: %v", err)
		}
		return
	}
	spotBalances, _, err := e.fetchSpotAccountBalances(ctx)
	if err != nil && e.logger != nil {
		e.logger.Printf("实时浮亏 panic flatten 获取现货余额失败: %v", err)
	}
	for _, target := range targets {
		positionQty := positions[target.Symbol]
		if math.Abs(positionQty) > executionQtyTolerance {
			side := "BUY"
			if positionQty > 0 {
				side = "SELL"
			}
			if _, closeErr := e.placeSingleFuturesMarketOrderIdempotent(ctx, target.Symbol, side, math.Abs(positionQty), true, newClientOrderID("ftrim", target.Symbol)); closeErr != nil && e.logger != nil {
				e.logger.Printf("实时浮亏 panic flatten 合约平仓失败: symbol=%s qty=%.8f err=%v", target.Symbol, positionQty, closeErr)
			}
		}
		if target.Pair == "" || target.BaseAsset == "" || spotBalances == nil {
			continue
		}
		spotQty := spotBalances[target.BaseAsset]
		if spotQty <= executionQtyTolerance {
			continue
		}
		if _, sellErr := e.placeSpotMarketSellIdempotent(ctx, target.Pair, spotQty, newClientOrderID("srevert", target.Pair)); sellErr != nil && e.logger != nil {
			e.logger.Printf("实时浮亏 panic flatten 现货平仓失败: pair=%s qty=%.8f err=%v", target.Pair, spotQty, sellErr)
		}
	}
}
