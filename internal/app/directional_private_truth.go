package app

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/execution"
	"lingxi/internal/money"
	"lingxi/internal/venue"
	"lingxi/internal/venue/okx"
)

func (e *OKXDirectionalEngine) startPrivateExecutionTruth(ctx context.Context) {
	if e == nil || e.v == nil || e.venueName != okx.VenueName {
		return
	}
	go func() {
		for {
			events, err := e.v.StreamUserData(ctx)
			if err == nil {
				e.consumePrivateExecutionTruth(ctx, events)
			}
			if ctx.Err() != nil {
				return
			}
			e.logger.Printf("[execution_truth] OKX 私有流未就绪，保持 REST recovery + shadow-only，30s后重试: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
}

func (e *OKXDirectionalEngine) consumePrivateExecutionTruth(ctx context.Context, events <-chan venue.UserEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				e.logger.Printf("[execution_truth] OKX 私有流关闭，adapter 将负责重连；若最终退出则保持 shadow-only")
				return
			}
			e.applyPrivateExecutionEvent(event)
		}
	}
}

func (e *OKXDirectionalEngine) applyPrivateExecutionEvent(event venue.UserEvent) {
	if e.executionReducer == nil {
		e.executionReducer = execution.NewTradeReducer()
	}
	switch event.Kind {
	case venue.UserEventOrderUpdate:
		if event.Order == nil {
			return
		}
		state := executionOrderState(event.Order.Status)
		if err := e.executionReducer.UpdateOrderState(event.Order.ClientOrderID, state); err != nil {
			e.quarantineExecutionSymbol(string(event.Symbol), "private_order_state_invalid", event.Order.ExchangeOrderID)
		}
	case venue.UserEventTrade:
		if event.Trade == nil {
			return
		}
		side := execution.SideBuy
		if event.Trade.Side == venue.SideSell {
			side = execution.SideSell
		}
		outcome, err := e.executionReducer.ApplyFill(execution.Fill{
			Symbol: string(event.Trade.Symbol), ClientOrderID: event.Trade.ClientOrderID, ExchangeOrderID: event.Trade.OrderID,
			TradeID: event.Trade.ID, Side: side, Qty: event.Trade.Qty, Price: event.Trade.Price,
			FeeMicros: money.FromUSDT(event.Trade.Commission), IsMaker: event.Trade.IsMaker,
			ExchangeTime: event.Trade.Time, ReceiveTime: event.ReceivedAt,
		})
		if err != nil || outcome.Anomaly != "" {
			reason := "private_fill_invalid"
			if outcome.Anomaly != "" {
				reason = outcome.Anomaly
			}
			e.quarantineExecutionSymbol(string(event.Symbol), reason, event.Trade.OrderID)
		}
	case venue.UserEventAccountUpdate:
		if event.Position == nil {
			return
		}
		local := e.executionReducer.PositionQty(string(event.Position.Symbol))
		if math.Abs(local-event.Position.PositionAmt) > executionQtyTolerance {
			e.quarantineExecutionSymbol(string(event.Position.Symbol), "private_position_divergence", "")
		}
	}
}

func (e *OKXDirectionalEngine) quarantineExecutionSymbol(symbol, reason, orderID string) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return
	}
	if e.executionClaims == nil {
		e.executionClaims = execution.NewClaimManager()
	}
	e.executionClaims.Quarantine(symbol, reason, e.nowFn().UTC(), orderID)
	_ = appendJSONLRecord(filepath.Join(e.recordDir, executionRecoveryIncidentFile), executionRecoveryIncident{
		RecordedAt: e.nowFn().UTC(), Venue: e.venueName, Symbol: symbol, Reason: reason,
	})
}

func executionOrderState(status string) execution.OrderState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "new", "live", "open":
		return execution.OrderStateAcked
	case "partially_filled", "partial":
		return execution.OrderStatePartiallyFilled
	case "filled":
		return execution.OrderStateFilled
	case "canceled", "cancelled":
		return execution.OrderStateCanceled
	case "rejected":
		return execution.OrderStateRejected
	default:
		return execution.OrderStateUnknown
	}
}
