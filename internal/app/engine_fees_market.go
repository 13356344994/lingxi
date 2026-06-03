package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/marketdata"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载手续费估算与币种换算、签名请求与本地限频/权重
// 守卫、服务器时间同步、行情扫描信号与市场容量/深度/成交量采集等费用与行情域逻辑（同包搬移、
// 行为不变），向单文件 ≤ 800 行门禁靠拢。

func futuresResultToSnapshot(result FuturesBatchOrderResult) OrderExecutionSnapshot {
	snapshot := OrderExecutionSnapshot{
		Market:        "futures",
		Symbol:        result.Symbol,
		ClientOrderID: result.ClientOrderID,
		OrderID:       result.OrderID,
		Status:        result.Status,
		ExecutedQty:   parseFloat(result.ExecutedQty),
		AvgPrice:      parseFloat(result.AvgPrice),
		EventTime:     time.UnixMilli(result.UpdateTime),
	}
	if snapshot.ExecutedQty > 0 && snapshot.AvgPrice > 0 {
		snapshot.CumQuoteQty = snapshot.ExecutedQty * snapshot.AvgPrice
	}
	return snapshot
}

func (e *FundingRateSniperEngine) estimateSpotCommissionUSDT(ctx context.Context, order SpotOrderResponse) float64 {
	avgPrice := 0.0
	executedQty := parseFloat(order.ExecutedQty)
	quoteQty := parseFloat(order.CummulativeQuoteQty)
	if executedQty > 0 && quoteQty > 0 {
		avgPrice = quoteQty / executedQty
	}

	total := 0.0
	for _, fill := range order.Fills {
		commission := parseFloat(fill.Commission)
		commissionAsset := strings.ToUpper(fill.CommissionAsset)
		switch commissionAsset {
		case "", "USDT":
			total += commission
		default:
			if meta, ok := e.spotMeta[order.Symbol]; ok && commissionAsset == strings.ToUpper(meta.BaseAsset) && avgPrice > 0 {
				total += commission * avgPrice
				continue
			}
			assetPrice, err := e.fetchSpotAssetUSDTPrice(ctx, commissionAsset)
			if err == nil && assetPrice > 0 {
				total += commission * assetPrice
			}
		}
	}
	return total
}

func (e *FundingRateSniperEngine) estimateFuturesExecutionFeeUSDT(ctx context.Context, snapshot OrderExecutionSnapshot) float64 {
	if snapshot.OrderID > 0 {
		total, err := e.fetchFuturesTradeCommissionUSDT(ctx, snapshot.Symbol, snapshot.OrderID, snapshot.AvgPrice)
		if err == nil && total > 0 {
			return total
		}
		if err != nil {
			e.logger.Printf("查询合约成交手续费失败，symbol=%s orderId=%d err=%v", snapshot.Symbol, snapshot.OrderID, err)
		}
	}
	return e.convertCommissionToUSDT(ctx, snapshot.Symbol, snapshot.CommissionAsset, snapshot.Commission, snapshot.AvgPrice)
}

func (e *FundingRateSniperEngine) fetchFuturesTradeCommissionUSDT(ctx context.Context, symbol string, orderID int64, referencePrice float64) (float64, error) {
	if strings.TrimSpace(symbol) == "" || orderID == 0 {
		return 0, nil
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	params.Set("limit", "1000")
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+"/fapi/v1/userTrades", params)
	if err != nil {
		return 0, err
	}

	var payload []struct {
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("解析合约成交手续费失败: %w", err)
	}

	total := 0.0
	for _, trade := range payload {
		total += e.convertCommissionToUSDT(ctx, symbol, trade.CommissionAsset, parseFloat(trade.Commission), referencePrice)
	}
	return total, nil
}

func (e *FundingRateSniperEngine) convertCommissionToUSDT(ctx context.Context, symbol string, commissionAsset string, commission float64, referencePrice float64) float64 {
	if commission <= 0 {
		return 0
	}
	switch strings.ToUpper(strings.TrimSpace(commissionAsset)) {
	case "", "USDT":
		return commission
	default:
		if meta, ok := e.spotMeta[symbol]; ok && strings.EqualFold(meta.BaseAsset, commissionAsset) && referencePrice > 0 {
			return commission * referencePrice
		}
		if spotPair, ok := e.goldenSpotPairByFuture[symbol]; ok {
			if spotMeta, spotOK := e.spotMeta[spotPair]; spotOK && strings.EqualFold(spotMeta.BaseAsset, commissionAsset) && referencePrice > 0 {
				return commission * referencePrice
			}
		} else if meta, ok := e.futuresMeta[symbol]; ok {
			if spotMeta, spotOK := e.spotMeta[meta.Pair]; spotOK && strings.EqualFold(spotMeta.BaseAsset, commissionAsset) && referencePrice > 0 {
				return commission * referencePrice
			}
		}
		assetPrice, err := e.fetchSpotAssetUSDTPrice(ctx, commissionAsset)
		if err == nil && assetPrice > 0 {
			return commission * assetPrice
		}
		return 0
	}
}

func estimateExpectedFundingIncome(hedge OpenHedge) float64 {
	referencePrice := hedge.OpenFuturesAvgPrice
	if referencePrice <= 0 {
		referencePrice = hedge.Target.MarkPrice
	}
	if referencePrice <= 0 || hedge.HedgeQty <= 0 || hedge.Target.EstimatedRate == 0 {
		return 0
	}

	return hedge.carryDirection().expectedFundingIncome(referencePrice, hedge.HedgeQty, hedge.Target.EstimatedRate)
}

func calculateResidualSpotValue(balances map[string]float64, hedges []OpenHedge) float64 {
	priceByAsset := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		priceByAsset[hedge.Target.SpotBaseAsset] = hedge.LastKnownSpotPrice()
	}

	total := 0.0
	for asset, balance := range balances {
		if balance <= 0 {
			continue
		}
		price := priceByAsset[asset]
		if price <= 0 {
			continue
		}
		total += balance * price
	}
	return total
}

func (e *FundingRateSniperEngine) fetchSpotAssetUSDTPrice(ctx context.Context, asset string) (float64, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" {
		return 0, fmt.Errorf("手续费资产为空")
	}
	if asset == "USDT" {
		return 1, nil
	}

	symbol := asset + "USDT"
	ticker := BookTicker{}
	if err := e.getJSON(ctx, e.client.SpotBaseURL()+"/api/v3/ticker/bookTicker?symbol="+symbol, &ticker); err != nil {
		return 0, err
	}

	price := parseFloat(nonEmpty(ticker.AskPrice, ticker.BidPrice))
	if price <= 0 {
		return 0, fmt.Errorf("无法获取 %s 的 USDT 价格", asset)
	}
	return price, nil
}

func (e *FundingRateSniperEngine) buildSettlementAlerts(afterSnapshot CycleAccountSnapshot, hedges []OpenHedge, residualSpotValue float64, netPnL float64, snapshotNetPnL float64) []string {
	alerts := make([]string, 0)

	if math.Abs(residualSpotValue) >= e.cfg.ResidualSpotAlertUSDT {
		alerts = append(alerts, fmt.Sprintf("现货残余价值过高: %.6f USDT", residualSpotValue))
	}

	for symbol, qty := range afterSnapshot.FuturesPositionAmounts {
		if math.Abs(qty) < e.cfg.ResidualPositionAlertQty {
			continue
		}
		alerts = append(alerts, fmt.Sprintf("合约残仓未清零: %s positionAmt=%.10f", symbol, qty))
	}

	for asset, balance := range afterSnapshot.SpotAssetBalances {
		if strings.EqualFold(asset, "USDT") || strings.EqualFold(asset, "BNB") {
			continue
		}
		if balance <= 0 {
			continue
		}
		assetPrice := latestSpotAssetPriceFromHedges(asset, hedges)
		if assetPrice <= 0 {
			continue
		}
		if balance*assetPrice < e.cfg.ResidualSpotAlertUSDT {
			continue
		}
		alerts = append(alerts, fmt.Sprintf("现货残仓未清零: %s balance=%.10f value=%.6f USDT", asset, balance, balance*assetPrice))
	}

	diff := math.Abs(netPnL - snapshotNetPnL)
	if diff >= e.cfg.SettlementDiffAlertUSDT {
		alerts = append(alerts, fmt.Sprintf("交易口径与账户口径偏差过大: diff=%.6f USDT", diff))
	}

	return alerts
}

func latestSpotAssetPriceFromHedges(asset string, hedges []OpenHedge) float64 {
	for _, hedge := range hedges {
		if !strings.EqualFold(asset, hedge.Target.SpotBaseAsset) {
			continue
		}
		price := hedge.LastKnownSpotPrice()
		if price > 0 {
			return price
		}
	}
	return 0
}

func resolveSpotNetBaseQuantity(order SpotOrderResponse, baseAsset string) float64 {
	executedQty := parseFloat(order.ExecutedQty)
	if executedQty <= 0 {
		return 0
	}

	netQty := executedQty
	for _, fill := range order.Fills {
		if !strings.EqualFold(fill.CommissionAsset, baseAsset) {
			continue
		}
		netQty -= parseFloat(fill.Commission)
	}

	if netQty < 0 {
		return 0
	}

	return netQty
}

func (e *FundingRateSniperEngine) signedRequest(ctx context.Context, method string, endpoint string, params url.Values) ([]byte, error) {
	// 影子模式硬拦截：这是所有签名请求的唯一咽喉点，在此挡住一切非 GET 调用即可保证「绝无真实订单/
	// 资金动作漏出」——无论引擎走开仓/平仓/恢复/划转哪条路径。只读 GET 放行以获取真实行情与账户状态。
	if shadowShouldBlockSignedRequest(e.cfg.ExecutionConfig.ShadowTrading, method) {
		e.noteShadowBlocked(method, endpoint, params)
		return nil, shadowBlockedError{method: strings.ToUpper(strings.TrimSpace(method)), endpoint: strings.TrimSpace(endpoint)}
	}
	gateway := e.ensureBinanceGateway()
	if gateway == nil {
		return nil, fmt.Errorf("binance gateway 未初始化")
	}
	return gateway.SignedRequest(ctx, method, endpoint, params)
}

func cloneURLValues(values url.Values) url.Values {
	cloned := url.Values{}
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func (e *FundingRateSniperEngine) isCriticalExposureRequest(method string, endpoint string, params url.Values) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	clientOrderID := ""
	if params != nil {
		clientOrderID = strings.ToLower(strings.TrimSpace(params.Get("newClientOrderId")))
	}
	if isSignedOrderEndpoint(endpoint) {
		if method == http.MethodDelete {
			return true
		}
		if strings.EqualFold(params.Get("reduceOnly"), "true") {
			return true
		}
		for _, prefix := range []string{"fclose", "ftrim", "frescue", "sclose", "srevert", "sclose_revert", "exp_"} {
			if strings.HasPrefix(clientOrderID, prefix) {
				return true
			}
		}
	}
	return false
}

func isSignedOrderEndpoint(endpoint string) bool {
	return exchange.IsSignedOrderEndpoint(endpoint)
}

func signedOrderRequestUnits(method string, endpoint string, params url.Values) int {
	return exchange.SignedOrderRequestUnits(method, endpoint, params)
}

func (e *FundingRateSniperEngine) blockOrderRequestByLocalRateLimit(method string, endpoint string, params url.Values) error {
	if e == nil {
		return nil
	}
	units := signedOrderRequestUnits(method, endpoint, params)
	if units <= 0 {
		return nil
	}
	critical := e.isCriticalExposureRequest(method, endpoint, params)
	now := time.Now().UTC()
	e.orderRateMu.Lock()
	e.pruneOrderRateRequestsLocked(now)
	if !e.platformState.OrderRateLimitUntil.IsZero() && e.platformState.OrderRateLimitUntil.After(now) {
		remaining := e.platformState.OrderRateLimitUntil.Sub(now).Round(time.Millisecond)
		e.orderRateMu.Unlock()
		return e.noteOrderRateLimitEvent("exchange_cooldown", majorIfCritical(critical), fmt.Sprintf("endpoint=%s units=%d critical=%t remaining=%s", endpoint, units, critical, remaining))
	}
	limit10s := orderRateLimitCapacity10s
	limit1m := orderRateLimitCapacity1m
	if !critical {
		limit10s -= orderRateLimitReserve10s
		limit1m -= orderRateLimitReserve1m
	}
	projected10s := len(e.orderRateRequests10s) + units
	projected1m := len(e.orderRateRequests1m) + units
	if projected10s > limit10s || projected1m > limit1m {
		e.orderRateMu.Unlock()
		return e.noteOrderRateLimitEvent("local_guard", majorIfCritical(critical), fmt.Sprintf("endpoint=%s units=%d critical=%t projected10s=%d/%d projected1m=%d/%d", endpoint, units, critical, projected10s, limit10s, projected1m, limit1m))
	}
	for i := 0; i < units; i++ {
		e.orderRateRequests10s = append(e.orderRateRequests10s, now)
		e.orderRateRequests1m = append(e.orderRateRequests1m, now)
	}
	e.orderRateMu.Unlock()
	return nil
}

func (e *FundingRateSniperEngine) pruneOrderRateRequestsLocked(now time.Time) {
	prune := func(values []time.Time, window time.Duration) []time.Time {
		kept := values[:0]
		for _, item := range values {
			if now.Sub(item) < window {
				kept = append(kept, item)
			}
		}
		return kept
	}
	e.orderRateRequests10s = prune(e.orderRateRequests10s, orderRateLimitWindow10s)
	e.orderRateRequests1m = prune(e.orderRateRequests1m, orderRateLimitWindow1m)
}

func majorIfCritical(critical bool) string {
	if critical {
		return "critical"
	}
	return "major"
}

func (e *FundingRateSniperEngine) noteOrderRateLimitEvent(category string, severity string, detail string) error {
	if e == nil {
		return fmt.Errorf("order rate limiter 触发: %s", detail)
	}
	e.platformRiskMu.Lock()
	e.platformState.RecordException("order_rate_limit", strings.TrimSpace(nonEmpty(severity, "major")), fmt.Sprintf("%s %s", strings.TrimSpace(category), strings.TrimSpace(detail)))
	e.runtimeReporter.EmitLocked(EngineStage(""))
	e.platformRiskMu.Unlock()
	return fmt.Errorf("order rate limiter 触发: %s", detail)
}

func (e *FundingRateSniperEngine) blockNonCriticalRequestByUsedWeight(method string, endpoint string, params url.Values) (func(), error) {
	if e == nil || e.client == nil || e.isCriticalExposureRequest(method, endpoint, params) {
		return func() {}, nil
	}
	projected, threshold, ok := e.client.ReserveUsedWeightBudget(1)
	if threshold <= 0 || ok {
		return func() {
			e.client.ReleaseUsedWeightBudget(1)
		}, nil
	}
	return func() {}, e.tripPlatformFuse("used_weight", "critical", fmt.Sprintf("usedWeightProjected=%d 已达到阈值 %d，主动阻断非平仓/非救援请求 endpoint=%s", projected, threshold, endpoint))
}

func (e *FundingRateSniperEngine) syncServerTimeOffset(ctx context.Context) error {
	var payload struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := e.getJSON(ctx, e.client.FuturesBaseURL()+"/fapi/v1/time", &payload); err != nil {
		return fmt.Errorf("同步服务器时间失败: %w", err)
	}

	e.serverTimeOffset = time.Until(time.UnixMilli(payload.ServerTime))
	return nil
}

func (e *FundingRateSniperEngine) currentServerTimeMillis() int64 {
	return e.serverNow().UnixMilli()
}

func (e *FundingRateSniperEngine) serverNow() time.Time {
	return time.Now().Add(e.serverTimeOffset)
}

func sleepUntilServerTimeResyncInterval(remaining time.Duration) time.Duration {
	if remaining <= sleepNearThreshold {
		return sleepResyncIntervalNear
	}
	if remaining <= sleepMidThreshold {
		return sleepResyncIntervalMid
	}
	return sleepResyncIntervalFar
}

func sleepUntilServerTimeWaitChunk(remaining time.Duration) time.Duration {
	if remaining <= sleepNearThreshold {
		return sleepWaitChunkNear
	}
	if remaining <= sleepMidThreshold {
		return sleepWaitChunkMid
	}
	return sleepWaitChunkFar
}

func (e *FundingRateSniperEngine) sleepUntilServerTime(ctx context.Context, target time.Time) error {
	lastResyncAttemptAt := time.Now()
	for {
		if err := e.checkLiveSafetyGuards("sleep_until_server_time"); err != nil {
			return err
		}
		remainingServer := target.Sub(e.serverNow())
		if remainingServer <= 0 {
			return nil
		}

		now := time.Now()
		resyncInterval := sleepUntilServerTimeResyncInterval(remainingServer)
		if now.Sub(lastResyncAttemptAt) >= resyncInterval {
			lastResyncAttemptAt = now
			if err := e.syncServerTimeOffset(ctx); err != nil && e.logger != nil {
				e.logger.Printf("等待唤醒期间刷新服务器时间偏移失败，将沿用旧偏移: %v", err)
			}
			continue
		}

		waitUntil := target.Add(-e.serverTimeOffset)
		remainingLocal := time.Until(waitUntil)
		if remainingLocal <= 0 {
			return nil
		}

		untilNextResync := resyncInterval - now.Sub(lastResyncAttemptAt)
		if untilNextResync > 0 && untilNextResync < remainingLocal {
			remainingLocal = untilNextResync
		}
		waitChunk := sleepUntilServerTimeWaitChunk(remainingServer)
		if waitChunk > 0 && remainingLocal > waitChunk {
			remainingLocal = waitChunk
		}
		timer := time.NewTimer(remainingLocal)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func estimateScanFundingSignal(item PremiumIndexItem) (float64, float64, float64, float64, float64) {
	lastFundingRate := parseFloat(item.LastFundingRate)
	interestRate := parseFloat(item.InterestRate)
	markPrice := parseFloat(item.MarkPrice)
	indexPrice := parseFloat(item.IndexPrice)
	estimatedSettlePrice := parseFloat(item.EstimatedSettlePrice)

	basisRate := 0.0
	if markPrice > 0 && indexPrice > 0 {
		basisRate = (markPrice - indexPrice) / indexPrice
	}

	premiumEstimate := basisRate
	if estimatedSettlePrice > 0 && indexPrice > 0 {
		settleBasisRate := (estimatedSettlePrice - indexPrice) / indexPrice
		if math.Abs(settleBasisRate) > math.Abs(premiumEstimate) {
			premiumEstimate = settleBasisRate
		}
	}

	estimatedFundingRate := lastFundingRate
	conservativeRate := conservativeLiveFundingRate(lastFundingRate, premiumEstimate, interestRate)
	if conservativeRate != 0 {
		estimatedFundingRate = conservativeRate
	}

	fundingJump := math.Abs(estimatedFundingRate - lastFundingRate)
	scanScoreRate := math.Abs(estimatedFundingRate) - math.Abs(basisRate)
	return estimatedFundingRate, lastFundingRate, fundingJump, basisRate, scanScoreRate
}

func premiumIndexStaleness(now time.Time, item PremiumIndexItem) time.Duration {
	return marketdata.Staleness(now, item.Time)
}

func (e *FundingRateSniperEngine) estimateTargetCapitalRequirement(target *MonitoredTarget, spotReferencePrice float64, futuresReferencePrice float64, entryTime time.Time) (float64, float64) {
	if target == nil || target.Candidate.OrderQuantity <= 0 {
		return 0, 0
	}
	if spotReferencePrice <= 0 {
		spotReferencePrice = target.Candidate.MarkPrice
	}
	if futuresReferencePrice <= 0 {
		futuresReferencePrice = target.Candidate.MarkPrice
	}
	if spotReferencePrice <= 0 || futuresReferencePrice <= 0 {
		return 0, 0
	}

	spotFeeRate := e.cfg.HistoricalConfig.SpotTakerFeeRate
	if spotFeeRate <= 0 {
		spotFeeRate = 0.0010
	}
	futuresFeeRate := e.cfg.HistoricalConfig.FuturesTakerFeeRate
	if futuresFeeRate <= 0 {
		futuresFeeRate = 0.0005
	}
	spotRequiredUSDT := target.Candidate.OrderQuantity*spotReferencePrice*(1+spotFeeRate) +
		e.liveBorrowReserveCost(target.Candidate, spotReferencePrice, entryTime)
	futuresRequiredUSDT := target.Candidate.OrderQuantity * futuresReferencePrice * (1 + futuresFeeRate)
	return spotRequiredUSDT, futuresRequiredUSDT
}

func (e *FundingRateSniperEngine) getJSON(ctx context.Context, endpoint string, target any) error {
	gateway := e.ensureBinanceGateway()
	if gateway == nil {
		return fmt.Errorf("binance gateway 未初始化")
	}
	return gateway.GetJSON(ctx, endpoint, target)
}

func (e *FundingRateSniperEngine) constrainOrderQuantityByMarketCapacity(ctx context.Context, target *MonitoredTarget, spotReferencePrice float64, futuresReferencePrice float64) (float64, string, error) {
	if target == nil || target.Candidate.OrderQuantity <= 0 {
		return 0, "target_or_quantity_invalid", nil
	}

	referencePrice := math.Max(spotReferencePrice, futuresReferencePrice)
	if referencePrice <= 0 {
		referencePrice = target.Candidate.MarkPrice
	}

	snapshot, err := e.collectMarketCapacitySnapshot(ctx, target, futuresReferencePrice)
	if err != nil && snapshot.AvailableMetrics == 0 {
		return 0, "", err
	}

	profile := GetSymbolRiskProfile(target.Candidate.Symbol)
	strategy := NewDefaultFundingStrategy()

	limitedQty, reasons, limitErr := strategy.EvaluateExecutionOpportunity(
		target.Candidate,
		snapshot,
		referencePrice,
		profile,
		e.cfg,
	)

	if limitErr != nil {
		return 0, reasons, limitErr
	}

	if limitedQty <= 0 {
		return 0, reasons, nil
	}

	futuresRule := e.futuresRules[target.Candidate.Symbol]
	finalQty := normalizeQuantity(limitedQty, futuresRule, target.Candidate.QuantityPrecision)
	return finalQty, reasons, nil
}

func (e *FundingRateSniperEngine) collectMarketCapacitySnapshot(ctx context.Context, target *MonitoredTarget, futuresReferencePrice float64) (MarketCapacitySnapshot, error) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		errs     []string
		snapshot MarketCapacitySnapshot
	)

	wg.Add(4)

	go func() {
		defer wg.Done()
		openInterestNotional, err := e.fetchFuturesOpenInterestNotional(ctx, target.Candidate.Symbol, futuresReferencePrice)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("open_interest=%v", err))
			mu.Unlock()
			return
		}
		if openInterestNotional <= 0 {
			return
		}
		mu.Lock()
		snapshot.FuturesOpenInterestNotional = openInterestNotional
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		quoteVolume, err := e.fetchRecentSpotQuoteVolume(ctx, target.Candidate.Pair)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("spot_recent_volume=%v", err))
			mu.Unlock()
			return
		}
		if quoteVolume <= 0 {
			return
		}
		mu.Lock()
		snapshot.RecentSpotQuoteVolume = quoteVolume
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		quoteVolume, err := e.fetchRecentFuturesQuoteVolume(ctx, target.Candidate.Symbol)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("futures_recent_volume=%v", err))
			mu.Unlock()
			return
		}
		if quoteVolume <= 0 {
			return
		}
		mu.Lock()
		snapshot.RecentFuturesQuoteVolume = quoteVolume
		snapshot.AvailableMetrics++
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		spotDepth, futuresDepth, err := e.fetchDualOrderBookDepth(ctx, target.Candidate.Pair, target.Candidate.Symbol)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("visible_depth=%v", err))
			mu.Unlock()
			return
		}
		direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
		spotVisible, futuresVisible := visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
		mu.Lock()
		if spotVisible > 0 {
			snapshot.VisibleSpotAskNotional = spotVisible
			snapshot.AvailableMetrics++
		}
		if futuresVisible > 0 {
			snapshot.VisibleFuturesBidNotional = futuresVisible
			snapshot.AvailableMetrics++
		}
		mu.Unlock()
	}()

	wg.Wait()
	if snapshot.AvailableMetrics == 0 && len(errs) > 0 {
		return snapshot, fmt.Errorf("市场容量数据抓取失败: %s", strings.Join(errs, " | "))
	}
	return snapshot, nil
}

func (e *FundingRateSniperEngine) fetchFuturesOpenInterestNotional(ctx context.Context, symbol string, referencePrice float64) (float64, error) {
	if referencePrice <= 0 {
		return 0, fmt.Errorf("参考价格无效")
	}

	var payload struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := e.getJSON(ctx, e.client.FuturesBaseURL()+"/fapi/v1/openInterest?symbol="+symbol, &payload); err != nil {
		return 0, err
	}
	openInterestQty := parseFloat(payload.OpenInterest)
	if openInterestQty <= 0 {
		return 0, fmt.Errorf("openInterest 为空")
	}
	return openInterestQty * referencePrice, nil
}

func (e *FundingRateSniperEngine) fetchRecentSpotQuoteVolume(ctx context.Context, symbol string) (float64, error) {
	return e.fetchRecentQuoteVolume(ctx, e.client.SpotBaseURL()+"/api/v3/klines", symbol)
}

func (e *FundingRateSniperEngine) fetchRecentFuturesQuoteVolume(ctx context.Context, symbol string) (float64, error) {
	return e.fetchRecentQuoteVolume(ctx, e.client.FuturesBaseURL()+"/fapi/v1/klines", symbol)
}

func (e *FundingRateSniperEngine) fetchRecentQuoteVolume(ctx context.Context, endpoint string, symbol string) (float64, error) {
	interval := klineIntervalForDuration(e.cfg.RecentTradeVolumeWindow)
	var payload [][]any
	url := fmt.Sprintf("%s?symbol=%s&interval=%s&limit=2", endpoint, symbol, interval)
	if err := e.getJSON(ctx, url, &payload); err != nil {
		return 0, err
	}
	if len(payload) == 0 {
		return 0, fmt.Errorf("未返回 K 线")
	}

	row := payload[len(payload)-1]
	if len(payload) > 1 {
		row = payload[len(payload)-2]
	}
	if len(row) < 8 {
		return 0, fmt.Errorf("K 线字段不足")
	}
	quoteVolume := parseAnyFloat(row[7])
	if quoteVolume <= 0 {
		return 0, fmt.Errorf("quoteVolume 无效")
	}
	return quoteVolume, nil
}

func klineIntervalForDuration(window time.Duration) string {
	switch {
	case window >= 15*time.Minute:
		return "15m"
	case window >= 5*time.Minute:
		return "5m"
	case window >= 3*time.Minute:
		return "3m"
	default:
		return "1m"
	}
}

func estimateVisibleDepthNotional(levels []DepthLevel) float64 {
	return marketdata.EstimateVisibleDepthNotional(levels)
}

func minPositiveFloat(values ...float64) float64 {
	return marketdata.MinPositiveFloat(values...)
}
