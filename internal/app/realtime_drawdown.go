package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"
)

const realtimeDrawdownMonitorInterval = 100 * time.Millisecond
const singleSymbolDrawdownTriggerShare = 0.5

// realtimeSpotPriceStaleAfter 现货 book ticker 超过该时长视为过期不可信,组合浮亏估值回退到合约腿口径。
const realtimeSpotPriceStaleAfter = 15 * time.Second

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
		if !ok {
			continue
		}
		combined, _ := e.combinedManagedUnrealizedLocked(symbol, position.UnrealizedProfit)
		if combined >= 0 {
			continue
		}
		loss := -combined
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
		combined, spotIncluded := e.combinedManagedUnrealizedLocked(symbol, position.UnrealizedProfit)
		if combined < 0 {
			managedUnrealizedLoss += -combined
			legNote := "fut+spot"
			if !spotIncluded {
				legNote = "fut-only"
			}
			worst = append(worst, fmt.Sprintf("%s combined=%.6f(%s) futUpnl=%.6f qty=%.8f", symbol, combined, legNote, position.UnrealizedProfit, position.PositionAmt))
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

// combinedManagedUnrealizedLocked 计算单个 symbol 的「组合级」实时浮动盈亏(合约腿 + 现货腿)。
// 修复 P0:旧口径只累计合约腿 UnrealizedProfit,对 delta 中性对冲会让「合约腿单边浮亏」与组合真实
// 盈亏脱钩——价格不利于合约腿时现货腿等额获利、组合接近中性,旧逻辑却会提前误触熔断;反之两腿漂移
// /缺腿导致的真实组合亏损才是该抓的风险。现货腿用实时 book ticker 估值,价缺失/过期时回退到合约腿
// 口径(对已对冲仓位这是高估亏损的保守方向),并以 spotIncluded=false 告知调用方。
// 调用方必须持有 platformRiskMu。
func (e *FundingRateSniperEngine) combinedManagedUnrealizedLocked(symbol string, futuresUPnL float64) (combined float64, spotIncluded bool) {
	hedge, ok := e.activeOpenHedgeBySymbolLocked(symbol)
	if !ok {
		return futuresUPnL, false
	}
	spotPnL, ok := e.spotLegUnrealizedLocked(hedge)
	if !ok {
		return futuresUPnL, false
	}
	return futuresUPnL + spotPnL, true
}

// activeOpenHedgeBySymbolLocked 在已持有 platformRiskMu 的前提下按 symbol 查找受管对冲(无锁版本,
// 避免与 managedOpenHedgeBySymbol 的二次加锁死锁)。
func (e *FundingRateSniperEngine) activeOpenHedgeBySymbolLocked(symbol string) (OpenHedge, bool) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return OpenHedge{}, false
	}
	for i := range e.activeOpenHedges {
		if strings.ToUpper(strings.TrimSpace(e.activeOpenHedges[i].Target.Symbol)) == normalized {
			return e.activeOpenHedges[i], true
		}
	}
	return OpenHedge{}, false
}

// spotLegUnrealizedLocked 用实时现货 book ticker 估算现货腿的浮动盈亏。
// 正 carry 现货多头:(mid-entry)*qty;负 carry 借币空头:(entry-mid)*qty。
// 现货价缺失/过期或入场价不可得 → 返回 ok=false,由调用方回退。
func (e *FundingRateSniperEngine) spotLegUnrealizedLocked(hedge OpenHedge) (float64, bool) {
	pair := strings.ToUpper(strings.TrimSpace(hedge.Target.Pair))
	if pair == "" || e.spotTickerCache == nil {
		return 0, false
	}
	baseQty := parseFloat(hedge.SpotBaseQty)
	if baseQty <= executionQtyTolerance {
		baseQty = hedge.HedgeQty
	}
	if baseQty <= executionQtyTolerance || hedge.OpenSpotQuoteQty <= 0 {
		return 0, false
	}
	entryPrice := hedge.OpenSpotQuoteQty / baseQty
	if entryPrice <= 0 {
		return 0, false
	}
	ticker, receivedAt, ok := e.spotTickerCache.GetWithReceivedAt(pair)
	if !ok {
		return 0, false
	}
	if receivedAt.IsZero() || time.Since(receivedAt) > realtimeSpotPriceStaleAfter {
		return 0, false
	}
	bid := parseFloat(ticker.BidPrice)
	ask := parseFloat(ticker.AskPrice)
	mid := 0.0
	switch {
	case bid > 0 && ask > 0:
		mid = (bid + ask) / 2
	case bid > 0:
		mid = bid
	case ask > 0:
		mid = ask
	default:
		return 0, false
	}
	sign := 1.0
	if hedge.carryDirection() == fundingCarryDirectionNegative {
		sign = -1.0
	}
	return (mid - entryPrice) * baseQty * sign, true
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
	return e.portfolioManager.managedExposureSymbols()
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

// requestPanicHardStopIfEnabled 在配置开启 PanicFlattenHardStopEnabled(P5-2 opt-in)时,标记 panic-flatten 后
// 硬停机原因;主循环 checkLiveSafetyGuards 据此返回 fatalEngineError 永久停机,要求人工核对后重启(而非熔断冷却
// 自动复活)。开关关闭(默认)时为 no-op,零行为变更。仅首次触发记录原因(后续触发不覆盖,保留最初根因)。
func (e *FundingRateSniperEngine) requestPanicHardStopIfEnabled(reason string) {
	if e == nil || !e.cfg.RiskConfig.PanicFlattenHardStopEnabled {
		return
	}
	e.platformRiskMu.Lock()
	first := e.panicHardStopReason == ""
	if first {
		e.panicHardStopReason = reason
	}
	e.platformRiskMu.Unlock()
	if first && e.logger != nil {
		e.logger.Printf("[硬停机] 实时浮亏 panic-flatten 已触发,配置要求停机待人工核对(PanicFlattenHardStopEnabled): %s", reason)
	}
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
	e.requestPanicHardStopIfEnabled(reason)
	targets := e.managedExposureTargets()
	if len(targets) == 0 {
		e.noteManagedDrawdownAction("global_panic_skip", "无托管敞口可处理")
		return
	}
	// 修 FIX_PLAN #6:快照「单币止血并发收敛中」的标的,全局 panic 跳过它们,避免对同一对冲重复下平仓单。
	// 此刻 panicFlattenInFlight 已置 true,触发函数已阻断新的单币止血启动,故快照即权威(不会漏新增)。
	// 被跳过的标的由其单币止血路径完成收敛(同样方向感知双腿平 + 同套恢复兜底),不留敞口。
	inflightCuts := e.snapshotSymbolCutsInFlight()
	timeout := e.cfg.ExecutionConfig.ExecutionRecoveryTimeout
	if timeout < 12*time.Second {
		timeout = 12 * time.Second
	}
	// 标记为恢复/平残腿请求：panic flatten 自身刚触发了 platform fuse，
	// 必须以恢复语义绕过该熔断与急停，才能把刚才的浮亏敞口真正平掉（仅 reduceOnly 减仓）。
	parent := context.Background()
	if e.runCtx != nil {
		parent = e.runCtx
	}
	ctx, cancel := context.WithTimeout(withExposureManagerRequest(parent), timeout)
	defer cancel()

	// 合约实仓拉取失败不再直接放弃：它只用于「无跟踪对冲的孤儿残腿」兜底，下面方向感知的双腿平仓
	// 不依赖它，因此即便拉取失败也要继续尽力收敛已跟踪的对冲。
	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		positions = nil
		e.noteManagedDrawdownAction("global_panic_fetch_failed", err.Error())
		if e.logger != nil {
			e.logger.Printf("实时浮亏 panic flatten 获取合约仓位失败(转为方向感知平仓+尽力兜底): %v", err)
		}
	}
	spotBalances, _, err := e.fetchSpotAccountBalances(ctx)
	if err != nil && e.logger != nil {
		e.logger.Printf("实时浮亏 panic flatten 获取现货余额失败: %v", err)
	}
	for _, target := range targets {
		if _, cutting := inflightCuts[strings.ToUpper(strings.TrimSpace(target.Symbol))]; cutting {
			e.noteManagedDrawdownAction("global_panic_skip_symbol_cut", target.Symbol)
			continue
		}
		// 优先用方向感知的双腿平仓：正 carry(现货 SELL)与负 carry(全仓杠杆 BUY + AUTO_BORROW_REPAY 还币)
		// 都能正确收敛。这修复了旧全局 panic「只 SELL 现货钱包正余额、对负 carry 杠杆空头完全无效」的窟窿(R3)。
		hedge, hedgeTracked := e.managedOpenHedgeBySymbol(target.Symbol)
		if hedgeTracked {
			alerts, closeErr := e.closeHedgePositionParallel(ctx, closeSequencePlan{
				hedge:             &hedge,
				futuresOrder:      e.buildProtectedCloseFuturesOrder(ctx, hedge),
				preferPassiveSpot: false,
			})
			if closeErr == nil {
				filtered := e.removeManagedOpenHedgeSymbol(target.Symbol)
				if persistErr := e.syncPersistedOpenHedges(filtered); persistErr != nil && e.logger != nil {
					e.logger.Printf("全局 panic flatten 平仓后同步未平仓台账失败: symbol=%s err=%v", target.Symbol, persistErr)
				}
				e.noteManagedDrawdownAction("global_panic_hedge_closed", fmt.Sprintf("%s | alerts=%s", target.Symbol, strings.Join(alerts, "; ")))
				continue
			}
			e.noteManagedDrawdownAction("global_panic_hedge_close_failed", fmt.Sprintf("%s | %v", target.Symbol, closeErr))
			if e.logger != nil {
				e.logger.Printf("全局 panic flatten 双腿平仓失败，转交易所实仓兜底: symbol=%s err=%v", target.Symbol, closeErr)
			}
		}
		// 兜底：无跟踪对冲的孤儿残腿，或上面双腿平仓失败 → 按交易所实际持仓尽力收敛。
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
		// 现货腿方向感知收敛(共享兜底:负 carry BUY 买回 + AUTO_BORROW_REPAY 还币;正 carry / 孤儿 SELL 现货钱包)。
		e.convergeManagedSpotLegBestEffort(ctx, target.Pair, target.BaseAsset, hedge, hedgeTracked, spotBalances)
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

// snapshotSymbolCutsInFlight 返回当前「单币止血并发收敛中」标的的快照(大写归一键),供全局 panic 跳过它们(FIX_PLAN #6)。
func (e *FundingRateSniperEngine) snapshotSymbolCutsInFlight() map[string]struct{} {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if len(e.symbolPanicFlattenInFlight) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(e.symbolPanicFlattenInFlight))
	for sym := range e.symbolPanicFlattenInFlight {
		out[strings.ToUpper(strings.TrimSpace(sym))] = struct{}{}
	}
	return out
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
	hedge, hedgeTracked := e.managedOpenHedgeBySymbol(normalized)
	if hedgeTracked {
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
	// 兜底：双腿平仓失败 / 无跟踪对冲 → 按交易所实仓尽力收敛【双腿】。
	// R3 修复：旧兜底只平合约腿，且在「合约腿已被强平(qty≈0)」时直接 return，会留下裸露的现货/杠杆腿；
	// 现改为合约腿(若有残余)与现货/杠杆腿(方向感知)都收敛，覆盖 ADL/单腿强平后另一腿裸露的场景。
	timeout := e.cfg.ExecutionRecoveryTimeout
	if timeout < 12*time.Second {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(withExposureManagerRequest(context.Background()), timeout)
	defer cancel()

	pair, baseAsset := e.resolveManagedSymbolSpotLeg(normalized, hedge, hedgeTracked)

	_, _, positions, posErr := e.fetchFuturesAccountState(ctx)
	if posErr != nil {
		e.noteManagedDrawdownAction("symbol_cut_fetch_failed", fmt.Sprintf("%s | %v", normalized, posErr))
		if e.logger != nil {
			e.logger.Printf("单币实时止血获取合约仓位失败(仍尝试收敛现货/杠杆腿): symbol=%s err=%v", normalized, posErr)
		}
	} else if positionQty := positions[normalized]; math.Abs(positionQty) > executionQtyTolerance {
		side := "BUY"
		if positionQty > 0 {
			side = "SELL"
		}
		if _, closeErr := e.placeSingleFuturesMarketOrderIdempotent(ctx, normalized, side, math.Abs(positionQty), true, newClientOrderID("ftrim", normalized)); closeErr != nil {
			e.noteManagedDrawdownAction("symbol_cut_futures_trim_failed", fmt.Sprintf("%s | qty=%.8f | %v", normalized, positionQty, closeErr))
			if e.logger != nil {
				e.logger.Printf("单币实时止血合约兜底平仓失败: symbol=%s qty=%.8f err=%v", normalized, positionQty, closeErr)
			}
		} else {
			e.noteManagedDrawdownAction("symbol_cut_futures_trimmed", fmt.Sprintf("%s | qty=%.8f", normalized, positionQty))
			e.recordExecutionAction("realtime_drawdown_symbol", normalized, "poison_symbol_futures_trimmed", fmt.Sprintf("qty=%.8f", positionQty))
		}
	}

	// 现货/杠杆腿方向感知收敛（R3 修复核心：兜底也必须平另一条腿，否则单腿强平后裸腿无人收敛）。
	var spotBalances map[string]float64
	if !(hedgeTracked && hedge.carryDirection() == fundingCarryDirectionNegative) {
		balances, _, balErr := e.fetchSpotAccountBalances(ctx)
		if balErr != nil && e.logger != nil {
			e.logger.Printf("单币实时止血获取现货余额失败(现货腿可能未收敛): symbol=%s err=%v", normalized, balErr)
		}
		spotBalances = balances
	}
	e.convergeManagedSpotLegBestEffort(ctx, pair, baseAsset, hedge, hedgeTracked, spotBalances)
	e.noteManagedDrawdownAction("symbol_cut_fallback_completed", fmt.Sprintf("%s | pair=%s tracked=%v", normalized, pair, hedgeTracked))
}

// resolveManagedSymbolSpotLeg 解析某 symbol 的现货腿 pair / base 资产：有跟踪台账时取台账（权威），
// 否则回退到 managedExposureTargets()（覆盖孤儿残腿：经 goldenSpotPairByFuture / spotMeta 推断）。
func (e *FundingRateSniperEngine) resolveManagedSymbolSpotLeg(symbol string, hedge OpenHedge, hedgeTracked bool) (string, string) {
	if hedgeTracked {
		pair := strings.ToUpper(strings.TrimSpace(hedge.Target.Pair))
		baseAsset := strings.ToUpper(strings.TrimSpace(hedge.Target.SpotBaseAsset))
		if pair != "" {
			return pair, baseAsset
		}
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	for _, t := range e.managedExposureTargets() {
		if strings.EqualFold(strings.TrimSpace(t.Symbol), normalized) {
			return t.Pair, t.BaseAsset
		}
	}
	return "", ""
}

// convergeManagedSpotLegBestEffort 方向感知地收敛某 symbol 的现货 / 全仓杠杆腿（panic-flatten 与单币止血的
// 「双腿平仓失败 / 孤儿残腿」兜底共用，best-effort、失败仅记日志）：
//   - 负 carry（全仓杠杆借入 base 做空现货）→ BUY 买回 + AUTO_BORROW_REPAY 还币（数量取台账借入量）；
//   - 正 carry / 未跟踪孤儿 → SELL 现货钱包正余额。
//
// 抽成共享函数是为了消灭两处兜底逻辑漂移的隐患——R3 的根因正是「兜底只平合约腿、漏平现货/杠杆腿」，
// 而该漏洞曾只在单币止血路径出现、与全局 panic 不一致。pair 为空（无法定位现货腿）时直接返回。
func (e *FundingRateSniperEngine) convergeManagedSpotLegBestEffort(ctx context.Context, pair string, baseAsset string, hedge OpenHedge, hedgeTracked bool, spotBalances map[string]float64) {
	pair = strings.ToUpper(strings.TrimSpace(pair))
	if pair == "" {
		return
	}
	if hedgeTracked && hedge.carryDirection() == fundingCarryDirectionNegative {
		buyQty := parseFloat(hedge.SpotBaseQty)
		if buyQty <= executionQtyTolerance {
			buyQty = hedge.HedgeQty
		}
		if buyQty <= executionQtyTolerance {
			return
		}
		if _, buyErr := e.placeDirectionalSpotMarketOrderIdempotent(ctx, pair, "BUY", buyQty, newClientOrderID("srepay", pair), spotExecutionVenueCrossMargin, "panic_neg_carry_repay", true); buyErr != nil && e.logger != nil {
			e.logger.Printf("实时止血兜底 负 carry 借币空头买回还币失败: pair=%s qty=%.8f err=%v", pair, buyQty, buyErr)
		}
		return
	}
	baseAsset = strings.ToUpper(strings.TrimSpace(baseAsset))
	if baseAsset == "" || spotBalances == nil {
		return
	}
	spotQty := spotBalances[baseAsset]
	if spotQty <= executionQtyTolerance {
		return
	}
	if _, sellErr := e.placeSpotMarketSellIdempotent(ctx, pair, spotQty, newClientOrderID("srevert", pair)); sellErr != nil && e.logger != nil {
		e.logger.Printf("实时止血兜底 现货平仓失败: pair=%s qty=%.8f err=%v", pair, spotQty, sellErr)
	}
}

// handleFuturesForcedClosure 响应「交易所强平/ADL 平掉了合约腿」事件：此时对冲的另一条腿(现货/全仓杠杆)
// 已裸露，必须立即收敛，否则在滚动长持语境下会演变成无对冲的单边敞口。它复用单币止血路径
// flattenManagedSymbolExposure(方向感知双腿平仓 + 兜底)：即便合约腿已被平光，并发的现货平仓腿仍会把裸露的
// 现货/杠杆腿收敛掉，合约侧的 reduceOnly 在零仓位上被拒绝属无害噪声。仅对托管中的 symbol 触发收敛，
// 非托管事件只记录不动作。
func (e *FundingRateSniperEngine) handleFuturesForcedClosure(symbol string, kind string, clientOrderID string) {
	if e == nil {
		return
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return
	}
	reason := fmt.Sprintf(
		"交易所强制平仓事件(%s): symbol=%s clientOrderId=%s，合约腿已被交易所平掉，立即收敛剩余裸腿",
		kind, normalized, strings.TrimSpace(clientOrderID),
	)
	_ = e.recordExecutionIncident("forced_closure", normalized, reason)
	e.emitRiskEvent(newRiskEvent("incident", RiskCategorySafety, RiskSeverityCritical, "forced_closure", normalized, kind, int(atomic.LoadInt32(&e.executionIncidents)), e.cfg.RiskConfig.MaxExecutionIncidents, true, reason))
	if e.logger != nil {
		e.logger.Printf("检测到交易所强平/ADL: %s", reason)
	}

	// 仅对托管中的 symbol 触发收敛，避免对历史 / 无关 symbol 的强平事件误动作。
	if _, ok := e.managedOpenHedgeBySymbol(normalized); !ok {
		e.noteManagedDrawdownAction("forced_closure_untracked", fmt.Sprintf("%s | %s | 非托管 symbol，仅记录", normalized, kind))
		return
	}

	// 抢占单币止血 in-flight 守卫，避免与实时浮亏监控重复派发；抢不到说明已有止血在跑，本次让位。
	// 抢到后由 flattenManagedSymbolExposure 的 defer 负责清理该守卫，契约与监控派发路径一致。
	e.platformRiskMu.Lock()
	if e.symbolPanicFlattenInFlight == nil {
		e.symbolPanicFlattenInFlight = make(map[string]time.Time)
	}
	if _, inFlight := e.symbolPanicFlattenInFlight[normalized]; inFlight {
		e.platformRiskMu.Unlock()
		e.noteManagedDrawdownAction("forced_closure_flatten_skip", fmt.Sprintf("%s | %s | 已有止血在途", normalized, kind))
		return
	}
	e.symbolPanicFlattenInFlight[normalized] = time.Now().UTC()
	e.platformRiskMu.Unlock()

	e.noteManagedDrawdownAction("forced_closure_flatten_start", fmt.Sprintf("%s | %s", normalized, kind))
	go e.flattenManagedSymbolExposure(normalized, reason)
}
