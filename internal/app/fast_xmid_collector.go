package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ===== v5.5/v5.6 跨所价格标尺(2026-07-04,record-only)=====
// 头脑风暴①"跨所价差锚"的数据地基:暴动时刻 OKX 与别所的瞬时价差状态,可能是
// "本地流动性事故(套利商必须拽回=接)vs 全局重定价(无人拽=跟)"的方向分类器——
// 场上手被绑得最死的人是套利商,价差一开他们不得不动。
// v5.6 扩到三锚:Bybit(WS 曾验证可用)+ Binance(WS 不通但 REST 通,定价权最重的共识锚)
// + Gate(长尾币覆盖最广,与我们猎场重合度最高)。各 1 请求/3s 批发端点,内存标尺,
// 价差标注进普查/评估事件。不下单/不读账户。

var (
	fastXMidMu sync.Mutex
	fastXMid   = map[string]map[string]fastXMidPt{ // venue -> canon(BTC-USDT) -> mid
		"bybit": {}, "binance": {}, "gate": {},
	}
)

type fastXMidPt struct {
	mid float64
	ts  time.Time
}

func fastXMidStore(venue string, mids map[string]float64, now time.Time) {
	if len(mids) == 0 {
		return
	}
	fastXMidMu.Lock()
	defer fastXMidMu.Unlock()
	m := fastXMid[venue]
	if m == nil {
		m = map[string]fastXMidPt{}
		fastXMid[venue] = m
	}
	for sym, mid := range mids {
		m[sym] = fastXMidPt{mid, now}
	}
}

// fastXMidGapBpsVenue OKX mid 相对某所 mid 的价差(bps,正=OKX 贵);无数据/过期(>15s)返回 (0,false)。
func fastXMidGapBpsVenue(venue, sym string, okxMid float64, now time.Time) (float64, bool) {
	if okxMid <= 0 {
		return 0, false
	}
	fastXMidMu.Lock()
	p, ok := fastXMid[venue][sym]
	fastXMidMu.Unlock()
	if !ok || p.mid <= 0 || now.Sub(p.ts) > 15*time.Second {
		return 0, false
	}
	return (okxMid - p.mid) / p.mid * 10000, true
}

// fastXMidGapBps 兼容入口(v5.5 首日数据=bybit 锚)。
func fastXMidGapBps(sym string, okxMid float64, now time.Time) (float64, bool) {
	return fastXMidGapBpsVenue("bybit", sym, okxMid, now)
}

// ---- 三所批发 tickers 解析(纯函数) ----

// parseBybitTickers /v5/market/tickers?category=linear → canon->mid。
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
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, tk.Last); mid > 0 {
			out[strings.TrimSuffix(raw, "USDT")+"-USDT"] = mid
		}
	}
	return out
}

// parseBinanceBookTickers /fapi/v1/ticker/bookTicker(无参=全市场)→ canon->mid。
func parseBinanceBookTickers(body []byte) map[string]float64 {
	var list []struct {
		Symbol string `json:"symbol"`
		Bid    string `json:"bidPrice"`
		Ask    string `json:"askPrice"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	out := make(map[string]float64, len(list))
	for _, tk := range list {
		raw := strings.ToUpper(tk.Symbol)
		if !strings.HasSuffix(raw, "USDT") {
			continue
		}
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, ""); mid > 0 {
			out[strings.TrimSuffix(raw, "USDT")+"-USDT"] = mid
		}
	}
	return out
}

// parseGateTickers /api/v4/futures/usdt/tickers → canon->mid(contract 形如 BTC_USDT)。
func parseGateTickers(body []byte) map[string]float64 {
	var list []struct {
		Contract string `json:"contract"`
		Bid      string `json:"highest_bid"`
		Ask      string `json:"lowest_ask"`
		Last     string `json:"last"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	out := make(map[string]float64, len(list))
	for _, tk := range list {
		raw := strings.ToUpper(tk.Contract)
		if !strings.HasSuffix(raw, "_USDT") {
			continue
		}
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, tk.Last); mid > 0 {
			out[strings.TrimSuffix(raw, "_USDT")+"-USDT"] = mid
		}
	}
	return out
}

func midFromBidAskLast(bidS, askS, lastS string) float64 {
	bid, _ := strconv.ParseFloat(bidS, 64)
	ask, _ := strconv.ParseFloat(askS, 64)
	if bid > 0 && ask > 0 && ask >= bid {
		return (bid + ask) / 2
	}
	if l, _ := strconv.ParseFloat(lastS, 64); l > 0 {
		return l
	}
	return 0
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
	e.logger.Printf("[fast_xmid] 跨所价格标尺启动 间隔=%ds 锚=bybit+binance+gate(跨所价差,record-only)", iv)
	t := time.NewTicker(time.Duration(iv) * time.Second)
	defer t.Stop()
	fails := map[string]int{}
	srcs := []struct {
		venue string
		url   string
		parse func([]byte) map[string]float64
	}{
		{"bybit", "https://api.bybit.com/v5/market/tickers?category=linear", parseBybitTickers},
		{"binance", "https://fapi.binance.com/fapi/v1/ticker/bookTicker", parseBinanceBookTickers},
		{"gate", "https://api.gateio.ws/api/v4/futures/usdt/tickers", parseGateTickers},
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := e.nowFn().UTC()
			for _, s := range srcs {
				var body json.RawMessage
				err := peerGetJSON(s.url, &body)
				mids := s.parse(body)
				if err != nil || len(mids) == 0 {
					fails[s.venue]++
					if fails[s.venue]%40 == 1 { // 每 ~2 分钟至多一条/所
						e.logger.Printf("[fast_xmid] %s tickers 拉取失败(连续%d): %v", s.venue, fails[s.venue], err)
					}
					continue
				}
				fails[s.venue] = 0
				fastXMidStore(s.venue, mids, now)
			}
		}
	}
}
