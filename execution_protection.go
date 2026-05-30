package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const protectedExecutionIOC = "IOC"

const protectedTickerMaxStaleness = 500 * time.Millisecond
const protectedTickerSilentSymbolMaxStaleness = 15 * time.Second

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
	order.Price = formatFloat(limitPrice)
	if cfg.ExecutionConfig.FuturesPriceProtect {
		order.PriceProtect = "TRUE"
	}
	return true
}

func protectedLimitPrice(side string, referencePrice float64, slippageRate float64) float64 {
	referencePrice = maxFloat(referencePrice, 0)
	if referencePrice <= 0 {
		return 0
	}
	side = strings.ToUpper(strings.TrimSpace(side))
	switch side {
	case "BUY":
		return referencePrice * (1 + maxFloat(slippageRate, 0))
	case "SELL":
		return referencePrice * maxFloat(1-maxFloat(slippageRate, 0), 0.0001)
	default:
		return referencePrice
	}
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
	var ticker BookTicker
	if err := e.getJSON(ctx, e.client.spotBaseURL+"/api/v3/ticker/bookTicker?symbol="+symbol, &ticker); err != nil {
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
	var ticker BookTicker
	if err := e.getJSON(ctx, e.client.futuresBaseURL+"/fapi/v1/ticker/bookTicker?symbol="+symbol, &ticker); err != nil {
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
	limitPrice := protectedLimitPrice(side, referencePrice, slippageRate)
	if limitPrice <= 0 || tickSize <= 0 {
		return limitPrice
	}
	side = strings.ToUpper(strings.TrimSpace(side))
	switch side {
	case "BUY":
		return ceilToStep(limitPrice, tickSize)
	case "SELL":
		return floorToStep(limitPrice, tickSize)
	default:
		return limitPrice
	}
}

func (e *FundingRateSniperEngine) spotProtectedOrderParams(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string) (url.Values, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("quantity", formatFloat(quantity))
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
	params.Set("price", formatFloat(limitPrice))
	return params, nil
}

func (e *FundingRateSniperEngine) futuresProtectedOrderParams(ctx context.Context, symbol string, side string, quantity float64, reduceOnly bool, clientOrderID string) (url.Values, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("quantity", formatFloat(quantity))
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
	params.Set("price", formatFloat(limitPrice))
	if e.cfg.ExecutionConfig.FuturesPriceProtect {
		params.Set("priceProtect", "TRUE")
	}
	return params, nil
}

func (e *FundingRateSniperEngine) buildProtectedOpenFuturesOrder(target *MonitoredTarget) FuturesBatchOrderRequest {
	order := FuturesBatchOrderRequest{
		Symbol:           target.Candidate.Symbol,
		Side:             "SELL",
		Type:             "MARKET",
		Quantity:         formatFloat(target.Candidate.OrderQuantity),
		SelfTradeMode:    normalizedSTPMode(e.cfg.ExecutionConfig.FuturesSTPMode),
		NewClientOrderID: newClientOrderID("fopen", target.Candidate.Symbol),
		NewOrderRespType: "RESULT",
	}
	referencePrice := maxFloat(target.LastFuturesVWAP, target.LastFuturesBid)
	if referencePrice <= 0 {
		referencePrice = target.Candidate.MarkPrice
	}
	applyProtectedFuturesLimitOrder(e.cfg, &order, referencePrice, e.futuresRules[target.Candidate.Symbol].PriceTick)
	return order
}

func (e *FundingRateSniperEngine) buildProtectedCloseFuturesOrder(ctx context.Context, hedge OpenHedge) FuturesBatchOrderRequest {
	order := FuturesBatchOrderRequest{
		Symbol:           hedge.Target.Symbol,
		Side:             "BUY",
		Type:             "MARKET",
		Quantity:         hedge.HedgeQtyText,
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
	if referencePrice <= 0 {
		return order
	}
	applyProtectedFuturesLimitOrder(e.cfg, &order, referencePrice, e.futuresRules[hedge.Target.Symbol].PriceTick)
	return order
}

func (e *FundingRateSniperEngine) spotOrderRequestPath() string {
	return e.client.spotBaseURL + "/api/v3/order"
}

func (e *FundingRateSniperEngine) futuresOrderRequestPath() string {
	return e.client.futuresBaseURL + "/fapi/v1/order"
}

func (e *FundingRateSniperEngine) placeSignedOrder(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	return e.signedRequest(ctx, http.MethodPost, endpoint, params)
}
