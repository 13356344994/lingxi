package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ===== ㊳② 全市场磁带探针(2026-07-08,record-only)=====
// 结构债根治的第一步:burst 探测器此前只挂热币 WS(40 坑),冷币第一波 burst 结构性不可见
// ("几何上只看见第二波",07-06 体检结构债①;死线转向①一直搁置)。Go 的并发本性=多连接
// 全市场订阅毫无压力,此前 40 坑是自我设限不是交易所限制。
//
// 本采集器:N 条 OKX 公共 WS 连接分片订阅全市场 SWAP trades(仅 trades,不订 books=控消息量),
// 每片独立 burst 状态机(同口径复用 fastBurstUpdate 纯函数),识别事件写
// fast_full_burst_probe.jsonl(带 in_hot_ws 标注=热币 WS 是否同时覆盖→增量视野直读)。
// 探针阶段刻意不接 fastTapeEnqueue(不动 tape 确认管线,一次一刀);但喂 fastNoteBurst
// (曲线② burst 弹射标注从 40 币升级到全市场,record-only 语义不变、覆盖更真)。
// 每 60s 记录消息率/成交率/burst 率 = 可行性裁决数(消息量/延迟分布/增量 burst 占比)。
// 不下单/不读账户/失败只告警;开关 fast_full_tape_enabled 护栏锚定。

const fastFullBurstProbeFile = "fast_full_burst_probe.jsonl"
const fastFullTapeSymbolsPerConn = 150
const fastFullTapeMaxConns = 8
const fastFullTapeEpochSec = 1800 // 每 30min 重取全市场清单重连(吸收新上市/下架)

// fastFullBurstProbeEvent 探针识别事件(与 fastBurstEvent 同口径 + 覆盖标注 + 州标注)。
// 州标注(Layer A"每事件带州"):24h 裁决要按 燃料×广度×本币爆仓 分桶——盲区 burst 是
// 强平驱动还是主动暴力、集中在哪个州,决定第一波视野接不接 tape 确认管线。
type fastFullBurstProbeEvent struct {
	RecordedAt  time.Time `json:"recorded_at"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	Notional1s  float64   `json:"notional_1s"`
	BaselineMed float64   `json:"baseline_med_1s"`
	Multiple    float64   `json:"multiple"`
	LatencyMs   int64     `json:"latency_ms"`
	Px          float64   `json:"px"`
	InHotWS     bool      `json:"in_hot_ws"` // 热币 WS 当刻是否也覆盖(false=纯增量视野)
	ConnID      int       `json:"conn_id"`
	MarketLiq5m float64   `json:"market_liq_5m,omitempty"` // 燃料州
	Breadth30   float64   `json:"breadth_30m,omitempty"`   // 广度州(0 且 breadth_ok=false=不可算)
	BreadthOK   bool      `json:"breadth_ok,omitempty"`
	SymLiqN30s  int       `json:"sym_liq_n_30s,omitempty"` // 本币官方爆仓已到几笔(0=纯tape信号)
	XliqN30s    int       `json:"xliq_n_30s,omitempty"`    // Bybit 同币近30s强平
}

// 探针吞吐计数(60s 报告一次;可行性裁决数的原料)。
var (
	fastFullTapeMsgs   atomic.Int64
	fastFullTapeTrades atomic.Int64
	fastFullTapeBursts atomic.Int64
	fastFullTapeNewEye atomic.Int64 // in_hot_ws=false 的 burst(增量视野)
)

// fastFullTapeInHot 该币当刻是否在热币 WS 覆盖内(热度表 600s 粘性≈订阅集)。
func fastFullTapeInHot(sym string, now time.Time) bool {
	fastHotSetMu.Lock()
	defer fastHotSetMu.Unlock()
	t, ok := fastHotSet[sym]
	return ok && now.Sub(t) <= fastHotWSStickySec*time.Second
}

// parseOKXSwapSymbols SWAP tickers → canon 符号清单(BTC-USDT 形)。
func parseOKXSwapSymbols(body []byte) []string {
	var r struct {
		Data []struct {
			InstID string `json:"instId"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	out := make([]string, 0, len(r.Data))
	for _, d := range r.Data {
		up := strings.ToUpper(d.InstID)
		if !strings.HasSuffix(up, "-USDT-SWAP") {
			continue
		}
		out = append(out, strings.TrimSuffix(up, "-SWAP"))
	}
	sort.Strings(out)
	return out
}

// fastFullTapeChunks 纯函数:符号清单分片(每片 ≤perConn,片数 ≤maxConns,超出截断尾部)。
func fastFullTapeChunks(syms []string, perConn, maxConns int) [][]string {
	if perConn <= 0 || maxConns <= 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(syms) && len(out) < maxConns; i += perConn {
		j := i + perConn
		if j > len(syms) {
			j = len(syms)
		}
		out = append(out, syms[i:j])
	}
	return out
}

func (e *OKXDirectionalEngine) fastFullTapeEnabled() bool {
	return e.cfg.FastFullTapeEnabled && strings.EqualFold(e.venueName, "okx")
}

func (e *OKXDirectionalEngine) fastFullTapeLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_fulltape] loop panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[fast_fulltape] 全市场磁带探针启动(record-only)分片=%d币/连接 上限=%d连接 epoch=%ds → %s",
		fastFullTapeSymbolsPerConn, fastFullTapeMaxConns, fastFullTapeEpochSec, fastFullBurstProbeFile)
	// 吞吐报告(60s 一行=可行性裁决数)
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m := fastFullTapeMsgs.Swap(0)
				tr := fastFullTapeTrades.Swap(0)
				b := fastFullTapeBursts.Load()
				nb := fastFullTapeNewEye.Load()
				e.logger.Printf("[fast_fulltape] 60s吞吐 msgs=%d(%.0f/s) trades=%d burst累计=%d 增量视野burst=%d(%.0f%%)",
					m, float64(m)/60, tr, b, nb, pct(nb, b))
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var body json.RawMessage
		if err := peerGetJSON("https://www.okx.com/api/v5/market/tickers?instType=SWAP", &body); err != nil {
			e.logger.Printf("[fast_fulltape] 符号清单拉取失败,60s 后重试: %v", err)
			if !sleepCtx(ctx, 60*time.Second) {
				return
			}
			continue
		}
		syms := parseOKXSwapSymbols(body)
		if len(syms) < 100 {
			e.logger.Printf("[fast_fulltape] 符号清单异常(仅 %d),60s 后重试", len(syms))
			if !sleepCtx(ctx, 60*time.Second) {
				return
			}
			continue
		}
		chunks := fastFullTapeChunks(syms, fastFullTapeSymbolsPerConn, fastFullTapeMaxConns)
		covered := 0
		for _, c := range chunks {
			covered += len(c)
		}
		e.logger.Printf("[fast_fulltape] epoch 开始 全市场=%d币 覆盖=%d币 连接=%d", len(syms), covered, len(chunks))
		ectx, cancel := context.WithTimeout(ctx, fastFullTapeEpochSec*time.Second)
		var wg sync.WaitGroup
		for i, chunk := range chunks {
			wg.Add(1)
			go func(id int, cs []string) {
				defer wg.Done()
				for {
					select {
					case <-ectx.Done():
						return
					default:
					}
					if err := e.runFastFullTapeConn(ectx, id, cs); err != nil && ectx.Err() == nil {
						e.logger.Printf("[fast_fulltape] conn%d 中断,3s 后重连: %v", id, err)
					}
					if !sleepCtx(ectx, 3*time.Second) {
						return
					}
				}
			}(i, chunk)
			// OKX 连接频率礼让(~3次/s 限制):建连间隔 500ms
			if !sleepCtx(ctx, 500*time.Millisecond) {
				cancel()
				wg.Wait()
				return
			}
		}
		wg.Wait()
		cancel()
	}
}

// runFastFullTapeConn 单连接:订阅本片 trades → 逐笔喂独立 burst 状态机 → 识别落盘。
func (e *OKXDirectionalEngine) runFastFullTapeConn(ctx context.Context, id int, syms []string) error {
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).Dial(okxPublicWSURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	// 订阅(100 args/批,帧体积礼让)
	for i := 0; i < len(syms); i += 100 {
		j := i + 100
		if j > len(syms) {
			j = len(syms)
		}
		args := make([]map[string]string, 0, j-i)
		for _, s := range syms[i:j] {
			args = append(args, map[string]string{"channel": "trades", "instId": s + "-SWAP"})
		}
		if err := conn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": args}); err != nil {
			return err
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-cctx.Done():
				_ = conn.Close()
				return
			case <-ping.C:
				_ = conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			}
		}
	}()
	perSym := map[string]*symBurstState{}
	path := filepath.Join(e.recordDir, fastFullBurstProbeFile)
	for {
		select {
		case <-cctx.Done():
			return cctx.Err()
		default:
		}
		_, payload, rerr := conn.ReadMessage()
		if rerr != nil {
			return rerr
		}
		_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		if len(payload) == 4 && string(payload) == "pong" {
			continue
		}
		fastFullTapeMsgs.Add(1)
		var msg struct {
			Arg struct {
				Channel string `json:"channel"`
				InstID  string `json:"instId"`
			} `json:"arg"`
			Data []struct {
				Px, Sz, Side, Ts string
			} `json:"data"`
		}
		if json.Unmarshal(payload, &msg) != nil || msg.Arg.Channel != "trades" || len(msg.Data) == 0 {
			continue
		}
		sym := strings.TrimSuffix(msg.Arg.InstID, "-SWAP")
		now := e.nowFn().UTC()
		for _, t := range msg.Data {
			px, sz := pf(t.Px), pf(t.Sz)
			if px <= 0 || sz <= 0 {
				continue
			}
			fastFullTapeTrades.Add(1)
			st := perSym[sym]
			if st == nil {
				st = &symBurstState{}
				perSym[sym] = st
			}
			buy := strings.EqualFold(t.Side, "buy")
			hit, tot, med, buySide := fastBurstUpdate(st, now, px*sz, buy)
			if !hit || now.Sub(st.lastEmit) < fastBurstCooldownSec*time.Second {
				continue
			}
			st.lastEmit = now
			exch, _ := strconv.ParseInt(strings.TrimSpace(t.Ts), 10, 64)
			lat := int64(-1)
			if exch > 0 {
				lat = now.UnixMilli() - exch
			}
			side := "sell"
			if buySide {
				side = "buy"
			}
			inHot := fastFullTapeInHot(sym, now)
			br, brOK := fastBreadth30(now)
			ev := fastFullBurstProbeEvent{
				RecordedAt: now, Symbol: sym, Side: side,
				Notional1s: tot, BaselineMed: med, Multiple: tot / med,
				LatencyMs: lat, Px: px, InHotWS: inHot, ConnID: id,
				MarketLiq5m: fastMarketLiqRate(now, 300),
				Breadth30:   br, BreadthOK: brOK,
				SymLiqN30s: fastSymLiqCount(sym, now, 30),
				XliqN30s:   fastXliqCount(sym, now, 30),
			}
			fastFullTapeBursts.Add(1)
			if !inHot {
				fastFullTapeNewEye.Add(1)
			}
			if werr := appendJSONLRecord(path, ev); werr != nil {
				e.logger.Printf("[fast_fulltape] 落盘失败: %v", werr)
			}
			fastNoteBurst(sym, now, !buySide) // 曲线②弹射标注升级为全市场覆盖(record-only)
		}
	}
}

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
