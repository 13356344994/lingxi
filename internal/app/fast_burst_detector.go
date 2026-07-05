package app

import (
	"path/filepath"
	"time"
)

// ===== v5.9 亚秒 burst 探测器(2026-07-05,record-only)=====
// 背景:tape 候选的 edge 半衰期实测≈2s(burst+1s 进场中位+13.6bps,+3s 翻-13.0)——
// 吃到它的前提是在成交流上亚秒级识别爆量。本探测器挂在热币 WS 的逐笔 trades 上,
// 逐笔滚动判定 burst,并记录【识别延迟 = 本地识别时刻 - 交易所成交时间戳】,先用数据证明
// "我们够得着 1 秒内",再谈任何实盘。不下单/不影响任何交易路径。
// 判定与离线研究同口径:1s 滚动名义 ≥ 5×过去60秒的秒级中位 且 ≥500U;同币 30s 冷却。

const fastBurstDetectFile = "fast_burst_detect.jsonl"
const (
	fastBurstBaselineSecs = 60
	fastBurstMultiple     = 5.0
	fastBurstMinNotional  = 500.0
	fastBurstCooldownSec  = 30
)

// fastBurstEvent 一次识别(record-only)。latency_ms 是核心产出:识别时刻-交易所成交时刻。
type fastBurstEvent struct {
	RecordedAt   time.Time `json:"recorded_at"`
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"` // buy=向上爆量 sell=向下爆量(1s窗内主导方向)
	Notional1s   float64   `json:"notional_1s"`
	BaselineMed  float64   `json:"baseline_med_1s"`
	Multiple     float64   `json:"multiple"`
	LatencyMs    int64     `json:"latency_ms"`              // 本地识别 - 触发笔交易所ts
	Px           float64   `json:"px"`
	MarketLiq5m  float64   `json:"market_liq_5m,omitempty"` // 燃料标注(选择层研究用)
	SymLiqN30s   int       `json:"sym_liq_n_30s,omitempty"` // 本币官方爆仓已到几笔(0=纯tape信号,推送还没来)
	XliqN30s     int       `json:"xliq_n_30s,omitempty"`
}

// symBurstState 单币在线状态:2s 逐笔窗 + 60 格秒桶基线环。
type symBurstState struct {
	win       []burstTradePt // 最近 ~1.2s 逐笔
	secRing   [fastBurstBaselineSecs]float64
	ringSec   int64 // 当前秒(unix);写 ring[sec%60]
	lastEmit  time.Time
	warmSecs  int // 已滚动过的秒数(基线热身:<30 不判定)
}

type burstTradePt struct {
	ts       time.Time
	notional float64
	buy      bool
}

type fastBurstDetector struct {
	perSym map[string]*symBurstState
	path   string
}

func newFastBurstDetector(recordDir string) *fastBurstDetector {
	return &fastBurstDetector{perSym: map[string]*symBurstState{}, path: filepath.Join(recordDir, fastBurstDetectFile)}
}

// fastBurstUpdate 纯逻辑:进一笔,返回 (是否burst, 1s名义, 基线中位, 主导方向buy?)。
// 调用方须持 fastWSState.mu(与 trades 写入同锁,零额外竞态)。
func fastBurstUpdate(st *symBurstState, now time.Time, notional float64, buy bool) (bool, float64, float64, bool) {
	sec := now.Unix()
	if st.ringSec == 0 {
		st.ringSec = sec
	}
	// 推进秒桶环(跳过的秒补零)
	for st.ringSec < sec {
		st.ringSec++
		st.secRing[st.ringSec%fastBurstBaselineSecs] = 0
		if st.warmSecs < fastBurstBaselineSecs {
			st.warmSecs++
		}
	}
	st.secRing[sec%fastBurstBaselineSecs] += notional
	// 1.2s 逐笔窗
	st.win = append(st.win, burstTradePt{now, notional, buy})
	cut := now.Add(-1200 * time.Millisecond)
	i := 0
	for i < len(st.win) && st.win[i].ts.Before(cut) {
		i++
	}
	st.win = st.win[i:]
	var tot, buyN float64
	for _, p := range st.win {
		tot += p.notional
		if p.buy {
			buyN += p.notional
		}
	}
	if st.warmSecs < 30 || tot < fastBurstMinNotional {
		return false, tot, 0, buyN*2 >= tot
	}
	// 基线=环内(不含当前秒)非零中位;全零盘则用极小值防除零
	vals := make([]float64, 0, fastBurstBaselineSecs)
	cur := sec % fastBurstBaselineSecs
	for k := int64(0); k < fastBurstBaselineSecs; k++ {
		if k == cur {
			continue
		}
		vals = append(vals, st.secRing[k])
	}
	med := fastMedian(vals)
	if med <= 0 {
		med = fastBurstMinNotional / 10 // 死盘基线地板:tot≥500U 且10倍即算
	}
	return tot >= med*fastBurstMultiple, tot, med, buyN*2 >= tot
}

// fastBurstOnTrade 逐笔入口(调用方持 st.mu)。识别即落盘(带识别延迟与燃料/推送状态标注)。
func (e *OKXDirectionalEngine) fastBurstOnTrade(det *fastBurstDetector, sym string, now time.Time, exchMs int64, px, sz float64, buy bool) {
	st := det.perSym[sym]
	if st == nil {
		st = &symBurstState{}
		det.perSym[sym] = st
	}
	hit, tot, med, buySide := fastBurstUpdate(st, now, px*sz, buy)
	if !hit || now.Sub(st.lastEmit) < fastBurstCooldownSec*time.Second {
		return
	}
	st.lastEmit = now
	lat := int64(-1)
	if exchMs > 0 {
		lat = now.UnixMilli() - exchMs
	}
	side := "sell"
	if buySide {
		side = "buy"
	}
	ev := fastBurstEvent{
		RecordedAt: now, Symbol: sym, Side: side,
		Notional1s: tot, BaselineMed: med, Multiple: tot / med, LatencyMs: lat, Px: px,
		MarketLiq5m: fastMarketLiqRate(now, 300),
		SymLiqN30s:  fastSymLiqCount(sym, now, 30),
		XliqN30s:    fastXliqCount(sym, now, 30),
	}
	if err := appendJSONLRecord(det.path, ev); err != nil {
		e.logger.Printf("[fast_burst] 落盘失败: %v", err)
	}
	// v6:入 tape_burst 挂起队列(等官方确认;live 关时下游只记 shadow 不下单)
	fastTapeEnqueue(sym, map[bool]string{true: "long", false: "short"}[buySide], now, tot, tot/med, ev.SymLiqN30s)
}
