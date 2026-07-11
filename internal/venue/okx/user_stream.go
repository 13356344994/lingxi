package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const privateStreamBuffer = 512

// StreamUserData 建立 OKX 私有 orders/positions 流。首次登录/订阅失败直接返回 error；
// 成功后断线在后台有限退避重连，期间不把“无事件”解释成无订单或空仓。
func (a *Adapter) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	if a == nil || a.t == nil || a.privateDialer == nil {
		return nil, fmt.Errorf("okx: 私有 WS 未接线")
	}
	cfg := PrivateStreamConfig{
		URL: a.privateWSURL, APIKey: a.t.apiKey, APISecret: a.t.apiSecret, Passphrase: a.t.passphrase, InstType: "ANY",
	}
	conn, err := DialLivePrivateStream(ctx, a.privateDialer, cfg)
	if err != nil {
		return nil, err
	}
	out := make(chan venue.UserEvent, privateStreamBuffer)
	go a.runPrivateUserStream(ctx, conn, cfg, out)
	return out, nil
}

func (a *Adapter) runPrivateUserStream(ctx context.Context, conn Conn, cfg PrivateStreamConfig, out chan<- venue.UserEvent) {
	defer close(out)
	backoff := time.Second
	for {
		err := a.consumePrivateUserConn(ctx, conn, out)
		_ = conn.Close()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		next, dialErr := DialLivePrivateStream(ctx, a.privateDialer, cfg)
		if dialErr != nil {
			if err == nil {
				err = dialErr
			}
			continue
		}
		conn = next
		backoff = time.Second
		_ = err // 断线事实由调用方通过通道静默期/重连指标监控；绝不生成伪空仓事件。
	}
}

func (a *Adapter) consumePrivateUserConn(ctx context.Context, conn Conn, out chan<- venue.UserEvent) error {
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteMessage([]byte("ping")); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if string(raw) == "pong" {
			continue
		}
		var meta struct {
			Event string `json:"event"`
			Arg   struct {
				Channel string `json:"channel"`
			} `json:"arg"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		if meta.Event != "" {
			continue
		}
		receivedAt := time.Now().UTC()
		switch meta.Arg.Channel {
		case "orders":
			push, err := ParsePrivateOrdersPush(raw)
			if err != nil {
				continue
			}
			for _, row := range push.Data {
				events := a.orderUserEvents(ctx, row, receivedAt)
				for _, event := range events {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case out <- event:
					}
				}
			}
		case "positions":
			push, err := ParsePrivatePositionsPush(raw)
			if err != nil {
				continue
			}
			for _, row := range push.Data {
				event, ok := a.positionUserEvent(ctx, row, receivedAt)
				if !ok {
					continue
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case out <- event:
				}
			}
		}
	}
}

func (a *Adapter) orderUserEvents(ctx context.Context, row PrivateOrderData, receivedAt time.Time) []venue.UserEvent {
	market := marketFromInstType(row.InstType)
	symbol, err := a.ToCanonical(row.InstID, market)
	if err != nil {
		return nil
	}
	ctVal, ok := a.privateEventCTVal(ctx, market, symbol)
	if !ok {
		return []venue.UserEvent{{Kind: venue.UserEventOther, Symbol: symbol, Market: market, RawType: "instrument_metadata_unavailable", Time: receivedAt, ReceivedAt: receivedAt}}
	}
	result := row.ToOrderResult(symbol, market, ctVal)
	events := []venue.UserEvent{{
		Kind: venue.UserEventOrderUpdate, Symbol: symbol, Market: market,
		Order: &result, RawType: row.State, Time: result.CreatedAt, ReceivedAt: receivedAt,
	}}
	if row.HasFill() {
		side := venue.SideBuy
		if strings.EqualFold(row.Side, "sell") {
			side = venue.SideSell
		}
		trade := venue.Trade{
			ID: row.FillTradeID(), OrderID: row.OrdID, ClientOrderID: row.ClOrdID, Symbol: symbol, Side: side,
			Price: row.FillPrice(), Qty: row.FillQty(market, ctVal),
			Commission: mathAbs(parseFloat(row.Fee)), CommissionAsset: row.FeeCcy,
			IsMaker: strings.EqualFold(row.Category, "normal") && strings.Contains(strings.ToLower(row.OrdType), "post"),
			Time:    msStringToTime(row.UTime),
		}
		events = append(events, venue.UserEvent{
			Kind: venue.UserEventTrade, Symbol: symbol, Market: market,
			Trade: &trade, RawType: "fill", Time: trade.Time, ReceivedAt: receivedAt,
		})
	}
	return events
}

func (a *Adapter) positionUserEvent(ctx context.Context, row PrivatePositionData, receivedAt time.Time) (venue.UserEvent, bool) {
	market := marketFromInstType(row.InstType)
	symbol, err := a.ToCanonical(row.InstID, market)
	if err != nil {
		return venue.UserEvent{}, false
	}
	ctVal, ok := a.privateEventCTVal(ctx, market, symbol)
	if !ok {
		return venue.UserEvent{}, false
	}
	position := venue.Position{
		Symbol: symbol, PositionAmt: row.SignedQty(ctVal),
		EntryPrice: parseFloat(row.AvgPx), MarkPrice: parseFloat(row.MarkPx),
	}
	at := msStringToTime(row.UTime)
	return venue.UserEvent{
		Kind: venue.UserEventAccountUpdate, Symbol: symbol, Market: market,
		Position: &position, RawType: "positions", Time: at, ReceivedAt: receivedAt,
	}, true
}

func (a *Adapter) privateEventCTVal(ctx context.Context, market venue.Market, symbol venue.Symbol) (float64, bool) {
	if market != venue.MarketPerp {
		return 1, true
	}
	inst, err := a.lookupInstrument(ctx, market, symbol)
	if err != nil || inst.ContractSize <= 0 {
		return 0, false
	}
	return inst.ContractSize, true
}

func marketFromInstType(instType string) venue.Market {
	if strings.EqualFold(strings.TrimSpace(instType), "SPOT") {
		return venue.MarketSpot
	}
	return venue.MarketPerp
}
