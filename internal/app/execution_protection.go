package app

import (
	"context"
	"encoding/json"
	"fmt"
	"lingxi/internal/execution"
	"lingxi/internal/venue/binance"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const protectedExecutionIOC = "IOC"

const protectedTickerMaxStaleness = 500 * time.Millisecond
const protectedTickerSilentSymbolMaxStaleness = 15 * time.Second
const widenedProtectedIOCMultiplier = 3.0
const blindStateHardDeadline = 2 * time.Second

func normalizedSTPMode(mode string) string {
	return strings.ToUpper(strings.TrimSpace(mode))
}

func applyProtectedFuturesLimitOrder(cfg EngineConfig, order *FuturesBatchOrderRequest, referencePrice float64, tickSize float64) bool {
	if order == nil || !cfg.ExecutionConfig.UseProtectedIOC || referencePrice <= 0 {
		return false
	}
	limitPrice := normalizeProtectedLimitPrice(order.Side, referencePrice, cfg.ExecutionConfig.FuturesLimitSlippageRate, tickSize)
	if limitPrice <= 0 {
		return false
	}
	order.Type = "LIMIT"
	order.TimeInForce = protectedExecutionIOC
	order.Price = formatPriceForTick(limitPrice, tickSize)
	if cfg.ExecutionConfig.FuturesPriceProtect {
		order.PriceProtect = "TRUE"
	}
	return true
}

func protectedLimitPrice(side string, referencePrice float64, slippageRate float64) float64 {
	return execution.ProtectedLimitPrice(side, referencePrice, slippageRate)
}

func (e *FundingRateSniperEngine) fetchSpotBookTicker(ctx context.Context, symbol string) (BookTicker, error) {
	if e.spotTickerCache != nil {
		if ticker, receivedAt, ok := e.spotTickerCache.GetWithReceivedAt(symbol); ok && (parseFloat(ticker.AskPrice) > 0 || parseFloat(ticker.BidPrice) > 0) {
			if !e.allowSilentTickerReuse(symbol, receivedAt, e.spotTickerCache, spotTickerHeartbeatSymbol) {
				return BookTicker{}, fmt.Errorf("现货 bookTicker 已失效: symbol=%s age=%s", symbol, time.Since(receivedAt).Round(time.Millisecond))
			}
			return ticker, nil
		}
	}
	if e.bookTickerCacheOnlyModeEnabled() {
		return BookTicker{}, fmt.Errorf("现货 bookTicker 仅允许使用缓存: symbol=%s", symbol)
	}
	var ticker BookTicker
	if err := e.getJSON(ctx, e.client.SpotBaseURL()+binance.PathSpotBookTicker+"?symbol="+symbol, &ticker); err != nil {
		return BookTicker{}, err
	}
	return ticker, nil
}

func (e *FundingRateSniperEngine) fetchFuturesBookTicker(ctx context.Context, symbol string) (BookTicker, error) {
	if e.futuresTickerCache != nil {
		if ticker, receivedAt, ok := e.futuresTickerCache.GetWithReceivedAt(symbol); ok && (parseFloat(ticker.AskPrice) > 0 || parseFloat(ticker.BidPrice) > 0) {
			if !e.allowSilentTickerReuse(symbol, receivedAt, e.futuresTickerCache, futuresTickerHeartbeatSymbol) {
				return BookTicker{}, fmt.Errorf("合约 bookTicker 已失效: symbol=%s age=%s", symbol, time.Since(receivedAt).Round(time.Millisecond))
			}
			return ticker, nil
		}
	}
	if e.bookTickerCacheOnlyModeEnabled() {
		return BookTicker{}, fmt.Errorf("合约 bookTicker 仅允许使用缓存: symbol=%s", symbol)
	}
	var ticker BookTicker
	if err := e.getJSON(ctx, e.client.FuturesBaseURL()+binance.PathFuturesBookTicker+"?symbol="+symbol, &ticker); err != nil {
		return BookTicker{}, err
	}
	return ticker, nil
}

func (e *FundingRateSniperEngine) allowSilentTickerReuse(symbol string, receivedAt time.Time, cache *BookTickerCache, heartbeatSymbol string) bool {
	if receivedAt.IsZero() {
		return false
	}
	age := time.Since(receivedAt)
	if age <= protectedTickerMaxStaleness {
		return true
	}
	if age > protectedTickerSilentSymbolMaxStaleness {
		return false
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	heartbeatSymbol = strings.ToUpper(strings.TrimSpace(heartbeatSymbol))
	if symbol == "" || heartbeatSymbol == "" || symbol == heartbeatSymbol || cache == nil {
		return false
	}
	heartbeatAt := cache.LastReceivedAt(heartbeatSymbol)
	return !heartbeatAt.IsZero() && time.Since(heartbeatAt) <= protectedTickerMaxStaleness
}

func normalizeProtectedLimitPrice(side string, referencePrice float64, slippageRate float64, tickSize float64) float64 {
	return execution.NormalizeProtectedLimitPrice(side, referencePrice, slippageRate, tickSize)
}

func widenedProtectedLimitPrice(side string, referencePrice float64, slippageRate float64, tickSize float64) float64 {
	return execution.WidenedProtectedLimitPrice(side, referencePrice, slippageRate, tickSize, widenedProtectedIOCMultiplier)
}

func (e *FundingRateSniperEngine) percentPriceRejectLikely(symbol string, side string, orderPrice float64, referencePrice float64, rules SymbolTradeRules) (bool, string) {
	if orderPrice <= 0 || referencePrice <= 0 {
		return false, ""
	}
	side = strings.ToUpper(strings.TrimSpace(side))
	upper := 0.0
	lower := 0.0
	switch side {
	case "BUY":
		upper = rules.BidPercentPriceUp
		lower = rules.BidPercentPriceDown
	case "SELL":
		upper = rules.AskPercentPriceUp
		lower = rules.AskPercentPriceDown
	}
	if upper <= 0 {
		upper = rules.PercentPriceUp
	}
	if lower <= 0 {
		lower = rules.PercentPriceDown
	}
	if upper > 0 && orderPrice > referencePrice*upper {
		return true, fmt.Sprintf("price %.8f exceeds upper percent-price bound %.8f", orderPrice, referencePrice*upper)
	}
	if lower > 0 && orderPrice < referencePrice*lower {
		return true, fmt.Sprintf("price %.8f below lower percent-price bound %.8f", orderPrice, referencePrice*lower)
	}
	return false, ""
}

func isPercentPriceFilterError(err error) bool {
	if err == nil {
		return false
	}
	return isPercentPriceFilterMessage(err.Error())
}

func isOpenProtectedFuturesOrder(order FuturesBatchOrderRequest) bool {
	return isProtectedIOCRequestType(order.Type, order.TimeInForce) && !strings.EqualFold(strings.TrimSpace(order.ReduceOnly), "true")
}

func futuresProtectedOpenFallbackNeeded(order FuturesBatchOrderRequest, result FuturesBatchOrderResult) bool {
	if !isOpenProtectedFuturesOrder(order) {
		return false
	}
	return result.RequiresProtectedOpenFallback(parseFloat(order.Quantity))
}

func (e *FundingRateSniperEngine) spotProtectedOrderParams(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string) (url.Values, error) {
	params := url.Values{}
	rules := e.spotRules[symbol]
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("quantity", formatQuantityForRules(quantity, rules, 0))
	if mode := normalizedSTPMode(e.cfg.ExecutionConfig.SpotSTPMode); mode != "" {
		params.Set("selfTradePreventionMode", mode)
	}
	if clientOrderID != "" {
		params.Set("newClientOrderId", clientOrderID)
	}
	params.Set("newOrderRespType", "FULL")
	if !e.cfg.ExecutionConfig.UseProtectedIOC {
		params.Set("type", "MARKET")
		return params, nil
	}
	ticker, err := e.fetchSpotBookTicker(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("获取现货 bookTicker 失败: %w", err)
	}
	referencePrice := parseFloat(ticker.AskPrice)
	if strings.EqualFold(side, "SELL") {
		referencePrice = parseFloat(ticker.BidPrice)
	}
	limitPrice := normalizeProtectedLimitPrice(side, referencePrice, e.cfg.ExecutionConfig.SpotLimitSlippageRate, e.spotRules[symbol].PriceTick)
	if limitPrice <= 0 {
		return nil, fmt.Errorf("现货保护价无效: symbol=%s side=%s reference=%.8f", symbol, side, referencePrice)
	}
	params.Set("type", "LIMIT")
	params.Set("timeInForce", protectedExecutionIOC)
	params.Set("price", formatPriceForTick(limitPrice, rules.PriceTick))
	return params, nil
}

func (e *FundingRateSniperEngine) futuresProtectedOrderParams(ctx context.Context, symbol string, side string, quantity float64, reduceOnly bool, clientOrderID string) (url.Values, error) {
	params := url.Values{}
	rules := e.futuresRules[symbol]
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("quantity", formatQuantityForRules(quantity, rules, 0))
	if mode := normalizedSTPMode(e.cfg.ExecutionConfig.FuturesSTPMode); mode != "" {
		params.Set("selfTradePreventionMode", mode)
	}
	if reduceOnly {
		params.Set("reduceOnly", "true")
	}
	if clientOrderID != "" {
		params.Set("newClientOrderId", clientOrderID)
	}
	params.Set("newOrderRespType", "RESULT")
	if !e.cfg.ExecutionConfig.UseProtectedIOC {
		params.Set("type", "MARKET")
		return params, nil
	}
	ticker, err := e.fetchFuturesBookTicker(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("获取合约 bookTicker 失败: %w", err)
	}
	referencePrice := parseFloat(ticker.AskPrice)
	if strings.EqualFold(side, "SELL") {
		referencePrice = parseFloat(ticker.BidPrice)
	}
	limitPrice := normalizeProtectedLimitPrice(side, referencePrice, e.cfg.ExecutionConfig.FuturesLimitSlippageRate, e.futuresRules[symbol].PriceTick)
	if limitPrice <= 0 {
		return nil, fmt.Errorf("合约保护价无效: symbol=%s side=%s reference=%.8f", symbol, side, referencePrice)
	}
	params.Set("type", "LIMIT")
	params.Set("timeInForce", protectedExecutionIOC)
	params.Set("price", formatPriceForTick(limitPrice, rules.PriceTick))
	if e.cfg.ExecutionConfig.FuturesPriceProtect {
		params.Set("priceProtect", "TRUE")
	}
	return params, nil
}

func (e *FundingRateSniperEngine) placeSpotProtectedOpenDualTrack(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, protectedParams url.Values) (SpotOrderResponse, error) {
	action := "spot_market_" + strings.ToLower(side)
	if err := e.noteOrderPlaced(ctx, action, fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, strings.EqualFold(side, "BUY")); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), protectedParams)
	if err == nil {
		var result SpotOrderResponse
		if unmarshalErr := json.Unmarshal(body, &result); unmarshalErr != nil {
			return SpotOrderResponse{}, fmt.Errorf("解析现货保护单回执失败: %w", unmarshalErr)
		}
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
		if !result.RequiresProtectedOpenFallback(quantity) {
			return result, nil
		}
		// err==nil 且 IOC 几乎未成交（canceled/expired 且成交≈0）→ 已知主单没吃到，下方 widened/market 兜底安全。
	} else if isUnknownOrderRequestError(err) {
		// 不确定态（超时/5xx，不知主单是否已在交易所成交）：绝不能用【新 clientOrderID】走下面的 widened/market
		// 兜底——若主单其实已成交，新单号不会被去重，会叠加成交 → 现货超配、对冲裸腿。这里先按【原 clientOrderID】
		// 对账：确认成交即直接采用；无法确认成交则把不确定错误上抛，交由上层包装
		// （placeSpotMarketBuy/SellByQuantityIdempotent 用【同一 clientOrderID】重试，受币安重复单号拒绝 + 对账保护）
		// 安全收敛，而不是在此盲目补单。
		if confirmed, ok := e.confirmSpotPrimaryFilledOnUnknown(ctx, symbol, clientOrderID, quantity); ok {
			return confirmed, nil
		}
		return SpotOrderResponse{}, err
	} else if !isPercentPriceFilterError(err) {
		return SpotOrderResponse{}, err
	}

	ticker, tickerErr := e.fetchSpotBookTicker(ctx, symbol)
	if tickerErr != nil {
		if err != nil {
			return SpotOrderResponse{}, err
		}
		return SpotOrderResponse{}, tickerErr
	}
	referencePrice := parseFloat(ticker.AskPrice)
	if strings.EqualFold(side, "SELL") {
		referencePrice = parseFloat(ticker.BidPrice)
	}
	rules := e.spotRules[symbol]
	widenedPrice := widenedProtectedLimitPrice(side, referencePrice, e.cfg.ExecutionConfig.SpotLimitSlippageRate, rules.PriceTick)
	if rejectLikely, _ := e.percentPriceRejectLikely(symbol, side, widenedPrice, referencePrice, rules); !rejectLikely && widenedPrice > 0 {
		widenedClientOrderID := newClientOrderID("sopenw", symbol)
		widenedParams, buildErr := e.buildSpotLimitOrderParams(symbol, side, quantity, widenedClientOrderID, "LIMIT", protectedExecutionIOC, widenedPrice)
		if buildErr == nil {
			widenedOrder, widenedErr := e.placeSpotOrderIdempotentWithParams(ctx, symbol, side, quantity, widenedClientOrderID, "spot_"+strings.ToLower(side)+"_open_widened_ioc", widenedParams)
			if widenedErr == nil && !widenedOrder.RequiresProtectedOpenFallback(quantity) {
				return widenedOrder, nil
			}
		}
	}

	marketParams := url.Values{}
	marketParams.Set("symbol", symbol)
	marketParams.Set("side", side)
	marketParams.Set("type", "MARKET")
	marketParams.Set("quantity", formatQuantityForRules(quantity, rules, 0))
	marketParams.Set("newOrderRespType", "FULL")
	if mode := normalizedSTPMode(e.cfg.ExecutionConfig.SpotSTPMode); mode != "" {
		marketParams.Set("selfTradePreventionMode", mode)
	}
	if clientOrderID != "" {
		marketParams.Set("newClientOrderId", newClientOrderID("sopenm", symbol))
	}
	marketOrder, marketErr := e.placeSpotOrderIdempotentWithParams(ctx, symbol, side, quantity, marketParams.Get("newClientOrderId"), "spot_"+strings.ToLower(side)+"_open_market_fallback", marketParams)
	if marketErr != nil {
		return SpotOrderResponse{}, marketErr
	}
	return marketOrder, nil
}

// confirmSpotPrimaryFilledOnUnknown 在「保护单主腿」返回不确定态（超时/5xx，不知是否已成交）时，
// 按【原 clientOrderID】对账，仅当能确认「已成交」才返回该回执（ok=true）。用于双轨开仓在不确定态下
// 杜绝用新单号重复下单：确认成交即采用、避免现货超配裸腿；无法确认成交一律返回 false，由调用方安全失败。
func (e *FundingRateSniperEngine) confirmSpotPrimaryFilledOnUnknown(ctx context.Context, symbol string, clientOrderID string, quantity float64) (SpotOrderResponse, bool) {
	if strings.TrimSpace(clientOrderID) == "" {
		return SpotOrderResponse{}, false
	}
	e.noteOrderUncertainty(symbol, "spot_open_primary_unknown", fmt.Sprintf("clientOrderId=%s 对账确认成交以免重复下单", clientOrderID))
	waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
	defer cancel()
	if snapshot, ok := e.waitForSpotOrder(waitCtx, symbol, clientOrderID); ok && hasConfirmedOrderExecution(snapshot) {
		result := e.spotSnapshotToOrderResponse(symbol, snapshot)
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
		return result, true
	}
	if queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID); queryErr == nil && queried.HasConfirmedExecution() {
		e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
		return queried, true
	}
	if reconciled, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID); reconcileErr == nil && reconciled.HasConfirmedExecution() {
		e.noteProtectedIOCObservation(symbol, "spot", reconciled.ProtectedIOCObservation(quantity))
		return reconciled, true
	}
	return SpotOrderResponse{}, false
}

func (e *FundingRateSniperEngine) placeFuturesProtectedOpenDualTrack(ctx context.Context, order FuturesBatchOrderRequest, action string, submit func(context.Context, FuturesBatchOrderRequest, string) (OrderExecutionSnapshot, error)) (OrderExecutionSnapshot, error) {
	snapshot, err := submit(ctx, order, action)
	if err == nil && !snapshot.RequiresProtectedOpenFallback(parseFloat(order.Quantity)) {
		return snapshot, nil
	}
	if err != nil && !isPercentPriceFilterError(err) && !isUnknownOrderStateError(err) && !isUnknownOrderRequestError(err) {
		return OrderExecutionSnapshot{}, err
	}

	ticker, tickerErr := e.fetchFuturesBookTicker(ctx, order.Symbol)
	if tickerErr != nil {
		if err != nil {
			return OrderExecutionSnapshot{}, err
		}
		return OrderExecutionSnapshot{}, tickerErr
	}
	referencePrice := parseFloat(ticker.AskPrice)
	if strings.EqualFold(order.Side, "SELL") {
		referencePrice = parseFloat(ticker.BidPrice)
	}
	rules := e.futuresRules[order.Symbol]
	widenedPrice := widenedProtectedLimitPrice(order.Side, referencePrice, e.cfg.ExecutionConfig.FuturesLimitSlippageRate, rules.PriceTick)
	if rejectLikely, _ := e.percentPriceRejectLikely(order.Symbol, order.Side, widenedPrice, referencePrice, rules); !rejectLikely && widenedPrice > 0 {
		widenedOrder := order
		widenedOrder.Price = formatPriceForTick(widenedPrice, rules.PriceTick)
		widenedOrder.Type = "LIMIT"
		widenedOrder.TimeInForce = protectedExecutionIOC
		widenedOrder.NewClientOrderID = newClientOrderID("fopenw", order.Symbol)
		widenedSnapshot, widenedErr := submit(ctx, widenedOrder, action+"_widened_ioc")
		if widenedErr == nil && !widenedSnapshot.RequiresProtectedOpenFallback(parseFloat(widenedOrder.Quantity)) {
			return widenedSnapshot, nil
		}
		if widenedErr != nil && !isUnknownOrderStateError(widenedErr) && !isUnknownOrderRequestError(widenedErr) && !isPercentPriceFilterError(widenedErr) {
			return OrderExecutionSnapshot{}, widenedErr
		}
	}

	marketOrder := order
	marketOrder.Type = "MARKET"
	marketOrder.Price = ""
	marketOrder.TimeInForce = ""
	marketOrder.PriceProtect = ""
	marketOrder.NewClientOrderID = newClientOrderID("fopenm", order.Symbol)
	return submit(ctx, marketOrder, action+"_market_fallback")
}

func (e *FundingRateSniperEngine) buildProtectedOpenFuturesOrder(target *MonitoredTarget) FuturesBatchOrderRequest {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	side := direction.openFuturesSide()
	order := FuturesBatchOrderRequest{
		Symbol:           target.Candidate.Symbol,
		Side:             side,
		Type:             "MARKET",
		Quantity:         formatQuantityForRules(target.Candidate.OrderQuantity, e.futuresRules[target.Candidate.Symbol], target.Candidate.QuantityPrecision),
		SelfTradeMode:    normalizedSTPMode(e.cfg.ExecutionConfig.FuturesSTPMode),
		NewClientOrderID: newClientOrderID("fopen", target.Candidate.Symbol),
		NewOrderRespType: "RESULT",
	}
	referencePrice := maxFloat(target.LastFuturesVWAP, target.Candidate.MarkPrice)
	if side == "SELL" {
		referencePrice = maxFloat(target.LastFuturesVWAP, target.LastFuturesBid)
		if referencePrice <= 0 {
			referencePrice = target.Candidate.MarkPrice
		}
	}
	if referencePrice <= 0 {
		referencePrice = target.Candidate.MarkPrice
	}
	applyProtectedFuturesLimitOrder(e.cfg, &order, referencePrice, e.futuresRules[target.Candidate.Symbol].PriceTick)
	return order
}

func (e *FundingRateSniperEngine) buildProtectedCloseFuturesOrder(ctx context.Context, hedge OpenHedge) FuturesBatchOrderRequest {
	direction := hedge.carryDirection()
	side := direction.closeFuturesSide()
	order := FuturesBatchOrderRequest{
		Symbol:           hedge.Target.Symbol,
		Side:             side,
		Type:             "MARKET",
		Quantity:         formatQuantityForRules(hedge.HedgeQty, e.futuresRules[hedge.Target.Symbol], hedge.Target.QuantityPrecision),
		ReduceOnly:       "true",
		SelfTradeMode:    normalizedSTPMode(e.cfg.ExecutionConfig.FuturesSTPMode),
		NewClientOrderID: newClientOrderID("fclose", hedge.Target.Symbol),
		NewOrderRespType: "RESULT",
	}
	if !e.cfg.ExecutionConfig.UseProtectedIOC {
		return order
	}
	ticker, err := e.fetchFuturesBookTicker(ctx, hedge.Target.Symbol)
	if err != nil {
		e.logger.Printf("平仓保护价获取失败，回退为合约市价单，symbol=%s err=%v", hedge.Target.Symbol, err)
		return order
	}
	referencePrice := parseFloat(ticker.AskPrice)
	if side == "SELL" {
		referencePrice = parseFloat(ticker.BidPrice)
	}
	if referencePrice <= 0 {
		return order
	}
	applyProtectedFuturesLimitOrder(e.cfg, &order, referencePrice, e.futuresRules[hedge.Target.Symbol].PriceTick)
	return order
}

func (e *FundingRateSniperEngine) spotOrderRequestPath() string {
	return e.client.SpotBaseURL() + binance.PathSpotOrder
}

func (e *FundingRateSniperEngine) futuresOrderRequestPath() string {
	return e.client.FuturesBaseURL() + binance.PathFuturesOrder
}

func (e *FundingRateSniperEngine) placeSignedOrder(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	return e.signedRequest(ctx, http.MethodPost, endpoint, params)
}
