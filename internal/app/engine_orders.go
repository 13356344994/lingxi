package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载单笔合约/现货下单的幂等执行原语
// （平仓/反向开仓确认、市价单幂等提交、现货买卖幂等、快照转换，同包搬移、行为不变），
// 向单文件 ≤ 800 行门禁靠拢。

func (e *FundingRateSniperEngine) placeSingleFuturesClose(ctx context.Context, symbol string, quantity float64) error {
	_, err := e.placeSingleFuturesCloseConfirmed(ctx, symbol, quantity)
	return err
}

func (e *FundingRateSniperEngine) placeDirectionalFuturesCloseConfirmed(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection) (OrderExecutionSnapshot, error) {
	side := resolvedFundingCarryDirection(direction, 0).closeFuturesSide()
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, side, quantity, true, newClientOrderID("ftrim", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesCloseConfirmed(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeDirectionalFuturesCloseConfirmed(ctx, symbol, quantity, fundingCarryDirectionPositive)
}

func (e *FundingRateSniperEngine) placeDirectionalFuturesReopen(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection) (OrderExecutionSnapshot, error) {
	side := resolvedFundingCarryDirection(direction, 0).openFuturesSide()
	return e.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, side, quantity, false, newClientOrderID("frescue", symbol))
}

func (e *FundingRateSniperEngine) placeSingleFuturesReopenShort(ctx context.Context, symbol string, quantity float64) (OrderExecutionSnapshot, error) {
	return e.placeDirectionalFuturesReopen(ctx, symbol, quantity, fundingCarryDirectionPositive)
}

func (e *FundingRateSniperEngine) placeSingleFuturesMarketOrderIdempotent(ctx context.Context, symbol string, side string, quantity float64, reduceOnly bool, clientOrderID string) (OrderExecutionSnapshot, error) {
	if quantity <= executionQtyTolerance {
		return OrderExecutionSnapshot{}, fmt.Errorf("quantity 必须大于 0")
	}
	params := url.Values{}
	params, err := e.futuresProtectedOrderParams(ctx, symbol, side, quantity, reduceOnly, clientOrderID)
	if err != nil {
		return OrderExecutionSnapshot{}, err
	}
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := guardRecoveryFuturesReduceOnly(ctx, symbol, "side="+side, reduceOnly); err != nil {
		return OrderExecutionSnapshot{}, err
	}
	if err := e.noteOrderPlaced(ctx, "futures_market_"+strings.ToLower(side), fmt.Sprintf("%s qty=%s reduceOnly=%t", symbol, formatFloat(quantity), reduceOnly), 1, !reduceOnly); err != nil {
		return OrderExecutionSnapshot{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.futuresOrderRequestPath(), params)
	if err != nil {
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return OrderExecutionSnapshot{}, err
		}
		e.noteOrderUncertainty(symbol, "futures_market_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
			if protectedIOC {
				e.noteProtectedIOCObservation(symbol, "futures", snapshot.ProtectedIOCObservation(quantity))
			}
			return snapshot, nil
		} else if blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		}
		queried, queryErr := e.queryFuturesOrderByClientID(ctx, symbol, clientOrderID)
		if queryErr != nil {
			queried = FuturesBatchOrderResult{
				Symbol:        symbol,
				ClientOrderID: clientOrderID,
			}
		}
		snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, queried)
		if resolveErr == nil && protectedIOC {
			e.noteProtectedIOCObservation(symbol, "futures", snapshot.ProtectedIOCObservation(quantity))
		}
		return snapshot, resolveErr
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return OrderExecutionSnapshot{}, fmt.Errorf("解析合约单笔市价单回执失败: %w", err)
	}
	if result.IsTerminal() {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCObservation(symbol, "futures", snapshot.ProtectedIOCObservation(quantity))
		}
		return snapshot, nil
	}
	snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, symbol, clientOrderID, result)
	if resolveErr == nil && protectedIOC {
		e.noteProtectedIOCObservation(symbol, "futures", snapshot.ProtectedIOCObservation(quantity))
	}
	return snapshot, resolveErr
}

func (e *FundingRateSniperEngine) submitSingleFuturesOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest, action string) (OrderExecutionSnapshot, error) {
	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", order.Side)
	params.Set("type", order.Type)
	params.Set("quantity", order.Quantity)
	if order.Price != "" {
		params.Set("price", order.Price)
	}
	if order.TimeInForce != "" {
		params.Set("timeInForce", order.TimeInForce)
	}
	if order.PriceProtect != "" {
		params.Set("priceProtect", order.PriceProtect)
	}
	if order.NewClientOrderID != "" {
		params.Set("newClientOrderId", order.NewClientOrderID)
	}
	if order.NewOrderRespType != "" {
		params.Set("newOrderRespType", order.NewOrderRespType)
	}
	if order.ReduceOnly != "" {
		params.Set("reduceOnly", order.ReduceOnly)
	}
	if order.SelfTradeMode != "" {
		params.Set("selfTradePreventionMode", order.SelfTradeMode)
	}
	requestedQty := parseFloat(order.Quantity)
	protectedIOC := isProtectedIOCRequestType(order.Type, order.TimeInForce)
	if err := guardRecoveryFuturesReduceOnly(ctx, order.Symbol, "action="+action, strings.EqualFold(strings.TrimSpace(order.ReduceOnly), "true")); err != nil {
		return OrderExecutionSnapshot{}, err
	}
	if err := e.noteOrderPlaced(ctx, action, fmt.Sprintf("%s qty=%s", order.Symbol, order.Quantity), 1, strings.Contains(strings.ToLower(action), "open")); err != nil {
		return OrderExecutionSnapshot{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.futuresOrderRequestPath(), params)
	if err != nil {
		if order.NewClientOrderID == "" || !isUnknownOrderRequestError(err) {
			return OrderExecutionSnapshot{}, err
		}
		e.noteOrderUncertainty(order.Symbol, action+"_fallback", fmt.Sprintf("clientOrderId=%s err=%v", order.NewClientOrderID, err))
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, order.Symbol, order.NewClientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
			if protectedIOC {
				e.noteProtectedIOCObservation(order.Symbol, "futures", snapshot.ProtectedIOCObservation(requestedQty))
			}
			return snapshot, nil
		} else if blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		}
		queried, queryErr := e.queryFuturesOrderByClientID(ctx, order.Symbol, order.NewClientOrderID)
		if queryErr != nil {
			queried = FuturesBatchOrderResult{
				Symbol:        order.Symbol,
				ClientOrderID: order.NewClientOrderID,
			}
		}
		snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, order.Symbol, order.NewClientOrderID, queried)
		if resolveErr == nil && protectedIOC {
			e.noteProtectedIOCObservation(order.Symbol, "futures", snapshot.ProtectedIOCObservation(requestedQty))
		}
		return snapshot, resolveErr
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return OrderExecutionSnapshot{}, fmt.Errorf("解析合约单笔回执失败: %w", err)
	}
	if result.ExecutedQtyValue() > executionQtyTolerance {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCObservation(order.Symbol, "futures", snapshot.ProtectedIOCObservation(requestedQty))
		}
		return snapshot, nil
	}
	if result.IsTerminal() {
		snapshot := futuresResultToSnapshot(result)
		if protectedIOC {
			e.noteProtectedIOCObservation(order.Symbol, "futures", snapshot.ProtectedIOCObservation(requestedQty))
		}
		return snapshot, nil
	}
	snapshot, resolveErr := e.resolveFuturesOrderExecution(ctx, order.Symbol, order.NewClientOrderID, result)
	if resolveErr == nil && protectedIOC {
		e.noteProtectedIOCObservation(order.Symbol, "futures", snapshot.ProtectedIOCObservation(requestedQty))
	}
	return snapshot, resolveErr
}

func (e *FundingRateSniperEngine) placeSingleFuturesOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest, action string) (OrderExecutionSnapshot, error) {
	if isOpenProtectedFuturesOrder(order) && strings.Contains(strings.ToLower(action), "open") {
		return e.placeFuturesProtectedOpenDualTrack(ctx, order, action, e.submitSingleFuturesOrderIdempotent)
	}
	return e.submitSingleFuturesOrderIdempotent(ctx, order, action)
}

func (e *FundingRateSniperEngine) placeSingleFuturesOpenOrderIdempotent(ctx context.Context, order FuturesBatchOrderRequest) (OrderExecutionSnapshot, error) {
	return e.placeSingleFuturesOrderIdempotent(ctx, order, "futures_open_single")
}

func futuresExecutionToResult(snapshot OrderExecutionSnapshot, order FuturesBatchOrderRequest) FuturesBatchOrderResult {
	return FuturesBatchOrderResult{
		OrderID:       snapshot.OrderID,
		Symbol:        nonEmpty(snapshot.Symbol, order.Symbol),
		Status:        snapshot.Status,
		ClientOrderID: nonEmpty(snapshot.ClientOrderID, order.NewClientOrderID),
		ExecutedQty:   formatFloat(snapshot.ExecutedQty),
		OrigQty:       order.Quantity,
		AvgPrice:      formatFloat(snapshot.AvgPrice),
		Side:          order.Side,
		UpdateTime:    snapshot.EventTime.UnixMilli(),
	}
}

func (e *FundingRateSniperEngine) placeSpotMarketBuyByQuantityIdempotent(ctx context.Context, symbol string, quantity float64, clientOrderID string) (SpotOrderResponse, error) {
	params, err := e.spotProtectedOrderParams(ctx, symbol, "BUY", quantity, clientOrderID)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce")) {
		result, dualTrackErr := e.placeSpotProtectedOpenDualTrack(ctx, symbol, "BUY", quantity, clientOrderID, params)
		if dualTrackErr == nil || result.SemanticView().ExecutedQty > executionQtyTolerance {
			return result, dualTrackErr
		}
		if !isUnknownOrderRequestError(dualTrackErr) && !isPercentPriceFilterError(dualTrackErr) {
			return SpotOrderResponse{}, dualTrackErr
		}
	}
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := e.noteOrderPlaced(ctx, "spot_market_buy", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, true); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, "spot_buy_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			if snapshot, blindErr := e.waitForSpotOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
				}
				return result, nil
			} else if blindErr != nil {
				return SpotOrderResponse{}, blindErr
			}
			queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
			if queryErr == nil && queried.HasConfirmedExecution() {
				e.logger.Printf("现货买单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID)
			if reconcileErr == nil {
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			return SpotOrderResponse{}, reconcileErr
		}
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return SpotOrderResponse{}, err
		}
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货买单回执失败: %w", err)
	}
	if protectedIOC {
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeSpotMarketSell(ctx context.Context, symbol string, quantity string) (SpotOrderResponse, error) {
	qtyValue := parseFloat(quantity)
	params, err := e.spotProtectedOrderParams(ctx, symbol, "SELL", qtyValue, "")
	if err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货卖单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeSpotMarketSellIdempotent(ctx context.Context, symbol string, quantity float64, clientOrderID string) (SpotOrderResponse, error) {
	params, err := e.spotProtectedOrderParams(ctx, symbol, "SELL", quantity, clientOrderID)
	if err != nil {
		return SpotOrderResponse{}, err
	}
	if isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce")) {
		result, dualTrackErr := e.placeSpotProtectedOpenDualTrack(ctx, symbol, "SELL", quantity, clientOrderID, params)
		if dualTrackErr == nil || result.SemanticView().ExecutedQty > executionQtyTolerance {
			return result, dualTrackErr
		}
		if !isUnknownOrderRequestError(dualTrackErr) && !isPercentPriceFilterError(dualTrackErr) {
			return SpotOrderResponse{}, dualTrackErr
		}
	}
	protectedIOC := isProtectedIOCRequestType(params.Get("type"), params.Get("timeInForce"))
	if err := e.noteOrderPlaced(ctx, "spot_market_sell", fmt.Sprintf("%s qty=%s", symbol, formatFloat(quantity)), 1, false); err != nil {
		return SpotOrderResponse{}, err
	}

	body, err := e.placeSignedOrder(ctx, e.spotOrderRequestPath(), params)
	if err != nil {
		if clientOrderID != "" && isUnknownOrderRequestError(err) {
			e.noteOrderUncertainty(symbol, "spot_sell_request_fallback", fmt.Sprintf("clientOrderId=%s err=%v", clientOrderID, err))
			if snapshot, blindErr := e.waitForSpotOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr == nil && hasConfirmedOrderExecution(snapshot) {
				result := e.spotSnapshotToOrderResponse(symbol, snapshot)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
				}
				return result, nil
			} else if blindErr != nil {
				return SpotOrderResponse{}, blindErr
			}
			queried, queryErr := e.querySpotOrderByClientID(ctx, symbol, clientOrderID)
			if queryErr == nil && queried.HasConfirmedExecution() {
				e.logger.Printf("现货卖单出现不确定状态，已通过 clientOrderId 回查恢复，symbol=%s clientOrderId=%s", symbol, clientOrderID)
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			queried, reconcileErr := e.reconcileSpotOrderTerminalState(ctx, symbol, clientOrderID)
			if reconcileErr == nil {
				if protectedIOC {
					e.noteProtectedIOCObservation(symbol, "spot", queried.ProtectedIOCObservation(quantity))
				}
				return queried, nil
			}
			return SpotOrderResponse{}, reconcileErr
		}
		if clientOrderID == "" || !isUnknownOrderRequestError(err) {
			return SpotOrderResponse{}, err
		}
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货卖单回执失败: %w", err)
	}
	if protectedIOC {
		e.noteProtectedIOCObservation(symbol, "spot", result.ProtectedIOCObservation(quantity))
	}

	return result, nil
}

func (e *FundingRateSniperEngine) spotSnapshotToOrderResponse(symbol string, snapshot OrderExecutionSnapshot) SpotOrderResponse {
	response := SpotOrderResponse{
		Symbol:              symbol,
		OrderID:             snapshot.OrderID,
		Status:              snapshot.Status,
		ClientOrderID:       snapshot.ClientOrderID,
		ExecutedQty:         formatFloat(snapshot.ExecutedQty),
		CummulativeQuoteQty: formatFloat(snapshot.CumQuoteQty),
		TransactTime:        snapshot.EventTime.UnixMilli(),
	}
	if snapshot.ExecutedQty > 0 {
		response.OrigQty = formatFloat(snapshot.ExecutedQty)
	}
	if snapshot.Commission > 0 || strings.TrimSpace(snapshot.CommissionAsset) != "" {
		response.Fills = []SpotFill{{
			Price:           formatFloat(snapshot.AvgPrice),
			Qty:             formatFloat(snapshot.ExecutedQty),
			Commission:      formatFloat(snapshot.Commission),
			CommissionAsset: snapshot.CommissionAsset,
		}}
	}
	return response
}
