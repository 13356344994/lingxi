package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lingxi/internal/venue/binance"
)

func spotExecutionMarketLabel(venue spotExecutionVenue) string {
	if venue == spotExecutionVenueCrossMargin {
		return "margin"
	}
	return "spot"
}

func (e *FundingRateSniperEngine) directionalSpotOrderHub(venue spotExecutionVenue) *OrderEventHub {
	if e == nil {
		return nil
	}
	if venue == spotExecutionVenueCrossMargin {
		if e.marginOrderHub == nil {
			e.marginOrderHub = NewOrderEventHub()
		}
		return e.marginOrderHub
	}
	return e.spotOrderHub
}

func directionalSpotOrderToSnapshot(symbol string, venue spotExecutionVenue, result SpotOrderResponse) OrderExecutionSnapshot {
	snapshot := OrderExecutionSnapshot{
		Market:        spotExecutionMarketLabel(venue),
		Symbol:        symbol,
		ClientOrderID: result.ClientOrderID,
		OrderID:       result.OrderID,
		Status:        result.Status,
		ExecutedQty:   parseFloat(result.ExecutedQty),
		CumQuoteQty:   parseFloat(result.CummulativeQuoteQty),
		EventTime:     time.UnixMilli(result.TransactTime),
	}
	if snapshot.ExecutedQty > 0 && snapshot.CumQuoteQty > 0 {
		snapshot.AvgPrice = snapshot.CumQuoteQty / snapshot.ExecutedQty
	}
	if len(result.Fills) > 0 {
		for _, fill := range result.Fills {
			snapshot.Commission += parseFloat(fill.Commission)
			if snapshot.CommissionAsset == "" && strings.TrimSpace(fill.CommissionAsset) != "" {
				snapshot.CommissionAsset = fill.CommissionAsset
			}
		}
	}
	return snapshot
}

func (e *FundingRateSniperEngine) noteDirectionalSpotOrderObservation(symbol string, venue spotExecutionVenue, result SpotOrderResponse) {
	hub := e.directionalSpotOrderHub(venue)
	if hub == nil || strings.TrimSpace(result.ClientOrderID) == "" {
		return
	}
	hub.Update(directionalSpotOrderToSnapshot(symbol, venue, result))
}

func (e *FundingRateSniperEngine) waitForDirectionalSpotOrder(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) (OrderExecutionSnapshot, bool) {
	hub := e.directionalSpotOrderHub(venue)
	if hub == nil || strings.TrimSpace(clientOrderID) == "" || strings.TrimSpace(symbol) == "" {
		return OrderExecutionSnapshot{}, false
	}
	fallbackDelay := 2 * time.Second
	fallbackInterval := 500 * time.Millisecond
	if venue == spotExecutionVenueCrossMargin {
		fallbackDelay = 250 * time.Millisecond
		fallbackInterval = 250 * time.Millisecond
	}
	return hub.WaitForWithFallback(
		ctx,
		clientOrderID,
		func(snapshot OrderExecutionSnapshot) bool {
			return hasConfirmedOrderExecution(snapshot)
		},
		fallbackDelay,
		fallbackInterval,
		func(queryCtx context.Context) (OrderExecutionSnapshot, bool) {
			queried, err := e.queryDirectionalSpotOrderByClientID(queryCtx, symbol, clientOrderID, venue)
			if err != nil || queried.OrderID == 0 {
				return OrderExecutionSnapshot{}, false
			}
			snapshot := directionalSpotOrderToSnapshot(symbol, venue, queried)
			if strings.TrimSpace(snapshot.ClientOrderID) == "" {
				snapshot.ClientOrderID = clientOrderID
			}
			return snapshot, true
		},
	)
}

func (e *FundingRateSniperEngine) observeDirectionalSpotOrderUntilTerminal(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) {
	if e == nil || strings.TrimSpace(clientOrderID) == "" {
		return
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		queried, err := e.queryDirectionalSpotOrderByClientID(ctx, symbol, clientOrderID, venue)
		if err == nil && queried.OrderID != 0 {
			e.noteDirectionalSpotOrderObservation(symbol, venue, queried)
			if queried.IsClosed() {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func applySpotVenueAccountParams(params url.Values, venue spotExecutionVenue) {
	if venue != spotExecutionVenueCrossMargin {
		return
	}
	params.Set("isIsolated", "FALSE")
}

func applySpotVenueOrderParams(params url.Values, venue spotExecutionVenue) {
	applySpotVenueAccountParams(params, venue)
	if venue != spotExecutionVenueCrossMargin {
		return
	}
	params.Set("sideEffectType", "AUTO_BORROW_REPAY")
}

func (e *FundingRateSniperEngine) spotOrderRequestPathForVenue(venue spotExecutionVenue) string {
	if venue == spotExecutionVenueCrossMargin {
		return e.client.SpotBaseURL() + binance.PathMarginOrder
	}
	return e.client.SpotBaseURL() + binance.PathSpotOrder
}

func (e *FundingRateSniperEngine) buildDirectionalSpotProtectedOrderParams(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, venue spotExecutionVenue) (url.Values, error) {
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
	applySpotVenueOrderParams(params, venue)
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
	limitPrice := normalizeProtectedLimitPrice(side, referencePrice, e.cfg.ExecutionConfig.SpotLimitSlippageRate, rules.PriceTick)
	if limitPrice <= 0 {
		return nil, fmt.Errorf("现货保护价无效: symbol=%s side=%s reference=%.8f", symbol, side, referencePrice)
	}
	params.Set("type", "LIMIT")
	params.Set("timeInForce", protectedExecutionIOC)
	params.Set("price", formatPriceForTick(limitPrice, rules.PriceTick))
	return params, nil
}

func (e *FundingRateSniperEngine) buildDirectionalSpotLimitOrderParams(symbol string, side string, quantity float64, clientOrderID string, orderType string, timeInForce string, price float64, venue spotExecutionVenue) (url.Values, error) {
	params := url.Values{}
	rules := e.spotRules[symbol]
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("quantity", formatQuantityForRules(quantity, rules, 0))
	params.Set("newOrderRespType", "FULL")
	if mode := normalizedSTPMode(e.cfg.ExecutionConfig.SpotSTPMode); mode != "" {
		params.Set("selfTradePreventionMode", mode)
	}
	if clientOrderID != "" {
		params.Set("newClientOrderId", clientOrderID)
	}
	applySpotVenueOrderParams(params, venue)
	orderType = strings.ToUpper(strings.TrimSpace(orderType))
	switch orderType {
	case "LIMIT_MAKER":
		params.Set("type", "LIMIT_MAKER")
	case "LIMIT":
		params.Set("type", "LIMIT")
		if strings.TrimSpace(timeInForce) != "" {
			params.Set("timeInForce", strings.ToUpper(strings.TrimSpace(timeInForce)))
		}
	default:
		return nil, fmt.Errorf("不支持的现货限价类型: %s", orderType)
	}
	if price <= 0 {
		return nil, fmt.Errorf("现货限价单价格无效: symbol=%s side=%s price=%.8f", symbol, side, price)
	}
	params.Set("price", formatPriceForTick(price, rules.PriceTick))
	return params, nil
}

func (e *FundingRateSniperEngine) queryDirectionalSpotOrderByClientID(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) (SpotOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)
	applySpotVenueAccountParams(params, venue)
	body, err := e.signedRequest(ctx, http.MethodGet, e.spotOrderRequestPathForVenue(venue), params)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货订单查询回执失败: %w", err)
	}
	return result, nil
}

func (e *FundingRateSniperEngine) reconcileDirectionalSpotOrderTerminalState(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) (SpotOrderResponse, error) {
	return reconcileOrderTerminalState(
		ctx,
		unknownOrderReconcileTimeout(e.cfg),
		spotExecutionMarketLabel(venue),
		symbol,
		clientOrderID,
		func(queryCtx context.Context) (orderTerminalReconcilePollResult[SpotOrderResponse], error) {
			queried, err := e.queryDirectionalSpotOrderByClientID(queryCtx, symbol, clientOrderID, venue)
			if err != nil {
				return orderTerminalReconcilePollResult[SpotOrderResponse]{}, err
			}
			return orderTerminalReconcilePollResult[SpotOrderResponse]{
				Value: queried,
				View:  queried.SemanticView(),
			}, nil
		},
		func(polled orderTerminalReconcilePollResult[SpotOrderResponse]) {
			e.noteDirectionalSpotOrderObservation(symbol, venue, polled.Value)
		},
	)
}

func (e *FundingRateSniperEngine) placeDirectionalSpotOrderIdempotentWithParams(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, action string, venue spotExecutionVenue, params url.Values) (SpotOrderResponse, error) {
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	enforceLimit := strings.EqualFold(strings.TrimSpace(side), "BUY") && !strings.Contains(strings.ToLower(action), "rescue") && !strings.Contains(strings.ToLower(action), "close")
	if err := e.noteOrderPlaced(ctx, action, fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, enforceLimit); err != nil {
		return SpotOrderResponse{}, err
	}
	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPathForVenue(venue), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, action+"_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
			defer cancel()
			if snapshot, blindErr := e.waitForDirectionalSpotOrderWithBlindDeadline(waitCtx, symbol, clientOrderID, venue); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
				}
				return result, nil
			} else if blindErr != nil {
				return SpotOrderResponse{}, blindErr
			}
			queried, queryErr := e.queryDirectionalSpotOrderByClientID(ctx, symbol, clientOrderID, venue)
			if queryErr == nil && queried.HasConfirmedExecution() {
				e.noteDirectionalSpotOrderObservation(symbol, venue, queried)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileDirectionalSpotOrderTerminalState(ctx, symbol, clientOrderID, venue)
			if reconcileErr == nil {
				e.noteDirectionalSpotOrderObservation(symbol, venue, queried)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			return SpotOrderResponse{}, reconcileErr
		}
		return SpotOrderResponse{}, err
	}
	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货订单回执失败: %w", err)
	}
	e.noteDirectionalSpotOrderObservation(symbol, venue, result)
	if venue == spotExecutionVenueCrossMargin && strings.TrimSpace(result.ClientOrderID) != "" && !result.IsClosed() {
		go func() {
			observeCtx, cancel := context.WithTimeout(context.Background(), unknownOrderReconcileTimeout(e.cfg))
			defer cancel()
			e.observeDirectionalSpotOrderUntilTerminal(observeCtx, symbol, result.ClientOrderID, venue)
		}()
	}
	if protectedIOC {
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
	}
	return result, nil
}

func (e *FundingRateSniperEngine) placeDirectionalSpotProtectedOpenDualTrack(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, venue spotExecutionVenue, protectedParams url.Values) (SpotOrderResponse, error) {
	action := "spot_market_" + strings.ToLower(side)
	if err := e.noteOrderPlaced(ctx, action, fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, strings.EqualFold(side, "BUY")); err != nil {
		return SpotOrderResponse{}, err
	}
	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPathForVenue(venue), protectedParams)
	if err == nil {
		var result SpotOrderResponse
		if unmarshalErr := json.Unmarshal(body, &result); unmarshalErr != nil {
			return SpotOrderResponse{}, fmt.Errorf("解析现货保护单回执失败: %w", unmarshalErr)
		}
		e.noteDirectionalSpotOrderObservation(symbol, venue, result)
		if venue == spotExecutionVenueCrossMargin && strings.TrimSpace(result.ClientOrderID) != "" && !result.IsClosed() {
			go func() {
				observeCtx, cancel := context.WithTimeout(context.Background(), unknownOrderReconcileTimeout(e.cfg))
				defer cancel()
				e.observeDirectionalSpotOrderUntilTerminal(observeCtx, symbol, result.ClientOrderID, venue)
			}()
		}
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
		if !result.RequiresProtectedOpenFallback(quantity) {
			return result, nil
		}
	} else if !isPercentPriceFilterError(err) && !isUnknownOrderRequestError(err) {
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
		widenedParams, buildErr := e.buildDirectionalSpotLimitOrderParams(symbol, side, quantity, widenedClientOrderID, "LIMIT", protectedExecutionIOC, widenedPrice, venue)
		if buildErr == nil {
			widenedOrder, widenedErr := e.placeDirectionalSpotOrderIdempotentWithParams(ctx, symbol, side, quantity, widenedClientOrderID, "spot_"+strings.ToLower(side)+"_open_widened_ioc", venue, widenedParams)
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
	applySpotVenueOrderParams(marketParams, venue)
	return e.placeDirectionalSpotOrderIdempotentWithParams(ctx, symbol, side, quantity, marketParams.Get("newClientOrderId"), "spot_"+strings.ToLower(side)+"_open_market_fallback", venue, marketParams)
}

func (e *FundingRateSniperEngine) placeDirectionalSpotMarketOrderIdempotent(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, venue spotExecutionVenue, action string, enableDualTrack bool) (SpotOrderResponse, error) {
	params, err := e.buildDirectionalSpotProtectedOrderParams(ctx, symbol, side, quantity, clientOrderID, venue)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if enableDualTrack && isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce")) {
		result, dualTrackErr := e.placeDirectionalSpotProtectedOpenDualTrack(ctx, symbol, side, quantity, clientOrderID, venue, params)
		if dualTrackErr == nil || result.SemanticView().ExecutedQty > executionQtyTolerance {
			return result, dualTrackErr
		}
		if !isUnknownOrderRequestError(dualTrackErr) && !isPercentPriceFilterError(dualTrackErr) {
			return SpotOrderResponse{}, dualTrackErr
		}
	}
	return e.placeDirectionalSpotOrderIdempotentWithParams(ctx, symbol, side, quantity, clientOrderID, action, venue, params)
}

func (e *FundingRateSniperEngine) placeDirectionalSpotChasingOrderWithOptions(ctx context.Context, symbol string, side string, quantity float64, actionPrefix string, options spotChaseOptions, venue spotExecutionVenue) (SpotOrderResponse, error) {
	remaining := quantity
	var aggregate SpotOrderResponse
	for attempt := 0; attempt < executionChaseAttempts && remaining > executionQtyTolerance; attempt++ {
		ticker, err := e.fetchSpotBookTicker(ctx, symbol)
		if err != nil {
			return aggregate, err
		}
		bestBid := parseFloat(ticker.BidPrice)
		bestAsk := parseFloat(ticker.AskPrice)
		slippage := chaseSlippageForAttempt(attempt)
		var marketReference float64
		if strings.EqualFold(side, "SELL") {
			marketReference = bestBid
		} else {
			marketReference = bestAsk
		}
		referencePrice := chaseReferencePrice(marketReference, attempt, options)
		if err := e.enforceChaseLossLimit(symbol, side, remaining, referencePrice, marketReference, "spot_"+strings.ToLower(side)+"_chase"); err != nil {
			return aggregate, err
		}
		limitPrice := normalizeProtectedLimitPrice(side, referencePrice, slippage, e.spotRules[symbol].PriceTick)
		limitPrice = clampSpotChaseLimitPrice(side, limitPrice, options.HardLimitPrice)
		params, err := e.buildDirectionalSpotLimitOrderParams(symbol, side, remaining, newClientOrderID(actionPrefix, symbol), "LIMIT", protectedExecutionIOC, limitPrice, venue)
		if err != nil {
			return aggregate, err
		}
		order, err := e.placeDirectionalSpotOrderIdempotentWithParams(ctx, symbol, side, remaining, params.Get("newClientOrderId"), "spot_"+strings.ToLower(side)+"_chase", venue, params)
		if err != nil {
			return aggregate, err
		}
		aggregate = mergeSpotOrderResponses(aggregate, order)
		remaining = maxFloat(quantity-parseFloat(aggregate.ExecutedQty), 0)
		if remaining <= executionQtyTolerance {
			return aggregate, nil
		}
		if attempt+1 < executionChaseAttempts && !sleepWithContext(ctx, executionChaseWaitDuration(executionChaseInterval)) {
			return aggregate, ctx.Err()
		}
	}
	if parseFloat(aggregate.ExecutedQty) > executionQtyTolerance {
		return aggregate, fmt.Errorf("现货追价后仍有残余数量未成交: symbol=%s side=%s remaining=%.8f", symbol, side, remaining)
	}
	return aggregate, fmt.Errorf("现货追价未成交: symbol=%s side=%s quantity=%.8f", symbol, side, quantity)
}
