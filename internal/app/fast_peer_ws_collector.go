package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ===== cross-venue lead-lag peer 采集(OKX / Binance / Bybit 公共 WS)=====
// record-only:只订阅公共 trades + 最优买卖档,不接私有频道、不下单、不读账户、不影响交易主流程。
// 目的:验证 Binance/Bybit 是否领先 OKX、能否提供 >=5bps 可交易预测位移。
// 关键:只采「三所都上市」的重叠 symbol(否则 Binance/Bybit 无该合约=收不到数据),
//       并把 OKX 也作为一个 venue 收进同一文件,便于 review 在单文件内跨 venue 对齐。
// 失败只告警 + 断线重连。

const fastPeerFile = "crossvenue/fast_peer_leadlag_events.jsonl"
const binanceFStreamBase = "wss://fstream.binance.com/stream?streams="
const bybitLinearWS = "wss://stream.bybit.com/v5/public/linear"

// 主流/流动性种子(OKX 规范 USDT 永续形);实际订阅前会与三所合约清单取交集。
var peerSeedMajors = []string{
	"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "DOGE-USDT", "BNB-USDT", "ADA-USDT",
	"AVAX-USDT", "LINK-USDT", "LTC-USDT", "BCH-USDT", "DOT-USDT", "TRX-USDT", "NEAR-USDT",
	"APT-USDT", "SUI-USDT", "OP-USDT", "ARB-USDT", "FIL-USDT", "ATOM-USDT", "INJ-USDT",
	"TIA-USDT", "SEI-USDT", "WLD-USDT", "PEPE-USDT", "WIF-USDT", "ORDI-USDT", "RUNE-USDT",
	"AAVE-USDT", "UNI-USDT", "ETC-USDT", "XLM-USDT", "ICP-USDT", "HBAR-USDT",
}

// fastPeerEvent peer 行情事件(record-only)。symbol 用 OKX 规范形(如 AI-USDT)便于跨所对齐。
type fastPeerEvent struct {
	RecordedAt      time.Time `json:"recorded_at"`
	Venue           string    `json:"venue"`
	Symbol          string    `json:"symbol"`
	Bid             float64   `json:"bid"`
	Ask             float64   `json:"ask"`
	Mid             float64   `json:"mid"`
	SpreadBps       float64   `json:"spread_bps"`
	TradeBuyQty1s   float64   `json:"trade_buy_qty_1s"`
	TradeBuyQty3s   float64   `json:"trade_buy_qty_3s"`
	TradeBuyQty5s   float64   `json:"trade_buy_qty_5s"`
	TradeSellQty1s  float64   `json:"trade_sell_qty_1s"`
	TradeSellQty3s  float64   `json:"trade_sell_qty_3s"`
	TradeSellQty5s  float64   `json:"trade_sell_qty_5s"`
	TradeCount1s    int       `json:"trade_count_1s"`
	TradeCount3s    int       `json:"trade_count_3s"`
	TradeCount5s    int       `json:"trade_count_5s"`
	MidRet1s        float64   `json:"mid_ret_1s"`
	MidRet3s        float64   `json:"mid_ret_3s"`
	MidRet5s        float64   `json:"mid_ret_5s"`
	MidRet10s       float64   `json:"mid_ret_10s"`
	BookImbalance   float64   `json:"book_imbalance"`
	TopBidQty       float64   `json:"top_bid_qty"`
	TopAskQty       float64   `json:"top_ask_qty"`
	SourceLatencyMs float64   `json:"source_latency_ms,omitempty"`
	LocalRecvTime   time.Time `json:"local_receive_time"`
}

type fastPeerState struct {
	mu       sync.Mutex
	trades   map[string][]wsTradePt // key venue|canon
	books    map[string][]wsBookPt
	lastBook map[string]wsBookPt
	latency  map[string]float64
}

func newPeerState() *fastPeerState {
	return &fastPeerState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{}, lastBook: map[string]wsBookPt{}, latency: map[string]float64{}}
}

func (e *OKXDirectionalEngine) fastPeerEnabled() bool {
	return e.cfg.FastPeerWSEnabled && strings.EqualFold(e.venueName, "okx")
}

// canon(OKX 规范)→ peerRaw(无横杠大写,如 AI-USDT→AIUSDT)。
func peerRaw(canon string) string {
	return strings.ToUpper(strings.ReplaceAll(canon, "-", ""))
}

var peerHTTP = &http.Client{Timeout: 10 * time.Second}

func peerGetJSON(url string, out interface{}) error {
	resp, err := peerHTTP.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func okxSwapUSDTSet() (map[string]bool, error) {
	var r struct {
		Data []struct {
			InstID    string `json:"instId"`
			SettleCcy string `json:"settleCcy"`
			State     string `json:"state"`
		} `json:"data"`
	}
	if err := peerGetJSON("https://www.okx.com/api/v5/public/instruments?instType=SWAP", &r); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, d := range r.Data {
		if d.SettleCcy == "USDT" && strings.HasSuffix(d.InstID, "-USDT-SWAP") && (d.State == "" || d.State == "live") {
			set[strings.TrimSuffix(d.InstID, "-SWAP")] = true // canon BTC-USDT
		}
	}
	return set, nil
}

func binanceFuturesUSDTSet() (map[string]bool, error) {
	var r struct {
		Symbols []struct {
			Symbol       string `json:"symbol"`
			Status       string `json:"status"`
			ContractType string `json:"contractType"`
			QuoteAsset   string `json:"quoteAsset"`
		} `json:"symbols"`
	}
	if err := peerGetJSON("https://fapi.binance.com/fapi/v1/exchangeInfo", &r); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, s := range r.Symbols {
		if s.Status == "TRADING" && s.ContractType == "PERPETUAL" && s.QuoteAsset == "USDT" {
			set[strings.ToUpper(s.Symbol)] = true // raw BTCUSDT
		}
	}
	return set, nil
}

func bybitLinearUSDTSet() (map[string]bool, error) {
	set := map[string]bool{}
	cursor := ""
	for page := 0; page < 6; page++ {
		u := "https://api.bybit.com/v5/market/instruments-info?category=linear&limit=1000"
		if cursor != "" {
			u += "&cursor=" + cursor
		}
		var r struct {
			Result struct {
				List []struct {
					Symbol    string `json:"symbol"`
					QuoteCoin string `json:"quoteCoin"`
					Status    string `json:"status"`
				} `json:"list"`
				NextPageCursor string `json:"nextPageCursor"`
			} `json:"result"`
		}
		if err := peerGetJSON(u, &r); err != nil {
			if len(set) > 0 {
				return set, nil
			}
			return nil, err
		}
		for _, s := range r.Result.List {
			if s.QuoteCoin == "USDT" && (s.Status == "" || s.Status == "Trading") {
				set[strings.ToUpper(s.Symbol)] = true
			}
		}
		if r.Result.NextPageCursor == "" || r.Result.NextPageCursor == cursor {
			break
		}
		cursor = r.Result.NextPageCursor
	}
	return set, nil
}

// peerUniverse 三所交集(种子=主流 + OKX shortlist),上限 maxN。
func (e *OKXDirectionalEngine) peerUniverse() ([]string, error) {
	okxSet, err := okxSwapUSDTSet()
	if err != nil {
		return nil, fmt.Errorf("okx instruments: %w", err)
	}
	binSet, err := binanceFuturesUSDTSet()
	if err != nil {
		return nil, fmt.Errorf("binance exchangeInfo: %w", err)
	}
	bybitSet, err := bybitLinearUSDTSet()
	if err != nil {
		return nil, fmt.Errorf("bybit instruments: %w", err)
	}
	maxN := e.cfg.FastWSMaxSymbols
	if maxN <= 0 {
		maxN = 15
	}
	seed := append([]string{}, peerSeedMajors...)
	seed = append(seed, e.fastWSSymbols()...) // 叠加当前 OKX shortlist(若也在三所)
	seen := map[string]bool{}
	out := make([]string, 0, maxN)
	for _, c := range seed {
		if seen[c] {
			continue
		}
		seen[c] = true
		if okxSet[c] && binSet[peerRaw(c)] && bybitSet[peerRaw(c)] {
			out = append(out, c)
			if len(out) >= maxN {
				break
			}
		}
	}
	return out, nil
}

func (e *OKXDirectionalEngine) fastPeerCollectorLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_peer] collector panic 已恢复: %v", r)
		}
	}()
	venues := map[string]bool{}
	for _, v := range strings.Split(e.cfg.FastPeerVenues, ",") {
		if x := strings.ToLower(strings.TrimSpace(v)); x != "" {
			venues[x] = true
		}
	}
	// 计算三所交集 universe(带重试,失败只告警)
	var universe []string
	for {
		u, err := e.peerUniverse()
		if err == nil && len(u) > 0 {
			universe = u
			break
		}
		if err != nil {
			e.logger.Printf("[fast_peer] 计算交集 universe 失败,30s 重试: %v", err)
		} else {
			e.logger.Printf("[fast_peer] 三所交集为空,30s 重试")
		}
		if !sleepCtx(ctx, 30*time.Second) {
			return
		}
	}
	e.logger.Printf("[fast_peer] cross-venue lead-lag 采集启动(record-only)venues=%v universe(%d)=%v", venues, len(universe), universe)
	st := newPeerState()
	// OKX 也作为一个 venue 收进同一文件(便于单文件内跨 venue 对齐)
	go e.peerVenueLoop(ctx, "okx", universe, st)
	if venues["binance"] {
		go e.peerVenueLoop(ctx, "binance", universe, st)
	}
	if venues["bybit"] {
		go e.peerVenueLoop(ctx, "bybit", universe, st)
	}
	e.peerEmitLoop(ctx, st)
}

func (e *OKXDirectionalEngine) peerVenueLoop(ctx context.Context, venue string, universe []string, st *fastPeerState) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var err error
		switch venue {
		case "okx":
			err = e.runOKXPeer(ctx, universe, st)
		case "binance":
			err = e.runBinancePeer(ctx, universe, st)
		case "bybit":
			err = e.runBybitPeer(ctx, universe, st)
		}
		if err != nil && ctx.Err() == nil {
			e.logger.Printf("[fast_peer][%s] 中断,3s 重连: %v", venue, err)
		}
		if !sleepCtx(ctx, 3*time.Second) {
			return
		}
	}
}

func (e *OKXDirectionalEngine) peerStore(st *fastPeerState, venue, canon string, tr *wsTradePt, bk *wsBookPt, lat float64) {
	key := venue + "|" + canon
	now := e.nowFn().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	if tr != nil {
		st.trades[key] = pruneTrades(append(st.trades[key], *tr), now.Add(-6*time.Second))
	}
	if bk != nil {
		st.books[key] = pruneBooks(append(st.books[key], *bk), now.Add(-12*time.Second))
		st.lastBook[key] = *bk
	}
	if lat > 0 {
		st.latency[key] = lat
	}
}

// ---------- OKX ----------
func (e *OKXDirectionalEngine) runOKXPeer(ctx context.Context, universe []string, st *fastPeerState) error {
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(okxPublicWSURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	args := make([]map[string]string, 0, len(universe)*2)
	for _, s := range universe {
		inst := s + "-SWAP"
		args = append(args, map[string]string{"channel": "trades", "instId": inst})
		args = append(args, map[string]string{"channel": "books5", "instId": inst})
	}
	if err := conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args}); err != nil {
		return err
	}
	e.logger.Printf("[fast_peer][okx] 已连 args=%d", len(args))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		tk := time.NewTicker(20 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-tk.C:
				_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			}
		}
	}()
	for {
		select {
		case <-cctx.Done():
			return cctx.Err()
		default:
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		if len(payload) == 4 && string(payload) == "pong" {
			continue
		}
		var msg struct {
			Arg struct {
				Channel string `json:"channel"`
				InstID  string `json:"instId"`
			} `json:"arg"`
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &msg) != nil || msg.Arg.Channel == "" || len(msg.Data) == 0 {
			continue
		}
		canon := strings.TrimSuffix(msg.Arg.InstID, "-SWAP")
		switch msg.Arg.Channel {
		case "trades":
			for _, d := range msg.Data {
				var t struct{ Px, Sz, Side string }
				if json.Unmarshal(d, &t) != nil {
					continue
				}
				px, _ := strconv.ParseFloat(t.Px, 64)
				sz, _ := strconv.ParseFloat(t.Sz, 64)
				if sz <= 0 {
					continue
				}
				e.peerStore(st, "okx", canon, &wsTradePt{ts: e.nowFn().UTC(), buy: strings.EqualFold(t.Side, "buy"), qty: sz, px: px}, nil, 0)
			}
		case "books5":
			var b struct {
				Asks [][]string `json:"asks"`
				Bids [][]string `json:"bids"`
			}
			if json.Unmarshal(msg.Data[len(msg.Data)-1], &b) != nil || len(b.Asks) == 0 || len(b.Bids) == 0 {
				continue
			}
			bidPx, _ := strconv.ParseFloat(b.Bids[0][0], 64)
			bidQty, _ := strconv.ParseFloat(b.Bids[0][1], 64)
			askPx, _ := strconv.ParseFloat(b.Asks[0][0], 64)
			askQty, _ := strconv.ParseFloat(b.Asks[0][1], 64)
			if bidPx <= 0 || askPx <= 0 {
				continue
			}
			e.peerStore(st, "okx", canon, nil, &wsBookPt{ts: e.nowFn().UTC(), bidPx: bidPx, bidQty: bidQty, askPx: askPx, askQty: askQty, mid: (bidPx + askPx) / 2}, 0)
		}
	}
}

// ---------- Binance ----------
func (e *OKXDirectionalEngine) runBinancePeer(ctx context.Context, universe []string, st *fastPeerState) error {
	raw2canon := map[string]string{}
	streams := make([]string, 0, len(universe)*2)
	for _, s := range universe {
		r := peerRaw(s)
		raw2canon[r] = s
		lr := strings.ToLower(r)
		streams = append(streams, lr+"@aggTrade", lr+"@bookTicker")
	}
	url := binanceFStreamBase + strings.Join(streams, "/")
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	e.logger.Printf("[fast_peer][binance] 已连 streams=%d", len(streams))
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPingHandler(func(m string) error {
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(m), time.Now().Add(5*time.Second))
	})
	// 注:本机到 Binance 期货 aggTrade 流不可达(i/o timeout,仅 bookTicker 可用),
	// 故 Binance 侧只有 book/mid_ret/book_imbalance,trade flow 由 OKX+Bybit 提供。
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
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		var env struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &env) != nil || env.Stream == "" {
			continue
		}
		nowMs := float64(e.nowFn().UTC().UnixNano()) / 1e6
		if strings.HasSuffix(env.Stream, "@aggTrade") {
			var d struct {
				Ev string  `json:"e"` // 吃掉小写 "e"(事件类型),避免大小写不敏感匹配污染 E
				S  string  `json:"s"`
				P  string  `json:"p"`
				Q  string  `json:"q"`
				M  bool    `json:"m"`
				E  float64 `json:"E"`
			}
			if json.Unmarshal(env.Data, &d) != nil {
				continue
			}
			canon := raw2canon[strings.ToUpper(d.S)]
			if canon == "" {
				continue
			}
			px, _ := strconv.ParseFloat(d.P, 64)
			qty, _ := strconv.ParseFloat(d.Q, 64)
			if qty <= 0 {
				continue
			}
			lat := 0.0
			if d.E > 0 {
				lat = nowMs - d.E
			}
			e.peerStore(st, "binance", canon, &wsTradePt{ts: e.nowFn().UTC(), buy: !d.M, qty: qty, px: px}, nil, lat)
		} else if strings.HasSuffix(env.Stream, "@bookTicker") {
			var d struct {
				Ev string  `json:"e"` // 吃掉小写 "e",避免污染 E
				S  string  `json:"s"`
				B  string  `json:"b"`
				BB string  `json:"B"`
				A  string  `json:"a"`
				AA string  `json:"A"`
				E  float64 `json:"E"`
			}
			if json.Unmarshal(env.Data, &d) != nil {
				continue
			}
			canon := raw2canon[strings.ToUpper(d.S)]
			if canon == "" {
				continue
			}
			bidPx, _ := strconv.ParseFloat(d.B, 64)
			bidQty, _ := strconv.ParseFloat(d.BB, 64)
			askPx, _ := strconv.ParseFloat(d.A, 64)
			askQty, _ := strconv.ParseFloat(d.AA, 64)
			if bidPx <= 0 || askPx <= 0 {
				continue
			}
			lat := 0.0
			if d.E > 0 {
				lat = nowMs - d.E
			}
			e.peerStore(st, "binance", canon, nil, &wsBookPt{ts: e.nowFn().UTC(), bidPx: bidPx, bidQty: bidQty, askPx: askPx, askQty: askQty, mid: (bidPx + askPx) / 2}, lat)
		}
	}
}

// ---------- Bybit ----------
func (e *OKXDirectionalEngine) runBybitPeer(ctx context.Context, universe []string, st *fastPeerState) error {
	raw2canon := map[string]string{}
	args := make([]string, 0, len(universe)*2)
	for _, s := range universe {
		r := peerRaw(s)
		raw2canon[r] = s
		args = append(args, "publicTrade."+r, "tickers."+r)
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(bybitLinearWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	if err := conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args}); err != nil {
		return err
	}
	e.logger.Printf("[fast_peer][bybit] 已连 args=%d", len(args))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		tk := time.NewTicker(20 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-cctx.Done():
				return
			case <-tk.C:
				_ = conn.WriteJSON(map[string]string{"op": "ping"})
			}
		}
	}()
	for {
		select {
		case <-cctx.Done():
			return cctx.Err()
		default:
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
		var msg struct {
			Topic string          `json:"topic"`
			Ts    float64         `json:"ts"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &msg) != nil || msg.Topic == "" {
			continue
		}
		nowMs := float64(e.nowFn().UTC().UnixNano()) / 1e6
		lat := 0.0
		if msg.Ts > 0 {
			lat = nowMs - msg.Ts
		}
		if strings.HasPrefix(msg.Topic, "publicTrade.") {
			canon := raw2canon[strings.ToUpper(strings.TrimPrefix(msg.Topic, "publicTrade."))]
			if canon == "" {
				continue
			}
			var arr []struct{ S, V, P string }
			if json.Unmarshal(msg.Data, &arr) != nil {
				continue
			}
			for _, t := range arr {
				qty, _ := strconv.ParseFloat(t.V, 64)
				px, _ := strconv.ParseFloat(t.P, 64)
				if qty <= 0 {
					continue
				}
				e.peerStore(st, "bybit", canon, &wsTradePt{ts: e.nowFn().UTC(), buy: strings.EqualFold(t.S, "Buy"), qty: qty, px: px}, nil, lat)
			}
		} else if strings.HasPrefix(msg.Topic, "tickers.") {
			canon := raw2canon[strings.ToUpper(strings.TrimPrefix(msg.Topic, "tickers."))]
			if canon == "" {
				continue
			}
			var d struct {
				Bid1Price string `json:"bid1Price"`
				Bid1Size  string `json:"bid1Size"`
				Ask1Price string `json:"ask1Price"`
				Ask1Size  string `json:"ask1Size"`
			}
			if json.Unmarshal(msg.Data, &d) != nil {
				continue
			}
			key := "bybit|" + canon
			st.mu.Lock()
			last := st.lastBook[key]
			st.mu.Unlock()
			bp := parseOr(d.Bid1Price, last.bidPx)
			bq := parseOr(d.Bid1Size, last.bidQty)
			ap := parseOr(d.Ask1Price, last.askPx)
			aq := parseOr(d.Ask1Size, last.askQty)
			if bp <= 0 || ap <= 0 {
				continue
			}
			e.peerStore(st, "bybit", canon, nil, &wsBookPt{ts: e.nowFn().UTC(), bidPx: bp, bidQty: bq, askPx: ap, askQty: aq, mid: (bp + ap) / 2}, lat)
		}
	}
}

func parseOr(s string, def float64) float64 {
	if strings.TrimSpace(s) == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

func (e *OKXDirectionalEngine) peerEmitLoop(ctx context.Context, st *fastPeerState) {
	iv := e.cfg.FastWSEmitIntervalMs
	if iv <= 0 {
		iv = 1000
	}
	tk := time.NewTicker(time.Duration(iv) * time.Millisecond)
	defer tk.Stop()
	path := filepath.Join(filepath.Dir(e.recordDir), fastPeerFile) // records/crossvenue/...
	e.logger.Printf("[fast_peer] emit loop 启动 path=%s iv=%dms", path, iv)
	dbg, wrote := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		now := e.nowFn().UTC()
		st.mu.Lock()
		keys := make([]string, 0, len(st.books))
		for k := range st.books {
			keys = append(keys, k)
		}
		nTr := len(st.trades)
		st.mu.Unlock()
		if dbg++; dbg%30 == 0 {
			e.logger.Printf("[fast_peer] 心跳 books_keys=%d trade_keys=%d 已写=%d", len(keys), nTr, wrote)
		}
		for _, key := range keys {
			ev := e.peerBuild(st, key, now)
			if ev == nil {
				continue
			}
			if err := appendJSONLRecord(path, *ev); err != nil {
				e.logger.Printf("[fast_peer] 落盘失败: %v", err)
				break
			}
			wrote++
		}
	}
}

func (e *OKXDirectionalEngine) peerBuild(st *fastPeerState, key string, now time.Time) *fastPeerEvent {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return nil
	}
	st.mu.Lock()
	trades := append([]wsTradePt(nil), st.trades[key]...)
	books := append([]wsBookPt(nil), st.books[key]...)
	lat := st.latency[key]
	st.mu.Unlock()
	if len(books) == 0 {
		return nil
	}
	cur := books[len(books)-1]
	if cur.mid <= 0 {
		return nil
	}
	ev := &fastPeerEvent{RecordedAt: now, Venue: parts[0], Symbol: parts[1], Bid: cur.bidPx, Ask: cur.askPx, Mid: cur.mid,
		TopBidQty: cur.bidQty, TopAskQty: cur.askQty, SourceLatencyMs: lat, LocalRecvTime: now}
	ev.SpreadBps = (cur.askPx - cur.bidPx) / cur.mid * 10000
	if cur.bidQty+cur.askQty > 0 {
		ev.BookImbalance = (cur.bidQty - cur.askQty) / (cur.bidQty + cur.askQty)
	}
	sumWin := func(w time.Duration) (b, s float64, c int) {
		cut := now.Add(-w)
		for _, t := range trades {
			if t.ts.Before(cut) {
				continue
			}
			c++
			if t.buy {
				b += t.qty
			} else {
				s += t.qty
			}
		}
		return
	}
	ev.TradeBuyQty1s, ev.TradeSellQty1s, ev.TradeCount1s = sumWin(1 * time.Second)
	ev.TradeBuyQty3s, ev.TradeSellQty3s, ev.TradeCount3s = sumWin(3 * time.Second)
	ev.TradeBuyQty5s, ev.TradeSellQty5s, ev.TradeCount5s = sumWin(5 * time.Second)
	ret := func(w time.Duration) float64 {
		prev, ok := bookAt(books, now.Add(-w))
		if !ok || prev.mid <= 0 {
			return 0
		}
		return (cur.mid - prev.mid) / prev.mid
	}
	ev.MidRet1s, ev.MidRet3s, ev.MidRet5s, ev.MidRet10s = ret(1*time.Second), ret(3*time.Second), ret(5*time.Second), ret(10*time.Second)
	return ev
}
