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
// 定位:耳朵不是枪口——fast 实盘只在 OKX(fast_scalper_v1_venue=okx,护栏锚死);本采集器只听
// 别所的被迫流(同一币在 Bybit 被强平,恐慌传染是同一股),攒数据与 OKX 价格标尺对齐做前向研究,
// 达标才谈进热队列。不下单/不读账户/不影响任何交易路径。
// 源选择(2026-07-04 09:10 探针实测):Binance fstream 的 forceOrder/aggTrade 从本机永远收不到数据帧
// (bookTicker 正常;与 HANDOFF §-1 fast_peer 教训同源),连接成功但零数据=僵尸 → 弃用;
// Bybit 公共 WS 本机实测可用(fast_peer 时代验证),用 allLiquidation.{symbol} 订阅 OKX∩Bybit 交集。

const fastXvenueLiqFile = "fast_xvenue_liq_events.jsonl"

// fastXvenueLiqEvent 一条跨所强平事件(record-only)。symbol 为 OKX 规范形(BTCUSDT→BTC-USDT);
// side_raw 永远保留交易所原始值——pos_side 的映射约定(订单方向 Sell=多头被强平)与 Binance forceOrder
// 一致,但 Bybit 文档语义有歧义,须用 OKX 同币同时刻爆仓事件做一次实证核对(_xliq_side_check),
// 若发现反向,以 side_raw 重放修正,历史数据不失真。
type fastXvenueLiqEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"`
	RawSymbol  string    `json:"raw_symbol"`
	SideRaw    string    `json:"side_raw"`
	PosSide    string    `json:"pos_side"` // long=多头被强平(强平单卖出) short=空头被强平(强平单买入)
	Qty        float64   `json:"qty"`
	Price      float64   `json:"price"`
	ExchTs     int64     `json:"exch_ts"`
}

// parseBybitAllLiquidation 纯解析:allLiquidation 推送 → 事件列表。
// 消息形如 {"topic":"allLiquidation.BTCUSDT","ts":...,"data":[{"T":...,"s":"BTCUSDT","S":"Sell","v":"0.003","p":"95892.9"}]}。
func parseBybitAllLiquidation(payload []byte, now time.Time) []fastXvenueLiqEvent {
	var msg struct {
		Topic string `json:"topic"`
		Data  []struct {
			T int64  `json:"T"`
			S string `json:"s"`
			// Side:按订单方向约定映射(Sell=多头被强平);原始值存 side_raw 供实证核对/重放修正。
			Side string `json:"S"`
			V    string `json:"v"`
			P    string `json:"p"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &msg) != nil || !strings.HasPrefix(msg.Topic, "allLiquidation.") {
		return nil
	}
	out := make([]fastXvenueLiqEvent, 0, len(msg.Data))
	for _, d := range msg.Data {
		raw := strings.ToUpper(strings.TrimSpace(d.S))
		if !strings.HasSuffix(raw, "USDT") {
			continue
		}
		qty, _ := strconv.ParseFloat(d.V, 64)
		px, _ := strconv.ParseFloat(d.P, 64)
		if qty <= 0 {
			continue
		}
		var posSide string
		switch strings.ToUpper(d.Side) {
		case "SELL":
			posSide = "long"
		case "BUY":
			posSide = "short"
		default:
			continue
		}
		out = append(out, fastXvenueLiqEvent{
			RecordedAt: now,
			Venue:      "bybit",
			Symbol:     strings.TrimSuffix(raw, "USDT") + "-USDT",
			RawSymbol:  raw,
			SideRaw:    d.Side,
			PosSide:    posSide,
			Qty:        qty,
			Price:      px,
			ExchTs:     d.T,
		})
	}
	return out
}

func (e *OKXDirectionalEngine) fastXvenueLiqEnabled() bool {
	return e.cfg.FastXvenueLiqEnabled && strings.EqualFold(e.venueName, "okx")
}

// fastXvenueLiqLoop 外层循环:算 OKX∩Bybit 交集 → 订阅 allLiquidation → 断线重连(失败只告警)。
func (e *OKXDirectionalEngine) fastXvenueLiqLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_xliq] loop panic 已恢复: %v", r)
		}
	}()
	// 交集计算(带重试;两边清单都是公共 REST)
	var syms []string
	for {
		okxSet, err1 := okxSwapUSDTSet()
		bybitSet, err2 := bybitLinearUSDTSet()
		if err1 == nil && err2 == nil {
			for c := range okxSet {
				if bybitSet[peerRaw(c)] {
					syms = append(syms, peerRaw(c))
				}
			}
		}
		if len(syms) > 0 {
			break
		}
		e.logger.Printf("[fast_xliq] 交集计算失败/为空(okx=%v bybit=%v),60s 重试", err1, err2)
		if !sleepCtx(ctx, 60*time.Second) {
			return
		}
	}
	e.logger.Printf("[fast_xliq] Bybit 全市场强平流采集启动(record-only,耳朵不是枪口;OKX∩Bybit=%d 币)", len(syms))
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := e.runBybitLiqWS(ctx, syms)
		if ctx.Err() != nil {
			return
		}
		e.logger.Printf("[fast_xliq] 连接中断,%s 后重连: %v", backoff, err)
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func (e *OKXDirectionalEngine) runBybitLiqWS(ctx context.Context, rawSyms []string) error {
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(bybitLinearWS, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	// 分批订阅(单条消息 args 过大有被拒风险;100/批与 fast_peer 实测口径一致)
	for i := 0; i < len(rawSyms); i += 100 {
		end := i + 100
		if end > len(rawSyms) {
			end = len(rawSyms)
		}
		args := make([]string, 0, 100)
		for _, r := range rawSyms[i:end] {
			args = append(args, "allLiquidation."+r)
		}
		if err := conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args}); err != nil {
			return err
		}
	}
	e.logger.Printf("[fast_xliq][bybit] 已连并订阅 %d 币", len(rawSyms))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // bybit 要求客户端主动 ping(20s)
		tk := time.NewTicker(20 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-cctx.Done():
				_ = conn.Close() // ctx 取消时主动断连,防 ReadMessage 挂死
				return
			case <-tk.C:
				_ = conn.WriteJSON(map[string]string{"op": "ping"})
			}
		}
	}()
	path := filepath.Join(e.recordDir, fastXvenueLiqFile)
	for {
		_, payload, rerr := conn.ReadMessage()
		if rerr != nil {
			return rerr
		}
		_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
		for _, ev := range parseBybitAllLiquidation(payload, e.nowFn().UTC()) {
			if werr := appendJSONLRecord(path, ev); werr != nil {
				e.logger.Printf("[fast_xliq] 落盘失败: %v", werr)
			}
		}
	}
}
