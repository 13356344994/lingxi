package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/lifecycle"
	"lingxi/internal/venue/binance"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载平台熔断/冷却、杠杆风险评估、市场风险决策应用、
// 对冲不变量守卫、监控目标/持仓状态维护、健康状态与追踪元数据等风险与运行时状态逻辑（同包搬移、
// 行为不变），向单文件 ≤ 800 行门禁靠拢。

// baseAssetOf 从交易对符号解析 base asset,用于 venue-neutral 的风险分层判定(不参与下单符号转换)。
// 兼容两种形态:① 中性 canonical "BASE-QUOTE"(取 '-' 前段);② 交易所原始拼接 "BASEQUOTE"
// (剥离已知计价币后缀,长后缀优先以免 USD 抢先于 USDT/USDC)。无法识别时原样返回(归入长尾层)。
func baseAssetOf(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	for _, quote := range []string{"USDT", "USDC", "FDUSD", "TUSD", "BUSD", "USD"} {
		if len(s) > len(quote) && strings.HasSuffix(s, quote) {
			return s[:len(s)-len(quote)]
		}
	}
	return s
}

func GetSymbolRiskProfile(symbol string) symbolRiskProfile {
	base := baseAssetOf(symbol)
	if _, ok := majorRiskTierAssets[base]; ok {
		return symbolRiskProfile{
			Tier:                       "major",
			PerSymbolQuoteMultiplier:   1.0,
			MinEstimatedRateMultiplier: 1.0,
			MinNetProfitMultiplier:     1.0,
			MaxRecentVolumeShareMul:    1.0,
			MaxOpenInterestShareMul:    1.0,
			MaxVisibleDepthShareMul:    1.0,
		}
	}
	if _, ok := mainstreamRiskTierAssets[base]; ok {
		return symbolRiskProfile{
			Tier:                       "mainstream",
			PerSymbolQuoteMultiplier:   0.75,
			MinEstimatedRateMultiplier: 1.15,
			MinNetProfitMultiplier:     1.20,
			ImmediateInvalidateBump:    0.0002,
			MaxRecentVolumeShareMul:    0.75,
			MaxOpenInterestShareMul:    0.75,
			MaxVisibleDepthShareMul:    0.80,
		}
	}
	return symbolRiskProfile{
		Tier:                       "long_tail",
		PerSymbolQuoteMultiplier:   0.40,
		MinEstimatedRateMultiplier: 1.40,
		MinNetProfitMultiplier:     1.50,
		ImmediateInvalidateBump:    0.0005,
		MaxRecentVolumeShareMul:    0.50,
		MaxOpenInterestShareMul:    0.50,
		MaxVisibleDepthShareMul:    0.60,
	}
}

func (e *FundingRateSniperEngine) resetCyclePlatformRiskCounters() {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	e.consecutiveRequestErrors = 0
	e.consecutivePlatformRejects = 0

	e.riskBudgetState.ResetCycle()

	e.protectedIOCAttempts = 0
	e.protectedIOCCanceled = 0
	e.protectedIOCExpired = 0
	e.protectedIOCPartial = 0
	e.protectedIOCUnfilledSum = 0

	e.platformState.ClearFuse()

	e.currentMonitorTargets = nil
	e.currentPositions = nil
}

func (e *FundingRateSniperEngine) checkPlatformFuse() error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return nil
	}
	return supervisor.CheckPlatformFuse()
}

func (e *FundingRateSniperEngine) notePlatformRequestSuccess() {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.NotePlatformRequestSuccess()
}

func (e *FundingRateSniperEngine) notePlatformRequestResult(endpoint string, err error) error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return err
	}
	return supervisor.NotePlatformRequestResult(endpoint, err)
}

// notePlatformReadRequestResult 用于只读公共行情 GET：超时/EOF 等客户端取数失败不计入致命熔断，
// 避免网络抖动把一次只读取数失败升级成 pause_cycle 停摆整轮（见 RiskSupervisor.NotePlatformReadRequestResult）。
func (e *FundingRateSniperEngine) notePlatformReadRequestResult(endpoint string, err error) error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return err
	}
	return supervisor.NotePlatformReadRequestResult(endpoint, err)
}

func (e *FundingRateSniperEngine) noteRecoveryAction(phase string, symbol string, action string, detail string) error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return nil
	}
	return supervisor.NoteRecoveryAction(phase, symbol, action, detail)
}

// noteRescueAction 记录 ExposureManager 残腿救援动作：只审计 + 发风险事件，不累加熔断预算、永不触发/被
// 平台熔断阻断（详见 RiskSupervisor.NoteRescueAction）。救援是最后一道防线，必须能持续重试到把裸腿收敛归零。
func (e *FundingRateSniperEngine) noteRescueAction(phase string, symbol string, action string, detail string) {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.NoteRescueAction(phase, symbol, action, detail)
}

func (e *FundingRateSniperEngine) tripPlatformFuse(category string, severity string, reason string) error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return platformFuseError{message: reason}
	}
	return supervisor.TripPlatformFuse(category, severity, reason)
}

// assertHedgeInvariant 是 Gate 4 的运行时硬保证：在对冲两腿都已建立的时点，校验
// |现货腿名义 − 合约腿名义| 是否超过配置阈值。一旦突破立即熔断（trip platform fuse），
// 防止裸敞口在运行时被悄悄放大。两个阈值默认 0（不启用），即不改变既有行为。
//
// 返回非 nil error 表示已熔断，调用方应中止当前流程。
func (e *FundingRateSniperEngine) assertHedgeInvariant(symbol string, spotNotional, futuresNotional float64) error {
	absThr := e.cfg.RiskConfig.HedgeImbalanceAbortUSDT
	rateThr := e.cfg.RiskConfig.HedgeImbalanceAbortRate
	if absThr <= 0 && rateThr <= 0 {
		return nil
	}
	decision := EvaluateHedgeImbalance(spotNotional, futuresNotional, absThr, rateThr)
	if !decision.Breached {
		return nil
	}
	reason := fmt.Sprintf(
		"hedge_imbalance symbol=%s spot_notional=%.6f futures_notional=%.6f imbalance=%.6f rate=%.4f abs_thr=%.6f rate_thr=%.4f",
		symbol, spotNotional, futuresNotional, decision.Imbalance, decision.ImbalanceRate, absThr, rateThr,
	)
	if e.logger != nil {
		e.logger.Printf("关键不变量被突破，立即熔断: %s", reason)
	}
	return e.tripPlatformFuse("hedge_invariant", string(decision.Severity), reason)
}

// guardHedgeOrderIntent 是下单前置门（Gate 3：消灭双轨状态）：以对冲生命周期 SSOT 状态判定本次
// 下单意图是否自洽。例如已处「干净终态(closed/aborted)」的对冲不得再发平仓单（防重复平仓），
// 「持仓中(active)」不得再发开仓腿。意图不合法时返回错误，调用方据此中止下单。
func (e *FundingRateSniperEngine) guardHedgeOrderIntent(hedge *OpenHedge, intent lifecycle.Intent) error {
	if err := lifecycle.GuardIntent(hedge.lifecycleOrderGate(), intent); err != nil {
		if e != nil && e.logger != nil {
			e.logger.Printf("下单前置门拦截: %v", err)
		}
		return err
	}
	return nil
}

func (e *FundingRateSniperEngine) waitPlatformCooldown(ctx context.Context) error {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return nil
	}
	return supervisor.WaitPlatformCooldown(ctx)
}

func (e *FundingRateSniperEngine) clearPlatformFuse() {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.ClearPlatformFuse()
}

func classifyPlatformException(err error) (string, string) {
	return exchange.ClassifyException(err)
}

func (e *FundingRateSniperEngine) loadFuturesLeverageBrackets(ctx context.Context, symbol string) ([]futuresLeverageBracket, error) {
	if e == nil || e.client == nil {
		return nil, fmt.Errorf("leverage bracket 客户端未初始化")
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return nil, fmt.Errorf("symbol 不能为空")
	}
	e.futuresLeverageBracketMu.Lock()
	if cached, ok := e.futuresLeverageBrackets[symbol]; ok && len(cached) > 0 {
		cloned := append([]futuresLeverageBracket(nil), cached...)
		e.futuresLeverageBracketMu.Unlock()
		return cloned, nil
	}
	e.futuresLeverageBracketMu.Unlock()

	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+binance.PathFuturesLeverageBracket, params)
	if err != nil {
		return nil, fmt.Errorf("查询 %s leverageBracket 失败: %w", symbol, err)
	}
	var envelopes []futuresLeverageBracketEnvelope
	if err := json.Unmarshal(body, &envelopes); err != nil || len(envelopes) == 0 {
		var single futuresLeverageBracketEnvelope
		if singleErr := json.Unmarshal(body, &single); singleErr != nil {
			if err != nil {
				return nil, fmt.Errorf("解析 %s leverageBracket 响应失败: %w", symbol, err)
			}
			return nil, fmt.Errorf("解析 %s leverageBracket 响应失败: %w", symbol, singleErr)
		}
		envelopes = []futuresLeverageBracketEnvelope{single}
	}
	for _, envelope := range envelopes {
		if strings.EqualFold(strings.TrimSpace(envelope.Symbol), symbol) && len(envelope.Brackets) > 0 {
			sort.Slice(envelope.Brackets, func(i int, j int) bool {
				return envelope.Brackets[i].NotionalFloor < envelope.Brackets[j].NotionalFloor
			})
			e.futuresLeverageBracketMu.Lock()
			if e.futuresLeverageBrackets == nil {
				e.futuresLeverageBrackets = make(map[string][]futuresLeverageBracket)
			}
			e.futuresLeverageBrackets[symbol] = append([]futuresLeverageBracket(nil), envelope.Brackets...)
			e.futuresLeverageBracketMu.Unlock()
			return append([]futuresLeverageBracket(nil), envelope.Brackets...), nil
		}
	}
	return nil, fmt.Errorf("%s leverageBracket 响应为空", symbol)
}

func assessFuturesLeverageRisk(symbol string, configuredLeverage int, currentAbsQty float64, orderQty float64, referencePrice float64, availableUSDT float64, brackets []futuresLeverageBracket) (futuresLeverageRiskAssessment, bool, string) {
	if referencePrice <= 0 || math.Abs(orderQty) <= executionQtyTolerance {
		return futuresLeverageRiskAssessment{}, false, "reference price 或 order qty 无效"
	}
	projectedNotional := (math.Abs(currentAbsQty) + math.Abs(orderQty)) * referencePrice
	if projectedNotional <= 0 {
		return futuresLeverageRiskAssessment{}, false, "projected notional 无效"
	}
	if len(brackets) == 0 {
		return futuresLeverageRiskAssessment{}, false, "leverage bracket 为空"
	}
	var matched futuresLeverageBracket
	matchedFound := false
	for _, bracket := range brackets {
		upperOK := projectedNotional <= bracket.NotionalCap+1e-9
		if bracket.NotionalCap <= 0 {
			upperOK = true
		}
		if projectedNotional+1e-9 >= bracket.NotionalFloor && upperOK {
			matched = bracket
			matchedFound = true
			break
		}
	}
	if !matchedFound {
		last := brackets[len(brackets)-1]
		return futuresLeverageRiskAssessment{}, false, fmt.Sprintf("projectedNotional=%.6f 超出 %s leverage bracket 上限 cap=%.6f", projectedNotional, symbol, last.NotionalCap)
	}
	if matched.InitialLeverage > 0 && configuredLeverage > matched.InitialLeverage {
		return futuresLeverageRiskAssessment{}, false, fmt.Sprintf("projectedNotional=%.6f 命中 bracket=%d，仅允许最大初始杠杆=%dx，当前配置=%dx", projectedNotional, matched.Bracket, matched.InitialLeverage, configuredLeverage)
	}
	effectiveLeverage := configuredLeverage
	if effectiveLeverage <= 0 {
		effectiveLeverage = 1
	}
	if matched.InitialLeverage > 0 && effectiveLeverage > matched.InitialLeverage {
		effectiveLeverage = matched.InitialLeverage
	}
	initialMargin := projectedNotional / float64(effectiveLeverage)
	maintenanceMargin := projectedNotional*matched.MaintMarginRatio + matched.Cum
	requiredBuffer := initialMargin + maintenanceMargin
	if availableUSDT > 0 && requiredBuffer > availableUSDT+1e-9 {
		return futuresLeverageRiskAssessment{}, false, fmt.Sprintf("projectedNotional=%.6f 预计 initialMargin=%.6f + maintMargin=%.6f 超过 futuresAvailable=%.6f", projectedNotional, initialMargin, maintenanceMargin, availableUSDT)
	}
	return futuresLeverageRiskAssessment{
		Bracket:               matched,
		ProjectedNotionalUSDT: projectedNotional,
		EffectiveLeverage:     effectiveLeverage,
		InitialMarginUSDT:     initialMargin,
		MaintenanceMarginUSDT: maintenanceMargin,
	}, true, ""
}

func (e *FundingRateSniperEngine) assessOpenFuturesLeverage(ctx context.Context, symbol string, currentAbsQty float64, orderQty float64, referencePrice float64, availableUSDT float64) (futuresLeverageRiskAssessment, bool, string, error) {
	brackets, err := e.loadFuturesLeverageBrackets(ctx, symbol)
	if err != nil {
		return futuresLeverageRiskAssessment{}, false, "", err
	}
	assessment, ok, detail := assessFuturesLeverageRisk(symbol, e.cfg.ExecutionConfig.FuturesLeverage, currentAbsQty, orderQty, referencePrice, availableUSDT, brackets)
	return assessment, ok, detail, nil
}

func (e *FundingRateSniperEngine) cooldownForSeverity(severity string) time.Duration {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return 0
	}
	return supervisor.CooldownForSeverity(severity)
}

func (e *FundingRateSniperEngine) listActiveSymbolCooldownsLocked() []string {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return nil
	}
	return supervisor.ListActiveSymbolCooldownsLocked()
}

func (e *FundingRateSniperEngine) setSymbolCooldown(symbol string, category string, severity string, reason string) {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.SetSymbolCooldown(symbol, category, severity, reason)
}

func (e *FundingRateSniperEngine) filterTargetsBySymbolCooldown(targets []*MonitoredTarget) []*MonitoredTarget {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return targets
	}
	return supervisor.FilterTargetsBySymbolCooldown(targets)
}

func (e *FundingRateSniperEngine) handleMarketRisk(target *MonitoredTarget, consecutiveBreaches *int, netProfit float64, decision MarketRiskDecision, detail string) {
	if target == nil {
		return
	}
	if err := e.applyMarketRiskDecision(target, decision, marketRiskActionOptions{
		detail:                  detail,
		netProfit:               netProfit,
		consecutiveBreaches:     consecutiveBreaches,
		allowObserve:            true,
		invalidateOnCooldown:    true,
		invalidateOnGlobalPause: true,
		resetHealthyState:       true,
	}); err != nil {
		e.logger.Printf("平台级风控已触发: %v", err)
	}
}

func (e *FundingRateSniperEngine) applyMarketRiskDecision(target *MonitoredTarget, decision MarketRiskDecision, opts marketRiskActionOptions) error {
	decision = decision.Normalized()
	detail := strings.TrimSpace(opts.detail)
	if target != nil {
		target.LastMarketRiskCategory = strings.TrimSpace(nonEmpty(decision.Category, "market"))
		target.LastMarketRiskSeverity = strings.TrimSpace(nonEmpty(decision.Severity, "healthy"))
		target.LastMarketRiskAction = string(decision.Action)
		if target.LastMarketRiskAction == "" {
			target.LastMarketRiskAction = string(RiskActionObserve)
		}
		target.LastMarketRiskDetail = detail
	}
	message := fmt.Sprintf("%s severity=%s action=%s %s", nonEmpty(decision.Category, "market"), nonEmpty(decision.Severity, "unknown"), nonEmpty(string(decision.Action), "unknown"), detail)

	if decision.Action != RiskActionObserve {
		level := EventLevelWarning
		if decision.Action == RiskActionTripPlatformFuse || decision.Action == RiskActionManualTakeover {
			level = EventLevelCritical
		}
		e.emitDomainEvents([]DomainEvent{{
			Type:       EventTypeRiskDowngrade,
			Level:      level,
			OccurredAt: time.Now().UTC(),
			RunID:      e.runID,
			CycleID:    e.cycleIDSnapshot(),
			Protocol:   "RiskSupervisor",
			Stage:      e.currentLifecycleStage,
			Successful: true,
			Reason:     message,
		}})
	}

	switch decision.Action {
	case RiskActionObserve:
		if target != nil && opts.allowObserve && opts.consecutiveBreaches != nil {
			e.applyMonitorObserveBreach(target, opts.consecutiveBreaches, opts.netProfit, message)
		}
	case RiskActionTighten:
		symbolCooldownReason := strings.TrimSpace(nonEmpty(opts.symbolCooldownReason, detail))
		if target != nil {
			e.setSymbolCooldown(target.Candidate.Symbol, decision.Category, decision.Severity, symbolCooldownReason)
			if opts.invalidateOnCooldown {
				e.invalidateMonitorTarget(target, opts.netProfit, fmt.Sprintf("symbol 冷却: %s", message))
			}
		}
	case RiskActionPauseCycle:
		if target != nil && opts.invalidateOnGlobalPause {
			e.invalidateMonitorTarget(target, opts.netProfit, fmt.Sprintf("全局暂停: %s", message))
		}
		globalPauseReason := strings.TrimSpace(opts.globalPauseReason)
		if globalPauseReason == "" {
			if target != nil {
				globalPauseReason = fmt.Sprintf("symbol=%s %s", target.Candidate.Symbol, message)
			} else {
				globalPauseReason = message
			}
		}
		return e.tripPlatformFuse(decision.Category, decision.Severity, globalPauseReason)
	default:
		if target != nil && opts.resetHealthyState {
			e.transitionMonitorState(target, "healthy", detail)
		}
	}
	return nil
}

func (e *FundingRateSniperEngine) noteRecoveryStatus(symbol string, action string, detail string) {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.NoteRecoveryStatus(symbol, action, detail)
}

func (e *FundingRateSniperEngine) noteOrderUncertainty(symbol string, action string, detail string) {
	supervisor := e.ensureRiskSupervisor()
	if supervisor == nil {
		return
	}
	supervisor.NoteOrderUncertainty(symbol, action, detail)
}

func (e *FundingRateSniperEngine) setCurrentMonitorTargets(targets []*MonitoredTarget) {
	labels := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		borrowRate := target.LastBorrowReserveRate
		if borrowRate <= 0 {
			borrowRate = e.liveBorrowReserveRate(target.Candidate, target.Candidate.MarkPrice, e.serverNow())
		}
		labels = append(labels, fmt.Sprintf("%s funding=%.4f%% net=%.4f%% borrow=%.4f%%", target.Candidate.Symbol, target.Candidate.EstimatedRate*100, target.LastNetProfit*100, borrowRate*100))
	}
	e.setCurrentMonitorTargetLabels(labels)
}

func (e *FundingRateSniperEngine) setCurrentMonitorTargetLabels(labels []string) {
	e.platformRiskMu.Lock()
	e.currentMonitorTargets = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) updateCurrentMonitorTargetLabel(target *MonitoredTarget) {
	if e == nil || target == nil {
		return
	}
	label := fmt.Sprintf(
		"%s funding=%.4f%% net=%.4f%% borrow=%.4f%%",
		target.Candidate.Symbol,
		target.Candidate.EstimatedRate*100,
		target.LastNetProfit*100,
		target.LastBorrowReserveRate*100,
	)
	prefix := target.Candidate.Symbol + " "
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	replaced := false
	for idx, current := range e.currentMonitorTargets {
		if strings.HasPrefix(current, prefix) {
			e.currentMonitorTargets[idx] = label
			replaced = true
			break
		}
	}
	if !replaced {
		e.currentMonitorTargets = append(e.currentMonitorTargets, label)
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
}

// mergeResidentBaselineWith 把「本轮新开 / 正在开的对冲」并入「开仓前已在场的常驻持仓组合」,按 symbol 合并
// (同名以新开覆盖),返回完整组合快照。
//
// 为什么需要(修 #1 滚动持有组合抹除):开仓持久化(setCurrentPositionsFromHedges / syncPersistedOpenHedges)是
// 「整体替换」语义。单周期模式开仓前 activeOpenHedges 为空 → 替换==仅新开,行为正确且不变。但滚动持有
// (EnableRollingHold)下组合是常驻的:若直接用「仅新开」整体替换,会把既有 keep 仓从内存与台账双双抹掉 →
// 变成未跟踪裸敞口。本函数在替换前先并入既有常驻仓,使「被替换写入的内容」始终是完整组合。
// 读 activeOpenHedges 持 platformRiskMu;幂等(再次并入同名以新开覆盖,不会重复)。
func (e *FundingRateSniperEngine) mergeResidentBaselineWith(newHedges []OpenHedge) []OpenHedge {
	if e == nil {
		return newHedges
	}
	newSymbols := make(map[string]struct{}, len(newHedges))
	for _, h := range newHedges {
		if s := strings.ToUpper(strings.TrimSpace(h.Target.Symbol)); s != "" {
			newSymbols[s] = struct{}{}
		}
	}
	e.platformRiskMu.Lock()
	baseline := make([]OpenHedge, 0, len(e.activeOpenHedges))
	for _, h := range e.activeOpenHedges {
		s := strings.ToUpper(strings.TrimSpace(h.Target.Symbol))
		if s == "" {
			continue
		}
		if _, isNew := newSymbols[s]; isNew {
			continue // 同名由新开覆盖
		}
		baseline = append(baseline, h)
	}
	e.platformRiskMu.Unlock()
	return append(baseline, newHedges...)
}

func (e *FundingRateSniperEngine) setCurrentPositionsFromHedges(hedges []OpenHedge) {
	labels := make([]string, 0, len(hedges))
	for _, hedge := range hedges {
		if hedge.HedgeQty <= 0 {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s %s %.8f", hedge.Target.Symbol, hedge.carryDirection().positionLabel(hedge.Target.SpotBaseAsset, hedge.Target.Symbol), hedge.HedgeQty))
	}
	e.platformRiskMu.Lock()
	e.activeOpenHedges = append([]OpenHedge(nil), hedges...)
	e.currentPositions = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
}

func (e *FundingRateSniperEngine) setCurrentPositions(labels []string) {
	e.platformRiskMu.Lock()
	if len(labels) == 0 {
		e.activeOpenHedges = nil
	}
	e.currentPositions = append([]string(nil), labels...)
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	e.wakeRealtimeDrawdownMonitor()
}

func remainingRiskBudget(limit int, used int) int {
	if limit <= 0 {
		return -1
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func maxFloat(a float64, b float64) float64 {
	if a >= b {
		return a
	}
	return b
}

func (e *FundingRateSniperEngine) healthStatusLocked() (string, string, bool) {
	activeCooldowns := e.listActiveSymbolCooldownsLocked()
	protectedIOCAttempts, _, _, _, protectedIOCAvgUnfilledRatio, protectedIOCCancelRate := e.protectedIOCStatsLocked()
	if e.emergencyStopTriggered {
		return "paused", nonEmpty(e.emergencyStopReason, "检测到 emergency stop，当前任务应立即停止"), false
	}
	if e.riskBudgetState.DailyLimitTriggered {
		return "paused", nonEmpty(e.riskBudgetState.DailyLimitReason, "触发日级风控限制，当前任务应立即停止"), false
	}
	if e.platformState.FuseTripped {
		return "paused", nonEmpty(e.platformState.FuseReason, "平台异常，当前轮应暂停"), false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxExecutionIncidents, int(atomic.LoadInt32(&e.executionIncidents))) == 0 {
		return "paused", "执行事故预算已耗尽，当前轮不应继续扩张", false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxRecoveryActionsPerCycle, e.recoveryActionsThisCycle) == 0 {
		return "paused", "回补预算已耗尽，当前轮不应继续扩张", false
	}
	if remainingRiskBudget(e.cfg.RiskConfig.MaxOrderUncertaintyPerCycle, e.orderUncertaintyCount) == 0 {
		return "paused", "不确定成交预算已耗尽，当前轮应立即暂停", false
	}
	if protectedIOCAttempts >= e.cfg.RiskConfig.ProtectedIOCMinAttempts {
		if protectedIOCAvgUnfilledRatio >= e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio {
			return "paused", fmt.Sprintf("保护 IOC 平均未成交率 %.2f%% 超过阈值 %.2f%%，当前轮应暂停扩张", protectedIOCAvgUnfilledRatio*100, e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*100), false
		}
		if protectedIOCCancelRate >= e.cfg.RiskConfig.ProtectedIOCMaxCancelRate {
			return "paused", fmt.Sprintf("保护 IOC 撤单/过期率 %.2f%% 超过阈值 %.2f%%，当前轮应暂停扩张", protectedIOCCancelRate*100, e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*100), false
		}
		if protectedIOCAvgUnfilledRatio >= e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*0.8 {
			return "degraded", fmt.Sprintf("保护 IOC 平均未成交率 %.2f%% 已接近阈值 %.2f%%", protectedIOCAvgUnfilledRatio*100, e.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio*100), true
		}
		if protectedIOCCancelRate >= e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*0.8 {
			return "degraded", fmt.Sprintf("保护 IOC 撤单/过期率 %.2f%% 已接近阈值 %.2f%%", protectedIOCCancelRate*100, e.cfg.RiskConfig.ProtectedIOCMaxCancelRate*100), true
		}
	}
	if e.consecutivePlatformRejects >= maxIntValue(1, e.cfg.RiskConfig.MaxConsecutivePlatformRejects-1) && e.cfg.RiskConfig.MaxConsecutivePlatformRejects > 1 {
		return "degraded", "平台已接近连续拒单阈值，应谨慎继续", true
	}
	if e.consecutiveRequestErrors >= maxIntValue(1, e.cfg.RiskConfig.MaxConsecutiveRequestErrors-1) && e.cfg.RiskConfig.MaxConsecutiveRequestErrors > 1 {
		return "degraded", "平台异常请求接近阈值，应谨慎继续", true
	}
	if e.orderUncertaintyCount > 0 || e.recoveryActionsThisCycle > 0 {
		return "degraded", "本轮已出现不确定成交或回补动作，应停止扩张并优先收敛", true
	}
	if strings.TrimSpace(e.platformState.LastExceptionSeverity) == "major" || strings.TrimSpace(e.platformState.LastExceptionSeverity) == "critical" {
		return "degraded", fmt.Sprintf("最近异常等级=%s category=%s，应谨慎继续", nonEmpty(e.platformState.LastExceptionSeverity, "unknown"), nonEmpty(e.platformState.LastExceptionCategory, "unknown")), true
	}
	if len(activeCooldowns) > 0 {
		return "degraded", fmt.Sprintf("当前有 %d 个 symbol 处于冷却中", len(activeCooldowns)), true
	}
	return "healthy", "当前轮运行健康，可继续观察但仍按阈值保守执行", true
}

func maxIntValue(a int, b int) int {
	if a >= b {
		return a
	}
	return b
}

// traceIDs 供日志写入器（runtimeLogWriter）旁路读取当前 run/cycle id 作为日志元数据。
// 它【刻意不锁 platformRiskMu】：本函数会在「已持有 platformRiskMu 的协程正在写日志」时被回调，
// 若再锁 platformRiskMu 会造成同协程自死锁（曾导致引擎在首个 ready->scan 状态迁移处整体卡死）。
// 改用独立的 traceLogMu 保护一份 id 副本，写入侧通过 setTraceLogIDs 同步，读侧永不触碰风控锁。
func (e *FundingRateSniperEngine) traceIDs() (string, string) {
	e.traceLogMu.Lock()
	defer e.traceLogMu.Unlock()
	return strings.TrimSpace(e.traceLogRunID), strings.TrimSpace(e.traceLogCycleID)
}

// cycleIDSnapshot 返回当前 cycle id 的并发安全快照(修 FIX_PLAN 待复核#1)。此前后台 goroutine(面板快照 / 救援 /
// 平仓 / 台账持久化)直接裸读 e.currentCycleID,与主循环每轮改写并发 → string 双字读取可能撕裂。改为统一经 cycleIDMu
// 读写:读侧用独立的 cycleIDMu(绝不触碰 platformRiskMu,无锁序/死锁问题)。
func (e *FundingRateSniperEngine) cycleIDSnapshot() string {
	if e == nil {
		return ""
	}
	e.cycleIDMu.RLock()
	defer e.cycleIDMu.RUnlock()
	return e.currentCycleID
}

// setCurrentCycleID 在 cycleIDMu 下写入本轮 cycle id,与 cycleIDSnapshot 配对消除撕裂读(FIX_PLAN 待复核#1)。
func (e *FundingRateSniperEngine) setCurrentCycleID(id string) {
	if e == nil {
		return
	}
	e.cycleIDMu.Lock()
	e.currentCycleID = id
	e.cycleIDMu.Unlock()
}

// setTraceLogIDs 更新供日志旁路读取的 run/cycle id 副本。只锁 traceLogMu，绝不在持有 platformRiskMu
// 时调用（调用方需在释放 platformRiskMu 之后再调用），从而保持与风控锁的解耦。
func (e *FundingRateSniperEngine) setTraceLogIDs(runID string, cycleID string) {
	if e == nil {
		return
	}
	e.traceLogMu.Lock()
	e.traceLogRunID = runID
	e.traceLogCycleID = cycleID
	e.traceLogMu.Unlock()
}

func (e *FundingRateSniperEngine) recordMetadata() TraceRecordMetadata {
	if e == nil {
		return TraceRecordMetadata{}
	}
	return buildTraceRecordMetadata(e.cfg)
}

func filterValidTargets(targets []*MonitoredTarget) []*MonitoredTarget {
	result := make([]*MonitoredTarget, 0, len(targets))
	for _, target := range targets {
		if atomic.LoadInt32(&target.IsValid) != 1 {
			continue
		}
		result = append(result, target)
	}
	return result
}

func earliestFundingTime(targets []*MonitoredTarget) time.Time {
	earliest := targets[0].Candidate.NextFundingTime
	for _, target := range targets[1:] {
		if target.Candidate.NextFundingTime.Before(earliest) {
			earliest = target.Candidate.NextFundingTime
		}
	}
	return earliest
}

func earliestFundingTimeFromHedges(hedges []OpenHedge) time.Time {
	earliest := hedges[0].Target.NextFundingTime
	for _, hedge := range hedges[1:] {
		if hedge.Target.NextFundingTime.Before(earliest) {
			earliest = hedge.Target.NextFundingTime
		}
	}
	return earliest
}

func nextWakeTime(now time.Time, minute int, second int) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), minute, second, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(time.Hour)
	}
	return candidate
}

func sleepUntil(ctx context.Context, target time.Time) error {
	if !target.After(time.Now()) {
		return nil
	}

	timer := time.NewTimer(time.Until(target))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
