package app

import (
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ===== Layer A 粒子账本 v0(2026-07-07 粒子化宪法;record-only)=====
// 用户架构:粒子级协程——粒子每事件只算一次,组合=账本上的订阅(bitmask join),
// 数量对算力自由。本文件是它的进程内 v0:E 模块触发点(全市场标尺 3s 逐币价差检查命中
// entry_gap 且过同币冷却)逐事件评估粒子位,虚拟持仓按 E 出场规则(收敛/到时/灾难)结账,
// 完成行写 particle_ledger.jsonl(小文件)——阅卷/回放从此读账本,不再重解析 GB 级原始流;
// 规则单一来源:粒子函数在 Go,实枪未来加闸(新鲜度/派班)复用同一函数,消灭 Python/Go 口径漂移。
// 零交易影响:不下单、不读账户;开关 fast_particle_ledger_enabled 护栏锚定。

const fastParticleLedgerFile = "particle_ledger.jsonl"

// 粒子位(位序冻结——账本是数据,位序漂移=作弊;只许追加不许重排)
const (
	pbFresh     = 1 << iota // 新鲜度:回看1800s价差出现过 >-20bps(可算点≥10,fail-closed)
	pbCalmFuel              // 州:燃料 5min 爆仓 <40(非风暴)
	pbBreadthOK             // 州:全市场 30min 收益中位 >-15bps(非广跌;不可算=0)
	pbDepth4060             // 触发深度档:-60 < gap ≤ -40
	pbDepthGE60             // 触发深度档:gap ≤ -60
	pbNonStock              // 币种:非股票 perp
	pbHabitual              // 惯犯:本次之前 24h 内同币触发 ≥3 次
	pbDutyXgap              // 派班合成位:calm ∧ breadthOK(冗余存储便于账本直读)
	// v1 追加(REST 入场行专属;㉝残余带发现的机器原生化):
	pbResidual2040          // REST 残余价差 ∈ [-40,-20](执行层唯一活区,预注册 12:10Z)
)

// fastLedgerStockPerps 股票 perp 名单(粒子 pbNonStock 用;静态集=标的性质,非黑名单)。
var fastLedgerStockPerps = map[string]bool{
	"SAMSUNG-USDT": true, "SKHYNIX-USDT": true, "SNDK-USDT": true, "AAPL-USDT": true,
	"SOXL-USDT": true, "TSLA-USDT": true, "NVDA-USDT": true, "MSTR-USDT": true,
	"COIN-USDT": true, "CRCL-USDT": true, "HOOD-USDT": true, "META-USDT": true,
	"AMZN-USDT": true, "GOOGL-USDT": true, "ORCL-USDT": true, "MCD-USDT": true,
}

// ---- 价差历史环(新鲜度粒子的数据地板:60s 降采样,1800s 窗)----
type fastGapPt struct {
	ts  time.Time
	gap float64
}

var (
	fastGapHistMu   sync.Mutex
	fastGapHist     = map[string][]fastGapPt{}
	fastGapHistLast = map[string]time.Time{}
)

// fastNoteGapHist 标尺循环逐币调用(有新鲜锚时);60s/币 降采样,窗 1800s。
func fastNoteGapHist(sym string, now time.Time, gapBps float64) {
	fastGapHistMu.Lock()
	defer fastGapHistMu.Unlock()
	if last, ok := fastGapHistLast[sym]; ok && now.Sub(last) < 60*time.Second {
		return
	}
	fastGapHistLast[sym] = now
	h := append(fastGapHist[sym], fastGapPt{now, gapBps})
	cut := now.Add(-1800 * time.Second)
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastGapHist[sym] = h[i:]
	if len(fastGapHist) > 4096 {
		for k, v := range fastGapHist {
			if len(v) == 0 || now.Sub(v[len(v)-1].ts) > time.Hour {
				delete(fastGapHist, k)
				delete(fastGapHistLast, k)
			}
		}
	}
}

// fastGapFresh 新鲜度粒子(纯读):回看窗内可算点≥minPts 且出现过 > freshBps。
// fail-closed:点数不足=不新鲜(与 E-v2 判决口径一致;实枪加闸时复用本函数=单一来源)。
func fastGapFresh(sym string, now time.Time, freshBps float64, minPts int) bool {
	fastGapHistMu.Lock()
	defer fastGapHistMu.Unlock()
	h := fastGapHist[sym]
	cut := now.Add(-1800 * time.Second)
	n, seen := 0, false
	for _, p := range h {
		if p.ts.Before(cut) || !p.ts.Before(now) {
			continue
		}
		n++
		if p.gap > freshBps {
			seen = true
		}
	}
	return n >= minPts && seen
}

// ---- 广度(州粒子;标尺循环每 60s 重算一次,读 fastRangeHist 的 30min 环)----
var (
	fastBreadthMu sync.Mutex
	fastBreadthV  float64
	fastBreadthAt time.Time
)

// fastBreadthRecalc 由标尺循环节流调用:全市场 30min 收益中位(样本<100 → 不可算)。
func fastBreadthRecalc(now time.Time) {
	rets := make([]float64, 0, 1024)
	fastRangeMu.Lock()
	for _, h := range fastRangeHist {
		if len(h) < 2 {
			continue
		}
		oldest, newest := h[0], h[len(h)-1]
		if newest.ts.Sub(oldest.ts) < 1500*time.Second || oldest.mid <= 0 {
			continue
		}
		rets = append(rets, (newest.mid-oldest.mid)/oldest.mid*1e4)
	}
	fastRangeMu.Unlock()
	fastBreadthMu.Lock()
	defer fastBreadthMu.Unlock()
	if len(rets) < 100 {
		fastBreadthAt = time.Time{} // 不可算(fail-closed)
		return
	}
	sort.Float64s(rets)
	fastBreadthV = rets[len(rets)/2]
	fastBreadthAt = now
}

// fastBreadth30 返回 (中位bps, 可算?);超过 5min 未更新视为不可算。
func fastBreadth30(now time.Time) (float64, bool) {
	fastBreadthMu.Lock()
	defer fastBreadthMu.Unlock()
	if fastBreadthAt.IsZero() || now.Sub(fastBreadthAt) > 5*time.Minute {
		return 0, false
	}
	return fastBreadthV, true
}

// ---- 惯犯环(24h 触发登记)----
var (
	fastLedgerTrigMu sync.Mutex
	fastLedgerTrig   = map[string][]time.Time{}
)

// fastLedgerTrigCountThenNote 返回本次之前 24h 触发数,并登记本次(评估在登记前=无前视)。
func fastLedgerTrigCountThenNote(sym string, now time.Time) int {
	fastLedgerTrigMu.Lock()
	defer fastLedgerTrigMu.Unlock()
	cut := now.Add(-24 * time.Hour)
	h := fastLedgerTrig[sym]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	n := len(h)
	fastLedgerTrig[sym] = append(h, now)
	return n
}

// fastParticleBits 粒子评估(每事件恰一次;各粒子独立可测)。
func fastParticleBits(sym string, now time.Time, gapBps, fuel5m float64, breadth float64, breadthOK bool, prevTrigs int) int {
	bits := 0
	if fastGapFresh(sym, now, -20, 10) {
		bits |= pbFresh
	}
	calm := fuel5m < 40
	if calm {
		bits |= pbCalmFuel
	}
	bok := breadthOK && breadth > -15
	if bok {
		bits |= pbBreadthOK
	}
	if gapBps <= -60 {
		bits |= pbDepthGE60
	} else if gapBps <= -40 {
		bits |= pbDepth4060
	}
	if !fastLedgerStockPerps[sym] {
		bits |= pbNonStock
	}
	if prevTrigs >= 3 {
		bits |= pbHabitual
	}
	if calm && bok {
		bits |= pbDutyXgap
	}
	return bits
}

// ---- 虚拟持仓与结账 ----
type fastLedgerPos struct {
	Sym      string
	Src      string // det=探测mid(v0) / rest=REST mid可执行口径(v1) / mkr=maker挂单口径(v2,头号曲线测量仪)
	T0       time.Time // 触发时刻(mkr:挂单时刻)
	FilledAt time.Time // mkr 专属:成交时刻(零值=仍挂着)
	Entry    float64   // mkr:挂单价(REST bid),成交后即入场价
	GapDet   float64
	GapRest  float64 // rest/mkr:REST 复核时刻残余价差
	SpreadBps float64 // rest/mkr:REST 盘口点差
	Bits     int
	Fuel     float64
	Breadth  float64
	PeakBps  float64
}

// maker 口径冻结先验(v2,2026-07-07 曲线救国方针):
//   挂单价=REST bid;成交判定=后续 mid ≤ 挂单价(mid 跌到我们的 bid=几乎必然成交,保守计成交);
//   挂单超时 120s 未成交=撤单(记 unfilled 行,net=0,量"错过的收敛");
//   成本=9bps(maker进2+taker出5+滑2) vs rest 口径 12。裁决问题:逆选择税是否 < 省下的 3-6bps。
const (
	fastLedgerMakerTimeoutSec = 120.0
	fastLedgerMakerCostBps    = 9.0
)

type fastLedgerRow struct {
	Time      time.Time `json:"time"` // 出场时刻
	Symbol    string    `json:"symbol"`
	Src       string    `json:"src"` // det / rest(可执行口径)
	T0        time.Time `json:"t0"`
	GapDetBps float64   `json:"gap_det_bps"`
	GapRestBps float64  `json:"gap_rest_bps,omitempty"` // rest 行:REST残余价差
	SpreadBps  float64  `json:"spread_bps,omitempty"`   // rest 行:REST盘口点差
	Bits      int       `json:"bits"`
	Fuel5m    float64   `json:"fuel_5m"`
	Breadth30 float64   `json:"breadth_30m"`
	NetBps    float64   `json:"net_bps"` // 含成本12
	GrossBps  float64   `json:"gross_bps"`
	PeakBps   float64   `json:"peak_bps"`
	HoldSec   int       `json:"hold_sec"`
	Exit      string    `json:"exit"` // converged / max_hold / disaster
}

var (
	fastLedgerMu        sync.Mutex
	fastLedgerOpen      = map[string]*fastLedgerPos{} // sym -> 虚拟仓(每币一仓,与实枪同约束)
	fastLedgerBreadthAt time.Time                     // 广度重算节流(标尺循环单goroutine访问,无竞态)
)

const fastLedgerCostBps = 12.0

// fastLedgerExit 纯判定:虚拟仓此刻是否出场("");与 E 出场规则同口径。
func fastLedgerExit(retBps float64, gapNow float64, gapOK bool, holdSec float64, exitGap, disaster float64, maxHold int) string {
	if retBps <= -disaster {
		return "disaster"
	}
	if gapOK && gapNow >= -exitGap {
		return "converged"
	}
	if holdSec >= float64(maxHold) {
		return "max_hold"
	}
	return ""
}

// fastLedgerOnTrigger E 触发点调用(标尺循环内,与 fastXgapCheck 同点位;冷却已过)。src=det。
func (e *OKXDirectionalEngine) fastLedgerOnTrigger(sym string, okxMid, gapBps float64, now time.Time) {
	if !e.cfg.FastParticleLedgerEnabled || okxMid <= 0 {
		return
	}
	key := "det|" + sym
	fastLedgerMu.Lock()
	if _, held := fastLedgerOpen[key]; held {
		fastLedgerMu.Unlock()
		return
	}
	fastLedgerMu.Unlock()
	prev := fastLedgerTrigCountThenNote(sym, now)
	fuel := fastMarketLiqRate(now, 300)
	br, brOK := fastBreadth30(now)
	bits := fastParticleBits(sym, now, gapBps, fuel, br, brOK, prev)
	fastLedgerMu.Lock()
	fastLedgerOpen[key] = &fastLedgerPos{Sym: sym, Src: "det", T0: now, Entry: okxMid, GapDet: gapBps, Bits: bits, Fuel: fuel, Breadth: br}
	fastLedgerMu.Unlock()
}

// fastLedgerOnTriggerREST v1 可执行口径:影子线 REST 建档通过守卫后调用(fastXgapFire 内)。
// 入场=REST 时刻 mid(与㉝执行层证据包同口径,含 144ms 延迟后的真实价格);残余带粒子在此评估。
// 惯犯环不重复登记(det 触发已记),粒子重新评估(REST 时刻的州可能已变)。
func (e *OKXDirectionalEngine) fastLedgerOnTriggerREST(sym string, restMid, restBid, spreadBps, gapDet, gapRest float64, now time.Time) {
	if !e.cfg.FastParticleLedgerEnabled || restMid <= 0 {
		return
	}
	key := "rest|" + sym
	fastLedgerMu.Lock()
	if _, held := fastLedgerOpen[key]; held {
		fastLedgerMu.Unlock()
		return
	}
	fastLedgerMu.Unlock()
	fuel := fastMarketLiqRate(now, 300)
	br, brOK := fastBreadth30(now)
	prev := 0
	fastLedgerTrigMu.Lock()
	cut := now.Add(-24 * time.Hour)
	for _, t := range fastLedgerTrig[sym] {
		if !t.Before(cut) {
			prev++
		}
	}
	fastLedgerTrigMu.Unlock()
	if prev > 0 {
		prev-- // det 触发刚登记过本次,剔除自身
	}
	bits := fastParticleBits(sym, now, gapDet, fuel, br, brOK, prev)
	if gapRest >= -40 && gapRest <= -20 {
		bits |= pbResidual2040
	}
	fastLedgerMu.Lock()
	fastLedgerOpen[key] = &fastLedgerPos{Sym: sym, Src: "rest", T0: now, Entry: restMid,
		GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br}
	// v2 maker 口径并行:同一触发点挂虚拟被动单(价=REST bid),成交/超时由 manage 裁
	if restBid > 0 {
		if _, held := fastLedgerOpen["mkr|"+sym]; !held {
			fastLedgerOpen["mkr|"+sym] = &fastLedgerPos{Sym: sym, Src: "mkr", T0: now, Entry: restBid,
				GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br}
		}
	}
	fastLedgerMu.Unlock()
}

// fastLedgerManage 标尺循环每轮调用(books=本轮全市场盘口);虚拟仓按 E 规则结账落盘。
func (e *OKXDirectionalEngine) fastLedgerManage(mids map[string]float64, now time.Time) {
	if !e.cfg.FastParticleLedgerEnabled {
		return
	}
	fastLedgerMu.Lock()
	open := make([]*fastLedgerPos, 0, len(fastLedgerOpen))
	for _, p := range fastLedgerOpen {
		open = append(open, p)
	}
	fastLedgerMu.Unlock()
	path := filepath.Join(e.recordDir, fastParticleLedgerFile)
	for _, p := range open {
		mid, ok := mids[p.Sym]
		if !ok || mid <= 0 {
			continue // 本轮无价,下轮再看(600s 上限兜底)
		}
		// mkr 挂单态:先裁成交/超时,成交后才进入持有态
		if p.Src == "mkr" && p.FilledAt.IsZero() {
			if mid <= p.Entry { // mid 跌到挂单价=保守计成交
				p.FilledAt = now
			} else if now.Sub(p.T0).Seconds() >= fastLedgerMakerTimeoutSec {
				row := fastLedgerRow{Time: now, Symbol: p.Sym, Src: p.Src, T0: p.T0,
					GapDetBps: p.GapDet, GapRestBps: p.GapRest, SpreadBps: p.SpreadBps, Bits: p.Bits,
					Fuel5m: p.Fuel, Breadth30: p.Breadth, HoldSec: int(now.Sub(p.T0).Seconds()), Exit: "unfilled"}
				if err := appendJSONLRecord(path, row); err != nil {
					e.logger.Printf("[particle_ledger] 落盘失败: %v", err)
				}
				fastLedgerMu.Lock()
				delete(fastLedgerOpen, "mkr|"+p.Sym)
				fastLedgerMu.Unlock()
			}
			continue
		}
		ret := (mid - p.Entry) / p.Entry * 1e4
		if ret > p.PeakBps {
			p.PeakBps = ret
		}
		gapNow, gapOK := fastXMidGapBpsVenueTol("bybit", p.Sym, mid, now, fastXgapFreshTol)
		holdFrom := p.T0
		if p.Src == "mkr" {
			holdFrom = p.FilledAt // maker 持有期从成交起算
		}
		hold := now.Sub(holdFrom).Seconds()
		exit := fastLedgerExit(ret, gapNow, gapOK, hold, e.cfg.FastXgapExitGapBps, e.cfg.FastXgapDisasterBps, e.cfg.FastXgapMaxHoldSeconds)
		if exit == "" {
			continue
		}
		cost := fastLedgerCostBps
		if p.Src == "mkr" {
			cost = fastLedgerMakerCostBps
		}
		row := fastLedgerRow{
			Time: now, Symbol: p.Sym, Src: p.Src, T0: p.T0,
			GapDetBps: p.GapDet, GapRestBps: p.GapRest, SpreadBps: p.SpreadBps, Bits: p.Bits,
			Fuel5m: p.Fuel, Breadth30: p.Breadth,
			GrossBps: ret, NetBps: ret - cost, PeakBps: p.PeakBps,
			HoldSec: int(hold), Exit: exit,
		}
		if err := appendJSONLRecord(path, row); err != nil {
			e.logger.Printf("[particle_ledger] 落盘失败: %v", err)
		}
		fastLedgerMu.Lock()
		delete(fastLedgerOpen, p.Src+"|"+p.Sym)
		fastLedgerMu.Unlock()
	}
}
