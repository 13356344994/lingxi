package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ===== v5.5/v5.6/v5.7 跨所信息标尺(2026-07-04,record-only)=====
// v5.5 头脑风暴①"跨所价差锚":暴动时刻价差已开=本地事故(套利商不得不拽回=接),
//      无价差=全局重定价(跟)。v5.6 扩三锚(Bybit/Binance/Gate)。
// v5.7 信息全景(用户"收集一切相关信息"指令):
//   - 免费扩容:Bybit tickers 响应本就带 funding/OI/mark/index(此前一直丢弃),Gate 带
//     funding_rate_indicative/index_price——同一请求零成本提取(拥挤度+燃料+瞬时溢价);
//   - OKX 现货锚:perp 甩开现货=纯杠杆流(回归),现货领跌=信息流(延续)——"接/跟"又一分类器;
//   - OKX 全市场 OI 截面:杠杆堆积价位=强平价埋伏图(燃料地图全市场版);
//   - 5min 快照持久化 fast_xmeta_snapshots.jsonl(funding/OI 是慢变量,5min 粒度够用;
//     热币的秒级 OI 已由热币 WS 覆盖)。
// 全部只读公共端点,不下单/不读账户。

const fastXMetaSnapFile = "fast_xmeta_snapshots.jsonl"
const fastXMetaSnapIntervalSec = 300

var (
	fastXMidMu sync.Mutex
	fastXMid   = map[string]map[string]fastXMidPt{ // venue -> canon(BTC-USDT) -> mid
		"bybit": {}, "binance": {}, "gate": {}, "okxspot": {},
	}
	fastXMetaMu sync.Mutex
	fastXMeta   = map[string]map[string]fastXTickMeta{ // venue -> canon -> 富字段
		"bybit": {}, "gate": {}, "okx": {},
	}
)

type fastXMidPt struct {
	mid float64
	ts  time.Time
}

// fastXTickMeta 富字段(0=该所该字段无数据)。
type fastXTickMeta struct {
	Funding     float64 // 当期资金费率
	OI          float64 // 持仓量(张/币,所内口径,横向只看变化率)
	Index       float64 // 指数价
	Mark        float64 // 标记价
	NextFunding int64   // 下次资费结算毫秒时间戳(结算倒计时假说 #8 的原料)
	TS          time.Time
}

func fastXMidStore(venue string, mids map[string]float64, now time.Time) {
	if len(mids) == 0 {
		return
	}
	fastXMidMu.Lock()
	m := fastXMid[venue]
	if m == nil {
		m = map[string]fastXMidPt{}
		fastXMid[venue] = m
	}
	for sym, mid := range mids {
		m[sym] = fastXMidPt{mid, now}
	}
	fastXMidMu.Unlock()
	// v5.8 锚历史入滚动窗(300s):赢家解剖需要"运动开始之前"的锚状态——
	// 检测时刻的价差已被运动本身污染,预测性检验必须用前置测量(信息集对齐,对分组条件同样适用)。
	for sym, mid := range mids {
		fastAppendMidHist(venue+"|"+sym, now, mid)
	}
}

func fastXMetaStore(venue string, metas map[string]fastXTickMeta) {
	if len(metas) == 0 {
		return
	}
	fastXMetaMu.Lock()
	defer fastXMetaMu.Unlock()
	m := fastXMeta[venue]
	if m == nil {
		m = map[string]fastXTickMeta{}
		fastXMeta[venue] = m
	}
	for sym, meta := range metas {
		m[sym] = meta
	}
}

// fastOKXOINotional 该币 OKX OI 名义(张×mid;30s REST 轮询,≤5min 新鲜度)。
// v6.3 选择层"占市值比"的分母;单位口径与离线研究(hot_micro oi_now×mid)一致。
func fastOKXOINotional(sym string, mid float64, now time.Time) (float64, bool) {
	if mid <= 0 {
		return 0, false
	}
	fastXMetaMu.Lock()
	defer fastXMetaMu.Unlock()
	meta, ok := fastXMeta["okx"][sym]
	if !ok || meta.OI <= 0 || now.Sub(meta.TS) > 5*time.Minute {
		return 0, false
	}
	return meta.OI * mid, true
}

// fastXMidGapBpsVenueTol 同下,自定义新鲜度容忍(E策略冻结口径=8s,与离线回放一致)。
func fastXMidGapBpsVenueTol(venue, sym string, okxMid float64, now time.Time, tol time.Duration) (float64, bool) {
	if okxMid <= 0 {
		return 0, false
	}
	fastXMidMu.Lock()
	defer fastXMidMu.Unlock()
	p, ok := fastXMid[venue][sym]
	if !ok || p.mid <= 0 || now.Sub(p.ts) > tol {
		return 0, false
	}
	return (okxMid - p.mid) / p.mid * 10000, true
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

// ---- 批发 tickers 解析(纯函数) ----

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

func pf(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// parseBybitTickers /v5/market/tickers?category=linear → mids + 富字段(funding/OI/mark/index 免费提取)。
func parseBybitTickers(body []byte, now time.Time) (map[string]float64, map[string]fastXTickMeta) {
	var r struct {
		Result struct {
			List []struct {
				Symbol  string `json:"symbol"`
				Bid     string `json:"bid1Price"`
				Ask     string `json:"ask1Price"`
				Last    string `json:"lastPrice"`
				Funding string `json:"fundingRate"`
				OI      string `json:"openInterest"`
				Index   string `json:"indexPrice"`
				Mark    string `json:"markPrice"`
				NextFT  string `json:"nextFundingTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, nil
	}
	mids := make(map[string]float64, len(r.Result.List))
	metas := make(map[string]fastXTickMeta, len(r.Result.List))
	for _, tk := range r.Result.List {
		raw := strings.ToUpper(tk.Symbol)
		if !strings.HasSuffix(raw, "USDT") {
			continue
		}
		canon := strings.TrimSuffix(raw, "USDT") + "-USDT"
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, tk.Last); mid > 0 {
			mids[canon] = mid
		}
		if f, oi := pf(tk.Funding), pf(tk.OI); f != 0 || oi > 0 {
			nft, _ := strconv.ParseInt(strings.TrimSpace(tk.NextFT), 10, 64)
			metas[canon] = fastXTickMeta{Funding: f, OI: oi, Index: pf(tk.Index), Mark: pf(tk.Mark), NextFunding: nft, TS: now}
		}
	}
	return mids, metas
}

// parseBinanceBookTickers /fapi/v1/ticker/bookTicker(全市场)→ mids。
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

// parseGateTickers /api/v4/futures/usdt/tickers → mids + 富字段(funding_rate_indicative/index_price)。
func parseGateTickers(body []byte, now time.Time) (map[string]float64, map[string]fastXTickMeta) {
	var list []struct {
		Contract string `json:"contract"`
		Bid      string `json:"highest_bid"`
		Ask      string `json:"lowest_ask"`
		Last     string `json:"last"`
		Funding  string `json:"funding_rate_indicative"`
		Index    string `json:"index_price"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil, nil
	}
	mids := make(map[string]float64, len(list))
	metas := make(map[string]fastXTickMeta, len(list))
	for _, tk := range list {
		raw := strings.ToUpper(tk.Contract)
		if !strings.HasSuffix(raw, "_USDT") {
			continue
		}
		canon := strings.TrimSuffix(raw, "_USDT") + "-USDT"
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, tk.Last); mid > 0 {
			mids[canon] = mid
		}
		if f := pf(tk.Funding); f != 0 {
			metas[canon] = fastXTickMeta{Funding: f, Index: pf(tk.Index), TS: now}
		}
	}
	return mids, metas
}

// parseOKXSpotTickers /api/v5/market/tickers?instType=SPOT → USDT 现货 mids(现货锚)。
func parseOKXSpotTickers(body []byte) map[string]float64 {
	var r struct {
		Data []struct {
			InstID string `json:"instId"`
			Bid    string `json:"bidPx"`
			Ask    string `json:"askPx"`
			Last   string `json:"last"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	out := make(map[string]float64, len(r.Data))
	for _, tk := range r.Data {
		raw := strings.ToUpper(tk.InstID)
		if !strings.HasSuffix(raw, "-USDT") {
			continue
		}
		if mid := midFromBidAskLast(tk.Bid, tk.Ask, tk.Last); mid > 0 {
			out[raw] = mid
		}
	}
	return out
}

// parseOKXOpenInterest /api/v5/public/open-interest?instType=SWAP → canon -> OI(张)。
func parseOKXOpenInterest(body []byte, now time.Time) map[string]fastXTickMeta {
	var r struct {
		Data []struct {
			InstID string `json:"instId"`
			OI     string `json:"oi"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	out := make(map[string]fastXTickMeta, len(r.Data))
	for _, d := range r.Data {
		up := strings.ToUpper(d.InstID)
		if !strings.HasSuffix(up, "-USDT-SWAP") {
			continue
		}
		if oi := pf(d.OI); oi > 0 {
			out[strings.TrimSuffix(up, "-SWAP")] = fastXTickMeta{OI: oi, TS: now}
		}
	}
	return out
}

func (e *OKXDirectionalEngine) fastXMidEnabled() bool {
	return e.cfg.FastXMidEnabled && strings.EqualFold(e.venueName, "okx")
}

// fastXMetaSnapshotRow 5min 快照行:funding(6dp)/OI(原始)按所分组;离线做燃料地图/拥挤度研究。
type fastXMetaSnapshotRow struct {
	RecordedAt time.Time                       `json:"recorded_at"`
	Venues     map[string]map[string][]float64 `json:"venues"` // venue -> canon -> [funding, oi]
}

func fastXMetaSnapshot(now time.Time) fastXMetaSnapshotRow {
	row := fastXMetaSnapshotRow{RecordedAt: now, Venues: map[string]map[string][]float64{}}
	fastXMetaMu.Lock()
	defer fastXMetaMu.Unlock()
	for venue, m := range fastXMeta {
		if len(m) == 0 {
			continue
		}
		vm := make(map[string][]float64, len(m))
		for sym, meta := range m {
			if now.Sub(meta.TS) > 10*time.Minute {
				continue
			}
			// [funding, oi, next_funding_ms](第三元素 0=该所不提供;离线读端按变长处理)
			vm[sym] = []float64{meta.Funding, meta.OI, float64(meta.NextFunding)}
		}
		if len(vm) > 0 {
			row.Venues[venue] = vm
		}
	}
	return row
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
	e.logger.Printf("[fast_xmid] 跨所信息标尺启动 间隔=%ds 价锚=bybit+binance+gate+okx现货 富字段=funding/OI 快照=%ds(record-only)", iv, fastXMetaSnapIntervalSec)
	t := time.NewTicker(time.Duration(iv) * time.Second)
	defer t.Stop()
	fails := map[string]int{}
	snapPath := filepath.Join(e.recordDir, fastXMetaSnapFile)
	lastSnap := time.Time{}
	lastOIPoll := time.Time{}
	logFail := func(venue string, err error) {
		fails[venue]++
		if fails[venue]%40 == 1 { // 每 ~2 分钟至多一条/所
			e.logger.Printf("[fast_xmid] %s 拉取失败(连续%d): %v", venue, fails[venue], err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := e.nowFn().UTC()
			// bybit(mids+富字段)
			var body json.RawMessage
			if err := peerGetJSON("https://api.bybit.com/v5/market/tickers?category=linear", &body); err != nil {
				logFail("bybit", err)
			} else if mids, metas := parseBybitTickers(body, now); len(mids) > 0 {
				fails["bybit"] = 0
				fastXMidStore("bybit", mids, now)
				fastXMetaStore("bybit", metas)
			}
			// binance(仅 mids)
			if err := peerGetJSON("https://fapi.binance.com/fapi/v1/ticker/bookTicker", &body); err != nil {
				logFail("binance", err)
			} else if mids := parseBinanceBookTickers(body); len(mids) > 0 {
				fails["binance"] = 0
				fastXMidStore("binance", mids, now)
			}
			// gate(mids+富字段)
			if err := peerGetJSON("https://api.gateio.ws/api/v4/futures/usdt/tickers", &body); err != nil {
				logFail("gate", err)
			} else if mids, metas := parseGateTickers(body, now); len(mids) > 0 {
				fails["gate"] = 0
				fastXMidStore("gate", mids, now)
				fastXMetaStore("gate", metas)
			}
			// okx 现货锚(现货 vs perp 基差=杠杆流/信息流分类器)
			if err := peerGetJSON("https://www.okx.com/api/v5/market/tickers?instType=SPOT", &body); err != nil {
				logFail("okxspot", err)
			} else if mids := parseOKXSpotTickers(body); len(mids) > 0 {
				fails["okxspot"] = 0
				fastXMidStore("okxspot", mids, now)
			}
			// okx 全市场 OI(30s 一发;燃料地图全市场版)
			if now.Sub(lastOIPoll) >= 30*time.Second {
				lastOIPoll = now
				if err := peerGetJSON("https://www.okx.com/api/v5/public/open-interest?instType=SWAP", &body); err != nil {
					logFail("okxoi", err)
				} else if metas := parseOKXOpenInterest(body, now); len(metas) > 0 {
					fails["okxoi"] = 0
					fastXMetaStore("okx", metas)
				}
			}
			// 5min 快照落盘(funding/OI 慢变量;热币秒级 OI 由热币 WS 覆盖)
			if now.Sub(lastSnap) >= fastXMetaSnapIntervalSec*time.Second {
				lastSnap = now
				row := fastXMetaSnapshot(now)
				if len(row.Venues) > 0 {
					if werr := appendJSONLRecord(snapPath, row); werr != nil {
						e.logger.Printf("[fast_xmid] 快照落盘失败: %v", werr)
					}
				}
			}
		}
	}
}
