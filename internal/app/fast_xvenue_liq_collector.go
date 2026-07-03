package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ===== v5 跨所强平流采集(2026-07-04,record-only)=====
// 动机:频率问题的正解=加事件源不降标准。OKX 爆仓推送只覆盖一家的被迫流;同一币在 Binance 被强平,
// 恐慌传染是同一股(cross-venue lead-lag 已证 binance→okx 方向性存在)。先采集攒数据,与 OKX 价格
// 标尺对齐做前向研究,达标才谈进热队列——绝不直接接实盘。
// 通道:Binance USDT-M 全市场 forceOrder 流(!forceOrder@arr,单流覆盖全部合约,零 symbol 管理)。
// 已知风险:本机到 Binance 期货部分流曾 i/o timeout(HANDOFF §-2 fast_peer 教训,aggTrade 不通、
// bookTicker 可用);forceOrder 通不通只能实测,失败只告警+重连,不影响任何交易路径。

const fastXvenueLiqFile = "fast_xvenue_liq_events.jsonl"
const binanceForceOrderWS = "wss://fstream.binance.com/ws/!forceOrder@arr"

// fastXvenueLiqEvent 一条跨所强平事件(record-only)。symbol 映射为 OKX 规范形(BTCUSDT→BTC-USDT,
// 尽力映射:1000X 类前缀原样保留,分析侧自行对齐乘数);raw_symbol 永远保留原始形。
type fastXvenueLiqEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"`
	RawSymbol  string    `json:"raw_symbol"`
	PosSide    string    `json:"pos_side"` // long=多头被强平(强平单卖出) short=空头被强平(强平单买入)
	Side       string    `json:"side"`     // 强平单成交方向 buy/sell
	Qty        float64   `json:"qty"`
	Price      float64   `json:"price"`
	ExchTs     int64     `json:"exch_ts"`
}

// binanceForceOrderMsg !forceOrder@arr 单条推送。
// 注意:显式声明 Ev(json:"e")吸收字符串事件类型,防 Go json 大小写不敏感匹配污染 EvTime(json:"E")
// ——fast_peer 采集器踩过的同一个坑(HANDOFF §-1)。
type binanceForceOrderMsg struct {
	Ev     string `json:"e"`
	EvTime int64  `json:"E"`
	Order  struct {
		Symbol string `json:"s"`
		Side   string `json:"S"` // SELL=多头被强平;BUY=空头被强平
		Qty    string `json:"q"`
		AvgPx  string `json:"ap"`
		Price  string `json:"p"`
		Time   int64  `json:"T"`
	} `json:"o"`
}

// parseBinanceForceOrder 纯解析:payload → 事件(非 forceOrder / 非 USDT 本位 / 无效数量返回 nil)。
func parseBinanceForceOrder(payload []byte, now time.Time) *fastXvenueLiqEvent {
	var m binanceForceOrderMsg
	if json.Unmarshal(payload, &m) != nil || m.Ev != "forceOrder" {
		return nil
	}
	raw := strings.ToUpper(strings.TrimSpace(m.Order.Symbol))
	if !strings.HasSuffix(raw, "USDT") {
		return nil
	}
	qty, _ := strconv.ParseFloat(m.Order.Qty, 64)
	if qty <= 0 {
		return nil
	}
	px, _ := strconv.ParseFloat(m.Order.AvgPx, 64)
	if px <= 0 {
		px, _ = strconv.ParseFloat(m.Order.Price, 64)
	}
	var posSide, side string
	switch strings.ToUpper(m.Order.Side) {
	case "SELL":
		posSide, side = "long", "sell"
	case "BUY":
		posSide, side = "short", "buy"
	default:
		return nil
	}
	return &fastXvenueLiqEvent{
		RecordedAt: now,
		Venue:      "binance",
		Symbol:     strings.TrimSuffix(raw, "USDT") + "-USDT",
		RawSymbol:  raw,
		PosSide:    posSide,
		Side:       side,
		Qty:        qty,
		Price:      px,
		ExchTs:     m.Order.Time,
	}
}

func (e *OKXDirectionalEngine) fastXvenueLiqEnabled() bool {
	return e.cfg.FastXvenueLiqEnabled && strings.EqualFold(e.venueName, "okx")
}

// fastXvenueLiqLoop 外层重连循环(失败只告警,不影响交易)。
func (e *OKXDirectionalEngine) fastXvenueLiqLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_xliq] loop panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[fast_xliq] Binance 全市场强平流采集启动(record-only,不下单/不进热队列)")
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := e.runBinanceForceOrderWS(ctx)
		if ctx.Err() != nil {
			return
		}
		e.logger.Printf("[fast_xliq] 连接中断,%s 后重连: %v", backoff, err)
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 60*time.Second { // 本机到 binance fstream 可能不通:指数退避防刷屏
			backoff *= 2
		}
	}
}

func (e *OKXDirectionalEngine) runBinanceForceOrderWS(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(binanceForceOrderWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Binance 每 ~3min 发 ping,10min 无 pong 断连;gorilla 默认 PingHandler 自动回 pong,
	// 此处只需在收到任何帧时顺延读超时。干涸时段可能长时间无 forceOrder,靠 ping 帧续命。
	const readTimeout = 10 * time.Minute
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	e.logger.Printf("[fast_xliq] Binance forceOrder 流已连接")
	path := filepath.Join(e.recordDir, fastXvenueLiqFile)
	done := make(chan struct{})
	defer close(done)
	go func() { // ctx 取消时主动断连,防 ReadMessage 挂死到读超时
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	for {
		_, payload, rerr := conn.ReadMessage()
		if rerr != nil {
			return rerr
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
		ev := parseBinanceForceOrder(payload, e.nowFn().UTC())
		if ev == nil {
			continue
		}
		if werr := appendJSONLRecord(path, *ev); werr != nil {
			e.logger.Printf("[fast_xliq] 落盘失败: %v", werr)
		}
	}
}
