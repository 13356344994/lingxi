package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ===== fast 动态特征采集器 v2(OKX 公共 WS trades+books5)=====
// record-only:只订阅公共频道(trades/books5),不接私有频道、不下单、不读账户、不影响交易主流程。
// 失败只告警 + 重连;独立 goroutine;symbol 数受 FastWSMaxSymbols 限制。
// 目的:采集能解释 3-5s 胜负分化的动态字段(trade flow / book delta / liquidity pull-refill / short return)。

const fastDynFeatureFile = "fast_dynamic_feature_events.jsonl"
const fastLiqEventFile = "fast_liquidation_events.jsonl"
const okxPublicWSURL = "wss://ws.okx.com:8443/ws/v5/public"

// fastLiqEvent 一条爆仓单事件(record-only 动机层原料:被迫的行为最可预测)。
// 覆盖全市场 SWAP(不只 shortlist):长尾池塘的强平密度/方向本身就是研究对象。
type fastLiqEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Symbol     string    `json:"symbol"`
	PosSide    string    `json:"pos_side"` // 被强平的持仓方向(long=多头爆,short=空头爆)
	Side       string    `json:"side"`     // 强平单的成交方向(多头爆=sell,空头爆=buy)
	Qty        float64   `json:"qty"`      // 张数
	BkPx       float64   `json:"bk_px"`    // 破产价
	BkLoss     float64   `json:"bk_loss"`  // 穿仓损失
	ExchTs     string    `json:"exch_ts"`  // 交易所毫秒时间戳(原样保留)
}

// fastDynEvent 一条动态特征事件(record-only)。
type fastDynEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Symbol     string    `json:"symbol"`
	Bid        float64   `json:"bid"`
	Ask        float64   `json:"ask"`
	Mid        float64   `json:"mid"`
	SpreadBps  float64   `json:"spread_bps"`
	// trade flow
	TradeBuyQty1s  float64 `json:"trade_buy_qty_1s"`
	TradeBuyQty3s  float64 `json:"trade_buy_qty_3s"`
	TradeBuyQty5s  float64 `json:"trade_buy_qty_5s"`
	TradeSellQty1s float64 `json:"trade_sell_qty_1s"`
	TradeSellQty3s float64 `json:"trade_sell_qty_3s"`
	TradeSellQty5s float64 `json:"trade_sell_qty_5s"`
	TradeImb1s     float64 `json:"trade_imbalance_1s"`
	TradeImb3s     float64 `json:"trade_imbalance_3s"`
	TradeImb5s     float64 `json:"trade_imbalance_5s"`
	TradeCount1s   int     `json:"trade_count_1s"`
	TradeCount3s   int     `json:"trade_count_3s"`
	TradeCount5s   int     `json:"trade_count_5s"`
	LargeTradeCnt  int     `json:"large_trade_count"`
	LargeTradeNot  float64 `json:"large_trade_notional"`
	// book delta
	BestBidQtyDelta1s float64 `json:"best_bid_qty_delta_1s"`
	BestBidQtyDelta3s float64 `json:"best_bid_qty_delta_3s"`
	BestBidQtyDelta5s float64 `json:"best_bid_qty_delta_5s"`
	BestAskQtyDelta1s float64 `json:"best_ask_qty_delta_1s"`
	BestAskQtyDelta3s float64 `json:"best_ask_qty_delta_3s"`
	BestAskQtyDelta5s float64 `json:"best_ask_qty_delta_5s"`
	BookImbNow        float64 `json:"book_imbalance_now"`
	BookImbDelta1s    float64 `json:"book_imbalance_delta_1s"`
	BookImbDelta3s    float64 `json:"book_imbalance_delta_3s"`
	BookImbDelta5s    float64 `json:"book_imbalance_delta_5s"`
	// liquidity(3s 窗口的抽单/补单比例)
	LiquidityPullBid   float64 `json:"liquidity_pull_bid"`
	LiquidityPullAsk   float64 `json:"liquidity_pull_ask"`
	LiquidityRefillBid float64 `json:"liquidity_refill_bid"`
	LiquidityRefillAsk float64 `json:"liquidity_refill_ask"`
	// returns
	MidRet1s  float64 `json:"mid_ret_1s"`
	MidRet3s  float64 `json:"mid_ret_3s"`
	MidRet5s  float64 `json:"mid_ret_5s"`
	MidRet10s float64 `json:"mid_ret_10s"`
	// meta
	CandidateReason string `json:"candidate_reason,omitempty"`
}

type wsTradePt struct {
	ts   time.Time
	buy  bool
	qty  float64
	px   float64
}
type wsBookPt struct {
	ts                             time.Time
	bidPx, bidQty, askPx, askQty, mid float64
}

type fastWSState struct {
	mu     sync.Mutex
	trades map[string][]wsTradePt
	books  map[string][]wsBookPt
}

func (e *OKXDirectionalEngine) fastWSEnabled() bool {
	if !e.cfg.FastWSCollectorEnabled {
		return false
	}
	// 采集器绑定 OKX(公共 WS 为 OKX);仅在 okx 引擎跑一份。
	return strings.EqualFold(e.venueName, "okx")
}

// fastWSCollectorLoop 外层重连循环(失败只告警,不影响交易)。
func (e *OKXDirectionalEngine) fastWSCollectorLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_ws] collector panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[fast_ws] OKX 动态采集器启动(record-only,不下单/不读账户)max_symbols=%d", e.cfg.FastWSMaxSymbols)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := e.runFastWS(ctx); err != nil && ctx.Err() == nil {
			e.logger.Printf("[fast_ws] 连接中断,3s 后重连: %v", err)
		}
		if !sleepCtx(ctx, 3*time.Second) {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (e *OKXDirectionalEngine) fastWSSymbols() []string {
	maxN := e.cfg.FastWSMaxSymbols
	if maxN <= 0 {
		maxN = 15
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, maxN)
	for _, ent := range e.shortlist {
		if strings.TrimSpace(ent.Symbol) == "" {
			continue
		}
		out = append(out, ent.Symbol)
		if len(out) >= maxN {
			break
		}
	}
	return out
}

func (e *OKXDirectionalEngine) runFastWS(ctx context.Context) error {
	syms := e.fastWSSymbols()
	if len(syms) == 0 {
		return fmt.Errorf("shortlist 空,暂不订阅")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(okxPublicWSURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	// 订阅 trades + books5
	args := make([]map[string]string, 0, len(syms)*2+1)
	for _, s := range syms {
		inst := s + "-SWAP"
		args = append(args, map[string]string{"channel": "trades", "instId": inst})
		args = append(args, map[string]string{"channel": "books5", "instId": inst})
	}
	// 动机层:全市场 SWAP 爆仓单(单条订阅覆盖全部合约;公共频道,量小,record-only)
	if e.cfg.FastLiqCollectorEnabled {
		args = append(args, map[string]string{"channel": "liquidation-orders", "instType": "SWAP"})
	}
	if err := conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args}); err != nil {
		return err
	}
	st := &fastWSState{trades: map[string][]wsTradePt{}, books: map[string][]wsBookPt{}}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// ping:每 20s 发 "ping"(OKX 回 "pong")
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
	// emit:每 EmitIntervalMs 输出各 symbol 特征事件
	go e.fastWSEmitLoop(cctx, st, syms)
	// read loop
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
		e.fastWSHandle(st, payload)
	}
}

func (e *OKXDirectionalEngine) fastWSHandle(st *fastWSState, payload []byte) {
	var msg struct {
		Event string `json:"event"`
		Arg   struct {
			Channel string `json:"channel"`
			InstID  string `json:"instId"`
		} `json:"arg"`
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Arg.Channel == "" || len(msg.Data) == 0 {
		return
	}
	now := e.nowFn().UTC()
	if msg.Arg.Channel == "liquidation-orders" {
		e.fastWSHandleLiquidations(now, msg.Data)
		return
	}
	sym := strings.TrimSuffix(msg.Arg.InstID, "-SWAP")
	st.mu.Lock()
	defer st.mu.Unlock()
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
			st.trades[sym] = append(st.trades[sym], wsTradePt{ts: now, buy: strings.EqualFold(t.Side, "buy"), qty: sz, px: px})
		}
		// prune >6s
		st.trades[sym] = pruneTrades(st.trades[sym], now.Add(-6*time.Second))
	case "books5":
		var b struct {
			Asks [][]string `json:"asks"`
			Bids [][]string `json:"bids"`
		}
		if json.Unmarshal(msg.Data[len(msg.Data)-1], &b) != nil || len(b.Asks) == 0 || len(b.Bids) == 0 {
			return
		}
		bidPx, _ := strconv.ParseFloat(b.Bids[0][0], 64)
		bidQty, _ := strconv.ParseFloat(b.Bids[0][1], 64)
		askPx, _ := strconv.ParseFloat(b.Asks[0][0], 64)
		askQty, _ := strconv.ParseFloat(b.Asks[0][1], 64)
		if bidPx <= 0 || askPx <= 0 {
			return
		}
		st.books[sym] = append(st.books[sym], wsBookPt{ts: now, bidPx: bidPx, bidQty: bidQty, askPx: askPx, askQty: askQty, mid: (bidPx + askPx) / 2})
		st.books[sym] = pruneBooks(st.books[sym], now.Add(-12*time.Second))
	}
}

// fastWSHandleLiquidations 解析爆仓单推送并逐条落盘(record-only;instId 在 data 元素里,arg 无 instId)。
func (e *OKXDirectionalEngine) fastWSHandleLiquidations(now time.Time, data []json.RawMessage) {
	path := filepath.Join(e.recordDir, fastLiqEventFile)
	for _, d := range data {
		var item struct {
			InstID  string `json:"instId"`
			Details []struct {
				PosSide string `json:"posSide"`
				Side    string `json:"side"`
				Sz      string `json:"sz"`
				BkPx    string `json:"bkPx"`
				BkLoss  string `json:"bkLoss"`
				Ts      string `json:"ts"`
			} `json:"details"`
		}
		if json.Unmarshal(d, &item) != nil || item.InstID == "" {
			continue
		}
		sym := strings.TrimSuffix(item.InstID, "-SWAP")
		for _, det := range item.Details {
			qty, _ := strconv.ParseFloat(det.Sz, 64)
			bkPx, _ := strconv.ParseFloat(det.BkPx, 64)
			bkLoss, _ := strconv.ParseFloat(det.BkLoss, 64)
			if qty <= 0 {
				continue
			}
			fastNoteLiq(sym, det.PosSide, qty, now) // v3② 内存缓冲:方向统计+簇强度(liq_catch/liq_follow 共用)
			fastNoteLiqHot(sym, now)              // v3③ 热队列:事件驱动全市场入场的注意力信号
			if err := appendJSONLRecord(path, fastLiqEvent{
				RecordedAt: now, Symbol: sym, PosSide: det.PosSide, Side: det.Side,
				Qty: qty, BkPx: bkPx, BkLoss: bkLoss, ExchTs: det.Ts,
			}); err != nil {
				e.logger.Printf("[fast_ws][liq] 落盘失败: %v", err)
				return
			}
		}
	}
}

func pruneTrades(a []wsTradePt, cut time.Time) []wsTradePt {
	i := 0
	for i < len(a) && a[i].ts.Before(cut) {
		i++
	}
	return a[i:]
}
func pruneBooks(a []wsBookPt, cut time.Time) []wsBookPt {
	i := 0
	for i < len(a) && a[i].ts.Before(cut) {
		i++
	}
	return a[i:]
}

func (e *OKXDirectionalEngine) fastWSEmitLoop(ctx context.Context, st *fastWSState, syms []string) {
	iv := e.cfg.FastWSEmitIntervalMs
	if iv <= 0 {
		iv = 1000
	}
	tk := time.NewTicker(time.Duration(iv) * time.Millisecond)
	defer tk.Stop()
	path := filepath.Join(e.recordDir, fastDynFeatureFile)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		now := e.nowFn().UTC()
		for _, sym := range syms {
			ev := e.fastWSBuildEvent(st, sym, now)
			if ev == nil {
				continue
			}
			if err := appendJSONLRecord(path, *ev); err != nil {
				e.logger.Printf("[fast_ws] 落盘失败: %v", err)
				break
			}
		}
	}
}

// bookAt 返回 <= target 的最后一个 book(近似 "w 秒前");无则返回 false。
func bookAt(a []wsBookPt, target time.Time) (wsBookPt, bool) {
	var res wsBookPt
	ok := false
	for _, b := range a {
		if !b.ts.After(target) {
			res, ok = b, true
		}
	}
	return res, ok
}

func (e *OKXDirectionalEngine) fastWSBuildEvent(st *fastWSState, sym string, now time.Time) *fastDynEvent {
	st.mu.Lock()
	trades := append([]wsTradePt(nil), st.trades[sym]...)
	books := append([]wsBookPt(nil), st.books[sym]...)
	st.mu.Unlock()
	if len(books) == 0 {
		return nil
	}
	cur := books[len(books)-1]
	if cur.mid <= 0 {
		return nil
	}
	ev := &fastDynEvent{RecordedAt: now, Symbol: sym, Bid: cur.bidPx, Ask: cur.askPx, Mid: cur.mid}
	ev.SpreadBps = (cur.askPx - cur.bidPx) / cur.mid * 10000
	// trade flow
	large := e.cfg.FastWSLargeTradeUSDT
	if large <= 0 {
		large = 500
	}
	sumWin := func(w time.Duration) (buy, sell float64, cnt int) {
		cut := now.Add(-w)
		for _, t := range trades {
			if t.ts.Before(cut) {
				continue
			}
			cnt++
			if t.buy {
				buy += t.qty
			} else {
				sell += t.qty
			}
		}
		return
	}
	imb := func(b, s float64) float64 {
		if b+s <= 0 {
			return 0
		}
		return (b - s) / (b + s)
	}
	b1, s1, c1 := sumWin(1 * time.Second)
	b3, s3, c3 := sumWin(3 * time.Second)
	b5, s5, c5 := sumWin(5 * time.Second)
	ev.TradeBuyQty1s, ev.TradeSellQty1s, ev.TradeCount1s = b1, s1, c1
	ev.TradeBuyQty3s, ev.TradeSellQty3s, ev.TradeCount3s = b3, s3, c3
	ev.TradeBuyQty5s, ev.TradeSellQty5s, ev.TradeCount5s = b5, s5, c5
	ev.TradeImb1s, ev.TradeImb3s, ev.TradeImb5s = imb(b1, s1), imb(b3, s3), imb(b5, s5)
	cut5 := now.Add(-5 * time.Second)
	for _, t := range trades {
		if t.ts.Before(cut5) {
			continue
		}
		if t.px*t.qty >= large {
			ev.LargeTradeCnt++
			ev.LargeTradeNot += t.px * t.qty
		}
	}
	// book delta + imbalance velocity
	bookImb := func(b wsBookPt) float64 {
		if b.bidQty+b.askQty <= 0 {
			return 0
		}
		return (b.bidQty - b.askQty) / (b.bidQty + b.askQty)
	}
	ev.BookImbNow = bookImb(cur)
	setDelta := func(w time.Duration) (dbid, dask, dimb float64, ok bool) {
		prev, o := bookAt(books, now.Add(-w))
		if !o {
			return 0, 0, 0, false
		}
		return cur.bidQty - prev.bidQty, cur.askQty - prev.askQty, bookImb(cur) - bookImb(prev), true
	}
	if d1b, d1a, d1i, ok := setDelta(1 * time.Second); ok {
		ev.BestBidQtyDelta1s, ev.BestAskQtyDelta1s, ev.BookImbDelta1s = d1b, d1a, d1i
	}
	if d3b, d3a, d3i, ok := setDelta(3 * time.Second); ok {
		ev.BestBidQtyDelta3s, ev.BestAskQtyDelta3s, ev.BookImbDelta3s = d3b, d3a, d3i
	}
	if d5b, d5a, d5i, ok := setDelta(5 * time.Second); ok {
		ev.BestBidQtyDelta5s, ev.BestAskQtyDelta5s, ev.BookImbDelta5s = d5b, d5a, d5i
	}
	// liquidity pull/refill(3s 窗口比例)
	if prev, ok := bookAt(books, now.Add(-3*time.Second)); ok {
		frac := func(prevQ, nowQ float64) (pull, refill float64) {
			if prevQ <= 0 {
				return 0, 0
			}
			ch := (nowQ - prevQ) / prevQ
			if ch < 0 {
				return -ch, 0
			}
			return 0, ch
		}
		ev.LiquidityPullBid, ev.LiquidityRefillBid = frac(prev.bidQty, cur.bidQty)
		ev.LiquidityPullAsk, ev.LiquidityRefillAsk = frac(prev.askQty, cur.askQty)
	}
	// returns
	ret := func(w time.Duration) float64 {
		prev, ok := bookAt(books, now.Add(-w))
		if !ok || prev.mid <= 0 {
			return 0
		}
		return (cur.mid - prev.mid) / prev.mid
	}
	ev.MidRet1s, ev.MidRet3s, ev.MidRet5s, ev.MidRet10s = ret(1*time.Second), ret(3*time.Second), ret(5*time.Second), ret(10*time.Second)
	// candidate_reason:用现有静态 scorer 的 tag(best-effort,便于与 settlement 对齐)
	se := &fastExecShadowEvent{Bid: cur.bidPx, Ask: cur.askPx, Mid: cur.mid, SpreadBps: ev.SpreadBps, OrderbookImbalance: ev.BookImbNow,
		MidImpulse15sBps: ev.MidRet5s * 10000, MidImpulse30sBps: ev.MidRet10s * 10000}
	_, _, ev.CandidateReason = scoreFastEvent(se)
	return ev
}
