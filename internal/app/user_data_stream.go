package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/exchange"

	"github.com/gorilla/websocket"
)

func (e *FundingRateSniperEngine) startUserDataStreams(ctx context.Context) {
	if e.spotOrderHub == nil {
		e.spotOrderHub = NewOrderEventHub()
	}
	if e.marginOrderHub == nil {
		e.marginOrderHub = NewOrderEventHub()
	}
	if e.futuresOrderHub == nil {
		e.futuresOrderHub = NewOrderEventHub()
	}

	go e.runSpotUserDataStream(ctx)
	go e.runFuturesUserDataStream(ctx)

	// cross margin(负 carry 现货腿)用户数据流：旧的 /sapi/v1/userDataStream listenKey 端点已被 Binance
	// 一并下线(返回 404)，新的全仓杠杆实时回报需走独立的 Margin listenToken WebSocket API，迁移尚未完成。
	// 在此之前不再启动这条死流(避免无意义重连刷屏)，cross margin 订单确认退回到 waitForDirectionalSpotOrder
	// 内置的 REST 轮询兜底(queryDirectionalSpotOrderByClientID)，这本就是该腿最初的确认机制，正确性不受影响，
	// 仅确认时延略高。正 carry(现货腿走现货账户)不受影响，已走下方 WS API 实时流。
	e.logger.Printf("全仓杠杆(cross margin)用户数据流暂未启用 WS 实时推送，订单确认退回 REST 轮询兜底；正 carry 现货流走 WS API")
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

// runSpotUserDataStream 通过 WebSocket API 的 userDataStream.subscribe.signature 订阅现货用户数据流。
// 不再依赖已下线的 listenKey REST 端点，因此也无需 listenKey 保活——连接本身存活即订阅有效。
func (e *FundingRateSniperEngine) runSpotUserDataStream(ctx context.Context) {
	retryDelay := 500 * time.Millisecond
	maxRetryDelay := 10 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		streamCtx, cancel := context.WithCancel(ctx)
		conn, err := e.dialWebSocket(binanceSpotWSAPIBaseURL)
		if err != nil {
			cancel()
			e.logger.Printf("连接现货 WS API 用户数据流失败: %v", err)
			if !sleepWithContext(ctx, retryDelay) {
				return
			}
			retryDelay = nextStreamBackoff(retryDelay, maxRetryDelay)
			continue
		}

		stopClose := closeWebSocketOnContextDone(streamCtx, conn)
		stopPing := startWebSocketPing(streamCtx, conn)

		err = e.consumeSpotWSAPIUserData(streamCtx, conn)

		stopPing()
		stopClose()
		cancel()
		_ = conn.Close()
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("现货 WS API 用户数据流中断，准备重连: %v", err)
		}

		if !sleepWithContext(ctx, retryDelay) {
			return
		}
		retryDelay = nextStreamBackoff(retryDelay, maxRetryDelay)
	}
}

// subscribeSpotUserDataWSAPI 在已建立的 WS API 连接上发起 userDataStream.subscribe.signature 订阅。
// 该方法对 HMAC/RSA/Ed25519 任意密钥类型均可用(2026-02-20 起官方放开)，签名口径与 SIGNED 请求一致：
// 取除 signature 外的全部参数按字母序拼成 query，对其做 HMAC-SHA256 得到十六进制签名。
func (e *FundingRateSniperEngine) subscribeSpotUserDataWSAPI(conn *websocket.Conn) (string, error) {
	apiKey := strings.TrimSpace(e.client.APIKey())
	secret := strings.TrimSpace(e.client.SecretKey())
	if apiKey == "" || secret == "" {
		return "", fmt.Errorf("缺少 API Key/Secret，无法订阅 WS API 用户数据流")
	}

	timestamp := time.Now().UnixMilli()
	// 参数按字母序排列：apiKey、timestamp。
	payload := "apiKey=" + apiKey + "&timestamp=" + strconv.FormatInt(timestamp, 10)
	signature := exchange.Sign(secret, payload)

	requestID := "uds-spot-" + strconv.FormatInt(timestamp, 10)
	request := map[string]any{
		"id":     requestID,
		"method": "userDataStream.subscribe.signature",
		"params": map[string]any{
			"apiKey":    apiKey,
			"timestamp": timestamp,
			"signature": signature,
		},
	}
	if err := conn.WriteJSON(request); err != nil {
		return "", fmt.Errorf("发送 userDataStream.subscribe.signature 失败: %w", err)
	}
	return requestID, nil
}

// consumeSpotWSAPIUserData 订阅后在同一连接上消费现货用户数据事件。
// WS API 的事件以 {"subscriptionId":N,"event":{...}} 文本帧推送，其中内层 event 与旧 listenKey 流格式完全一致，
// 因此解析逻辑复用 processSpotStyleUserDataEvent。请求应答帧形如 {"id":..,"status":200,"result":{...}}。
func (e *FundingRateSniperEngine) consumeSpotWSAPIUserData(ctx context.Context, conn *websocket.Conn) error {
	requestID, err := e.subscribeSpotUserDataWSAPI(conn)
	if err != nil {
		return err
	}

	subscribed := false
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
		// 连接级"距上一帧"打点：现货用户流读到任意一帧即视为存活（HoldMonitor 失明判定的真实连通性信号）。
		e.noteFeedFrame(feedSpotUser)

		var frame struct {
			ID     *string `json:"id"`
			Status *int    `json:"status"`
			Error  *struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			} `json:"error"`
			Event json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			// 未知/无法解析的帧不致命，跳过即可。
			continue
		}

		// 请求应答帧(订阅 ack / 错误)。
		if frame.Status != nil {
			if *frame.Status != http.StatusOK {
				if frame.Error != nil {
					return fmt.Errorf("WS API 订阅被拒绝: status=%d code=%d msg=%s", *frame.Status, frame.Error.Code, frame.Error.Msg)
				}
				return fmt.Errorf("WS API 订阅被拒绝: status=%d body=%s", *frame.Status, string(payload))
			}
			if frame.ID != nil && *frame.ID == requestID && !subscribed {
				subscribed = true
				e.logger.Printf("现货用户数据流(WS API)订阅成功")
			}
			continue
		}

		// 事件帧。
		if len(frame.Event) > 0 {
			reconnect, perr := e.processSpotStyleUserDataEvent(frame.Event, "spot", e.spotOrderHub, e.noteSpotAccountPositionUpdate, "解析现货用户流事件失败")
			if perr != nil {
				// 单帧解析失败不致命：记录后继续读后续事件，避免一条坏帧拖垮整条流。
				e.logger.Printf("现货 WS API 用户数据事件解析失败(已跳过): %v", perr)
				continue
			}
			if reconnect {
				return fmt.Errorf("收到 serverShutdown 事件，按要求重连")
			}
		}
	}
}

func (e *FundingRateSniperEngine) runListenKeyStream(
	ctx context.Context,
	start func(context.Context) (string, error),
	keepalive func(context.Context, string) error,
	wsBaseURL string,
	consume func(context.Context, *websocket.Conn) error,
) {
	retryDelay := 500 * time.Millisecond
	maxRetryDelay := 10 * time.Second

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
			retryDelay = nextStreamBackoff(retryDelay, maxRetryDelay)
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
			retryDelay = nextStreamBackoff(retryDelay, maxRetryDelay)
			continue
		}
		retryDelay = 500 * time.Millisecond
		stopClose := closeWebSocketOnContextDone(streamCtx, conn)

		stopPing := startWebSocketPing(streamCtx, conn)
		err = consume(streamCtx, conn)
		stopPing()
		stopClose()
		cancel()
		_ = conn.Close()
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("用户数据流中断，准备重连: %v", err)
		}

		if !sleepWithContext(ctx, retryDelay) {
			return
		}
		retryDelay = nextStreamBackoff(retryDelay, maxRetryDelay)
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

func (e *FundingRateSniperEngine) startFuturesListenKey(ctx context.Context) (string, error) {
	body, err := e.apiKeyRequest(ctx, http.MethodPost, e.client.FuturesBaseURL()+"/fapi/v1/listenKey", nil)
	if err != nil {
		return "", err
	}
	return parseListenKey(body)
}

func (e *FundingRateSniperEngine) keepaliveFuturesListenKey(ctx context.Context, listenKey string) error {
	_, err := e.apiKeyRequest(ctx, http.MethodPut, e.client.FuturesBaseURL()+"/fapi/v1/listenKey?listenKey="+listenKey, nil)
	return err
}

func (e *FundingRateSniperEngine) apiKeyRequest(ctx context.Context, method string, endpoint string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("创建 API KEY 请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", e.client.APIKey())

	resp, err := e.client.HTTPClient().Do(req)
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

// classifyFuturesForcedClosure 依据币安合约 ORDER_TRADE_UPDATE 的 clientOrderId 约定识别「交易所强制平仓」事件：
//   - "adl_autoclose..." : 自动减仓(ADL)
//   - "autoclose-..."    : 强平(liquidation)
//
// 这类订单由交易所而非本系统发起，clientOrderId 不在本系统托管命名空间内，因此必须在「跳过非托管订单」之前
// 识别，否则单腿被强平/ADL 后另一腿会一直裸露而系统毫不知情。
func classifyFuturesForcedClosure(clientOrderID string) (bool, string) {
	id := strings.ToLower(strings.TrimSpace(clientOrderID))
	switch {
	case strings.HasPrefix(id, "adl_autoclose"):
		return true, "adl"
	case strings.HasPrefix(id, "autoclose-"):
		return true, "liquidation"
	default:
		return false, ""
	}
}

func (e *FundingRateSniperEngine) consumeSpotUserDataMessages(ctx context.Context, conn *websocket.Conn) error {
	return e.consumeSpotStyleUserDataMessages(ctx, conn, "spot", e.spotOrderHub, e.noteSpotAccountPositionUpdate, "解析现货用户流消息失败")
}

// consumeMarginUserDataMessages 消费全仓杠杆(cross margin)用户数据流。
// cross margin 的 executionReport 与现货格式一致，但只会出现在 /sapi/v1/userDataStream 这条独立 listenKey 上，
// 不会出现在现货 listenKey 中——这正是负 carry 现货腿(走 cross margin)此前必须依赖 REST 轮询确认的根因。
// 这里把 margin 订单回报正确投递到 marginOrderHub，使 waitForDirectionalSpotOrder(cross margin) 能命中 WS 推送。
// 注意：不把 margin 余额并入 spot 余额映射，避免污染现货残腿判定；margin 敞口仍由 REST 对账负责。
func (e *FundingRateSniperEngine) consumeMarginUserDataMessages(ctx context.Context, conn *websocket.Conn) error {
	return e.consumeSpotStyleUserDataMessages(ctx, conn, "margin", e.marginOrderHub, nil, "解析全仓杠杆用户流消息失败")
}

// consumeSpotStyleUserDataMessages 共享现货 / 全仓杠杆这两条同构用户数据流的解析逻辑。
// 每个 WS 文本帧本身即一条内层 event(旧 listenKey 流格式)。WS API 流的帧外层带 {"event":{...}} 信封，
// 解包后复用同一个 processSpotStyleUserDataEvent。
func (e *FundingRateSniperEngine) consumeSpotStyleUserDataMessages(
	ctx context.Context,
	conn *websocket.Conn,
	market string,
	hub *OrderEventHub,
	onBalances func(map[string]float64),
	parseErrLabel string,
) error {
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

		reconnect, perr := e.processSpotStyleUserDataEvent(payload, market, hub, onBalances, parseErrLabel)
		if perr != nil {
			return perr
		}
		if reconnect {
			return fmt.Errorf("收到 serverShutdown 事件，按要求重连")
		}
	}
}

// processSpotStyleUserDataEvent 解析并分发单条现货/杠杆用户数据内层事件(executionReport / outboundAccountPosition)。
// 返回 reconnect=true 表示收到 serverShutdown，调用方应主动重连;返回 err 表示该帧解析失败。
func (e *FundingRateSniperEngine) processSpotStyleUserDataEvent(
	payload []byte,
	market string,
	hub *OrderEventHub,
	onBalances func(map[string]float64),
	parseErrLabel string,
) (bool, error) {
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
		Balances        []struct {
			Asset string `json:"a"`
			Free  string `json:"f"`
			Lock  string `json:"l"`
		} `json:"B"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return false, fmt.Errorf("%s: %w", parseErrLabel, err)
	}
	if event.EventType == "serverShutdown" {
		return true, nil
	}
	if event.EventType != "executionReport" {
		if event.EventType == "outboundAccountPosition" && onBalances != nil {
			balances := make(map[string]float64, len(event.Balances))
			for _, balance := range event.Balances {
				balances[balance.Asset] = parseFloat(balance.Free) + parseFloat(balance.Lock)
			}
			onBalances(balances)
		}
		return false, nil
	}
	if !isManagedClientOrderID(event.ClientOrderID) {
		return false, nil
	}
	if hub == nil {
		return false, nil
	}

	snapshot := OrderExecutionSnapshot{
		Market:          market,
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
	hub.Update(snapshot)
	return false, nil
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
		// 连接级"距上一帧"打点：合约用户流读到任意一帧即视为存活（ACCOUNT_UPDATE 事件驱动，
		// 不能用"距上次账户更新"判活，连接级帧才是真实连通性信号）。
		e.noteFeedFrame(feedFuturesUser)

		var envelope struct {
			EventType string `json:"e"`
			EventTime int64  `json:"E"`
			Account   struct {
				Balances []struct {
					Asset         string `json:"a"`
					WalletBalance string `json:"wb"`
				} `json:"B"`
				Positions []struct {
					Symbol           string `json:"s"`
					PositionAmt      string `json:"pa"`
					EntryPrice       string `json:"ep"`
					UnrealizedProfit string `json:"up"`
					MarkPrice        string `json:"mp"`
				} `json:"P"`
			} `json:"a"`
			Order struct {
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
			if envelope.EventType == "ACCOUNT_UPDATE" {
				walletUSDT := 0.0
				for _, balance := range envelope.Account.Balances {
					if strings.EqualFold(balance.Asset, "USDT") {
						walletUSDT = parseFloat(balance.WalletBalance)
						break
					}
				}
				positions := make([]futuresRealtimePosition, 0, len(envelope.Account.Positions))
				for _, position := range envelope.Account.Positions {
					positions = append(positions, futuresRealtimePosition{
						Symbol:           position.Symbol,
						PositionAmt:      parseFloat(position.PositionAmt),
						EntryPrice:       parseFloat(position.EntryPrice),
						UnrealizedProfit: parseFloat(position.UnrealizedProfit),
						MarkPrice:        parseFloat(position.MarkPrice),
					})
				}
				e.noteFuturesAccountUpdate(walletUSDT, positions)
			}
			continue
		}
		// 交易所强平/ADL 检测必须先于「跳过非托管订单」：强平/ADL 单由交易所发起、clientOrderId 不在托管命名空间，
		// 若不在此识别，单腿被强制平掉后另一腿会持续裸露而系统无感知(R3 单腿强平/ADL 盲区)。
		if forced, kind := classifyFuturesForcedClosure(envelope.Order.ClientOrderID); forced {
			e.handleFuturesForcedClosure(envelope.Order.Symbol, kind, envelope.Order.ClientOrderID)
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
