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

func TestClassifyFuturesForcedClosure(t *testing.T) {
	forced := map[string]string{
		"autoclose-1700000000000":  "liquidation",
		"AUTOCLOSE-BTCUSDT":        "liquidation",
		"adl_autoclose":            "adl",
		"adl_autoclose-1700000000": "adl",
	}
	for clientOrderID, wantKind := range forced {
		ok, kind := classifyFuturesForcedClosure(clientOrderID)
		if !ok || kind != wantKind {
			t.Fatalf("classifyFuturesForcedClosure(%q) = (%v, %q), want (true, %q)", clientOrderID, ok, kind, wantKind)
		}
	}

	// 本系统托管单与第三方单都不得被误判为强平/ADL。
	notForced := []string{
		"",
		"fopen-btcusdt-1",
		"fclose-btcusdt-1",
		"sclose-btcusdt-1",
		"manual-order-1",
		"autocloseable-typo",
	}
	for _, clientOrderID := range notForced {
		if ok, kind := classifyFuturesForcedClosure(clientOrderID); ok {
			t.Fatalf("classifyFuturesForcedClosure(%q) = (true, %q), want false", clientOrderID, kind)
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

// TestProcessFuturesUserDataEventParsesFullBinancePayload 用按真实键序排列的 ORDER_TRADE_UPDATE
// 报文钉死反序列化:报文里 "S"(方向)出现在 "s"(symbol)之后,若结构体不显式声明 "S",
// encoding/json 的大小写不敏感回退会把 Symbol 覆盖成 "BUY"(历史实锤:userTrades?symbol=BUY)。
func TestProcessFuturesUserDataEventParsesFullBinancePayload(t *testing.T) {
	payload := `{"e":"ORDER_TRADE_UPDATE","T":1700000000123,"E":1700000000125,"o":{` +
		`"s":"ZECUSDT","c":"fopen-zecusdt-1","S":"BUY","o":"MARKET","f":"GTC","q":"1.5","p":"0","ap":"38.2",` +
		`"sp":"0","x":"TRADE","X":"FILLED","i":801832024573,"l":"1.5","z":"1.5","L":"38.2","n":"0.01","N":"USDT",` +
		`"T":1700000000123,"t":99,"b":"0","a":"0","m":false,"R":false,"wt":"CONTRACT_PRICE","ot":"MARKET",` +
		`"ps":"BOTH","cp":false,"rp":"0.5","pP":false,"si":0,"ss":0,"V":"NONE","pm":"NONE","gtd":0}}`

	engine := &FundingRateSniperEngine{
		logger:          log.Default(),
		futuresOrderHub: NewOrderEventHub(),
	}
	if err := engine.processFuturesUserDataEvent([]byte(payload)); err != nil {
		t.Fatalf("解析合约用户流事件失败: %v", err)
	}
	snapshot, ok := engine.futuresOrderHub.Get("fopen-zecusdt-1")
	if !ok {
		t.Fatal("托管合约订单回报必须进入 futuresOrderHub")
	}
	if snapshot.Symbol != "ZECUSDT" {
		t.Fatalf("Symbol 被其它键覆盖: 期望 ZECUSDT, 实际 %q", snapshot.Symbol)
	}
	if snapshot.OrderID != 801832024573 {
		t.Fatalf("OrderID 错误: %d", snapshot.OrderID)
	}
	if snapshot.Status != "FILLED" || snapshot.ExecutionType != "TRADE" {
		t.Fatalf("状态字段错误: X=%q x=%q", snapshot.Status, snapshot.ExecutionType)
	}
	if snapshot.ExecutedQty != 1.5 || snapshot.AvgPrice != 38.2 {
		t.Fatalf("成交字段错误: z=%v ap=%v", snapshot.ExecutedQty, snapshot.AvgPrice)
	}
}

// TestProcessSpotUserDataEventParsesFullBinancePayload 用按真实键序排列的 executionReport
// 钉死现货流反序列化:"S"/"C"/"I" 分别出现在 "s"/"c"/"i" 之后。修复前 ClientOrderID 会被
// "C":""清空(所有现货回报被当非托管单丢弃,WS 确认通道整体失效)、OrderID 被 "I" 的垃圾值覆盖。
func TestProcessSpotUserDataEventParsesFullBinancePayload(t *testing.T) {
	payload := `{"e":"executionReport","E":1700000000125,"s":"ZECUSDT","c":"sopen-zecusdt-1","S":"BUY",` +
		`"o":"MARKET","f":"GTC","q":"1.5","p":"0","P":"0","F":"0","g":-1,"C":"","x":"TRADE","X":"FILLED",` +
		`"r":"NONE","i":4293153,"l":"1.5","z":"1.5","L":"38.2","n":"0.001","N":"ZEC","T":1700000000123,` +
		`"t":77,"v":3,"I":8641984,"w":false,"m":false,"M":true,"O":1700000000000,"Z":"57.3","Y":"57.3",` +
		`"Q":"0","W":1700000000000,"V":"EXPIRE_MAKER"}`

	engine := &FundingRateSniperEngine{logger: log.Default()}
	hub := NewOrderEventHub()
	if _, err := engine.processSpotStyleUserDataEvent([]byte(payload), "spot", hub, nil, "解析现货用户流消息失败"); err != nil {
		t.Fatalf("解析现货用户流事件失败: %v", err)
	}
	snapshot, ok := hub.Get("sopen-zecusdt-1")
	if !ok {
		t.Fatal("托管现货订单回报必须进入 hub(修复前被 C 键清空 ClientOrderID 后整体丢弃)")
	}
	if snapshot.Symbol != "ZECUSDT" {
		t.Fatalf("Symbol 被其它键覆盖: 期望 ZECUSDT, 实际 %q", snapshot.Symbol)
	}
	if snapshot.OrderID != 4293153 {
		t.Fatalf("OrderID 被 I(ignore) 覆盖: 期望 4293153, 实际 %d", snapshot.OrderID)
	}
	if snapshot.ExecutedQty != 1.5 || snapshot.CumQuoteQty != 57.3 {
		t.Fatalf("成交字段错误: z=%v Z=%v", snapshot.ExecutedQty, snapshot.CumQuoteQty)
	}
}

// TestProcessSpotUserDataEventAttributesCancelToOriginalOrder 钉死撤单回报归属:
// 撤单类 executionReport 的 "c" 是撤单请求 ID、"C" 才是原订单 ID,事件应按原订单 ID 进 hub。
func TestProcessSpotUserDataEventAttributesCancelToOriginalOrder(t *testing.T) {
	payload := `{"e":"executionReport","E":1700000000125,"s":"ZECUSDT","c":"external-cancel-1","S":"BUY",` +
		`"o":"LIMIT","f":"GTC","q":"1.5","p":"38.0","P":"0","F":"0","g":-1,"C":"sclosemaker-zecusdt-1",` +
		`"x":"CANCELED","X":"CANCELED","r":"NONE","i":4293153,"l":"0","z":"0.5","L":"0","n":"0","N":null,` +
		`"T":1700000000123,"t":-1,"v":3,"I":8641984,"w":false,"m":false,"M":false,"O":1700000000000,` +
		`"Z":"19.0","Y":"0","Q":"0"}`

	engine := &FundingRateSniperEngine{logger: log.Default()}
	hub := NewOrderEventHub()
	if _, err := engine.processSpotStyleUserDataEvent([]byte(payload), "spot", hub, nil, "解析现货用户流消息失败"); err != nil {
		t.Fatalf("解析现货撤单事件失败: %v", err)
	}
	snapshot, ok := hub.Get("sclosemaker-zecusdt-1")
	if !ok {
		t.Fatal("撤单回报必须按原订单 clientOrderID 归属进 hub")
	}
	if snapshot.Status != "CANCELED" {
		t.Fatalf("期望 CANCELED 终态, 实际 %q", snapshot.Status)
	}
	if _, leaked := hub.Get("external-cancel-1"); leaked {
		t.Fatal("撤单请求 ID 不应单独占据 hub 条目")
	}
}
