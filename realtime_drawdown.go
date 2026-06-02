package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

const realtimeDrawdownMonitorInterval = 100 * time.Millisecond
const singleSymbolDrawdownTriggerShare = 0.5

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
		if symbol, reason := e.shouldTriggerManagedSymbolDrawdownCut(); symbol != "" {
			go e.flattenManagedSymbolExposure(symbol, reason)
			continue
		}
		if shouldPanic, reason := e.shouldTriggerRealtimeDrawdownPanic(); shouldPanic {
			go e.panicFlattenManagedExposure(reason)
		}
	}
}

func (e *FundingRateSniperEngine) shouldTriggerManagedSymbolDrawdownCut() (string, string) {
	if e == nil {
		return "", ""
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if e.panicFlattenInFlight {
		return "", ""
	}
	symbols := e.managedExposureSymbolsLocked()
	if len(symbols) == 0 {
		return "", ""
	}
	triggerLoss := 0.0
	if limit := e.cfg.RiskConfig.MaxCycleLossUSDT; limit > 0 {
		triggerLoss = limit * singleSymbolDrawdownTriggerShare
	}
	if limit := e.cfg.RiskConfig.MaxDailyLossUSDT; limit > 0 {
		remaining := maxFloat(limit-maxFloat(-e.riskBudgetState.DailyRealizedPnL, 0), 0)
		if remaining > 0 {
			triggerLoss = minPositiveFloat(triggerLoss, remaining*singleSymbolDrawdownTriggerShare)
		}
	}
	if triggerLoss <= 0 {
		return "", ""
	}
	worstSymbol := ""
	worstLoss := 0.0
	worstPosition := futuresRealtimePosition{}
	managedUnrealizedLoss := 0.0
	for symbol := range symbols {
		position, ok := e.futuresRealtimePositions[symbol]
		if !ok || position.UnrealizedProfit >= 0 {
			continue
		}
		loss := -position.UnrealizedProfit
		managedUnrealizedLoss += loss
		if loss <= worstLoss {
			continue
		}
		worstSymbol = symbol
		worstLoss = loss
		worstPosition = position
	}
	summary := fmt.Sprintf("tracked=%d managedLoss=%.6f trigger=%.6f", len(symbols), managedUnrealizedLoss, triggerLoss)
	if worstSymbol != "" {
		summary = fmt.Sprintf("%s worst=%s upnl=%.6f qty=%.8f", summary, worstSymbol, worstPosition.UnrealizedProfit, worstPosition.PositionAmt)
	}
	e.noteManagedDrawdownSummaryLocked(summary)
	if worstSymbol == "" || worstLoss+1e-9 < triggerLoss {
		return "", ""
	}
	if _, inFlight := e.symbolPanicFlattenInFlight[worstSymbol]; inFlight {
		return "", ""
	}
	if e.symbolPanicFlattenInFlight == nil {
		e.symbolPanicFlattenInFlight = make(map[string]time.Time)
	}
	e.symbolPanicFlattenInFlight[worstSymbol] = time.Now().UTC()
	return worstSymbol, fmt.Sprintf("单币实时浮亏触发优先止血: symbol=%s upnl=%.6f qty=%.8f trigger=%.6f", worstSymbol, worstPosition.UnrealizedProfit, worstPosition.PositionAmt, triggerLoss)
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
	summary := fmt.Sprintf("tracked=%d managedLoss=%.6f", len(symbols), managedUnrealizedLoss)
	if len(worst) > 0 {
		summary = fmt.Sprintf("%s worst=%s", summary, strings.Join(worst, " | "))
	}
	e.noteManagedDrawdownSummaryLocked(summary)
	if managedUnrealizedLoss <= 0 {
		return false, ""
	}
	if limit := e.cfg.RiskConfig.MaxCycleLossUSDT; limit > 0 && managedUnrealizedLoss+1e-9 >= limit {
		e.panicFlattenInFlight = true
		return true, fmt.Sprintf("实时浮亏触发单周期熔断: managedUnrealizedLoss=%.6f limit=%.6f details=%s", managedUnrealizedLoss, limit, strings.Join(worst, " | "))
	}
	if limit := e.cfg.RiskConfig.MaxDailyLossUSDT; limit > 0 {
		totalLoss := maxFloat(-e.riskBudgetState.DailyRealizedPnL, 0) + managedUnrealizedLoss
		if totalLoss+1e-9 >= limit {
			e.panicFlattenInFlight = true
			return true, fmt.Sprintf("实时浮亏触发日级熔断: realizedLoss=%.6f managedUnrealizedLoss=%.6f limit=%.6f details=%s", maxFloat(-e.riskBudgetState.DailyRealizedPnL, 0), managedUnrealizedLoss, limit, strings.Join(worst, " | "))
		}
	}
	return false, ""
}

func (e *FundingRateSniperEngine) noteManagedDrawdownSummaryLocked(summary string) {
	if e == nil {
		return
	}
	summary = strings.TrimSpace(summary)
	if e.managedDrawdownSummary == summary {
		return
	}
	e.managedDrawdownSummary = summary
	e.runtimeReporter.EmitLocked(EngineStage(""))
}

func (e *FundingRateSniperEngine) noteManagedDrawdownAction(action string, detail string) {
	if e == nil {
		return
	}
	e.platformRiskMu.Lock()
	e.lastManagedDrawdownAt = time.Now().UTC()
	e.lastManagedDrawdownAction = strings.TrimSpace(fmt.Sprintf("%s | %s", nonEmpty(action, "drawdown"), nonEmpty(detail, "-")))
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
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
	e.noteManagedDrawdownAction("global_panic_start", reason)
	defer func() {
		e.platformRiskMu.Lock()
		e.panicFlattenInFlight = false
		e.platformRiskMu.Unlock()
		e.wakeRealtimeDrawdownMonitor()
	}()
	_ = e.tripPlatformFuse("realtime_drawdown", "critical", reason)
	targets := e.managedExposureTargets()
	if len(targets) == 0 {
		e.noteManagedDrawdownAction("global_panic_skip", "无托管敞口可处理")
		return
	}
	timeout := e.cfg.ExecutionConfig.ExecutionRecoveryTimeout
	if timeout < 12*time.Second {
		timeout = 12 * time.Second
	}
	// 标记为恢复/平残腿请求：panic flatten 自身刚触发了 platform fuse，
	// 必须以恢复语义绕过该熔断与急停，才能把刚才的浮亏敞口真正平掉（仅 reduceOnly 减仓）。
	ctx, cancel := context.WithTimeout(withExposureManagerRequest(context.Background()), timeout)
	defer cancel()

	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		e.noteManagedDrawdownAction("global_panic_fetch_failed", err.Error())
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
	e.noteManagedDrawdownAction("global_panic_completed", fmt.Sprintf("targets=%d", len(targets)))
}

func (e *FundingRateSniperEngine) managedOpenHedgeBySymbol(symbol string) (OpenHedge, bool) {
	if e == nil {
		return OpenHedge{}, false
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return OpenHedge{}, false
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	for _, hedge := range e.activeOpenHedges {
		if strings.EqualFold(strings.TrimSpace(hedge.Target.Symbol), normalized) {
			return hedge, true
		}
	}
	return OpenHedge{}, false
}

func (e *FundingRateSniperEngine) removeManagedOpenHedgeSymbol(symbol string) []OpenHedge {
	if e == nil {
		return nil
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	e.platformRiskMu.Lock()
	filtered := make([]OpenHedge, 0, len(e.activeOpenHedges))
	labels := make([]string, 0, len(e.activeOpenHedges))
	for _, hedge := range e.activeOpenHedges {
		if strings.EqualFold(strings.TrimSpace(hedge.Target.Symbol), normalized) {
			continue
		}
		filtered = append(filtered, hedge)
		if hedge.HedgeQty <= 0 {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s %s %.8f", hedge.Target.Symbol, hedge.carryDirection().positionLabel(hedge.Target.SpotBaseAsset, hedge.Target.Symbol), hedge.HedgeQty))
	}
	e.activeOpenHedges = filtered
	e.currentPositions = labels
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
	return append([]OpenHedge(nil), filtered...)
}

func (e *FundingRateSniperEngine) flattenManagedSymbolExposure(symbol string, reason string) {
	if e == nil {
		return
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	defer func() {
		e.platformRiskMu.Lock()
		delete(e.symbolPanicFlattenInFlight, normalized)
		e.platformRiskMu.Unlock()
		e.wakeRealtimeDrawdownMonitor()
	}()
	if normalized == "" {
		return
	}
	e.noteManagedDrawdownAction("symbol_cut_start", fmt.Sprintf("%s | %s", normalized, reason))
	e.setSymbolCooldown(normalized, "realtime_drawdown_symbol", "critical", reason)
	_ = e.recordExecutionIncident("realtime_drawdown_symbol", normalized, reason)
	e.recordExecutionAction("realtime_drawdown_symbol", normalized, "poison_symbol_flatten_start", reason)
	if hedge, ok := e.managedOpenHedgeBySymbol(normalized); ok {
		timeout := e.cfg.ExecutionRecoveryTimeout
		if timeout < 12*time.Second {
			timeout = 12 * time.Second
		}
		ctx, cancel := context.WithTimeout(withExposureManagerRequest(context.Background()), timeout)
		defer cancel()
		alerts, err := e.closeHedgePositionParallel(ctx, closeSequencePlan{
			hedge:             &hedge,
			futuresOrder:      e.buildProtectedCloseFuturesOrder(ctx, hedge),
			preferPassiveSpot: false,
		})
		if err == nil {
			filtered := e.removeManagedOpenHedgeSymbol(normalized)
			if persistErr := e.syncPersistedOpenHedges(filtered); persistErr != nil && e.logger != nil {
				e.logger.Printf("单币实时止血后同步未平仓台账失败: symbol=%s err=%v", normalized, persistErr)
			}
			e.noteManagedDrawdownAction("symbol_cut_completed", fmt.Sprintf("%s | alerts=%s", normalized, strings.Join(alerts, "; ")))
			e.recordExecutionAction("realtime_drawdown_symbol", normalized, "poison_symbol_flatten_completed", strings.Join(alerts, "; "))
			return
		}
		e.noteManagedDrawdownAction("symbol_cut_parallel_close_failed", fmt.Sprintf("%s | %v", normalized, err))
		if e.logger != nil {
			e.logger.Printf("单币实时止血平仓失败，symbol=%s err=%v", normalized, err)
		}
	}
	timeout := e.cfg.ExecutionRecoveryTimeout
	if timeout < 12*time.Second {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(withExposureManagerRequest(context.Background()), timeout)
	defer cancel()
	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		e.noteManagedDrawdownAction("symbol_cut_fetch_failed", fmt.Sprintf("%s | %v", normalized, err))
		if e.logger != nil {
			e.logger.Printf("单币实时止血获取合约仓位失败: symbol=%s err=%v", normalized, err)
		}
		return
	}
	positionQty := positions[normalized]
	if math.Abs(positionQty) <= executionQtyTolerance {
		e.noteManagedDrawdownAction("symbol_cut_skip", fmt.Sprintf("%s | futures position already flat", normalized))
		return
	}
	side := "BUY"
	if positionQty > 0 {
		side = "SELL"
	}
	if _, closeErr := e.placeSingleFuturesMarketOrderIdempotent(ctx, normalized, side, math.Abs(positionQty), true, newClientOrderID("ftrim", normalized)); closeErr != nil {
		e.noteManagedDrawdownAction("symbol_cut_futures_trim_failed", fmt.Sprintf("%s | qty=%.8f | %v", normalized, positionQty, closeErr))
		if e.logger != nil {
			e.logger.Printf("单币实时止血合约兜底平仓失败: symbol=%s qty=%.8f err=%v", normalized, positionQty, closeErr)
		}
		return
	}
	e.noteManagedDrawdownAction("symbol_cut_futures_trimmed", fmt.Sprintf("%s | qty=%.8f", normalized, positionQty))
	e.recordExecutionAction("realtime_drawdown_symbol", normalized, "poison_symbol_futures_trimmed", fmt.Sprintf("qty=%.8f", positionQty))
}
