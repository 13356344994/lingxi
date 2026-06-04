package app

import (
	"context"
	"errors"
	"fmt"
	"lingxi/internal/marketdata"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载开仓序列计划构建与执行、行情/深度抓取与
// 稳定性评估、监控状态迁移、执行事件记录、订单终态对账等执行链路逻辑（同包搬移、行为不变），
// 向单文件 ≤ 800 行门禁靠拢。

func (e *FundingRateSniperEngine) buildOpenSequencePlans(ctx context.Context, validTargets []*MonitoredTarget, beforeSnapshot CycleAccountSnapshot) ([]openSequencePlan, error) {
	state := newOpenSequenceBuildState(beforeSnapshot, e.cfg)
	plans := make([]openSequencePlan, 0, len(validTargets))
	for _, target := range validTargets {
		plan, err := e.buildOpenSequencePlan(ctx, target, &state)
		if err != nil {
			return nil, err
		}
		if plan == nil {
			continue
		}
		plans = append(plans, *plan)
	}
	// 通过 setCycleOpenNotionalUsed 落账：既记录本周期已用名义，
	// 也把增量累加进当日开仓名义并强制执行 MaxDailyOpenNotionalUSDT。
	// 使用实际计划名义(plannedCycleNotional)，即便未配置单周期上限也能准确统计当日名义。
	if err := e.setCycleOpenNotionalUsed(state.plannedCycleNotional()); err != nil {
		return nil, err
	}

	return plans, nil
}

func (e *FundingRateSniperEngine) buildOpenSequencePlan(ctx context.Context, target *MonitoredTarget, state *openSequenceBuildState) (*openSequencePlan, error) {
	profile := GetSymbolRiskProfile(target.Candidate.Symbol)
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	refreshErr := e.refreshTargetFundingEstimate(refreshCtx, target, true)
	refreshCancel()
	if refreshErr != nil {
		e.logger.Printf("模块四跳过 %s，开仓前刷新资金费预估失败: %v", target.Candidate.Symbol, refreshErr)
		return nil, nil
	}
	strategy := NewDefaultFundingStrategy()
	action, _ := strategy.EvaluateScanCandidate(
		target.Candidate,
		FundingSnapshot{
			EstimatedRate: target.Candidate.EstimatedRate,
			Direction:     target.Candidate.Direction,
			FundingJump:   target.Candidate.FundingRateJump,
			BasisRate:     target.Candidate.BasisRate,
		},
		profile,
		e.cfg,
	)
	if action == StrategyActionReject {
		e.logger.Printf("模块四跳过 %s，开仓前最新预估不满足入场门槛: %.6f", target.Candidate.Symbol, target.Candidate.EstimatedRate)
		return nil, nil
	}
	if !e.isLiveSpotPairFundable(target.Candidate.Pair) {
		e.logger.Printf("模块四跳过 %s，现货交易对 %s 的计价资产不是 live funding asset=%s", target.Candidate.Symbol, target.Candidate.Pair, strings.ToUpper(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset)))
		return nil, nil
	}

	futuresRule := e.futuresRules[target.Candidate.Symbol]
	rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / target.Candidate.MarkPrice
	target.Candidate.OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, target.Candidate.QuantityPrecision)
	if !isQuantityTradable(target.Candidate.OrderQuantity, target.Candidate.MarkPrice, futuresRule) {
		return nil, nil
	}
	spotReferencePrice := target.LastSpotAsk
	if spotReferencePrice <= 0 {
		spotReferencePrice = target.Candidate.MarkPrice
	}
	futuresReferencePrice := target.LastFuturesBid
	if futuresReferencePrice <= 0 {
		futuresReferencePrice = target.Candidate.MarkPrice
	}

	capacityCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	adjustedQty, capacityReason, capacityErr := e.constrainOrderQuantityByMarketCapacity(capacityCtx, target, spotReferencePrice, futuresReferencePrice)
	cancel()
	if capacityErr != nil {
		e.setSymbolCooldown(target.Candidate.Symbol, "liquidity", "major", fmt.Sprintf("开仓前市场容量校验失败: %v", capacityErr))
		e.logger.Printf("模块四跳过 %s，市场容量校验失败: %v", target.Candidate.Symbol, capacityErr)
		return nil, nil
	}
	if adjustedQty <= 0 {
		e.setSymbolCooldown(target.Candidate.Symbol, "liquidity", "major", fmt.Sprintf("开仓前市场容量校验后数量归零: %s", capacityReason))
		e.logger.Printf("模块四跳过 %s，市场容量校验后数量归零: %s", target.Candidate.Symbol, capacityReason)
		return nil, nil
	}
	if adjustedQty+1e-12 < target.Candidate.OrderQuantity {
		coverageRatio := adjustedQty / target.Candidate.OrderQuantity
		decision := EvaluateExecutionCoverageRisk("liquidity", coverageRatio)
		detail := fmt.Sprintf("开仓前市场容量不足 coverage=%.4f %s", coverageRatio, capacityReason)
		if err := e.applyMarketRiskDecision(target, decision, marketRiskActionOptions{
			detail:               detail,
			symbolCooldownReason: detail,
			globalPauseReason:    fmt.Sprintf("symbol=%s 开仓前市场容量极差 coverage=%.4f %s", target.Candidate.Symbol, coverageRatio, capacityReason),
		}); err != nil {
			return nil, err
		}
		e.logger.Printf(
			"模块四缩减 %s 下单数量，原数量=%.8f 新数量=%.8f 原名义=%.6fUSDT 新名义=%.6fUSDT %s",
			target.Candidate.Symbol,
			target.Candidate.OrderQuantity,
			adjustedQty,
			target.Candidate.OrderQuantity*futuresReferencePrice,
			adjustedQty*futuresReferencePrice,
			capacityReason,
		)
	}
	target.Candidate.OrderQuantity = adjustedQty
	if !isQuantityTradable(target.Candidate.OrderQuantity, futuresReferencePrice, futuresRule) {
		e.logger.Printf("模块四跳过 %s，市场容量限制后数量低于最小下单门槛: %s", target.Candidate.Symbol, capacityReason)
		return nil, nil
	}

	orderNotional := target.Candidate.OrderQuantity * futuresReferencePrice
	if state.remainingCycleNotional > 0 && orderNotional > state.remainingCycleNotional+1e-9 {
		cycleLimitedQty := normalizeQuantity(state.remainingCycleNotional/futuresReferencePrice, futuresRule, target.Candidate.QuantityPrecision)
		if !isQuantityTradable(cycleLimitedQty, futuresReferencePrice, futuresRule) {
			e.logger.Printf("模块四跳过 %s，单周期总敞口上限不足以继续开仓，remainingNotional=%.6f", target.Candidate.Symbol, state.remainingCycleNotional)
			return nil, nil
		}
		e.logger.Printf("模块四缩减 %s 下单数量以满足单周期总敞口上限，原名义=%.6fUSDT 新名义=%.6fUSDT 上限剩余=%.6fUSDT", target.Candidate.Symbol, orderNotional, cycleLimitedQty*futuresReferencePrice, state.remainingCycleNotional)
		target.Candidate.OrderQuantity = cycleLimitedQty
		orderNotional = target.Candidate.OrderQuantity * futuresReferencePrice
	}
	currentAbsQty := 0.0
	if state != nil && state.projectedFuturesAbsQty != nil {
		currentAbsQty = state.projectedFuturesAbsQty[strings.ToUpper(strings.TrimSpace(target.Candidate.Symbol))]
	}
	if assessment, allowed, detail, bracketErr := e.assessOpenFuturesLeverage(ctx, target.Candidate.Symbol, currentAbsQty, target.Candidate.OrderQuantity, futuresReferencePrice, state.remainingFuturesUSDT); bracketErr != nil {
		e.setSymbolCooldown(target.Candidate.Symbol, "leverage_bracket", "major", fmt.Sprintf("开仓前 leverage bracket 校验失败: %v", bracketErr))
		e.logger.Printf("模块四跳过 %s，开仓前 leverage bracket 校验失败: %v", target.Candidate.Symbol, bracketErr)
		return nil, nil
	} else if !allowed {
		e.setSymbolCooldown(target.Candidate.Symbol, "leverage_bracket", "major", detail)
		e.logger.Printf("模块四跳过 %s，%s", target.Candidate.Symbol, detail)
		return nil, nil
	} else {
		e.logger.Printf(
			"模块四通过 %s leverage bracket 校验，projectedNotional=%.6f initialMargin=%.6f maintMargin=%.6f leverage=%dx bracket=%d",
			target.Candidate.Symbol,
			assessment.ProjectedNotionalUSDT,
			assessment.InitialMarginUSDT,
			assessment.MaintenanceMarginUSDT,
			assessment.EffectiveLeverage,
			assessment.Bracket.Bracket,
		)
		// 开仓前强平距离安全闸（R2）：合约腿入场价到强平价的可承受逆向幅度低于阈值则跳过该候选、不开仓。
		// 仅冷却该 symbol（non-fatal），不触发平台熔断——与上方 leverage bracket 拒绝同一处置语义。
		if minDistance := e.cfg.RiskConfig.MinLiquidationDistanceRate; minDistance > 0 {
			distance := assessment.liquidationDistanceRate()
			if distance < minDistance {
				detail := fmt.Sprintf(
					"开仓前强平距离不足: 预计强平距离=%.4f 低于阈值=%.4f (leverage=%dx maintMarginRatio=%.4f projectedNotional=%.6f)",
					distance, minDistance, assessment.EffectiveLeverage, assessment.Bracket.MaintMarginRatio, assessment.ProjectedNotionalUSDT,
				)
				e.setSymbolCooldown(target.Candidate.Symbol, "liquidation_distance", "major", detail)
				e.logger.Printf("模块四跳过 %s，%s", target.Candidate.Symbol, detail)
				return nil, nil
			}
			e.logger.Printf("模块四通过 %s 强平距离校验，预计强平距离=%.4f 阈值=%.4f", target.Candidate.Symbol, distance, minDistance)
		}
	}

	spotRequiredUSDT, futuresRequiredUSDT := e.estimateTargetCapitalRequirement(target, spotReferencePrice, futuresReferencePrice, e.serverNow())
	if spotRequiredUSDT <= 0 || futuresRequiredUSDT <= 0 {
		e.logger.Printf("模块四跳过 %s，资金需求估算无效，spotRequired=%.6f futuresRequired=%.6f", target.Candidate.Symbol, spotRequiredUSDT, futuresRequiredUSDT)
		return nil, nil
	}
	if spotRequiredUSDT > state.remainingSpotUSDT+1e-9 || futuresRequiredUSDT > state.remainingFuturesUSDT+1e-9 {
		e.logger.Printf(
			"模块四跳过 %s，账户可用资金不足，spotRequired=%.6f spotFree=%.6f futuresRequired=%.6f futuresAvailable=%.6f",
			target.Candidate.Symbol,
			spotRequiredUSDT,
			state.remainingSpotUSDT,
			futuresRequiredUSDT,
			state.remainingFuturesUSDT,
		)
		return nil, nil
	}

	state.remainingSpotUSDT -= spotRequiredUSDT
	state.remainingFuturesUSDT -= futuresRequiredUSDT
	state.plannedNotional += orderNotional
	if state.remainingCycleNotional > 0 {
		state.remainingCycleNotional -= orderNotional
	}
	if state.projectedFuturesAbsQty != nil {
		state.projectedFuturesAbsQty[strings.ToUpper(strings.TrimSpace(target.Candidate.Symbol))] = currentAbsQty + math.Abs(target.Candidate.OrderQuantity)
	}

	return &openSequencePlan{
		target:       target,
		futuresOrder: e.buildProtectedOpenFuturesOrder(target),
	}, nil
}

func (e *FundingRateSniperEngine) executeOpenHedgePlans(ctx context.Context, plans []openSequencePlan, futuresResults []FuturesBatchOrderResult) ([]OpenHedge, error) {
	if len(plans) != len(futuresResults) {
		return nil, fmt.Errorf("开仓计划与合约回执数量不一致: plans=%d results=%d", len(plans), len(futuresResults))
	}

	hedges := make([]OpenHedge, 0, len(plans))
	var hedgesMu sync.Mutex
	err := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(e.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		plan := plans[idx]
		result := futuresResults[idx]
		if result.Code != 0 || result.OrderID == 0 {
			return e.recordExecutionIncident("open_futures_rejected", plan.futuresOrder.Symbol, fmt.Sprintf("code=%d msg=%s", result.Code, result.Msg))
		}

		hedge, openErr := e.openHedgeForTarget(workerCtx, plan.target, result)
		if openErr != nil {
			var skipped nonFatalOpenAbortError
			if errors.As(openErr, &skipped) {
				e.logger.Printf("模块四跳过 %s: %v", plan.target.Candidate.Symbol, skipped)
				return nil
			}
			return e.recordExecutionIncident("open_recovery", plan.target.Candidate.Symbol, openErr.Error())
		}

		hedgesMu.Lock()
		hedges = append(hedges, hedge)
		persistErr := e.syncPersistedOpenHedges(hedges)
		hedgesMu.Unlock()
		return persistErr
	})
	if err != nil {
		return nil, err
	}
	return hedges, nil
}

func closeSpotTarget(hedge *OpenHedge, closeableQty float64, spotRule SymbolTradeRules) (float64, float64, bool) {
	spotQty := normalizeQuantity(closeableQty, spotRule, hedge.Target.SpotBasePrecision)
	spotReferencePrice := hedge.LastKnownSpotPrice()
	if spotReferencePrice <= 0 {
		spotReferencePrice = hedge.Target.MarkPrice
	}
	return spotQty, spotReferencePrice, isQuantityTradable(spotQty, spotReferencePrice, spotRule)
}

func (e *FundingRateSniperEngine) revertSpotCloseAfterFuturesFailure(ctx context.Context, hedge *OpenHedge, spotOrder SpotOrderResponse, spotReferencePrice float64) error {
	direction := hedge.carryDirection()
	spotExecutedQty := parseFloat(spotOrder.ExecutedQty)
	if spotExecutedQty <= executionQtyTolerance {
		return nil
	}
	spotRule := e.spotRules[hedge.Target.Pair]
	recoverQty := normalizeQuantity(spotExecutedQty, spotRule, hedge.Target.SpotBasePrecision)
	if recoverQty <= executionQtyTolerance || !isQuantityTradable(recoverQty, spotReferencePrice, spotRule) {
		return fmt.Errorf("现货已平部分但无法安全回滚: spotQty=%.8f", spotExecutedQty)
	}
	recoveryResult, err := e.executeSpotRecoveryMarketProtocol(ctx, hedge.Target.Pair, direction.openSpotSide(), recoverQty, "sclose_revert", direction.spotVenue(), "close_recovery", hedge.Target.Symbol, "revert_spot_after_futures_close_failure", fmt.Sprintf("direction=%s spotQty=%.8f recoverQty=%.8f", direction, spotExecutedQty, recoverQty))
	if err != nil {
		return fmt.Errorf("现货回补失败: %w", err)
	}
	hedge.RecoveryFrictionUSDT += recoveryResult.Action.RecoveryFrictionUSDT
	return nil
}

func (e *FundingRateSniperEngine) fetchPremiumIndex(ctx context.Context) ([]PremiumIndexItem, error) {
	var payload []PremiumIndexItem
	if err := e.getJSON(ctx, e.client.FuturesBaseURL()+"/fapi/v1/premiumIndex", &payload); err != nil {
		return nil, fmt.Errorf("获取 premiumIndex 失败: %w", err)
	}
	return payload, nil
}

func (e *FundingRateSniperEngine) fetchPremiumIndexBySymbol(ctx context.Context, symbol string) (PremiumIndexItem, error) {
	var payload PremiumIndexItem
	if err := e.getJSON(ctx, e.client.FuturesBaseURL()+"/fapi/v1/premiumIndex?symbol="+url.QueryEscape(symbol), &payload); err != nil {
		return PremiumIndexItem{}, fmt.Errorf("获取 premiumIndex 失败: %w", err)
	}
	return payload, nil
}

func (e *FundingRateSniperEngine) refreshTargetFundingEstimate(ctx context.Context, target *MonitoredTarget, force bool) error {
	if target == nil {
		return fmt.Errorf("target 为空")
	}
	if !force && !target.LastEstimateAt.IsZero() && time.Since(target.LastEstimateAt) < monitorEstimateRefreshInterval {
		return nil
	}

	item, err := e.fetchPremiumIndexBySymbol(ctx, target.Candidate.Symbol)
	if err != nil {
		return err
	}
	if e.cfg.MaxPremiumIndexStaleness > 0 {
		staleness := premiumIndexStaleness(e.serverNow(), item)
		if staleness > e.cfg.MaxPremiumIndexStaleness {
			return fmt.Errorf("premiumIndex 数据过期: age=%s limit=%s", staleness.Round(time.Millisecond), e.cfg.MaxPremiumIndexStaleness)
		}
	}

	estimatedRate, lastFundingRate, fundingJump, basisRate, _ := estimateScanFundingSignal(item)
	target.Candidate.Direction = fundingDirectionFromRate(estimatedRate)
	target.Candidate.EstimatedRate = estimatedRate
	target.Candidate.EstimatedRateText = formatFloat(estimatedRate)
	target.Candidate.LastFundingRate = lastFundingRate
	target.Candidate.FundingRateJump = fundingJump
	target.Candidate.BasisRate = basisRate
	target.Candidate.ScanScoreRate = liveFundingPriorityScore(e.cfg.StrategyConfig.LiveFundingDirectionMode, estimatedRate, basisRate)
	target.Candidate.MarkPrice = parseFloat(item.MarkPrice)
	if nextFundingTime := time.UnixMilli(item.NextFundingTime); !nextFundingTime.IsZero() {
		target.Candidate.NextFundingTime = nextFundingTime
	}
	target.LastEstimateAt = time.Now()
	return nil
}

func (e *FundingRateSniperEngine) fetchDualBookTicker(ctx context.Context, spotSymbol string, futuresSymbol string) (BookTicker, BookTicker, error) {
	spotTicker, spotErr := e.fetchSpotBookTicker(ctx, spotSymbol)
	if spotErr != nil {
		return BookTicker{}, BookTicker{}, fmt.Errorf("现货盘口失败: %w", spotErr)
	}
	futuresTicker, futuresErr := e.fetchFuturesBookTicker(ctx, futuresSymbol)
	if futuresErr != nil {
		return BookTicker{}, BookTicker{}, fmt.Errorf("合约盘口失败: %w", futuresErr)
	}
	return spotTicker, futuresTicker, nil
}

func (e *FundingRateSniperEngine) fetchDualOrderBookDepth(ctx context.Context, spotSymbol string, futuresSymbol string) (ParsedOrderBookDepth, ParsedOrderBookDepth, error) {
	if e.spotDepthCache != nil && e.futuresDepthCache != nil {
		if spotDepth, ok := e.spotDepthCache.Get(spotSymbol); ok {
			if futuresDepth, ok := e.futuresDepthCache.Get(futuresSymbol); ok {
				return spotDepth, futuresDepth, nil
			}
		}
	}

	var (
		wg              sync.WaitGroup
		spotDepthRaw    OrderBookDepth
		futuresDepthRaw OrderBookDepth
		spotErr         error
		futuresErr      error
	)

	limit := strconv.Itoa(e.cfg.StrategyConfig.DepthLevels)
	wg.Add(2)
	go func() {
		defer wg.Done()
		spotErr = e.getJSON(ctx, e.client.SpotBaseURL()+"/api/v3/depth?symbol="+spotSymbol+"&limit="+limit, &spotDepthRaw)
	}()
	go func() {
		defer wg.Done()
		futuresErr = e.getJSON(ctx, e.client.FuturesBaseURL()+"/fapi/v1/depth?symbol="+futuresSymbol+"&limit="+limit, &futuresDepthRaw)
	}()
	wg.Wait()

	if spotErr != nil {
		return ParsedOrderBookDepth{}, ParsedOrderBookDepth{}, fmt.Errorf("现货深度失败: %w", spotErr)
	}
	if futuresErr != nil {
		return ParsedOrderBookDepth{}, ParsedOrderBookDepth{}, fmt.Errorf("合约深度失败: %w", futuresErr)
	}

	receivedAt := time.Now().UTC()
	return ParsedOrderBookDepth{
			LastUpdateID: spotDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(spotDepthRaw.Bids),
			Asks:         parseDepthLevels(spotDepthRaw.Asks),
			ReceivedAt:   receivedAt,
		}, ParsedOrderBookDepth{
			LastUpdateID: futuresDepthRaw.LastUpdateID,
			Bids:         parseDepthLevels(futuresDepthRaw.Bids),
			Asks:         parseDepthLevels(futuresDepthRaw.Asks),
			ReceivedAt:   receivedAt,
		}, nil
}

const (
	depthStabilityMaxAge         = 750 * time.Millisecond
	depthStabilityLookbackWindow = 1500 * time.Millisecond
	depthStabilityObserveRatio   = 0.80
	depthStabilityCooldownRatio  = 0.65
	depthStabilityPriceDriftRate = 0.0008
)

func openDepthLevelsForDirection(direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth) ([]DepthLevel, []DepthLevel) {
	switch direction {
	case fundingCarryDirectionNegative:
		return spotDepth.Bids, futuresDepth.Asks
	default:
		return spotDepth.Asks, futuresDepth.Bids
	}
}

func visibleDepthNotionalForDirection(direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth) (float64, float64) {
	spotLevels, futuresLevels := openDepthLevelsForDirection(direction, spotDepth, futuresDepth)
	return estimateVisibleDepthNotional(spotLevels), estimateVisibleDepthNotional(futuresLevels)
}

func openEntrySpreadRate(direction fundingCarryDirection, spotVWAP float64, futuresVWAP float64) float64 {
	return marketdata.EntrySpreadRate(direction == fundingCarryDirectionNegative, spotVWAP, futuresVWAP)
}

func priceMovedAdversely(direction fundingCarryDirection, previousSpot float64, currentSpot float64, previousFutures float64, currentFutures float64) bool {
	return marketdata.PriceMovedAdversely(direction == fundingCarryDirectionNegative, previousSpot, currentSpot, previousFutures, currentFutures, depthStabilityPriceDriftRate)
}

func (e *FundingRateSniperEngine) evaluateOpenDepthStability(target *MonitoredTarget, direction fundingCarryDirection, spotDepth ParsedOrderBookDepth, futuresDepth ParsedOrderBookDepth, currentSpotBest float64, currentFuturesBest float64) (MarketRiskDecision, string, bool, float64, float64, float64) {
	spotVisible, futuresVisible := visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
	now := time.Now().UTC()
	if !spotDepth.ReceivedAt.IsZero() {
		spotAge := now.Sub(spotDepth.ReceivedAt)
		if spotAge > depthStabilityMaxAge {
			return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
				fmt.Sprintf("spot depth 延迟过高: age=%s limit=%s", spotAge.Round(time.Millisecond), depthStabilityMaxAge), true, spotVisible, futuresVisible, 0
		}
	}
	if !futuresDepth.ReceivedAt.IsZero() {
		futuresAge := now.Sub(futuresDepth.ReceivedAt)
		if futuresAge > depthStabilityMaxAge {
			return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
				fmt.Sprintf("futures depth 延迟过高: age=%s limit=%s", futuresAge.Round(time.Millisecond), depthStabilityMaxAge), true, spotVisible, futuresVisible, 0
		}
	}
	if target == nil || target.LastObserveTime.IsZero() || now.Sub(target.LastObserveTime) > depthStabilityLookbackWindow {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, 0
	}
	previousVisible := minPositiveFloat(target.LastSpotVisibleNotional, target.LastFuturesVisibleNotional)
	currentVisible := minPositiveFloat(spotVisible, futuresVisible)
	if previousVisible <= 0 || currentVisible <= 0 {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, 0
	}
	ratio := currentVisible / previousVisible
	if !priceMovedAdversely(direction, target.LastSpotAsk, currentSpotBest, target.LastFuturesBid, currentFuturesBest) {
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, ratio
	}
	switch {
	case ratio < depthStabilityCooldownRatio:
		return MarketRiskDecision{Category: "depth_stability", Severity: "major", Action: "symbol_cooldown"},
			fmt.Sprintf("可见深度快速塌陷: ratio=%.4f prev=%.6f current=%.6f", ratio, previousVisible, currentVisible), true, spotVisible, futuresVisible, ratio
	case ratio < depthStabilityObserveRatio:
		return MarketRiskDecision{Category: "depth_stability", Severity: "minor", Action: "observe"},
			fmt.Sprintf("可见深度快速恶化: ratio=%.4f prev=%.6f current=%.6f", ratio, previousVisible, currentVisible), true, spotVisible, futuresVisible, ratio
	default:
		return MarketRiskDecision{}, "", false, spotVisible, futuresVisible, ratio
	}
}

func computeAverageFillPrice(levels []DepthLevel, requiredQty float64) (float64, float64, float64, bool) {
	return marketdata.ComputeAverageFillPrice(levels, requiredQty)
}

func (e *FundingRateSniperEngine) applyMonitorObserveBreach(target *MonitoredTarget, consecutiveBreaches *int, netProfit float64, reason string) {
	*consecutiveBreaches = *consecutiveBreaches + 1
	target.BreachCount = *consecutiveBreaches
	target.LastNetProfit = netProfit
	e.transitionMonitorState(target, "observing", reason)
	if *consecutiveBreaches < e.cfg.RiskConfig.MonitorMaxConsecutiveBreach {
		return
	}
	e.invalidateMonitorTarget(target, netProfit, fmt.Sprintf("连续 %d 次触发观察阈值后作废: %s", e.cfg.RiskConfig.MonitorMaxConsecutiveBreach, reason))
}

func (e *FundingRateSniperEngine) invalidateMonitorTarget(target *MonitoredTarget, netProfit float64, reason string) {
	target.LastNetProfit = netProfit
	e.transitionMonitorState(target, "invalidated", reason)
	if atomic.CompareAndSwapInt32(&target.IsValid, 1, 0) {
		target.InvalidReason = reason
		e.logger.Printf("目标 %s 已作废: reason=%s net=%.6f spread=%.6f borrowRate=%.6f borrowUSDT=%.6f", target.Candidate.Symbol, target.InvalidReason, target.LastNetProfit, target.LastSpread, target.LastBorrowReserveRate, target.LastBorrowReserveUSDT)
	}
}

func (e *FundingRateSniperEngine) transitionMonitorState(target *MonitoredTarget, nextState string, detail string) {
	prevState := target.MonitorState
	if prevState == nextState {
		return
	}

	target.MonitorState = nextState
	e.logger.Printf(
		"模块三状态变更，symbol=%s %s->%s breach=%d net=%.6f spread=%.6f borrowRate=%.6f borrowUSDT=%.6f detail=%s",
		target.Candidate.Symbol,
		nonEmpty(prevState, "unknown"),
		nextState,
		target.BreachCount,
		target.LastNetProfit,
		target.LastSpread,
		target.LastBorrowReserveRate,
		target.LastBorrowReserveUSDT,
		nonEmpty(detail, "-"),
	)
}

func (e *FundingRateSniperEngine) recordExecutionIncident(phase string, symbol string, detail string) error {
	count := atomic.AddInt32(&e.executionIncidents, 1)
	message := fmt.Sprintf("执行事故，phase=%s symbol=%s count=%d detail=%s", nonEmpty(phase, "unknown"), nonEmpty(symbol, "-"), count, nonEmpty(detail, "-"))
	e.logger.Print(message)
	fatal := e.cfg.RiskConfig.MaxExecutionIncidents > 0 && int(count) >= e.cfg.RiskConfig.MaxExecutionIncidents
	e.emitRiskEvent(newRiskEvent("incident", RiskCategoryExecution, RiskSeverityWarning, phase, symbol, "incident", int(count), e.cfg.RiskConfig.MaxExecutionIncidents, fatal, detail))
	if fatal {
		return fatalEngineError{message: fmt.Sprintf("执行事故次数达到熔断阈值(%d)，已停止引擎: %s", e.cfg.RiskConfig.MaxExecutionIncidents, message)}
	}
	return nil
}

func (e *FundingRateSniperEngine) recordExecutionAction(phase string, symbol string, action string, detail string) {
	category := RiskCategoryExecution
	if strings.Contains(phase, "recovery") {
		category = RiskCategoryRecovery
	}
	e.emitRiskEvent(newRiskEvent("action", category, RiskSeverityInfo, phase, symbol, action, int(atomic.LoadInt32(&e.executionIncidents)), e.cfg.RiskConfig.MaxExecutionIncidents, false, detail))
	if strings.Contains(phase, "recovery") {
		e.noteRecoveryStatus(symbol, action, detail)
		if err := e.noteRecoveryAction(phase, symbol, action, detail); err != nil {
			e.logger.Printf("平台级风控已触发: %v", err)
		}
	}
}

func isProtectedIOCRequestType(orderType string, tif string) bool {
	return strings.EqualFold(strings.TrimSpace(orderType), "LIMIT") &&
		strings.EqualFold(strings.TrimSpace(tif), protectedExecutionIOC)
}

func protectedIOCUnfilledRatio(requestedQty float64, executedQty float64) float64 {
	if requestedQty <= executionQtyTolerance {
		return 0
	}
	executedQty = maxFloat(executedQty, 0)
	if executedQty > requestedQty {
		executedQty = requestedQty
	}
	return maxFloat(requestedQty-executedQty, 0) / requestedQty
}

func (e *FundingRateSniperEngine) noteProtectedIOCObservation(symbol string, market string, observation ProtectedIOCObservation) {
	if e == nil || observation.RequestedQty <= executionQtyTolerance {
		return
	}
	e.platformRiskMu.Lock()
	e.protectedIOCAttempts++
	e.protectedIOCUnfilledSum += observation.UnfilledRatio
	if observation.Canceled {
		e.protectedIOCCanceled++
	}
	if observation.Expired {
		e.protectedIOCExpired++
	}
	if observation.Partial {
		e.protectedIOCPartial++
	}
	if observation.NeedsAttention() {
		e.platformState.RecordException("protected_ioc", "major", fmt.Sprintf(
			"market=%s symbol=%s status=%s requested=%.8f executed=%.8f unfilled_ratio=%.4f",
			nonEmpty(strings.TrimSpace(market), "unknown"),
			nonEmpty(symbol, "-"),
			nonEmpty(strings.ToUpper(strings.TrimSpace(observation.RawStatus)), "UNKNOWN"),
			observation.RequestedQty,
			observation.ExecutedQty,
			observation.UnfilledRatio,
		))
	}
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) protectedIOCStatsLocked() (attempts int, canceled int, expired int, partial int, avgUnfilledRatio float64, cancelRate float64) {
	attempts = e.protectedIOCAttempts
	canceled = e.protectedIOCCanceled
	expired = e.protectedIOCExpired
	partial = e.protectedIOCPartial
	if attempts <= 0 {
		return attempts, canceled, expired, partial, 0, 0
	}
	avgUnfilledRatio = e.protectedIOCUnfilledSum / float64(attempts)
	cancelRate = float64(canceled+expired) / float64(attempts)
	return attempts, canceled, expired, partial, avgUnfilledRatio, cancelRate
}

func hasConfirmedOrderExecution(snapshot OrderExecutionSnapshot) bool {
	return snapshot.HasConfirmedExecution()
}

func unknownOrderReconcileTimeout(cfg EngineConfig) time.Duration {
	if cfg.ExecutionRecoveryTimeout > 0 {
		return cfg.ExecutionRecoveryTimeout
	}
	return 8 * time.Second
}

func (e *FundingRateSniperEngine) reconcileSpotOrderTerminalState(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	return reconcileOrderTerminalState(
		ctx,
		unknownOrderReconcileTimeout(e.cfg),
		"spot",
		symbol,
		clientOrderID,
		func(queryCtx context.Context) (orderTerminalReconcilePollResult[SpotOrderResponse], error) {
			queried, err := e.querySpotOrderByClientID(queryCtx, symbol, clientOrderID)
			if err != nil {
				return orderTerminalReconcilePollResult[SpotOrderResponse]{}, err
			}
			return orderTerminalReconcilePollResult[SpotOrderResponse]{
				Value: queried,
				View:  queried.SemanticView(),
			}, nil
		},
		nil,
	)
}

func (e *FundingRateSniperEngine) reconcileFuturesOrderTerminalState(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	return reconcileOrderTerminalState(
		ctx,
		unknownOrderReconcileTimeout(e.cfg),
		"futures",
		symbol,
		clientOrderID,
		func(queryCtx context.Context) (orderTerminalReconcilePollResult[FuturesBatchOrderResult], error) {
			queried, err := e.queryFuturesOrderByClientID(queryCtx, symbol, clientOrderID)
			if err != nil {
				return orderTerminalReconcilePollResult[FuturesBatchOrderResult]{}, err
			}
			return orderTerminalReconcilePollResult[FuturesBatchOrderResult]{
				Value: queried,
				View:  queried.SemanticView(),
			}, nil
		},
		nil,
	)
}
