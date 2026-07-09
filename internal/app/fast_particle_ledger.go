package app

import (
	"math"
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
	pbResidual2040 // REST 残余价差 ∈ [-40,-20](执行层唯一活区,预注册 12:10Z)
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
	Sym       string
	Module    string    // ""/"e"=E模块 / "fire"=火刀模块(2026-07-08 多模块化v0:把整座迷宫接上电)
	Side      string    // ""/"long"(E) / "short"(fire=顺卖方burst做空)
	Src       string    // det=探测mid(v0) / rest=REST mid可执行口径(v1) / mkr=maker挂单口径(v2,头号曲线测量仪)
	T0        time.Time // 触发时刻(mkr:挂单时刻)
	FilledAt  time.Time // mkr 专属:成交时刻(零值=仍挂着)
	Entry     float64   // mkr:挂单价(REST bid),成交后即入场价
	GapDet    float64
	GapRest   float64 // rest/mkr:REST 复核时刻残余价差
	SpreadBps float64 // rest/mkr:REST 盘口点差
	Bits      int
	Fuel      float64
	Breadth   float64
	PeakBps   float64
	// ㊱曲线② burst弹射反事实标注(零行为改动:只记不弹,弹不弹由数据裁):
	BurstSeen  bool
	RetAtBurst float64 // 首见同币卖方burst时的浮盈bps(3s采样近似)
	// ㊳① det 时刻价差路径信息集(生龄/速度/归因;Go 账本=判决口径,分桶直读)
	Path fastGapPathStats
	// 杠杆L5 棘轮反事实(2026-07-08:固定TP剪肥尾全灭[PF≤0.34],峰值近似棘轮PF2.58=上界,
	// 须逐tick精确实测):峰值≥+10 激活后回吐 8bps 即记虚拟棘轮出场(只记不出;冻结先验 A=10/G=8)。
	TrailFired  bool
	TrailExitBp float64
	// 07-09 烟花惯犯计数(走前:入场时刻此前24h同币烟花触发数;holdout 校验过的否决粒子)
	PriorFw24h int
	// 07-09 烟花三特征粒子数据地板(K折过检组合:非惯犯×完全力竭×无本币爆仓)
	FwRet15    float64
	FwRet60    float64
	FwSymLiq5m int
}

const (
	fastLedgerTrailActBps  = 10.0 // 冻结先验(与现役 FastV3TrailActivate 同族刻度)
	fastLedgerTrailGiveBps = 8.0
)

// maker 口径冻结先验(v2,2026-07-07 曲线救国方针):
//
//	挂单价=REST bid;成交判定=后续 mid ≤ 挂单价(mid 跌到我们的 bid=几乎必然成交,保守计成交);
//	挂单超时 120s 未成交=撤单(记 unfilled 行,net=0,量"错过的收敛");
//	成本=9bps(maker进2+taker出5+滑2) vs rest 口径 12。裁决问题:逆选择税是否 < 省下的 3-6bps。
const (
	fastLedgerMakerTimeoutSec = 120.0
	fastLedgerMakerCostBps    = 9.0
)

type fastLedgerRow struct {
	Time       time.Time `json:"time"` // 出场时刻
	Symbol     string    `json:"symbol"`
	Module     string    `json:"module,omitempty"` // 空=E / fire=火刀
	Side       string    `json:"side,omitempty"`   // 空=long(E) / short(fire)
	Src        string    `json:"src"` // det / rest(可执行口径)
	T0         time.Time `json:"t0"`
	GapDetBps  float64   `json:"gap_det_bps"`
	GapRestBps float64   `json:"gap_rest_bps,omitempty"` // rest 行:REST残余价差
	SpreadBps  float64   `json:"spread_bps,omitempty"`   // rest 行:REST盘口点差
	Bits       int       `json:"bits"`
	Fuel5m     float64   `json:"fuel_5m"`
	Breadth30  float64   `json:"breadth_30m"`
	NetBps     float64   `json:"net_bps"` // 含成本12
	GrossBps   float64   `json:"gross_bps"`
	PeakBps    float64   `json:"peak_bps"`
	HoldSec    int       `json:"hold_sec"`
	Exit       string    `json:"exit"` // converged / max_hold / disaster / unfilled(mkr)
	// burst弹射反事实(㊱曲线②):持仓中出现同币卖方burst=瀑布砸向多头;
	// 若弹射粒子成立,ret_at_burst 与最终 gross 的差=每笔可省的钱。
	BurstDuringHold bool    `json:"burst_during_hold,omitempty"`
	RetAtBurstBps   float64 `json:"ret_at_burst_bps,omitempty"`
	// 杠杆L5 棘轮反事实(逐tick;A=10/G=8 冻结):fired=持有期触发过,exit_bps=触发时刻毛值
	TrailFired   bool    `json:"trail_fired,omitempty"`
	TrailExitBps float64 `json:"trail_exit_bps,omitempty"`
	// 07-09 烟花惯犯计数(走前口径;holdout 校验过的否决粒子的数据地板)
	PriorFw24h int `json:"prior_fw_24h,omitempty"`
	// 07-09 烟花三特征(入场时刻冻结;fw_pure 考试口径)
	FwRet15Bps float64 `json:"fw_ret15_bps,omitempty"`
	FwRet60Bps float64 `json:"fw_ret60_bps,omitempty"`
	FwSymLiq5m int     `json:"fw_sym_liq_5m,omitempty"`
	// ㊳① det 时刻价差路径(path_ok=false 表示 15s 历史不足,路径字段不可用):
	PathOK            bool    `json:"path_ok,omitempty"`
	GapAgeSec         float64 `json:"gap_age_sec,omitempty"`
	GapVel3Bps        float64 `json:"gap_vel3_bps,omitempty"`
	GapVel15Bps       float64 `json:"gap_vel15_bps,omitempty"`
	GapOkxRet15Bps    float64 `json:"gap_okx_ret15_bps,omitempty"`
	GapAnchorRet15Bps float64 `json:"gap_anchor_ret15_bps,omitempty"`
}

// applyGapPath 把 det 时刻路径信息集写入账本行(path.OK=false 只标不可算,不填数)。
func (r *fastLedgerRow) applyGapPath(path fastGapPathStats) {
	if !path.OK {
		return
	}
	r.PathOK = true
	r.GapAgeSec = path.AgeSec
	r.GapVel3Bps = path.Vel3Bps
	r.GapVel15Bps = path.Vel15Bps
	r.GapOkxRet15Bps = path.OkxRet15Bps
	r.GapAnchorRet15Bps = path.AnchorRet15Bps
}

var (
	fastLedgerMu        sync.Mutex
	fastLedgerOpen      = map[string]*fastLedgerPos{} // sym -> 虚拟仓(每币一仓,与实枪同约束)
	fastLedgerBreadthAt time.Time                     // 广度重算节流(标尺循环单goroutine访问,无竞态)
)

const fastLedgerCostBps = 12.0

// fastSymLiqBetween 该币官方爆仓是否落在 (a, b] 内(火刀二波止错的论点证据)。
func fastSymLiqBetween(sym string, a, b time.Time) bool {
	fastLiqBufMu.Lock()
	defer fastLiqBufMu.Unlock()
	for _, l := range fastLiqBuf[sym] {
		if l.ts.After(a) && !l.ts.After(b) {
			return true
		}
	}
	return false
}

// fastLedgerFireExit 纯判定:火刀模块虚拟仓出场(程序S,冻结口径:trail激活10/回吐20/灾难40/
// 二波止错t0+20s无接力/cap120s)。ret=空头口径浮盈(价格下跌为正)。
func fastLedgerFireExit(ret, peak, holdSec, sinceT0Sec float64, wave2 bool) string {
	if ret <= -40 {
		return "disaster"
	}
	if peak >= 10 && ret <= peak-20 {
		return "trail"
	}
	if !wave2 && sinceT0Sec >= 20 {
		return "no_second_wave"
	}
	if holdSec >= 120 {
		return "max_hold"
	}
	return ""
}

// fastLedgerOnFireConfirm 火刀模块入账(2026-07-08 多模块化v0):tape 确认通过全部闸门的
// 卖方burst(=火灾),按确认时刻 mid 开虚拟空仓;出场=程序S。record-only,把火刀格点接进织网。
func (e *OKXDirectionalEngine) fastLedgerOnFireConfirm(sym string, mid, spreadBps float64, now time.Time) {
	if !e.cfg.FastParticleLedgerEnabled || mid <= 0 {
		return
	}
	key := "fire|rest|" + sym
	fastLedgerMu.Lock()
	if _, held := fastLedgerOpen[key]; held {
		fastLedgerMu.Unlock()
		return
	}
	fastLedgerMu.Unlock()
	fuel := fastMarketLiqRate(now, 300)
	br, brOK := fastBreadth30(now)
	bits := fastParticleBits(sym, now, 0, fuel, br, brOK, 0) // 州位有效;深度/惯犯位对fire无义=0
	fastLedgerMu.Lock()
	fastLedgerOpen[key] = &fastLedgerPos{Sym: sym, Module: "fire", Side: "short", Src: "rest",
		T0: now, Entry: mid, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br}
	fastLedgerMu.Unlock()
}

// fastLedgerFireworkExit 纯判定:烟花刀模块出场(冻结口径=前向判决同款:固定300s/-40灾难;
// 出场刀型定理:回归局用固定持有,trail 在回归局是绞肉机——07-06 精磨实证)。
func fastLedgerFireworkExit(ret, holdSec float64) string {
	if ret <= -40 {
		return "disaster"
	}
	if holdSec >= 300 {
		return "fixed_300"
	}
	return ""
}

// ===== 烟花能量刻度出场(2026-07-09 坟场翻案)=====
// 病根:烟花细胞定义即 r60≥60 的暴动币(能量单位中位 75bps),-40 固定灾难线=0.5 个噪声
// 步长,双向都被呼吸打穿(同点位多空中位均 -53=止损在收割噪声,v3⑤ 同款病,当时修了
// liq_follow/tape 唯独没修烟花)。能量口径离线重测(`_fw_energy_exit_retrial.py`,参数运行前
// 冻结):空单 train 中位 +17.4 / holdout +5.9;完全力竭子集 +21.2/+22.7 PF1.32/1.36,
// 12h 分片 3/3 同号——原三特征组合本身无罪,是被出场尺子杀死的。
// k 系数全部移植自已验证的能量单位制(灾难≈2×30s池塘能量≈1×r60;trail 0.8×激活/0.4×回吐),
// 非新挖掘参数。纪律 14f:本函数=考试口径=枪口径,单一来源。

// fastFwEnergyUnit 烟花能量单位:入场时刻 |r60|,下限 40(原灾难线)、上限 200(防闪崩把
// 灾难线推到永不触发;与 fastLiqFollowImpUnitCapBps 同哲学但烟花口径独立冻结)。
func fastFwEnergyUnit(entryRet60 float64) float64 {
	u := math.Abs(entryRet60)
	if u < 40 {
		u = 40
	}
	if u > 200 {
		u = 200
	}
	return u
}

// fastLedgerFireworkExitES 纯判定:烟花能量刻度出场(""=持有)。ret=空头口径浮盈(跌为正)。
// 灾难=1.0×unit;trail=峰值≥0.8×unit 后回吐 0.4×unit 落袋;cap 600s。
func fastLedgerFireworkExitES(ret, peak, holdSec, unit float64) string {
	if ret <= -1.0*unit {
		return "disaster"
	}
	if peak >= 0.8*unit && ret <= peak-0.4*unit {
		return "trail"
	}
	if holdSec >= 600 {
		return "max_hold"
	}
	return ""
}

// ---- 烟花惯犯计数环(07-09 仪器缺口补齐:pbHabitual 只计 E 触发,烟花要自己的环)----
var (
	fastLedgerFwTrigMu sync.Mutex
	fastLedgerFwTrig   = map[string][]time.Time{}
)

// fastLedgerFwPrior24h 记录本次触发并返回"此前 24h 同币烟花触发数"(走前口径,入场时刻可知)。
func fastLedgerFwPrior24h(sym string, now time.Time) int {
	fastLedgerFwTrigMu.Lock()
	defer fastLedgerFwTrigMu.Unlock()
	cut := now.Add(-24 * time.Hour)
	h := fastLedgerFwTrig[sym]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	prior := len(h)
	fastLedgerFwTrig[sym] = append(h, now)
	if len(fastLedgerFwTrig) > 4096 {
		for k, v := range fastLedgerFwTrig {
			if len(v) == 0 || now.Sub(v[len(v)-1]).Hours() > 25 {
				delete(fastLedgerFwTrig, k)
			}
		}
	}
	return prior
}

// fastLedgerOnFireworkRiot 烟花刀模块入账:普查暴动命中"力竭×拉升≥60×点差≤6"细胞
// (与 `_firework_verdict.py` 前向判决同口径)→ 开虚拟空仓。record-only。
func (e *OKXDirectionalEngine) fastLedgerOnFireworkRiot(sym string, mid, ret15, ret60, spreadBps float64, symLiq5m int, now time.Time) {
	if !e.cfg.FastParticleLedgerEnabled || mid <= 0 {
		return
	}
	if ret60 < 60 || ret15 >= 0.25*ret60 || spreadBps <= 0 || spreadBps > 6 {
		return // 细胞未命中(拉升不足/仍在加速/点差宽)
	}
	key := "firework|rest|" + sym
	fastLedgerMu.Lock()
	if _, held := fastLedgerOpen[key]; held {
		fastLedgerMu.Unlock()
		return
	}
	fastLedgerMu.Unlock()
	prior := fastLedgerFwPrior24h(sym, now) // 惯犯计数(train/holdout 校验过的否决粒子的数据地板)
	fuel := fastMarketLiqRate(now, 300)
	br, brOK := fastBreadth30(now)
	bits := fastParticleBits(sym, now, 0, fuel, br, brOK, 0)
	fastLedgerMu.Lock()
	fastLedgerOpen[key] = &fastLedgerPos{Sym: sym, Module: "firework", Side: "short", Src: "rest",
		T0: now, Entry: mid, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br,
		PriorFw24h: prior, FwRet15: ret15, FwRet60: ret60, FwSymLiq5m: symLiq5m}
	// 能量刻度出场并行口径(rest2,07-09 坟场翻案):同触发同入场,出场走
	// fastLedgerFireworkExitES;rest 原口径照记=对照组不丢(口径断代,新键新考)。
	if _, held := fastLedgerOpen["firework|rest2|"+sym]; !held {
		fastLedgerOpen["firework|rest2|"+sym] = &fastLedgerPos{Sym: sym, Module: "firework", Side: "short", Src: "rest2",
			T0: now, Entry: mid, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br,
			PriorFw24h: prior, FwRet15: ret15, FwRet60: ret60, FwSymLiq5m: symLiq5m}
	}
	fastLedgerMu.Unlock()
}

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

// fastLedgerExitV2 纯判定:E 改良版出场(src=rest2,07-08 用户"我看到的要用最新策略跑"指令)。
// = 原版三条兜底 + 改良两刀真跑(非事后标注):
//   burst弹射(持仓中同币卖方爆量→立即离场,治灾难单;反事实 78 笔省中位+17.9 的机器化)
//   棘轮(峰值≥A10 回吐≥G8→离场,治吐回税;L5 冻结先验)。
// 优先级:弹射>灾难>棘轮>收敛>到点(弹射在灾难前=它本来就是提前逃生)。
// 影子账本口径,零实盘影响;实枪出场机器不动(铁律,过前向+用户批才谈)。
func fastLedgerExitV2(retBps, peakBps float64, burstSeen bool, gapNow float64, gapOK bool, holdSec float64, exitGap, disaster float64, maxHold int) string {
	if burstSeen {
		return "burst_eject"
	}
	if retBps <= -disaster {
		return "disaster"
	}
	if peakBps >= fastLedgerTrailActBps && retBps <= peakBps-fastLedgerTrailGiveBps {
		return "trail"
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
func (e *OKXDirectionalEngine) fastLedgerOnTrigger(sym string, okxMid, gapBps float64, now time.Time, path fastGapPathStats) {
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
	fastLedgerOpen[key] = &fastLedgerPos{Sym: sym, Src: "det", T0: now, Entry: okxMid, GapDet: gapBps, Bits: bits, Fuel: fuel, Breadth: br, Path: path}
	fastLedgerMu.Unlock()
}

// fastLedgerOnTriggerREST v1 可执行口径:影子线 REST 建档通过守卫后调用(fastXgapFire 内)。
// 入场=REST 时刻 mid(与㉝执行层证据包同口径,含 144ms 延迟后的真实价格);残余带粒子在此评估。
// 惯犯环不重复登记(det 触发已记),粒子重新评估(REST 时刻的州可能已变)。
func (e *OKXDirectionalEngine) fastLedgerOnTriggerREST(sym string, restMid, restBid, spreadBps, gapDet, gapRest float64, now time.Time, path fastGapPathStats) {
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
		GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br, Path: path}
	// 改良版口径并行(rest2=E·下一版:同触发同入场,出场真跑棘轮+弹射;07-08 用户指令
	// "看到的要用最新策略跑"——影子层真跑,实枪出场机器不动):
	if _, held := fastLedgerOpen["rest2|"+sym]; !held {
		fastLedgerOpen["rest2|"+sym] = &fastLedgerPos{Sym: sym, Src: "rest2", T0: now, Entry: restMid,
			GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br, Path: path}
	}
	// v3 棘轮版(07-09 改良包死因解剖:弹射粒子级反向[11笔配对差-249bps],棘轮无罪[+57]——
	// v3=只棘轮不弹射;v2 判负口径原封保留作对照,口径断代=新键新考):
	if _, held := fastLedgerOpen["rest3|"+sym]; !held {
		fastLedgerOpen["rest3|"+sym] = &fastLedgerPos{Sym: sym, Src: "rest3", T0: now, Entry: restMid,
			GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br, Path: path}
	}
	// v2 maker 口径并行:同一触发点挂虚拟被动单(价=REST bid),成交/超时由 manage 裁
	if restBid > 0 {
		if _, held := fastLedgerOpen["mkr|"+sym]; !held {
			fastLedgerOpen["mkr|"+sym] = &fastLedgerPos{Sym: sym, Src: "mkr", T0: now, Entry: restBid,
				GapDet: gapDet, GapRest: gapRest, SpreadBps: spreadBps, Bits: bits, Fuel: fuel, Breadth: br, Path: path}
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
				row.applyGapPath(p.Path)
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
		if p.Side == "short" {
			ret = -ret // 火刀=空头口径:价格下跌为正
		}
		if ret > p.PeakBps {
			p.PeakBps = ret
		}
		holdFrom := p.T0
		if p.Src == "mkr" {
			holdFrom = p.FilledAt // maker 持有期从成交起算
		}
		if !p.BurstSeen && fastSellBurstSince(p.Sym, holdFrom, now) {
			p.BurstSeen = true
			p.RetAtBurst = ret // ㊱曲线②:只标注不弹射
		}
		// 杠杆L5 棘轮反事实(逐tick精确,非峰值近似):激活后回吐≥G 即记虚拟出场价(只记不出)
		if !p.TrailFired && p.PeakBps >= fastLedgerTrailActBps && ret <= p.PeakBps-fastLedgerTrailGiveBps {
			p.TrailFired = true
			p.TrailExitBp = ret
		}
		hold := now.Sub(holdFrom).Seconds()
		var exit string
		if p.Module == "fire" {
			wave2 := fastSymLiqBetween(p.Sym, p.T0.Add(3*time.Second), p.T0.Add(20*time.Second))
			exit = fastLedgerFireExit(ret, p.PeakBps, hold, now.Sub(p.T0).Seconds(), wave2)
		} else if p.Module == "firework" && p.Src == "rest2" {
			exit = fastLedgerFireworkExitES(ret, p.PeakBps, hold, fastFwEnergyUnit(p.FwRet60))
		} else if p.Module == "firework" {
			exit = fastLedgerFireworkExit(ret, hold)
		} else if p.Src == "rest2" {
			// E 改良版 v2:出场真跑棘轮+弹射(不是标注),兜底三条同原版
			gapNow, gapOK := fastXMidGapBpsVenueTol("bybit", p.Sym, mid, now, fastXgapFreshTol)
			exit = fastLedgerExitV2(ret, p.PeakBps, p.BurstSeen, gapNow, gapOK, hold, e.cfg.FastXgapExitGapBps, e.cfg.FastXgapDisasterBps, e.cfg.FastXgapMaxHoldSeconds)
		} else if p.Src == "rest3" {
			// E 棘轮版 v3:只棘轮不弹射(burstSeen 恒 false 即关闭弹射分支,其余同 v2)
			gapNow, gapOK := fastXMidGapBpsVenueTol("bybit", p.Sym, mid, now, fastXgapFreshTol)
			exit = fastLedgerExitV2(ret, p.PeakBps, false, gapNow, gapOK, hold, e.cfg.FastXgapExitGapBps, e.cfg.FastXgapDisasterBps, e.cfg.FastXgapMaxHoldSeconds)
		} else {
			gapNow, gapOK := fastXMidGapBpsVenueTol("bybit", p.Sym, mid, now, fastXgapFreshTol)
			exit = fastLedgerExit(ret, gapNow, gapOK, hold, e.cfg.FastXgapExitGapBps, e.cfg.FastXgapDisasterBps, e.cfg.FastXgapMaxHoldSeconds)
		}
		if exit == "" {
			continue
		}
		cost := fastLedgerCostBps
		if p.Src == "mkr" {
			cost = fastLedgerMakerCostBps
		}
		row := fastLedgerRow{
			Time: now, Symbol: p.Sym, Module: p.Module, Side: p.Side, Src: p.Src, T0: p.T0,
			GapDetBps: p.GapDet, GapRestBps: p.GapRest, SpreadBps: p.SpreadBps, Bits: p.Bits,
			Fuel5m: p.Fuel, Breadth30: p.Breadth,
			GrossBps: ret, NetBps: ret - cost, PeakBps: p.PeakBps,
			HoldSec: int(hold), Exit: exit,
			BurstDuringHold: p.BurstSeen, RetAtBurstBps: p.RetAtBurst,
			TrailFired: p.TrailFired, TrailExitBps: p.TrailExitBp,
			PriorFw24h: p.PriorFw24h,
			FwRet15Bps: p.FwRet15, FwRet60Bps: p.FwRet60, FwSymLiq5m: p.FwSymLiq5m,
		}
		row.applyGapPath(p.Path)
		if err := appendJSONLRecord(path, row); err != nil {
			e.logger.Printf("[particle_ledger] 落盘失败: %v", err)
		}
		fastLedgerMu.Lock()
		if p.Module != "" && p.Module != "e" {
			delete(fastLedgerOpen, p.Module+"|"+p.Src+"|"+p.Sym)
		} else {
			delete(fastLedgerOpen, p.Src+"|"+p.Sym)
		}
		fastLedgerMu.Unlock()
	}
}
