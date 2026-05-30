package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func (e *FundingRateSniperEngine) startUserDataStreams(ctx context.Context) {
	if e.spotOrderHub == nil {
		e.spotOrderHub = NewOrderEventHub()
	}
	if e.futuresOrderHub == nil {
		e.futuresOrderHub = NewOrderEventHub()
	}

	go e.runSpotUserDataStream(ctx)
	go e.runFuturesUserDataStream(ctx)
}

func (e *FundingRateSniperEngine) runSpotUserDataStream(ctx context.Context) {
	e.runListenKeyStream(
		ctx,
		e.startSpotListenKey,
		e.keepaliveSpotListenKey,
		binanceSpotUserWSBaseURL,
		e.consumeSpotUserDataMessages,
	)
}

func (e *FundingRateSniperEngine) runFuturesUserDataStream(ctx context.Context) {
	e.runListenKeyStream(
		ctx,
		e.startFuturesListenKey,
		e.keepaliveFuturesListenKey,
		binanceFuturesUserWSBaseURL,
		e.consumeFuturesUserDataMessages,
	)
}

func (e *FundingRateSniperEngine) runListenKeyStream(
	ctx context.Context,
	start func(context.Context) (string, error),
	keepalive func(context.Context, string) error,
	wsBaseURL string,
	consume func(context.Context, *websocket.Conn) error,
) {
	retryDelay := 500 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}

		listenKey, err := start(ctx)
		if err != nil {
			e.logger.Printf("创建 listenKey 失败: %v", err)
			if !sleepWithContext(ctx, retryDelay) {
				return
			}
			continue
		}

		streamCtx, cancel := context.WithCancel(ctx)
		go e.keepaliveListenKey(streamCtx, listenKey, keepalive)

		conn, err := e.dialWebSocket(wsBaseURL + listenKey)
		if err != nil {
			cancel()
			e.logger.Printf("连接用户数据流失败: %v", err)
			if !sleepWithContext(ctx, retryDelay) {
				return
			}
			continue
		}
		stopClose := closeWebSocketOnContextDone(streamCtx, conn)

		err = consume(streamCtx, conn)
		stopClose()
		cancel()
		_ = conn.Close()
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("用户数据流中断，准备重连: %v", err)
		}

		if !sleepWithContext(ctx, retryDelay) {
			return
		}
	}
}

func (e *FundingRateSniperEngine) keepaliveListenKey(ctx context.Context, listenKey string, keepalive func(context.Context, string) error) {
	ticker := time.NewTicker(userStreamKeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := keepalive(ctx, listenKey); err != nil {
				e.logger.Printf("listenKey 保活失败: %v", err)
			}
		}
	}
}

func (e *FundingRateSniperEngine) startSpotListenKey(ctx context.Context) (string, error) {
	body, err := e.apiKeyRequest(ctx, http.MethodPost, e.client.spotBaseURL+"/api/v3/userDataStream", nil)
	if err != nil {
		return "", err
	}
	return parseListenKey(body)
}

func (e *FundingRateSniperEngine) keepaliveSpotListenKey(ctx context.Context, listenKey string) error {
	_, err := e.apiKeyRequest(ctx, http.MethodPut, e.client.spotBaseURL+"/api/v3/userDataStream?listenKey="+listenKey, nil)
	return err
}

func (e *FundingRateSniperEngine) startFuturesListenKey(ctx context.Context) (string, error) {
	body, err := e.apiKeyRequest(ctx, http.MethodPost, e.client.futuresBaseURL+"/fapi/v1/listenKey", nil)
	if err != nil {
		return "", err
	}
	return parseListenKey(body)
}

func (e *FundingRateSniperEngine) keepaliveFuturesListenKey(ctx context.Context, listenKey string) error {
	_, err := e.apiKeyRequest(ctx, http.MethodPut, e.client.futuresBaseURL+"/fapi/v1/listenKey?listenKey="+listenKey, nil)
	return err
}

func (e *FundingRateSniperEngine) apiKeyRequest(ctx context.Context, method string, endpoint string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("创建 API KEY 请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", e.client.apiKey)

	resp, err := e.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送 API KEY 请求失败: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 API KEY 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API KEY 请求失败: status=%s body=%s", resp.Status, string(payload))
	}

	return payload, nil
}

func parseListenKey(body []byte) (string, error) {
	var result struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析 listenKey 失败: %w", err)
	}
	if result.ListenKey == "" {
		return "", fmt.Errorf("listenKey 为空")
	}
	return result.ListenKey, nil
}

func isManagedClientOrderID(clientOrderID string) bool {
	clientOrderID = strings.ToLower(strings.TrimSpace(clientOrderID))
	if clientOrderID == "" {
		return false
	}
	prefixes := []string{
		"fopen",
		"fclose",
		"ftrim",
		"frescue",
		"sopen",
		"sclose",
		"sclose_revert",
		"srevert",
		"srebalance",
		"srebalf",
		"sclosemaker",
		"sclosechase",
		"exp_sell",
		"exp_buy",
		"expfclose",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(clientOrderID, prefix) {
			return true
		}
	}
	return false
}

func (e *FundingRateSniperEngine) consumeSpotUserDataMessages(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var event struct {
			EventType       string `json:"e"`
			EventTime       int64  `json:"E"`
			Symbol          string `json:"s"`
			ClientOrderID   string `json:"c"`
			Status          string `json:"X"`
			ExecutionType   string `json:"x"`
			OrderID         int64  `json:"i"`
			CumFilledQty    string `json:"z"`
			CumQuoteQty     string `json:"Z"`
			LastCommission  string `json:"n"`
			CommissionAsset string `json:"N"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("解析现货用户流消息失败: %w", err)
		}
		if event.EventType != "executionReport" {
			continue
		}
		if !isManagedClientOrderID(event.ClientOrderID) {
			continue
		}

		snapshot := OrderExecutionSnapshot{
			Market:          "spot",
			Symbol:          event.Symbol,
			ClientOrderID:   event.ClientOrderID,
			OrderID:         event.OrderID,
			Status:          event.Status,
			ExecutionType:   event.ExecutionType,
			ExecutedQty:     parseFloat(event.CumFilledQty),
			CumQuoteQty:     parseFloat(event.CumQuoteQty),
			Commission:      parseFloat(event.LastCommission),
			CommissionAsset: event.CommissionAsset,
			EventTime:       time.UnixMilli(event.EventTime),
		}
		if snapshot.ExecutedQty > 0 && snapshot.CumQuoteQty > 0 {
			snapshot.AvgPrice = snapshot.CumQuoteQty / snapshot.ExecutedQty
		}
		e.spotOrderHub.Update(snapshot)
	}
}

func (e *FundingRateSniperEngine) consumeFuturesUserDataMessages(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var envelope struct {
			EventType string `json:"e"`
			EventTime int64  `json:"E"`
			Order     struct {
				Symbol          string `json:"s"`
				ClientOrderID   string `json:"c"`
				Status          string `json:"X"`
				ExecutionType   string `json:"x"`
				OrderID         int64  `json:"i"`
				CumFilledQty    string `json:"z"`
				AvgPrice        string `json:"ap"`
				Commission      string `json:"n"`
				CommissionAsset string `json:"N"`
				RealizedPnL     string `json:"rp"`
			} `json:"o"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("解析合约用户流消息失败: %w", err)
		}
		if envelope.EventType != "ORDER_TRADE_UPDATE" {
			continue
		}
		if !isManagedClientOrderID(envelope.Order.ClientOrderID) {
			continue
		}

		snapshot := OrderExecutionSnapshot{
			Market:          "futures",
			Symbol:          envelope.Order.Symbol,
			ClientOrderID:   envelope.Order.ClientOrderID,
			OrderID:         envelope.Order.OrderID,
			Status:          envelope.Order.Status,
			ExecutionType:   envelope.Order.ExecutionType,
			ExecutedQty:     parseFloat(envelope.Order.CumFilledQty),
			AvgPrice:        parseFloat(envelope.Order.AvgPrice),
			Commission:      parseFloat(envelope.Order.Commission),
			CommissionAsset: envelope.Order.CommissionAsset,
			RealizedPnL:     parseFloat(envelope.Order.RealizedPnL),
			EventTime:       time.UnixMilli(envelope.EventTime),
		}
		if snapshot.ExecutedQty > 0 && snapshot.AvgPrice > 0 {
			snapshot.CumQuoteQty = snapshot.ExecutedQty * snapshot.AvgPrice
		}
		e.futuresOrderHub.Update(snapshot)
	}
}

func (e *FundingRateSniperEngine) waitForSpotOrder(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, bool) {
	if e.spotOrderHub == nil || strings.TrimSpace(clientOrderID) == "" || strings.TrimSpace(symbol) == "" {
		return OrderExecutionSnapshot{}, false
	}

	return e.spotOrderHub.WaitForWithFallback(
		ctx,
		clientOrderID,
		func(snapshot OrderExecutionSnapshot) bool {
			return hasConfirmedOrderExecution(snapshot)
		},
		2*time.Second,
		500*time.Millisecond,
		func(queryCtx context.Context) (OrderExecutionSnapshot, bool) {
			queried, err := e.querySpotOrderByClientID(queryCtx, symbol, clientOrderID)
			if err != nil || queried.OrderID == 0 {
				return OrderExecutionSnapshot{}, false
			}
			return OrderExecutionSnapshot{
				Market:        "spot",
				Symbol:        symbol,
				ClientOrderID: clientOrderID,
				OrderID:       queried.OrderID,
				Status:        queried.Status,
				ExecutedQty:   parseFloat(queried.ExecutedQty),
				CumQuoteQty:   parseFloat(queried.CummulativeQuoteQty),
				AvgPrice:      spotOrderAvgPrice(queried),
			}, true
		},
	)
}

func (e *FundingRateSniperEngine) waitForFuturesOrder(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, bool) {
	if e.futuresOrderHub == nil || strings.TrimSpace(clientOrderID) == "" || strings.TrimSpace(symbol) == "" {
		return OrderExecutionSnapshot{}, false
	}

	return e.futuresOrderHub.WaitForWithFallback(
		ctx,
		clientOrderID,
		func(snapshot OrderExecutionSnapshot) bool {
			return hasConfirmedOrderExecution(snapshot)
		},
		2*time.Second,
		500*time.Millisecond,
		func(queryCtx context.Context) (OrderExecutionSnapshot, bool) {
			queried, err := e.queryFuturesOrderByClientID(queryCtx, symbol, clientOrderID)
			if err != nil {
				return OrderExecutionSnapshot{}, false
			}
			snapshot := futuresResultToSnapshot(queried)
			if strings.TrimSpace(snapshot.ClientOrderID) == "" {
				snapshot.ClientOrderID = clientOrderID
			}
			return snapshot, true
		},
	)
}

func spotOrderAvgPrice(response SpotOrderResponse) float64 {
	executedQty := parseFloat(response.ExecutedQty)
	if executedQty <= 0 {
		return 0
	}
	cumQuoteQty := parseFloat(response.CummulativeQuoteQty)
	if cumQuoteQty <= 0 {
		return 0
	}
	return cumQuoteQty / executedQty
}

func isTerminalOrderStatus(status string) bool {
	switch status {
	case "FILLED", "CANCELED", "REJECTED", "EXPIRED":
		return true
	default:
		return false
	}
}

func isClosedOrderStatus(status string) bool {
	return isTerminalOrderStatus(strings.ToUpper(strings.TrimSpace(status)))
}
