package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ===== v5.5 Bybit 全市场价格标尺(2026-07-04,record-only)=====
// 头脑风暴①"跨所价差锚"的数据地基:暴动时刻 OKX 与 Bybit 的瞬时价差状态,可能是
// "本地流动性事故(套利商必须拽回=接)vs 全局重定价(无人拽=跟)"的方向分类器——
// 场上手被绑得最死的人是套利商,价差一开他们不得不动。
// 采集:单请求拉 Bybit 全市场 linear tickers(/v5/market/tickers,一次全量),每 3s 一发,
// 存内存标尺 + 价差标注进普查/评估事件(xgap_bps,正=OKX 溢价)。不下单/不读账户。

var (
	fastXMidMu sync.Mutex
	fastXMid   = map[string]fastXMidPt{} // canon(BTC-USDT) -> bybit mid
)

type fastXMidPt struct {
	mid float64
	ts  time.Time
}

// fastXMidGapBps OKX mid 相对 Bybit mid 的价差(bps,正=OKX 贵);Bybit 无数据或过期(>15s)返回 (0,false)。
func fastXMidGapBps(sym string, okxMid float64, now time.Time) (float64, bool) {
	if okxMid <= 0 {
		return 0, false
	}
	fastXMidMu.Lock()
	p, ok := fastXMid[sym]
	fastXMidMu.Unlock()
	if !ok || p.mid <= 0 || now.Sub(p.ts) > 15*time.Second {
		return 0, false
	}
	return (okxMid - p.mid) / p.mid * 10000, true
}

// parseBybitTickers 纯解析:/v5/market/tickers 响应 → canon->mid。
func parseBybitTickers(body []byte) map[string]float64 {
	var r struct {
		Result struct {
			List []struct {
				Symbol string `json:"symbol"`
				Bid    string `json:"bid1Price"`
				Ask    string `json:"ask1Price"`
				Last   string `json:"lastPrice"`
			} `json:"list"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	out := make(map[string]float64, len(r.Result.List))
	for _, tk := range r.Result.List {
		raw := strings.ToUpper(tk.Symbol)
		if !strings.HasSuffix(raw, "USDT") {
			continue
		}
		bid, _ := strconv.ParseFloat(tk.Bid, 64)
		ask, _ := strconv.ParseFloat(tk.Ask, 64)
		mid := 0.0
		if bid > 0 && ask > 0 && ask >= bid {
			mid = (bid + ask) / 2
		} else if l, _ := strconv.ParseFloat(tk.Last, 64); l > 0 {
			mid = l
		}
		if mid <= 0 {
			continue
		}
		out[strings.TrimSuffix(raw, "USDT")+"-USDT"] = mid
	}
	return out
}

func (e *OKXDirectionalEngine) fastXMidEnabled() bool {
	return e.cfg.FastXMidEnabled && strings.EqualFold(e.venueName, "okx")
}

func (e *OKXDirectionalEngine) fastXMidLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_xmid] loop panic 已恢复: %v", r)
		}
	}()
	iv := e.cfg.FastMarketMidIntervalSeconds
	if iv < 2 {
		iv = 3
	}
	e.logger.Printf("[fast_xmid] Bybit 全市场价格标尺启动 间隔=%ds(跨所价差锚数据地基,record-only)", iv)
	t := time.NewTicker(time.Duration(iv) * time.Second)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var body json.RawMessage
			err := peerGetJSON("https://api.bybit.com/v5/market/tickers?category=linear", &body)
			mids := parseBybitTickers(body)
			if err != nil || len(mids) == 0 {
				fails++
				if fails%20 == 1 {
					e.logger.Printf("[fast_xmid] bybit tickers 拉取失败(连续%d): %v", fails, err)
				}
				continue
			}
			fails = 0
			now := e.nowFn().UTC()
			fastXMidMu.Lock()
			for sym, mid := range mids {
				fastXMid[sym] = fastXMidPt{mid, now}
			}
			fastXMidMu.Unlock()
		}
	}
}
