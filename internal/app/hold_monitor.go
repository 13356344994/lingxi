package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// hold_monitor.go 是 S2「持仓期常驻监控」架构的地基（M-S2.0）。
//
// 背景（已核对代码）：行情流(bookTicker/partial depth)在 monitorTargets 内以 monitorCtx 启动、
// 在开仓预备时点(stopAt)就关闭，因此「开仓→持有→平仓」全程缺少常驻数据流；而合约/现货用户数据流
// 虽已常驻，但其 ACCOUNT_UPDATE 是事件驱动(变更才推送)，"距上一次账户更新的时延"并不能当作连通性信号。
//
// 因此 HoldMonitor 的失明判定建立在**连接级"距上一帧"时延**之上：每条 WebSocket 读到任意一帧
// (含心跳/应答帧)即调用 noteFeedFrame 打点，这才是真正的连通性信号。
//
// M-S2.0 仅做**纯观测**：常驻 goroutine 周期性把各流的失明分级汇成摘要、在分级跃迁时记日志并刷新
// 运行时快照（附加字段，向后兼容）。不触发任何交易动作——撤离/降级/熔断分别在 S2.2/S2.3/S2.4 接入。

// feedKind 标识一条 HoldMonitor 关注的常驻数据流。
type feedKind string

const (
	feedSpotUser     feedKind = "spot_user"
	feedFuturesUser  feedKind = "futures_user"
	feedMarket       feedKind = "market"        // 持有期常驻行情流，S2.1 接入
	feedMarginHealth feedKind = "margin_health" // cross-margin 健康轮询，S2.4 接入
)

// feedLivenessState 是单条数据流的失明分级（R32 状态机的输入）。
type feedLivenessState string

const (
	feedStateLive     feedLivenessState = "live"
	feedStateUnknown  feedLivenessState = "unknown"
	feedStateDegraded feedLivenessState = "degraded"
	feedStateBlind    feedLivenessState = "blind"
)

// feedLivenessThresholds 定义失明分级的时延阈值；任一阈值 <=0 表示该级别关闭。
type feedLivenessThresholds struct {
	DegradedAfter time.Duration
	BlindAfter    time.Duration
}

const (
	holdMonitorInterval        = 1 * time.Second
	holdMonitorFeedDegradedAge = 15 * time.Second
	holdMonitorFeedBlindAge    = 45 * time.Second
	// feedBlindnessWithdrawGrace 是行情流连续"失明/未知"多久后触发保守撤离(在 blind 阈值之上再叠加),
	// 给资格重连留出宽限,避免瞬时抖动误撤离;总静默约 holdMonitorFeedBlindAge + 本宽限。
	feedBlindnessWithdrawGrace = 30 * time.Second
)

// holdMonitorWatchedFeeds 是 HoldMonitor 观测的常驻流集合。S2.1 起纳入 feedMarket(持仓期常驻行情流)；
// margin 健康流(feedMarginHealth)在 S2.4 接入。
func holdMonitorWatchedFeeds() []feedKind {
	return []feedKind{feedFuturesUser, feedSpotUser, feedMarket}
}

// classifyFeedLiveness 是纯函数：按"距上一帧时延"把单条流分级。age<0 视为从未收到帧（unknown）。
func classifyFeedLiveness(age time.Duration, thr feedLivenessThresholds) feedLivenessState {
	if age < 0 {
		return feedStateUnknown
	}
	if thr.BlindAfter > 0 && age >= thr.BlindAfter {
		return feedStateBlind
	}
	if thr.DegradedAfter > 0 && age >= thr.DegradedAfter {
		return feedStateDegraded
	}
	return feedStateLive
}

// feedStateRank 给失明分级排序，便于取"最坏"的一条流；rank 越大越坏。
func feedStateRank(state feedLivenessState) int {
	switch state {
	case feedStateBlind:
		return 3
	case feedStateDegraded:
		return 2
	case feedStateUnknown:
		return 1
	default:
		return 0
	}
}

// worseFeedState 返回两个分级里更坏的一个。
func worseFeedState(a, b feedLivenessState) feedLivenessState {
	if feedStateRank(b) > feedStateRank(a) {
		return b
	}
	return a
}

// formatFeedAge 把时延格式化进摘要；负值表示从未收到帧。
func formatFeedAge(age time.Duration) string {
	if age < 0 {
		return "never"
	}
	return age.Truncate(time.Millisecond).String()
}

// noteFeedFrame 在某条 WebSocket 读到任意一帧时打点，记录该流"最近一帧"时刻。
// 它走独立叶子锁 feedLivenessMu，路径极短，可安全放在 WS 读循环热路径上。
func (e *FundingRateSniperEngine) noteFeedFrame(kind feedKind) {
	if e == nil || strings.TrimSpace(string(kind)) == "" {
		return
	}
	now := time.Now().UTC()
	e.feedLivenessMu.Lock()
	if e.feedLastFrameAt == nil {
		e.feedLastFrameAt = make(map[feedKind]time.Time, 4)
	}
	e.feedLastFrameAt[kind] = now
	e.feedLivenessMu.Unlock()
}

// feedAgeSnapshot 返回各关注流"距上一帧"的时延；从未收到帧的流返回 -1（unknown）。
func (e *FundingRateSniperEngine) feedAgeSnapshot(now time.Time, kinds []feedKind) map[feedKind]time.Duration {
	ages := make(map[feedKind]time.Duration, len(kinds))
	e.feedLivenessMu.Lock()
	for _, kind := range kinds {
		last, ok := e.feedLastFrameAt[kind]
		if !ok || last.IsZero() {
			ages[kind] = -1
			continue
		}
		ages[kind] = now.Sub(last)
	}
	e.feedLivenessMu.Unlock()
	return ages
}

// startHoldMonitor 启动持仓期常驻监控（进程内只起一次，跟随 runCtx 生命周期）。
func (e *FundingRateSniperEngine) startHoldMonitor(ctx context.Context) {
	if e == nil {
		return
	}
	e.holdMonitorWorkerOnce.Do(func() {
		go e.runHoldMonitor(ctx)
	})
}

func (e *FundingRateSniperEngine) runHoldMonitor(ctx context.Context) {
	ticker := time.NewTicker(holdMonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		e.observeHoldMonitor()
		if symbol, reason := e.shouldTriggerHoldHedgeFlatten(); symbol != "" {
			go e.flattenManagedSymbolExposure(symbol, reason)
		}
		if reason, ok := e.shouldTriggerFeedBlindnessWithdraw(); ok {
			go e.panicFlattenManagedExposure(reason)
		}
	}
}

// shouldTriggerFeedBlindnessWithdraw 判定行情流是否已"持续失明"到必须保守撤离(R32)：在持有期、行情流
// 连续失明/未知超过 feedBlindnessWithdrawGrace 时，抢占全局 panic 在途守卫并返回撤离理由，由调用方走
// panicFlattenManagedExposure(方向感知双腿全平 + 平台熔断进入冷却)。opt-in，关闭时恒不触发。
func (e *FundingRateSniperEngine) shouldTriggerFeedBlindnessWithdraw() (string, bool) {
	if e == nil || !e.cfg.RiskConfig.FeedBlindnessGuardEnabled {
		return "", false
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if e.panicFlattenInFlight {
		return "", false
	}
	if len(e.managedExposureSymbolsLocked()) == 0 {
		return "", false
	}
	if e.feedMarketBlindSince.IsZero() {
		return "", false
	}
	blindFor := time.Now().UTC().Sub(e.feedMarketBlindSince)
	if blindFor < feedBlindnessWithdrawGrace {
		return "", false
	}
	e.panicFlattenInFlight = true
	return fmt.Sprintf("数据流持续失明保守撤离: market_blind_for=%s grace=%s",
		blindFor.Truncate(time.Second), feedBlindnessWithdrawGrace), true
}

// feedBlindnessBlockingOpens 报告断连盲窗守卫当前是否要求暂停开新仓（供主循环在每轮开仓前门控）。
func (e *FundingRateSniperEngine) feedBlindnessBlockingOpens() (bool, string) {
	if e == nil || !e.cfg.RiskConfig.FeedBlindnessGuardEnabled {
		return false, ""
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.feedBlindnessBlockOpens, e.feedBlindnessBlockReason
}

// observeHoldMonitor 在持有期把各常驻流的失明分级汇成摘要。M-S2.0 仅观测：分级跃迁时记日志、
// 刷新运行时快照；无托管敞口时清空摘要（开仓窗外的流空档属正常，不评估失明）。不触发任何交易动作。
func (e *FundingRateSniperEngine) observeHoldMonitor() {
	if e == nil {
		return
	}
	now := time.Now().UTC()
	watched := holdMonitorWatchedFeeds()
	ages := e.feedAgeSnapshot(now, watched)
	thr := feedLivenessThresholds{DegradedAfter: holdMonitorFeedDegradedAge, BlindAfter: holdMonitorFeedBlindAge}

	e.platformRiskMu.Lock()
	managed := len(e.managedExposureSymbolsLocked())
	if managed == 0 {
		cleared := e.holdMonitorSummary != "" || e.holdMonitorWorstState != ""
		e.holdMonitorSummary = ""
		e.holdMonitorWorstState = ""
		// 无托管敞口：清空盲窗守卫状态（撤离/停开仓只在持有期有意义）。
		e.feedMarketBlindSince = time.Time{}
		if e.feedBlindnessBlockOpens {
			e.feedBlindnessBlockOpens = false
			e.feedBlindnessBlockReason = ""
		}
		if cleared {
			e.runtimeReporter.EmitLocked(EngineStage(""))
		}
		e.platformRiskMu.Unlock()
		return
	}

	worst := feedStateLive
	marketState := feedStateLive
	parts := make([]string, 0, len(watched))
	for _, kind := range watched {
		state := classifyFeedLiveness(ages[kind], thr)
		worst = worseFeedState(worst, state)
		if kind == feedMarket {
			marketState = state
		}
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", kind, state, formatFeedAge(ages[kind])))
	}
	summary := fmt.Sprintf("managed=%d worst=%s %s", managed, worst, strings.Join(parts, " "))
	prevWorst := e.holdMonitorWorstState
	transitioned := worst != prevWorst
	e.lastHoldMonitorAt = now
	// 只在最坏分级跃迁时刷新摘要与快照，避免每秒时延文本变动造成 1Hz 报告抖动。
	if transitioned {
		e.holdMonitorSummary = summary
		e.holdMonitorWorstState = worst
		e.runtimeReporter.EmitLocked(EngineStage(""))
	}

	// 断连盲窗守卫状态机(R32, opt-in)：以行情流(持续推送、判活可靠)为准。
	//   - blind / unknown：无可信行情 → 起算失明持续时长(供撤离判定) + 停开新仓；
	//   - degraded：行情迟滞 → 停开新仓，但不算"无数据"，重置失明计时；
	//   - live：解除停开仓、清失明计时。
	if e.cfg.RiskConfig.FeedBlindnessGuardEnabled {
		switch marketState {
		case feedStateBlind, feedStateUnknown:
			if e.feedMarketBlindSince.IsZero() {
				e.feedMarketBlindSince = now
			}
			e.feedBlindnessBlockOpens = true
			e.feedBlindnessBlockReason = "行情流失明，暂停开新仓: " + summary
		case feedStateDegraded:
			e.feedMarketBlindSince = time.Time{}
			e.feedBlindnessBlockOpens = true
			e.feedBlindnessBlockReason = "行情流降级，暂停开新仓: " + summary
		default:
			e.feedMarketBlindSince = time.Time{}
			e.feedBlindnessBlockOpens = false
			e.feedBlindnessBlockReason = ""
		}
	}
	e.platformRiskMu.Unlock()

	if transitioned && worst != feedStateLive && e.logger != nil {
		e.logger.Printf("HoldMonitor 数据流失明分级跃迁: %s", summary)
	}
}

// shouldTriggerHoldHedgeFlatten 是 M0.2b「持有期对冲偏离守卫」(M-S2.3)：在持有期按实时两腿名义
// 周期性复核 EvaluateHedgeImbalance，偏离突破即抢占单币止血 in-flight 守卫并返回待平仓 symbol，
// 由调用方走方向感知双腿平仓(flattenManagedSymbolExposure，与强平/ADL 处置一致)。
//
// 安全契约（避免缺数据误平）：
//   - opt-in：HoldHedgeGuardEnabled 默认 false，关闭时恒不触发(零行为变更)；
//   - 仅正 carry：现货腿在现货钱包可实时测量；负 carry 在全仓杠杆(借币空头)，其持有期复核依赖
//     cross-margin 实时健康数据(S2.4)，此处跳过；
//   - 两腿都须有可信实时测量(合约实时仓位 present 且 >0、现货实时余额 >0、有新鲜现货价)才评估；
//     任一腿测量缺失即跳过——「某腿完全消失」由强平/ADL 与实时浮亏止血路径覆盖，本守卫只抓「两腿都在
//     但名义漂移」的裸敞口，避免把"数据未到"误判成"腿没了"而错误平仓。
func (e *FundingRateSniperEngine) shouldTriggerHoldHedgeFlatten() (string, string) {
	if e == nil || !e.cfg.RiskConfig.HoldHedgeGuardEnabled {
		return "", ""
	}
	absThr := e.cfg.RiskConfig.HedgeImbalanceAbortUSDT
	rateThr := e.cfg.RiskConfig.HedgeImbalanceAbortRate
	if absThr <= 0 && rateThr <= 0 {
		return "", ""
	}

	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if e.panicFlattenInFlight {
		return "", ""
	}
	for i := range e.activeOpenHedges {
		hedge := e.activeOpenHedges[i]
		symbol := strings.ToUpper(strings.TrimSpace(hedge.Target.Symbol))
		if symbol == "" {
			continue
		}
		if _, inFlight := e.symbolPanicFlattenInFlight[symbol]; inFlight {
			continue
		}
		if hedge.carryDirection().spotVenue() != spotExecutionVenueSpot {
			continue
		}
		baseAsset := strings.ToUpper(strings.TrimSpace(hedge.Target.SpotBaseAsset))
		pair := strings.ToUpper(strings.TrimSpace(hedge.Target.Pair))
		if baseAsset == "" || pair == "" {
			continue
		}
		pos, ok := e.futuresRealtimePositions[symbol]
		if !ok {
			continue
		}
		futuresAbsQty := math.Abs(pos.PositionAmt)
		spotQty := e.spotRealtimeBalances[baseAsset]
		if futuresAbsQty <= executionQtyTolerance || spotQty <= executionQtyTolerance {
			continue
		}
		spotMid, spotOK := e.heldSpotMid(pair)
		if !spotOK {
			continue
		}
		futMid := pos.MarkPrice
		if mid, ok := e.heldFuturesMid(symbol); ok {
			futMid = mid
		}
		if futMid <= 0 {
			continue
		}
		spotNotional := spotQty * spotMid
		futuresNotional := futuresAbsQty * futMid
		decision := EvaluateHedgeImbalance(spotNotional, futuresNotional, absThr, rateThr)
		if !decision.Breached {
			continue
		}
		if e.symbolPanicFlattenInFlight == nil {
			e.symbolPanicFlattenInFlight = make(map[string]time.Time)
		}
		e.symbolPanicFlattenInFlight[symbol] = time.Now().UTC()
		reason := fmt.Sprintf(
			"持有期对冲偏离守卫触发平仓: symbol=%s spot_notional=%.6f futures_notional=%.6f imbalance=%.6f rate=%.4f abs_thr=%.6f rate_thr=%.4f",
			symbol, spotNotional, futuresNotional, decision.Imbalance, decision.ImbalanceRate, absThr, rateThr,
		)
		return symbol, reason
	}
	return "", ""
}
