package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const (
	executionChaseAttempts        = 4
	executionChaseInterval        = 400 * time.Millisecond
	executionChaseIntervalJitter  = 150 * time.Millisecond
	executionChaseMinWaitInterval = 75 * time.Millisecond
	passiveMakerDwell             = 500 * time.Millisecond
	passiveMakerMaxAttempts       = 3
	passiveMakerCancelBudget      = 2
	passiveMakerDepthLevels       = 3
	passiveMakerAskBidMaxRatio    = 3.0
	passiveMakerMaxBidDecayRate   = 0.0010
	passiveMakerMaxSpreadRate     = 0.0025
)

var executionChaseSlippageSchedule = []float64{0.0010, 0.0025, 0.0020, 0.0035}
var executionChaseJitterDuration = defaultExecutionChaseJitterDuration

type spotChaseOptions struct {
	AnchorPrice    float64
	HardLimitPrice float64
	ReanchorAfter  int
}

func chaseSlippageForAttempt(attempt int) float64 {
	if attempt < 0 {
		attempt = 0
	}
	if attempt < len(executionChaseSlippageSchedule) && executionChaseSlippageSchedule[attempt] > 0 {
		return executionChaseSlippageSchedule[attempt]
	}
	if len(executionChaseSlippageSchedule) == 0 {
		return 0.0010
	}
	last := executionChaseSlippageSchedule[len(executionChaseSlippageSchedule)-1]
	if last <= 0 {
		last = 0.0010
	}
	return last
}

func defaultExecutionChaseJitterDuration(maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxJitter)*2+1))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64()) - maxJitter
}

func executionChaseWaitDuration(baseInterval time.Duration) time.Duration {
	wait := baseInterval + executionChaseJitterDuration(executionChaseIntervalJitter)
	if wait < executionChaseMinWaitInterval {
		return executionChaseMinWaitInterval
	}
	return wait
}

func (e *FundingRateSniperEngine) enforceChaseLossLimit(symbol string, side string, quantity float64, referencePrice float64, marketReference float64, action string) error {
	if e == nil || e.cfg.RiskConfig.MaxCycleLossUSDT <= 0 || quantity <= executionQtyTolerance || referencePrice <= 0 || marketReference <= 0 {
		return nil
	}
	side = strings.ToUpper(strings.TrimSpace(side))
	projectedLoss := 0.0
	switch side {
	case "SELL":
		projectedLoss = maxFloat(referencePrice-marketReference, 0) * quantity
	case "BUY":
		projectedLoss = maxFloat(marketReference-referencePrice, 0) * quantity
	default:
		return nil
	}
	if projectedLoss+1e-9 < e.cfg.RiskConfig.MaxCycleLossUSDT {
		return nil
	}
	detail := fmt.Sprintf("追价期间潜在损失超过单周期阈值: symbol=%s action=%s side=%s qty=%.8f reference=%.8f market=%.8f projected_loss=%.6f limit=%.6f",
		nonEmpty(symbol, "-"), nonEmpty(action, "chase"), side, quantity, referencePrice, marketReference, projectedLoss, e.cfg.RiskConfig.MaxCycleLossUSDT)
	if err := e.recordExecutionIncident("chase_loss_limit", symbol, detail); err != nil {
		return err
	}
	return e.tripPlatformFuse("chase_loss_limit", "critical", detail)
}

func makerLimitPrice(side string, bestBid float64, bestAsk float64, tickSize float64) float64 {
	side = strings.ToUpper(strings.TrimSpace(side))
	switch side {
	case "BUY":
		if bestBid <= 0 {
			return 0
		}
		if tickSize > 0 {
			return floorToStep(bestBid, tickSize)
		}
		return bestBid
	case "SELL":
		if bestAsk <= 0 {
			return 0
		}
		if tickSize > 0 {
			return ceilToStep(bestAsk, tickSize)
		}
		return bestAsk
	default:
		return 0
	}
}

func (e *FundingRateSniperEngine) buildSpotLimitOrderParams(symbol string, side string, quantity float64, clientOrderID string, orderType string, timeInForce string, price float64) (url.Values, error) {
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

func buildAnchoredSpotChaseOptions(side string, anchorPrice float64, tickSize float64) spotChaseOptions {
	if anchorPrice <= 0 {
		return spotChaseOptions{}
	}
	maxSlippage := 0.0
	for attempt := 0; attempt < executionChaseAttempts; attempt++ {
		maxSlippage = math.Max(maxSlippage, chaseSlippageForAttempt(attempt))
	}
	return spotChaseOptions{
		AnchorPrice:    anchorPrice,
		HardLimitPrice: normalizeProtectedLimitPrice(side, anchorPrice, maxSlippage, tickSize),
		ReanchorAfter:  executionChaseAttempts - 1,
	}
}

func clampSpotChaseLimitPrice(side string, limitPrice float64, hardLimitPrice float64) float64 {
	if hardLimitPrice <= 0 || limitPrice <= 0 {
		return limitPrice
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "SELL":
		return math.Max(limitPrice, hardLimitPrice)
	case "BUY":
		return math.Min(limitPrice, hardLimitPrice)
	default:
		return limitPrice
	}
}

func chaseReferencePrice(marketReference float64, attempt int, options spotChaseOptions) float64 {
	if options.AnchorPrice > 0 {
		reanchorAfter := options.ReanchorAfter
		if reanchorAfter <= 0 || attempt < reanchorAfter {
			return options.AnchorPrice
		}
	}
	if marketReference > 0 {
		return marketReference
	}
	return options.AnchorPrice
}

func (e *FundingRateSniperEngine) placeSpotOrderIdempotentWithParams(ctx context.Context, symbol string, side string, quantity float64, clientOrderID string, action string, params url.Values) (SpotOrderResponse, error) {
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	enforceLimit := strings.EqualFold(strings.TrimSpace(side), "BUY") && !strings.Contains(strings.ToLower(action), "rescue") && !strings.Contains(strings.ToLower(action), "close")
	if err := e.noteOrderPlaced(action, fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, enforceLimit); err != nil {
		return SpotOrderResponse{}, err
	}
	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, action+"_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			waitCtx, cancel := context.WithTimeout(ctx, e.cfg.ExecutionConfig.ExecutionConfirmTimeout)
			defer cancel()
			if snapshot, ok := e.waitForSpotOrder(waitCtx, symbol, clientOrderID); ok && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
				}
				return result, nil
			}
			queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
			if queryErr == nil && hasConfirmedSpotOrderResult(queried) {
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID)
			if reconcileErr == nil {
				if protectedIOC {
					e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(queried.ExecutedQty), queried.Status)
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
	if protectedIOC {
		e.noteProtectedIOCResult(symbol, "spot", quantity, parseFloat(result.ExecutedQty), result.Status)
	}
	return result, nil
}

func (e *FundingRateSniperEngine) placeSpotChasingOrder(ctx context.Context, symbol string, side string, quantity float64, actionPrefix string) (SpotOrderResponse, error) {
	return e.placeSpotChasingOrderWithOptions(ctx, symbol, side, quantity, actionPrefix, spotChaseOptions{})
}

func (e *FundingRateSniperEngine) placeSpotChasingOrderWithOptions(ctx context.Context, symbol string, side string, quantity float64, actionPrefix string, options spotChaseOptions) (SpotOrderResponse, error) {
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
		params, err := e.buildSpotLimitOrderParams(symbol, side, remaining, newClientOrderID(actionPrefix, symbol), "LIMIT", protectedExecutionIOC, limitPrice)
		if err != nil {
			return aggregate, err
		}
		order, err := e.placeSpotOrderIdempotentWithParams(ctx, symbol, side, remaining, params.Get("newClientOrderId"), "spot_"+strings.ToLower(side)+"_chase", params)
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

func sumDepthQuantity(levels []DepthLevel, limit int) float64 {
	total := 0.0
	for idx, level := range levels {
		if limit > 0 && idx >= limit {
			break
		}
		total += math.Max(level.Quantity, 0)
	}
	return total
}

func (e *FundingRateSniperEngine) shouldAttemptPassiveSpotClose(symbol string) (bool, string) {
	if e == nil || e.spotDepthCache == nil {
		return true, ""
	}
	depth, ok := e.spotDepthCache.Get(symbol)
	if !ok {
		return true, ""
	}
	bidQty := sumDepthQuantity(depth.Bids, passiveMakerDepthLevels)
	askQty := sumDepthQuantity(depth.Asks, passiveMakerDepthLevels)
	if bidQty <= executionQtyTolerance {
		return false, "bid_depth_empty"
	}
	if askQty > bidQty*passiveMakerAskBidMaxRatio {
		return false, fmt.Sprintf("bearish_imbalance ask=%.4f bid=%.4f", askQty, bidQty)
	}
	return true, ""
}

func passiveMakerSellTrendAllowed(anchorBid float64, ticker BookTicker) (bool, string) {
	currentBid := parseFloat(ticker.BidPrice)
	currentAsk := parseFloat(ticker.AskPrice)
	if currentBid <= 0 || currentAsk <= 0 {
		return false, "maker_ticker_invalid"
	}
	if currentAsk <= currentBid {
		return true, ""
	}
	if anchorBid > 0 && currentBid < anchorBid*(1-passiveMakerMaxBidDecayRate) {
		return false, fmt.Sprintf("maker_bid_decay current=%.8f anchor=%.8f", currentBid, anchorBid)
	}
	spreadRate := (currentAsk - currentBid) / currentBid
	if spreadRate > passiveMakerMaxSpreadRate {
		return false, fmt.Sprintf("maker_spread_too_wide spread=%.6f limit=%.6f", spreadRate, passiveMakerMaxSpreadRate)
	}
	return true, ""
}

func mergeSpotOrderResponses(aggregate SpotOrderResponse, latest SpotOrderResponse) SpotOrderResponse {
	if aggregate.Symbol == "" {
		aggregate.Symbol = latest.Symbol
	}
	aggregate.OrderID = latest.OrderID
	aggregate.Status = latest.Status
	aggregate.ClientOrderID = latest.ClientOrderID
	aggregate.ExecutedQty = formatFloat(parseFloat(aggregate.ExecutedQty) + parseFloat(latest.ExecutedQty))
	aggregate.CummulativeQuoteQty = formatFloat(parseFloat(aggregate.CummulativeQuoteQty) + parseFloat(latest.CummulativeQuoteQty))
	aggregate.OrigQty = formatFloat(parseFloat(aggregate.OrigQty) + parseFloat(latest.OrigQty))
	aggregate.TransactTime = latest.TransactTime
	aggregate.SelfTradePreventionMode = nonEmpty(latest.SelfTradePreventionMode, aggregate.SelfTradePreventionMode)
	aggregate.Fills = append(aggregate.Fills, latest.Fills...)
	return aggregate
}

func mergeExecutionSnapshots(aggregate OrderExecutionSnapshot, latest OrderExecutionSnapshot) OrderExecutionSnapshot {
	if aggregate.Symbol == "" {
		aggregate.Symbol = latest.Symbol
	}
	if aggregate.Market == "" {
		aggregate.Market = latest.Market
	}
	aggregate.OrderID = latest.OrderID
	aggregate.ClientOrderID = latest.ClientOrderID
	totalExecuted := aggregate.ExecutedQty + latest.ExecutedQty
	if totalExecuted > executionQtyTolerance {
		aggregate.AvgPrice = ((aggregate.AvgPrice * aggregate.ExecutedQty) + (latest.AvgPrice * latest.ExecutedQty)) / totalExecuted
	}
	aggregate.ExecutedQty = totalExecuted
	aggregate.CumQuoteQty += latest.CumQuoteQty
	aggregate.Commission += latest.Commission
	aggregate.RealizedPnL += latest.RealizedPnL
	aggregate.Status = latest.Status
	aggregate.ExecutionType = latest.ExecutionType
	aggregate.EventTime = latest.EventTime
	return aggregate
}

func (e *FundingRateSniperEngine) placeFuturesChasingClose(ctx context.Context, symbol string, quantity float64, actionPrefix string) (OrderExecutionSnapshot, error) {
	remaining := quantity
	var aggregate OrderExecutionSnapshot
	for attempt := 0; attempt < executionChaseAttempts && remaining > executionQtyTolerance; attempt++ {
		ticker, err := e.fetchFuturesBookTicker(ctx, symbol)
		if err != nil {
			return aggregate, err
		}
		referencePrice := parseFloat(ticker.AskPrice)
		slippage := chaseSlippageForAttempt(attempt)
		if err := e.enforceChaseLossLimit(symbol, "BUY", remaining, referencePrice, referencePrice, "futures_close_chase"); err != nil {
			return aggregate, err
		}
		limitPrice := normalizeProtectedLimitPrice("BUY", referencePrice, slippage, e.futuresRules[symbol].PriceTick)
		order := FuturesBatchOrderRequest{
			Symbol:           symbol,
			Side:             "BUY",
			Type:             "LIMIT",
			Quantity:         formatQuantityForRules(remaining, e.futuresRules[symbol], 0),
			Price:            formatPriceForTick(limitPrice, e.futuresRules[symbol].PriceTick),
			TimeInForce:      protectedExecutionIOC,
			ReduceOnly:       "true",
			SelfTradeMode:    normalizedSTPMode(e.cfg.ExecutionConfig.FuturesSTPMode),
			NewClientOrderID: newClientOrderID(actionPrefix, symbol),
			NewOrderRespType: "RESULT",
		}
		if e.cfg.ExecutionConfig.FuturesPriceProtect {
			order.PriceProtect = "TRUE"
		}
		snapshot, err := e.placeSingleFuturesOrderIdempotent(ctx, order, "futures_close_chase")
		if err != nil {
			return aggregate, err
		}
		aggregate = mergeExecutionSnapshots(aggregate, snapshot)
		remaining = maxFloat(quantity-aggregate.ExecutedQty, 0)
		if remaining <= executionQtyTolerance {
			return aggregate, nil
		}
		if attempt+1 < executionChaseAttempts && !sleepWithContext(ctx, executionChaseWaitDuration(executionChaseInterval)) {
			return aggregate, ctx.Err()
		}
	}
	if aggregate.ExecutedQty > executionQtyTolerance {
		return aggregate, fmt.Errorf("合约追价后仍有残余数量未成交: symbol=%s remaining=%.8f", symbol, remaining)
	}
	return aggregate, fmt.Errorf("合约追价未成交: symbol=%s quantity=%.8f", symbol, quantity)
}

func (e *FundingRateSniperEngine) placeSpotPassiveMakerClose(ctx context.Context, symbol string, quantity float64, actionPrefix string) (SpotOrderResponse, error) {
	remaining := quantity
	var aggregate SpotOrderResponse
	cancelCount := 0
	anchorBid := 0.0
	for attempt := 0; attempt < passiveMakerMaxAttempts && remaining > executionQtyTolerance; attempt++ {
		if attempt > 0 && cancelCount >= passiveMakerCancelBudget {
			return aggregate, nil
		}
		if ok, reason := e.shouldAttemptPassiveSpotClose(symbol); !ok {
			if e != nil && e.logger != nil {
				e.logger.Printf("跳过 maker 平仓，symbol=%s reason=%s", symbol, reason)
			}
			return aggregate, nil
		}
		ticker, err := e.fetchSpotBookTicker(ctx, symbol)
		if err != nil {
			return aggregate, err
		}
		currentBid := parseFloat(ticker.BidPrice)
		if anchorBid <= 0 {
			anchorBid = currentBid
		}
		if ok, reason := passiveMakerSellTrendAllowed(anchorBid, ticker); !ok {
			if e != nil && e.logger != nil {
				e.logger.Printf("终止 maker 平仓并回退追价，symbol=%s reason=%s", symbol, reason)
			}
			return aggregate, nil
		}
		price := makerLimitPrice("SELL", currentBid, parseFloat(ticker.AskPrice), e.spotRules[symbol].PriceTick)
		clientOrderID := newClientOrderID(actionPrefix, symbol)
		params, err := e.buildSpotLimitOrderParams(symbol, "SELL", remaining, clientOrderID, "LIMIT_MAKER", "", price)
		if err != nil {
			return aggregate, err
		}
		order, err := e.placeSpotOrderIdempotentWithParams(ctx, symbol, "SELL", remaining, clientOrderID, "spot_sell_passive_maker", params)
		if err != nil {
			return aggregate, err
		}
		finalOrder := order
		if parseFloat(order.ExecutedQty) <= executionQtyTolerance && !isClosedOrderStatus(order.Status) {
			if !sleepWithContext(ctx, passiveMakerDwell) {
				return aggregate, ctx.Err()
			}
			if queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID); queryErr == nil {
				finalOrder = queried
			}
			if parseFloat(finalOrder.ExecutedQty) <= executionQtyTolerance && !isClosedOrderStatus(finalOrder.Status) {
				if cancelCount >= passiveMakerCancelBudget {
					return aggregate, nil
				}
				canceled, cancelErr := e.cancelSpotOrderByClientID(ctx, symbol, clientOrderID)
				if cancelErr != nil {
					return aggregate, cancelErr
				}
				cancelCount++
				finalOrder = canceled
			}
		}
		aggregate = mergeSpotOrderResponses(aggregate, finalOrder)
		remaining = maxFloat(quantity-parseFloat(aggregate.ExecutedQty), 0)
		if remaining <= executionQtyTolerance {
			return aggregate, nil
		}
	}
	return aggregate, nil
}
