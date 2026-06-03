package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"

	"github.com/gorilla/websocket"
)

func TestIsManagedClientOrderIDRecognizesEnginePrefixes(t *testing.T) {
	cases := []string{
		"fopen-btcusdt-1",
		"fclose-btcusdt-1",
		"sopen-btcusdt-1",
		"sclosemaker-btcusdt-1",
		"sclosechase-btcusdt-1",
		"sclose_revert-btcusdt-1",
		"srebalance-btcusdt-1",
		"srebalf-btcusdt-1",
		"exp_sell-btcusdt-1",
		"exp_buy-btcusdt-1",
		"expfclose-btcusdt-1",
	}
	for _, clientOrderID := range cases {
		if !isManagedClientOrderID(clientOrderID) {
			t.Fatalf("expected managed client order id to be tracked: %s", clientOrderID)
		}
	}
}

func TestIsManagedClientOrderIDRejectsExternalOrderIDs(t *testing.T) {
	cases := []string{
		"",
		"manual-order-1",
		"grid-bot-ethusdt-1",
		"thirdparty-abc",
	}
	for _, clientOrderID := range cases {
		if isManagedClientOrderID(clientOrderID) {
			t.Fatalf("did not expect external client order id to be tracked: %s", clientOrderID)
		}
	}
}

// TestConsumeMarginUserDataRoutesToMarginHub 钉死 C4：cross margin 的 executionReport 必须落到 marginOrderHub，
// 而不能进入 spotOrderHub，否则负 carry 现货腿(走全仓杠杆)的 WS 确认会全部丢失、退化为 REST 轮询。
func TestConsumeMarginUserDataRoutesToMarginHub(t *testing.T) {
	const clientOrderID = "sopen-btcusdt-1"
	msg := `{"e":"executionReport","E":1700000000000,"s":"BTCUSDT","c":"` + clientOrderID + `","X":"FILLED","x":"TRADE","i":123,"z":"0.5","Z":"15000","n":"0.01","N":"USDT"}`

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(msg))
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	engine := &FundingRateSniperEngine{
		logger:         log.Default(),
		spotOrderHub:   NewOrderEventHub(),
		marginOrderHub: NewOrderEventHub(),
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号 websocket 失败: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = engine.consumeMarginUserDataMessages(ctx, conn)

	snapshot, ok := engine.marginOrderHub.Get(clientOrderID)
	if !ok {
		t.Fatal("cross margin 订单回报必须被路由到 marginOrderHub")
	}
	if snapshot.Market != "margin" {
		t.Fatalf("期望 snapshot.Market=margin, 实际=%q", snapshot.Market)
	}
	if !hasConfirmedOrderExecution(snapshot) {
		t.Fatalf("期望已确认成交的快照, 实际=%+v", snapshot)
	}
	if _, leaked := engine.spotOrderHub.Get(clientOrderID); leaked {
		t.Fatal("cross margin 订单不应泄漏进 spotOrderHub")
	}
}

// TestConsumeSpotWSAPIUserDataSubscribesAndRoutesEvents 钉死现货用户数据流从 listenKey(已下线)
// 迁移到 WebSocket API userDataStream.subscribe.signature 后的完整链路：
// 1) 发出的订阅请求 method 正确、签名口径正确(对 apiKey&timestamp 按字母序做 HMAC-SHA256);
// 2) 收到 200 ack 后能解包 {"subscriptionId":N,"event":{...}} 信封，并把 executionReport 路由进 spotOrderHub。
func TestConsumeSpotWSAPIUserDataSubscribesAndRoutesEvents(t *testing.T) {
	const (
		apiKey        = "test-api-key"
		secretKey     = "test-secret-key"
		clientOrderID = "sopen-btcusdt-9"
	)

	type subscribeRequest struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			APIKey    string `json:"apiKey"`
			Timestamp int64  `json:"timestamp"`
			Signature string `json:"signature"`
		} `json:"params"`
	}

	gotReq := make(chan subscribeRequest, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req subscribeRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return
		}
		gotReq <- req

		ack := `{"id":"` + req.ID + `","status":200,"result":{"subscriptionId":0}}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(ack))

		event := `{"subscriptionId":0,"event":{"e":"executionReport","E":1700000000000,"s":"BTCUSDT","c":"` + clientOrderID + `","X":"FILLED","x":"TRADE","i":777,"z":"0.5","Z":"15000","n":"0.01","N":"USDT"}}`
		_ = conn.WriteMessage(websocket.TextMessage, []byte(event))
		time.Sleep(150 * time.Millisecond)
	}))
	defer srv.Close()

	engine := &FundingRateSniperEngine{
		logger: log.Default(),
		client: exchange.NewClient(exchange.ClientConfig{
			APIKey:    apiKey,
			SecretKey: secretKey,
		}),
		spotOrderHub: NewOrderEventHub(),
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("拨号 websocket 失败: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = engine.consumeSpotWSAPIUserData(ctx, conn)

	select {
	case req := <-gotReq:
		if req.Method != "userDataStream.subscribe.signature" {
			t.Fatalf("订阅 method 错误: %q", req.Method)
		}
		if req.Params.APIKey != apiKey {
			t.Fatalf("订阅 apiKey 错误: %q", req.Params.APIKey)
		}
		wantSig := exchange.Sign(secretKey, "apiKey="+apiKey+"&timestamp="+strconv.FormatInt(req.Params.Timestamp, 10))
		if req.Params.Signature != wantSig {
			t.Fatalf("订阅签名不匹配\n want=%s\n  got=%s", wantSig, req.Params.Signature)
		}
	default:
		t.Fatal("未收到订阅请求")
	}

	snapshot, ok := engine.spotOrderHub.Get(clientOrderID)
	if !ok {
		t.Fatal("现货 executionReport 必须被路由到 spotOrderHub")
	}
	if snapshot.Market != "spot" || !hasConfirmedOrderExecution(snapshot) {
		t.Fatalf("期望已确认成交的现货快照, 实际=%+v", snapshot)
	}
}
