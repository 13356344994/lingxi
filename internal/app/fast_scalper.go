package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

// ===== fast scalper v1(超频线)=====
// recorder/scorer:record-only 记录 + 打分(服务实盘记录与复盘)。
// fast live v1:命中分数即真实下单,固定 horizon 平仓;不限笔数/仓数、不因连亏自停;唯一急停 = .halt-live。
// 分层标记:signal_layer=fast_v1 / entry_layer=fast_executable_live / management_layer=fast_live_fixed_horizon。

const fastExecShadowFile = "fast_executable_shadow_events.jsonl"
const fastLivePositionFile = "fast_positions.json" // fast live 持仓持久化(重启接管/平仓,防孤儿)

const (
	fastSignalLayer     = "fast_v1"
	fastEntryLayer      = "fast_executable_live"
	fastManagementLayer = "fast_live_fixed_horizon"
)

// fast 幽灵仓清理:交易所已确认本地方向无持仓时,写独立留痕文件(不写 settlements.jsonl,不污染 fast PnL/PF/胜率)。
const fastPositionCleanupFile = "fast_position_cleanup.jsonl"
const fastCleanupReason = "fast_exchange_position_absent_cleanup"

// fastLivePosition 一笔 fast 实盘仓。
type fastLivePosition struct {
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"` // long/short
	EntryPrice   float64   `json:"entry_price"`
	Qty          float64   `json:"qty"`
	NotionalUSDT float64   `json:"notional_usdt"`
	OpenedAt     time.Time `json:"opened_at"`
	PositionID   string    `json:"position_id"`
	OpenFeeUSDT  float64   `json:"open_fee_usdt,omitempty"`
	Score        float64   `json:"score,omitempty"`
	ScoreReason  string    `json:"score_reason,omitempty"`
	OpenViaTaker bool      `json:"open_via_taker,omitempty"`
	// v2.2 自适应入场标注(loss_lvlN / weak_hour;平仓时写入 settlement 供分桶复盘)
	EntryAdaptiveTags string `json:"entry_adaptive_tags,omitempty"`
	// v3 接型入场来源(空=scorer 追型;fade_exhaustion=接型灰度;平仓时写入 settlement 分代对照)
	CandidateSource string `json:"candidate_source,omitempty"`
	// 入场上下文(复盘用,平仓时写入 settlement)
	EventID         string    `json:"event_id,omitempty"`
	EventTime       time.Time `json:"event_time,omitempty"`
	EntrySpreadBps  float64   `json:"entry_spread_bps,omitempty"`
	EntryImbalance  float64   `json:"entry_imbalance,omitempty"`
	EntryImpulse15s float64   `json:"entry_impulse_15s,omitempty"`
	EntryImpulse30s float64   `json:"entry_impulse_30s,omitempty"`
	EntryImpulse60s float64   `json:"entry_impulse_60s,omitempty"`
	// v3 maker 执行 + 成交后水位(bps,自成交价计)
	MakerEntry      bool    `json:"maker_entry,omitempty"`
	MakerOrderPrice float64 `json:"maker_order_price,omitempty"`
	MakerWaitMs     int     `json:"maker_wait_ms,omitempty"`
	FillMid         float64 `json:"fill_mid,omitempty"`
	MakerEdgeBps    float64 `json:"maker_edge_bps,omitempty"`
	PeakBps         float64 `json:"peak_bps,omitempty"`   // 浮盈峰值(移动止盈用;全程 all-time)
	TroughBps       float64 `json:"trough_bps,omitempty"` // 浮亏谷值(全程 all-time 最深回撤;早砍用)
	// seen-aware 成交后水位(bps):首次采样某窗口时 MFE=MAE=ubps(不被假 0 污染);窗口外不采样(Seen=false)。
	Mfe3  float64 `json:"mfe3,omitempty"`
	Mae3  float64 `json:"mae3,omitempty"`
	Seen3 bool    `json:"seen3,omitempty"`
	Mfe5  float64 `json:"mfe5,omitempty"`
	Mae5  float64 `json:"mae5,omitempty"`
	Seen5 bool    `json:"seen5,omitempty"`
	Mfe10  float64 `json:"mfe10,omitempty"`
	Mae10  float64 `json:"mae10,omitempty"`
	Seen10 bool    `json:"seen10,omitempty"`
	Mfe15  float64 `json:"mfe15,omitempty"`
	Mae15  float64 `json:"mae15,omitempty"`
	Seen15 bool    `json:"seen15,omitempty"`
	Mfe30  float64 `json:"mfe30,omitempty"`
	Mae30  float64 `json:"mae30,omitempty"`
	Seen30 bool    `json:"seen30,omitempty"`
	Mfe60  float64 `json:"mfe60,omitempty"`
	Mae60  float64 `json:"mae60,omitempty"`
	Seen60 bool    `json:"seen60,omitempty"`
}

// fastUpdateExcursion seen-aware 分窗采样成交后水位:hold(秒)在窗口内才采样;
// 首次采样该窗口 MFE=MAE=ubps(避免默认 0 污染真实负浮盈),之后取 max/min。
func fastUpdateExcursion(p *fastLivePosition, holdSec float64, ubps float64) {
	upd := func(w float64, mfe, mae *float64, seen *bool) {
		if holdSec > w {
			return
		}
		if !*seen {
			*seen = true
			*mfe = ubps
			*mae = ubps
			return
		}
		if ubps > *mfe {
			*mfe = ubps
		}
		if ubps < *mae {
			*mae = ubps
		}
	}
	upd(3, &p.Mfe3, &p.Mae3, &p.Seen3)
	upd(5, &p.Mfe5, &p.Mae5, &p.Seen5)
	upd(10, &p.Mfe10, &p.Mae10, &p.Seen10)
	upd(15, &p.Mfe15, &p.Mae15, &p.Seen15)
	upd(30, &p.Mfe30, &p.Mae30, &p.Seen30)
	upd(60, &p.Mfe60, &p.Mae60, &p.Seen60)
}

// fastMakerAttemptEvent 每次 maker 入场尝试(成交/未成交都记;供按 maker_filled/wait/edge 分桶)。record-only。
type fastMakerAttemptEvent struct {
	RecordedAt      time.Time `json:"recorded_at"`
	Venue           string    `json:"venue"`
	Symbol          string    `json:"symbol"`
	Side            string    `json:"side"`
	Score           float64   `json:"score,omitempty"`
	ScoreReason     string    `json:"score_reason,omitempty"`
	MakerOrderPrice float64   `json:"maker_order_price,omitempty"`
	MakerWaitMs     int       `json:"maker_wait_ms,omitempty"`
	MakerFilled     bool      `json:"maker_filled"`
	FillPrice       float64   `json:"fill_price,omitempty"`
	FillMid         float64   `json:"fill_mid,omitempty"`
	MakerEdgeBps    float64   `json:"maker_edge_bps,omitempty"`
	SpreadBps       float64   `json:"spread_bps,omitempty"`
	Imbalance       float64   `json:"imbalance,omitempty"`
	// 成交确认路径:immediate_fill/poll_fill/cancel_then_query_fill/suspicious_exec_qty_no_fill_status/maker_not_filled。
	EntryConfirmPath string `json:"entry_confirm_path,omitempty"`
}

const fastMakerAttemptFile = "fast_maker_attempts.jsonl"
const fastCooldownFile = "fast_cooldowns.jsonl" // fast 层短冷却审计(落盘可查;非永久黑名单)

// fastCooldownEvent 一条 fast 冷却记录(落盘审计:哪个 symbol、原因、到期时间)。
type fastCooldownEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"`
	Reason     string    `json:"reason"`
	UntilTime  time.Time `json:"until"`
	Seconds    int       `json:"seconds"`
}

// fastExecShadowEvent 一条可执行 fast 事件(record-only)。
type fastExecShadowEvent struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"`
	SnapshotID string    `json:"snapshot_id,omitempty"`

	Bid       float64 `json:"bid,omitempty"`
	Ask       float64 `json:"ask,omitempty"`
	Mid       float64 `json:"mid,omitempty"`
	SpreadBps float64 `json:"spread_bps,omitempty"`
	TopBidQty float64 `json:"top_bid_qty,omitempty"`
	TopAskQty float64 `json:"top_ask_qty,omitempty"`

	DepthNotional5bps  float64 `json:"depth_notional_5bps,omitempty"`
	DepthNotional10bps float64 `json:"depth_notional_10bps,omitempty"`
	DepthNotional20bps float64 `json:"depth_notional_20bps,omitempty"`

	OrderbookImbalance float64 `json:"orderbook_imbalance,omitempty"`

	MidImpulse15sBps float64 `json:"mid_impulse_15s_bps,omitempty"`
	MidImpulse30sBps float64 `json:"mid_impulse_30s_bps,omitempty"`
	MidImpulse60sBps float64 `json:"mid_impulse_60s_bps,omitempty"`

	RecentTradeDir       string  `json:"recent_trade_dir,omitempty"`
	RecentTradeVolImpact float64 `json:"recent_trade_vol_impact,omitempty"`

	MakerPlaceable       bool    `json:"maker_placeable,omitempty"`
	MakerTheoFillPrice   float64 `json:"maker_theo_fill_price,omitempty"`
	MakerTheoCancelPrice float64 `json:"maker_theo_cancel_price,omitempty"`

	Fwd15sBps  float64 `json:"fwd_15s_bps,omitempty"`
	Fwd30sBps  float64 `json:"fwd_30s_bps,omitempty"`
	Fwd60sBps  float64 `json:"fwd_60s_bps,omitempty"`
	Fwd180sBps float64 `json:"fwd_180s_bps,omitempty"`

	FastCandidateScore  float64 `json:"fast_candidate_score,omitempty"`
	FastCandidateSide   string  `json:"fast_candidate_side,omitempty"`
	FastCandidateReason string  `json:"fast_candidate_reason,omitempty"`

	// v3 蓝图字段(2026-07-03):
	// CandidateSource 非空 = 接型候选(liq_catch/fade_exhaustion),record-only,fastLiveOpen 硬闸拒绝实盘。
	CandidateSource string `json:"candidate_source,omitempty"`
	// 池塘质量打标(恒开,零成本;滚动2h逐扫描中位数,动态判定,禁止写死名单)
	PondEnergyMedBps float64 `json:"pond_energy_med_bps,omitempty"`
	PondDepthMedUSDT float64 `json:"pond_depth_med_usdt,omitempty"`
	PondSamples      int     `json:"pond_samples,omitempty"`
	PondOK           bool    `json:"pond_ok"`
}

// ---- 滚动 mid 历史 + fast live 持仓(包级,OKX-only 单引擎,按 venue 隔离)----
type fastMidPoint struct {
	ts  time.Time
	mid float64
}

var (
	fastMidMu      sync.Mutex
	fastMidHist    = map[string][]fastMidPoint{}
	fastLastLog    time.Time
	fastLiveMu     sync.Mutex
	fastLivePos    = map[string]map[string]*fastLivePosition{} // venue -> symbol -> pos
	fastLiveLoaded = map[string]bool{}
	fastSkipAgg    = map[string]int{} // 入场结果/跳过原因聚合(每 60s 打一行后清空)
	fastCooldownMu sync.Mutex
	fastCooldown   = map[string]map[string]time.Time{} // venue -> symbol -> 冷却到期(fast 层;仅内存,5min 短冷却)
	fastDailyMu    sync.Mutex
	fastDaily      = map[string]*fastDailyState{} // venue -> 当日 fast 已实现(北京日切;重启从 settlements 重建)
)

// ===== v3 蓝图①:池塘质量(2026-07-03 用户批准) =====
// 119k shadow 实证:能量(30s位移中位)是池塘主轴——高跟随池全部能量≥9.6bps;死水镜面(SNDK/MRVL)
// 深度17万但能量<6→跟随率38-56%;CELO 能量3.2=全场最差。滚动2h逐扫描样本取中位,动态判定,
// 禁止写死名单(币随行情变化自动进出;这是用户铁律:动态、不写死黑名单)。

type fastPondSample struct {
	ts        time.Time
	absMove30 float64 // |30s mid 位移| bps(能量)
	depth10   float64 // 10bp 内双边挂单名义 USDT
}

var (
	fastPondMu   sync.Mutex
	fastPondHist = map[string][]fastPondSample{} // venue|symbol -> 滚动 2h 样本
	fastLiqBufMu sync.Mutex
	fastLiqBuf   = map[string][]fastRecentLiq{} // symbol -> 最近强平(5min;公共全市场频道,无 venue 维度)
)

const fastPondWindowSec = 7200
const fastPondWarmupSamples = 150 // ~10min@4s扫描;样本不足 fail-open(pond_ok=true),新币/重启不误杀

// fastRecentLiq 最近强平内存缓冲(供接型候选 liq_catch;与落盘的 fastLiqEvent 解耦)。
type fastRecentLiq struct {
	ts      time.Time
	posSide string // 被强平的持仓方向:long=多头爆(被迫卖) short=空头爆(被迫买)
}

func fastPondRecord(key string, now time.Time, absMove30, depth10 float64) {
	fastPondMu.Lock()
	defer fastPondMu.Unlock()
	h := append(fastPondHist[key], fastPondSample{now, absMove30, depth10})
	cut := now.Add(-fastPondWindowSec * time.Second)
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastPondHist[key] = h[i:]
}

func fastMedian(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	sort.Float64s(a)
	if n := len(a); n%2 == 1 {
		return a[n/2]
	} else {
		return (a[n/2-1] + a[n/2]) / 2
	}
}

// fastPondEval 返回该池塘滚动窗口的 (能量中位bps, 深度中位USDT, 样本数)。
func fastPondEval(key string) (float64, float64, int) {
	fastPondMu.Lock()
	defer fastPondMu.Unlock()
	h := fastPondHist[key]
	if len(h) == 0 {
		return 0, 0, 0
	}
	en := make([]float64, 0, len(h))
	dp := make([]float64, 0, len(h))
	for _, s := range h {
		en = append(en, s.absMove30)
		dp = append(dp, s.depth10)
	}
	return fastMedian(en), fastMedian(dp), len(h)
}

// fastPondRestore 重启回放:从 shadow 文件尾部恢复最近 2h 池塘样本。
// 动机(2026-07-03 实盘暴露):池塘统计是内存态,重启清零 → 全部池塘回热身 fail-open,
// 死水池(如 CELO)获得 ~10 分钟免检窗口,重启一次送一次人头。回放后判定即刻成熟。
func (e *OKXDirectionalEngine) fastPondRestore() {
	path := filepath.Join(e.recordDir, fastExecShadowFile)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	const maxTail = int64(64 << 20) // 2h base 事件 ~20MB,64MB 尾部足够
	skipFirst := false
	if st.Size() > maxTail {
		if _, err := f.Seek(st.Size()-maxTail, 0); err != nil {
			return
		}
		skipFirst = true // seek 落点大概率在行中,掐掉半行
	}
	cut := e.nowFn().UTC().Add(-fastPondWindowSec * time.Second)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		if skipFirst {
			skipFirst = false
			continue
		}
		var d struct {
			RecordedAt         time.Time `json:"recorded_at"`
			Symbol             string    `json:"symbol"`
			CandidateSource    string    `json:"candidate_source"`
			MidImpulse15sBps   float64   `json:"mid_impulse_15s_bps"`
			MidImpulse30sBps   float64   `json:"mid_impulse_30s_bps"`
			MidImpulse60sBps   float64   `json:"mid_impulse_60s_bps"`
			DepthNotional10bps float64   `json:"depth_notional_10bps"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Symbol == "" || d.CandidateSource != "" {
			continue
		}
		if d.RecordedAt.Before(cut) {
			continue
		}
		// 三冲量全精确 0 ≈ 当时 mid 历史不足(上一次进程刚启动),跳过防污染能量中位
		if d.MidImpulse15sBps == 0 && d.MidImpulse30sBps == 0 && d.MidImpulse60sBps == 0 {
			continue
		}
		fastPondRecord(e.venueName+"|"+d.Symbol, d.RecordedAt.UTC(), math.Abs(d.MidImpulse30sBps), d.DepthNotional10bps)
		n++
	}
	if n > 0 {
		e.logger.Printf("[fast_scalper] 池塘统计回放完成: %d 样本(2h窗口,重启不失忆,免死水池热身fail-open)", n)
	}
}

// fastMidSpanSecs mid 历史覆盖秒数(池塘样本只在 30s 冲量真实可算时才记,避免 0 值污染能量中位)。
func fastMidSpanSecs(key string, now time.Time) float64 {
	fastMidMu.Lock()
	defer fastMidMu.Unlock()
	h := fastMidHist[key]
	if len(h) == 0 {
		return 0
	}
	return now.Sub(h[0].ts).Seconds()
}

// fastNoteLiq 由 fast_ws 强平推送调用:写入内存缓冲(保留5min),供接型候选生成(liq_catch)。
func fastNoteLiq(sym, posSide string, now time.Time) {
	fastLiqBufMu.Lock()
	defer fastLiqBufMu.Unlock()
	h := append(fastLiqBuf[sym], fastRecentLiq{now, posSide})
	cut := now.Add(-5 * time.Minute)
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastLiqBuf[sym] = h[i:]
}

// fastRecentLiqCatchSide 最近 windowSec 内该 symbol 有强平 → 返回"接被迫者"方向与强平笔数。
// 多头爆=被迫卖砸出坑→接多;空头爆=被迫买顶出尖→接空。两向平票不出候选(混战不接)。
func fastRecentLiqCatchSide(sym string, now time.Time, windowSec int) (string, int) {
	fastLiqBufMu.Lock()
	defer fastLiqBufMu.Unlock()
	cut := now.Add(-time.Duration(windowSec) * time.Second)
	nLong, nShort := 0, 0
	for _, l := range fastLiqBuf[sym] {
		if l.ts.Before(cut) {
			continue
		}
		switch l.posSide {
		case "long":
			nLong++
		case "short":
			nShort++
		}
	}
	switch {
	case nLong > nShort:
		return "long", nLong + nShort
	case nShort > nLong:
		return "short", nLong + nShort
	default:
		return "", nLong + nShort
	}
}

// fastDailyState fast 层当日状态(独立于 slow 的账户级风控):已实现净额(熔断用)+ 单币净亏笔数(自适应退避用)。
type fastDailyState struct {
	day      time.Time
	realized float64
	// symLosses 单币当日净亏笔数(赢单减一档,地板 0):驱动"亏得越多越难再进"的自适应惩罚,不禁开。
	symLosses map[string]int
	loaded    bool
}

// fastDailyStateLocked 返回当日状态(调用者须持 fastDailyMu);首次调用从 settlements.jsonl 重建(重启不丢日内计数)。
func (e *OKXDirectionalEngine) fastDailyStateLocked(now time.Time) *fastDailyState {
	dayStart := beijingCSTDayStart(now)
	st := fastDaily[e.venueName]
	if st == nil || !st.day.Equal(dayStart) {
		st = &fastDailyState{day: dayStart, symLosses: map[string]int{}}
		fastDaily[e.venueName] = st
	}
	if st.symLosses == nil {
		st.symLosses = map[string]int{}
	}
	if !st.loaded {
		st.loaded = true
		if data, err := os.ReadFile(e.settlementsPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") { // 文件按时间追加,顺序回放即时序
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var rec okxDirectionalSettlement
				if json.Unmarshal([]byte(line), &rec) != nil {
					continue
				}
				if rec.SignalLayer != fastSignalLayer || rec.Time.Before(dayStart) {
					continue
				}
				st.realized += rec.RealizedUSDT
				st.applyOutcome(rec.Symbol, rec.RealizedUSDT)
			}
		}
	}
	return st
}

// applyOutcome 更新单币亏损档位:净亏 +1 档,净赢 -1 档(地板 0,信号重新证明自己即自动降档)。
func (st *fastDailyState) applyOutcome(sym string, realized float64) {
	if realized < 0 {
		st.symLosses[sym]++
	} else if realized > 0 && st.symLosses[sym] > 0 {
		st.symLosses[sym]--
	}
}

// fastDailyRealized 返回当日 fast 已实现净额(熔断判定用)。
func (e *OKXDirectionalEngine) fastDailyRealized(now time.Time) float64 {
	fastDailyMu.Lock()
	defer fastDailyMu.Unlock()
	return e.fastDailyStateLocked(now).realized
}

// fastSymbolLossLevel 该币当日亏损档位(0=干净)。
func (e *OKXDirectionalEngine) fastSymbolLossLevel(sym string, now time.Time) int {
	fastDailyMu.Lock()
	defer fastDailyMu.Unlock()
	return e.fastDailyStateLocked(now).symLosses[sym]
}

// effectiveFastDailyLossLimit fast 层日熔断有效阈值:pct>0 → 本所交易本金×pct(随页面调本金缩放);
// 否则回退 USDT 绝对值;两者均未配 → 0(关)。
func (e *OKXDirectionalEngine) effectiveFastDailyLossLimit() float64 {
	if pct := e.cfg.FastDailyLossPctOfCapital; pct > 0 {
		if capital := e.getControl().TradingCapitalUSDT; capital > 0 {
			return capital * pct
		}
	}
	return e.cfg.FastDailyLossLimitUSDT
}

// fastOpenPositionsOpenedSince 当前在场 fast 仓中开仓时刻不早于 since 的数量(面板"今日开仓数"用)。
func (e *OKXDirectionalEngine) fastOpenPositionsOpenedSince(since time.Time) int {
	fastLiveMu.Lock()
	defer fastLiveMu.Unlock()
	n := 0
	for _, p := range fastLivePos[e.venueName] {
		if p != nil && !p.OpenedAt.Before(since) {
			n++
		}
	}
	return n
}

// fastDailyAdd 累加一笔 fast 结算到当日计数 + 更新该币亏损档位(平仓路径调用)。
func (e *OKXDirectionalEngine) fastDailyAdd(now time.Time, sym string, delta float64) {
	fastDailyMu.Lock()
	defer fastDailyMu.Unlock()
	st := e.fastDailyStateLocked(now)
	st.realized += delta
	st.applyOutcome(sym, delta)
}

// fastParseWeakHoursUTC 解析弱时段配置("0-7" / "0,1,2" / 混合"0-3,22,23")→ 24 位布尔表;解析失败的段忽略。
func fastParseWeakHoursUTC(spec string) [24]bool {
	var out [24]bool
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, aerr := strconv.Atoi(strings.TrimSpace(lo))
			b, berr := strconv.Atoi(strings.TrimSpace(hi))
			if aerr == nil && berr == nil && a >= 0 && b <= 23 && a <= b {
				for h := a; h <= b; h++ {
					out[h] = true
				}
			}
			continue
		}
		if h, err := strconv.Atoi(part); err == nil && h >= 0 && h <= 23 {
			out[h] = true
		}
	}
	return out
}

// fastEntryAdaptive 计算本次入场的自适应门槛与名义系数(v2.2:替代禁开的惩罚机制)。
// 返回 (有效分数门槛, 名义系数, 标注)。
func (e *OKXDirectionalEngine) fastEntryAdaptive(sym string, now time.Time) (threshold, notionalFrac float64, tags string) {
	threshold = e.cfg.FastLiveScoreThreshold
	notionalFrac = 1.0
	var notes []string
	// ① 单币亏损退避:每亏一档,门槛 +bump(cap 封顶)。
	if bump := e.cfg.FastLossScoreBumpPerLoss; bump > 0 {
		if lvl := e.fastSymbolLossLevel(sym, now); lvl > 0 {
			add := bump * float64(lvl)
			if cap := e.cfg.FastLossScoreBumpCap; cap > 0 && add > cap {
				add = cap
			}
			threshold += add
			notes = append(notes, fmt.Sprintf("loss_lvl%d(+%.2f)", lvl, add))
		}
	}
	// ② 弱时段:提高门槛 + 降名义(不禁开,便宜地继续采样)。
	if spec := strings.TrimSpace(e.cfg.FastWeakHoursUTC); spec != "" {
		if fastParseWeakHoursUTC(spec)[now.UTC().Hour()] {
			if b := e.cfg.FastWeakHoursScoreBump; b > 0 {
				threshold += b
			}
			if f := e.cfg.FastWeakHoursNotionalFrac; f > 0 && f < 1 {
				notionalFrac = f
			}
			notes = append(notes, "weak_hour")
		}
	}
	return threshold, notionalFrac, strings.Join(notes, "|")
}

// fastInCooldown 该 symbol 是否在 fast 冷却中(只影响 fast 层)。
func (e *OKXDirectionalEngine) fastInCooldown(sym string, now time.Time) bool {
	fastCooldownMu.Lock()
	defer fastCooldownMu.Unlock()
	m := fastCooldown[e.venueName]
	if m == nil {
		return false
	}
	until, ok := m[sym]
	return ok && now.Before(until)
}

// fastSetCooldown 设 fast 冷却 + 落盘审计 + 日志(非永久黑名单,到期自动恢复)。
// v2.2 亏损退避:该币当日每亏一档,冷却 ×backoff 倍(300s→15m→45m→…),cap 封顶——
// 对着同一坏行情片段反复锤(v1: CELO 两天 1124 笔)的直接解药;赢单降档后冷却自动缩短。
func (e *OKXDirectionalEngine) fastSetCooldown(sym, reason string, now time.Time) {
	secs := e.cfg.FastCooldownSeconds
	if secs <= 0 {
		return
	}
	if factor := e.cfg.FastLossCooldownBackoffFactor; factor > 1 {
		if lvl := e.fastSymbolLossLevel(sym, now); lvl > 1 {
			scaled := float64(secs)
			for i := 1; i < lvl; i++ {
				scaled *= factor
			}
			cap := e.cfg.FastLossCooldownCapSeconds
			if cap <= 0 {
				cap = 7200
			}
			if scaled > float64(cap) {
				scaled = float64(cap)
			}
			secs = int(scaled)
		}
	}
	until := now.Add(time.Duration(secs) * time.Second)
	fastCooldownMu.Lock()
	if fastCooldown[e.venueName] == nil {
		fastCooldown[e.venueName] = map[string]time.Time{}
	}
	fastCooldown[e.venueName][sym] = until
	fastCooldownMu.Unlock()
	_ = appendJSONLRecord(filepath.Join(e.recordDir, fastCooldownFile), fastCooldownEvent{
		RecordedAt: now, Venue: e.venueName, Symbol: sym, Reason: reason, UntilTime: until, Seconds: secs,
	})
	e.logger.Printf("[fast_cooldown] %s fast层冷却%ds 至%s 原因=%s(非永久黑名单)", sym, secs, until.Format("15:04:05"), reason)
}

func fastSign(x float64) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func (e *OKXDirectionalEngine) fastScalperEnabled() bool {
	if !e.cfg.FastScalperV1Enabled {
		return false
	}
	if v := strings.TrimSpace(e.cfg.FastScalperV1Venue); v != "" && !strings.EqualFold(v, e.venueName) {
		return false
	}
	return true
}

func (e *OKXDirectionalEngine) fastScalperInterval() time.Duration {
	s := e.cfg.FastScalperV1IntervalSeconds
	if s < 3 {
		s = 4
	}
	return time.Duration(s) * time.Second
}

// fastScalperLoop 扫描/开仓独立 goroutine(maker 入场可能阻塞,故与平仓分离)。
func (e *OKXDirectionalEngine) fastScalperLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_scalper] scan loop panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[OKX方向][fast_scalper] v3 扫描goroutine启动 周期=%s 扫描上限=%d 入场=%s live=%v", e.fastScalperInterval(), e.cfg.FastScalperV1MaxSymbols, e.cfg.FastEntryMode, e.cfg.FastLiveEnabled)
	e.fastPondRestore() // 池塘统计重启回放(同步,数秒;失败静默=退回热身fail-open)
	t := time.NewTicker(e.fastScalperInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.fastScalperTick(ctx)
		}
	}
}

// fastManageLoop 平仓/接管独立 goroutine(每 2s,绝不被扫描/maker入场阻塞;防孤儿 + 及时出场)。
func (e *OKXDirectionalEngine) fastManageLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_scalper] manage loop panic 已恢复: %v", r)
		}
	}()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.fastLiveLoad()
			e.fastLiveManage(ctx)
		}
	}
}

// fastScalperTick 一轮:先接管/平到期 fast 仓;再扫候选 record-only 写 shadow;若 fast_live 开则命中即真实开仓。
func (e *OKXDirectionalEngine) fastScalperTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[fast_scalper] tick panic 已恢复: %v", r)
		}
	}()
	if !e.fastScalperEnabled() {
		return
	}
	e.fastLiveLoad() // 幂等;平仓由独立 fastManageLoop 负责(避免被 maker 入场等待拖延)
	maxSym := e.cfg.FastScalperV1MaxSymbols
	if maxSym <= 0 {
		maxSym = 20
	}
	e.mu.RLock()
	syms := make([]string, 0, maxSym)
	for _, ent := range e.shortlist {
		if strings.TrimSpace(ent.Symbol) == "" {
			continue
		}
		syms = append(syms, ent.Symbol)
		if len(syms) >= maxSym {
			break
		}
	}
	e.mu.RUnlock()
	if len(syms) == 0 {
		return
	}
	path := filepath.Join(e.recordDir, fastExecShadowFile)
	now := e.nowFn().UTC()
	written := 0
	for _, sym := range syms {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ev := e.buildFastEvent(ctx, sym, now)
		if ev == nil {
			continue
		}
		if err := appendJSONLRecord(path, *ev); err != nil {
			e.logger.Printf("[fast_scalper] 落盘失败: %v", err)
			break
		}
		written++
		// v3② 接型候选:同盘口快照派生 liq_catch / fade_exhaustion 事件;fade 在灰度开关+OK池塘下可实盘,
		// 其余(liq_catch 等)由 fastLiveOpen 硬闸拒为 record_only_source。
		if e.cfg.FastAltCandidatesEnabled {
			for _, alt := range e.buildFastAltCandidates(ev, now) {
				if err := appendJSONLRecord(path, *alt); err != nil {
					e.logger.Printf("[fast_scalper] 接型候选落盘失败: %v", err)
					break
				}
				written++
				if e.cfg.FastLiveEnabled {
					fastSkipAgg["alt_"+e.fastLiveOpen(ctx, alt)]++
				}
			}
		}
		if e.cfg.FastLiveEnabled {
			r := e.fastLiveOpen(ctx, ev) // 命中即真实开仓(内部各 guard);返回结果/跳过原因
			fastSkipAgg[r]++
		}
	}
	if written > 0 && now.Sub(fastLastLog) >= 60*time.Second {
		fastLastLog = now
		e.logger.Printf("[fast_scalper] record-only 扫描=%d 记录=%d live=%v 入场结果=%v → %s", len(syms), written, e.cfg.FastLiveEnabled, fastSkipAgg, fastExecShadowFile)
		fastSkipAgg = map[string]int{}
	}
}

// buildFastEvent best-effort 拉盘口+深度组装一条 fast 事件 + 打分;失败返回 nil(跳过本 symbol)。
func (e *OKXDirectionalEngine) buildFastEvent(ctx context.Context, sym string, now time.Time) *fastExecShadowEvent {
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	book, berr := e.v.FetchBookTicker(bctx, venue.MarketPerp, venue.Symbol(sym))
	cancel()
	if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
		return nil
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	ev := &fastExecShadowEvent{
		RecordedAt: now, Venue: e.venueName, Symbol: sym,
		SnapshotID: fmt.Sprintf("%s|%s|%d", e.venueName, sym, now.UnixNano()),
		Bid:        book.BidPrice, Ask: book.AskPrice, Mid: mid,
		TopBidQty: book.BidQty, TopAskQty: book.AskQty,
	}
	if mid > 0 {
		ev.SpreadBps = (book.AskPrice - book.BidPrice) / mid * 10000
	}
	if book.BidQty+book.AskQty > 0 {
		ev.OrderbookImbalance = (book.BidQty - book.AskQty) / (book.BidQty + book.AskQty)
	}
	dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
	depth, derr := e.v.FetchDepth(dctx, venue.MarketPerp, venue.Symbol(sym), 50)
	dcancel()
	if derr == nil && mid > 0 {
		acc := func(levels []venue.DepthLevel) {
			for _, lv := range levels {
				if lv.Price <= 0 || lv.Qty <= 0 {
					continue
				}
				d := math.Abs(lv.Price-mid) / mid * 10000
				n := lv.Price * lv.Qty
				if d <= 5 {
					ev.DepthNotional5bps += n
				}
				if d <= 10 {
					ev.DepthNotional10bps += n
				}
				if d <= 20 {
					ev.DepthNotional20bps += n
				}
			}
		}
		acc(depth.Bids)
		acc(depth.Asks)
	}
	e.updateFastMidHist(sym, now, mid)
	ev.MidImpulse15sBps = e.fastImpulseBps(sym, mid, now, 15)
	ev.MidImpulse30sBps = e.fastImpulseBps(sym, mid, now, 30)
	ev.MidImpulse60sBps = e.fastImpulseBps(sym, mid, now, 60)
	// v3① 池塘质量:样本入池(仅当 30s 冲量真实可算)+ 打标(恒开,零成本;过滤由独立开关灰度)
	pondKey := e.venueName + "|" + sym
	if fastMidSpanSecs(pondKey, now) >= 30 {
		fastPondRecord(pondKey, now, math.Abs(ev.MidImpulse30sBps), ev.DepthNotional10bps)
	}
	enMed, dpMed, pn := fastPondEval(pondKey)
	ev.PondEnergyMedBps, ev.PondDepthMedUSDT, ev.PondSamples = enMed, dpMed, pn
	if pn < fastPondWarmupSamples {
		ev.PondOK = true // 热身期 fail-open:不因样本不足误杀
	} else {
		ev.PondOK = enMed >= e.cfg.FastPondMinEnergyBps && dpMed >= e.cfg.FastPondDepthLoUSDT
	}
	if ev.SpreadBps >= 1 {
		ev.MakerPlaceable = true
		ev.MakerTheoFillPrice = book.BidPrice
		ev.MakerTheoCancelPrice = book.AskPrice
	}
	ev.FastCandidateScore, ev.FastCandidateSide, ev.FastCandidateReason = scoreFastEvent(ev)
	return ev
}

// buildFastAltCandidates v3② 接型候选生成(record-only,绝不实盘;fastLiveOpen 有 CandidateSource 硬闸)。
// 动机:91k shadow 实证同冲量档"接"比"追"跟随率高 8-15pp,而现生成器 98% 产追单——缺的不是打分,是候选来源。
//   liq_catch:强平后 60s 内接被迫者(P2 首验:接被迫卖 82.0% 全场最高 vs 无强平 60.7%)。
//   fade_exhaustion:30s 冲量≥阈值 且 15s 冲量已减速(<40%)→ 追高者力竭,逆向接回落。
// 先 shadow 攒前向成绩(fwd_15/30/60s 由离线脚本回填核对),达标才谈灰度实盘。
func (e *OKXDirectionalEngine) buildFastAltCandidates(base *fastExecShadowEvent, now time.Time) []*fastExecShadowEvent {
	if base == nil || base.Mid <= 0 {
		return nil
	}
	out := make([]*fastExecShadowEvent, 0, 2)
	if side, n := fastRecentLiqCatchSide(base.Symbol, now, 60); side != "" {
		ev := *base
		ev.SnapshotID = base.SnapshotID + "|liq_catch"
		ev.CandidateSource = "liq_catch"
		ev.FastCandidateSide = side
		ev.FastCandidateScore = 0
		ev.FastCandidateReason = fmt.Sprintf("liq_catch_n%d", n)
		out = append(out, &ev)
	}
	minImp := e.cfg.FastFadeMinImpulseBps
	if minImp <= 0 {
		minImp = 8
	}
	if a30 := math.Abs(base.MidImpulse30sBps); a30 >= minImp && math.Abs(base.MidImpulse15sBps) <= 0.4*a30 {
		ev := *base
		ev.SnapshotID = base.SnapshotID + "|fade"
		ev.CandidateSource = "fade_exhaustion"
		if base.MidImpulse30sBps > 0 {
			ev.FastCandidateSide = "short"
		} else {
			ev.FastCandidateSide = "long"
		}
		ev.FastCandidateScore = 0
		ev.FastCandidateReason = "fade_exhaustion"
		out = append(out, &ev)
	}
	return out
}

func (e *OKXDirectionalEngine) updateFastMidHist(sym string, now time.Time, mid float64) {
	key := e.venueName + "|" + sym
	fastMidMu.Lock()
	defer fastMidMu.Unlock()
	h := append(fastMidHist[key], fastMidPoint{now, mid})
	cut := now.Add(-300 * time.Second)
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastMidHist[key] = h[i:]
}

func (e *OKXDirectionalEngine) fastImpulseBps(sym string, midNow float64, now time.Time, secs int) float64 {
	if midNow <= 0 {
		return 0
	}
	key := e.venueName + "|" + sym
	target := now.Add(-time.Duration(secs) * time.Second)
	fastMidMu.Lock()
	defer fastMidMu.Unlock()
	var past float64
	for _, p := range fastMidHist[key] {
		if !p.ts.After(target) {
			past = p.mid
		}
	}
	if past <= 0 {
		return 0
	}
	return (midNow - past) / past * 10000
}

// scoreFastEvent M4 规则型 record-only 打分(0..1)+ 方向。只用本事件当前/过去字段,不含任何未来收益。
func scoreFastEvent(ev *fastExecShadowEvent) (float64, string, string) {
	reasons := make([]string, 0, 5)
	score := 0.0
	if ev.SpreadBps > 0 {
		ss := (8.0 - ev.SpreadBps) / 8.0
		if ss < 0 {
			ss = 0
		}
		if ss > 1 {
			ss = 1
		}
		score += 0.30 * ss
		if ss >= 0.6 {
			reasons = append(reasons, "narrow_spread")
		}
	}
	ds := ev.DepthNotional10bps / 300.0
	if ds > 1 {
		ds = 1
	}
	score += 0.25 * ds
	if ds >= 0.6 {
		reasons = append(reasons, "deep_book")
	}
	ai := math.Abs(ev.OrderbookImbalance)
	var isc float64
	switch {
	case ai < 0.1:
		isc = 0.1
	case ai <= 0.7:
		isc = 1.0
		reasons = append(reasons, "clear_imbalance")
	case ai <= 0.85:
		isc = 0.5
	default:
		isc = 0.1
		reasons = append(reasons, "extreme_imbalance_penalty")
	}
	score += 0.20 * isc
	s15, s30, s60 := fastSign(ev.MidImpulse15sBps), fastSign(ev.MidImpulse30sBps), fastSign(ev.MidImpulse60sBps)
	// 方向:以 30s 主短动量定(回退 15s→60s);有动量即有方向(不要求三窗全同号,否则成熟数据下几乎永远 flat)。
	sd := s30
	if sd == 0 {
		sd = s15
	}
	if sd == 0 {
		sd = s60
	}
	side := "flat"
	switch {
	case sd > 0:
		side = "long"
	case sd < 0:
		side = "short"
	}
	// 延续质量加分:15/30/60s 三者同号 → 强延续 +0.25;仅 30s&60s 同号 → 弱延续 +0.12。
	nz := make([]int, 0, 3)
	for _, s := range []int{s15, s30, s60} {
		if s != 0 {
			nz = append(nz, s)
		}
	}
	allSame := len(nz) >= 2
	for _, s := range nz {
		if s != nz[0] {
			allSame = false
			break
		}
	}
	if allSame {
		score += 0.25
		reasons = append(reasons, "impulse_cont")
	} else if s30 != 0 && s30 == s60 {
		score += 0.12
		reasons = append(reasons, "impulse_partial")
	} else if side != "flat" && s15 != 0 && s30 != 0 && s15 != s30 {
		reasons = append(reasons, "impulse_conflict")
	}
	if side == "long" && ev.OrderbookImbalance < -0.3 {
		score -= 0.10
		reasons = append(reasons, "imbalance_against_side")
	}
	if side == "short" && ev.OrderbookImbalance > 0.3 {
		score -= 0.10
		reasons = append(reasons, "imbalance_against_side")
	}
	if score < 0 {
		score = 0
	}
	return score, side, strings.Join(reasons, "|")
}

// ============ fast live 执行(真实下单)============

func (e *OKXDirectionalEngine) fastLiveLoad() {
	fastLiveMu.Lock()
	defer fastLiveMu.Unlock()
	if fastLiveLoaded[e.venueName] {
		return
	}
	fastLiveLoaded[e.venueName] = true
	if fastLivePos[e.venueName] == nil {
		fastLivePos[e.venueName] = map[string]*fastLivePosition{}
	}
	data, err := os.ReadFile(filepath.Join(e.recordDir, fastLivePositionFile))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return
	}
	var list []*fastLivePosition
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, p := range list {
		if p != nil && strings.TrimSpace(p.Symbol) != "" {
			fastLivePos[e.venueName][p.Symbol] = p
		}
	}
	if len(list) > 0 {
		e.logger.Printf("[fast_live] 重启接管 fast 仓 %d 笔(将按 horizon 平掉)", len(list))
	}
}

// fastLivePersistLocked 调用者须持有 fastLiveMu。
// 原子写(tmp+fsync+rename)+ 失败留日志:此文件是重启接管 fast 仓的唯一依据,
// 半写损坏或静默失败 = 崩溃后丢仓(实盘孤儿仓无人管)。
func (e *OKXDirectionalEngine) fastLivePersistLocked() {
	list := make([]*fastLivePosition, 0, len(fastLivePos[e.venueName]))
	for _, p := range fastLivePos[e.venueName] {
		list = append(list, p)
	}
	if err := writeJSONFile(filepath.Join(e.recordDir, fastLivePositionFile), list); err != nil {
		e.logger.Printf("[fast_live] 持仓落盘失败(重启可能丢仓,需关注): %v", err)
	}
}

// fastCurrentMid best-effort 拉当前中价(决策用);失败返回 0。
func (e *OKXDirectionalEngine) fastCurrentMid(ctx context.Context, sym string) float64 {
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	book, err := e.v.FetchBookTicker(bctx, venue.MarketPerp, venue.Symbol(sym))
	cancel()
	if err != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
		return 0
	}
	return (book.BidPrice + book.AskPrice) / 2
}

// fastLiveManage 出场:v2 scalper exit(覆盖成本落袋 / 反向走弱快平 / 180s 兜底);"fixed"=旧 60s 固定。平仓必须重试到成功(防孤儿)。
func (e *OKXDirectionalEngine) fastLiveManage(ctx context.Context) {
	now := e.nowFn().UTC()
	fastLiveMu.Lock()
	poss := make([]*fastLivePosition, 0, len(fastLivePos[e.venueName]))
	for _, p := range fastLivePos[e.venueName] {
		poss = append(poss, p)
	}
	fastLiveMu.Unlock()
	maxHold := float64(e.cfg.FastV2MaxHoldSeconds)
	if maxHold <= 0 {
		maxHold = 180
	}
	for _, p := range poss {
		select {
		case <-ctx.Done():
			return
		default:
		}
		hold := now.Sub(p.OpenedAt).Seconds()
		mid := e.fastCurrentMid(ctx, p.Symbol)
		haveMid := mid > 0 && p.EntryPrice > 0
		var ubps float64
		var mae3, mfe10, mae10 float64
		var seen3, seen10 bool
		if haveMid {
			ubps = (mid - p.EntryPrice) / p.EntryPrice * 10000
			if p.Side == "short" {
				ubps = -ubps
			}
			// 持锁更新:fastLivePersistLocked(持 fastLiveMu)会并发 marshal 这些字段,无锁写是数据竞态。
			fastLiveMu.Lock()
			if ubps > p.PeakBps {
				p.PeakBps = ubps
			}
			if ubps < p.TroughBps {
				p.TroughBps = ubps
			}
			fastUpdateExcursion(p, hold, ubps)
			mae3, seen3 = p.Mae3, p.Seen3
			mfe10, mae10, seen10 = p.Mfe10, p.Mae10, p.Seen10
			fastLiveMu.Unlock()
		}
		// v2 出场时序闸(2026-07-03,交易所账本 6304 笔解剖):minHold 内禁一切亏损驱动出场
		// (<15s 出场毛均 -12.8bps 是出血主源;60s+ 桶毛正)。灾难线任何时刻有效(防插针深亏);
		// 盈利保护(trail_tp)不受 minHold 限制(锁盈不是翻炒来源)。
		minHold := float64(e.cfg.FastMinHoldSeconds)
		inMinHold := minHold > 0 && hold < minHold
		if haveMid && e.cfg.FastDisasterStopBps > 0 && ubps <= -e.cfg.FastDisasterStopBps {
			e.fastLiveClose(ctx, p, "fast_disaster_stop")
			continue
		}
		// v3③ 物种化出场(424笔实证;默认关,随接型入场灰度一起开):
		//   A(点燃者碾过):3s 内已深亏 ≤-6bps → PF0.26、仅26%回正,minHold 对它是刑具 → 立刻认错(豁免 minHold);
		//   C(死水):10s 无正反馈且不深亏 → PF0.51,时间成本主导 → 快撤换弹;
		//   B(打完即弹,PF1.50 唯一毛正物种)不在此砍,交给既有 trail/max_hold。
		if e.cfg.FastSpeciesExitEnabled && haveMid {
			if seen3 && hold >= 3 && mae3 <= -6 && ubps <= -4 {
				e.fastLiveClose(ctx, p, "fast_species_a_cut")
				continue
			}
			if seen10 && hold >= 10 && mfe10 < 1 && mae10 > -4 && ubps < 1 && ubps > -4 {
				e.fastLiveClose(ctx, p, "fast_species_c_exit")
				continue
			}
		}
		if p.MakerEntry { // v3 出场:早砍 / 移动止盈 / 无正反馈快平 / 止损 / 兜底(均 taker)
			// v3.1 早砍:成交后 >=Ns 仍无正反馈(峰值≤0)且已明显走坏(谷值≤-MAE 且当前≤-cur)→ 提前砍,别等 -15bps 大止损。
			if e.cfg.FastEarlyCutEnabled && !inMinHold && haveMid && hold >= float64(e.cfg.FastEarlyCutSeconds) &&
				p.PeakBps <= 0 && p.TroughBps <= -e.cfg.FastEarlyCutMaeBps && ubps <= -e.cfg.FastEarlyCutCurBps {
				e.fastLiveClose(ctx, p, "fast_early_cut")
				continue
			}
			if haveMid && !inMinHold && e.cfg.FastV2StopBps > 0 && ubps <= -e.cfg.FastV2StopBps {
				e.fastLiveClose(ctx, p, "fast_stop_reversal")
				continue
			}
			if haveMid && e.cfg.FastV3TrailActivateBps > 0 && p.PeakBps >= e.cfg.FastV3TrailActivateBps && ubps <= p.PeakBps-e.cfg.FastV3TrailGiveBackBps {
				e.fastLiveClose(ctx, p, "fast_trail_tp") // 浮盈扩大后回吐 → 移动止盈
				continue
			}
			if e.cfg.FastV3NoFeedbackSeconds > 0 && !inMinHold && hold >= float64(e.cfg.FastV3NoFeedbackSeconds) && p.PeakBps < e.cfg.FastV3NoFeedbackMinBps {
				e.fastLiveClose(ctx, p, "fast_no_feedback") // 成交后无正反馈 → 快平
				continue
			}
			if hold >= maxHold {
				e.fastLiveClose(ctx, p, "fast_max_hold")
			}
			continue
		}
		// 非 maker(v1 fixed / v2 scalper taker)
		if strings.EqualFold(strings.TrimSpace(e.cfg.FastLiveExitMode), "fixed") {
			horizon := float64(e.cfg.FastLiveHorizonSeconds)
			if horizon <= 0 {
				horizon = 60
			}
			if hold >= horizon {
				e.fastLiveClose(ctx, p, "fast_horizon_exit")
			}
			continue
		}
		if haveMid && e.cfg.FastV2TakeProfitBps > 0 && ubps >= e.cfg.FastV2TakeProfitBps {
			e.fastLiveClose(ctx, p, "fast_tp")
			continue
		}
		if haveMid && !inMinHold && e.cfg.FastV2StopBps > 0 && ubps <= -e.cfg.FastV2StopBps {
			e.fastLiveClose(ctx, p, "fast_stop_reversal")
			continue
		}
		if hold >= maxHold {
			e.fastLiveClose(ctx, p, "fast_max_hold")
		}
	}
}

func (e *OKXDirectionalEngine) fastLiveClose(ctx context.Context, p *fastLivePosition, reason string) {
	sym := p.Symbol
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(sym), venue.MarketPerp)
	if !ok {
		e.logger.Printf("[fast_live] %s 平仓: instrument 缺失,下轮重试", sym)
		return
	}
	caps := e.v.Capabilities()
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	book, berr := e.v.FetchBookTicker(bctx, venue.MarketPerp, venue.Symbol(sym))
	cancel()
	if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
		e.logger.Printf("[fast_live] %s 平仓: 盘口异常,下轮重试", sym)
		return
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	var cside venue.Side
	var dside DirectionSide
	if p.Side == "long" {
		cside, dside = venue.SideSell, DirectionLong
	} else {
		cside, dside = venue.SideBuy, DirectionShort
	}
	clientID := okxDirectionalClientID(sym, "fastclose")
	// v2.2 出场执行分级:盈利/到时出场(trail_tp/max_hold)非紧急 → maker-first 省 ~3bps(taker 5bps→maker 2bps,
	// 费用是 trail_tp 利润的一半);止损/灾难/早砍保持 urgent IOC 秒平(等 maker = 亏损扩大)。
	// maker 未成交由 placeDirectionalLeg 的平仓 IOC 兜底,不会挂死。
	urgentClose := reason != "fast_trail_tp" && reason != "fast_max_hold"
	res, legInfo, oerr := e.placeDirectionalLeg(ctx, venue.Symbol(sym), cside, p.Qty, clientID, true, instr, caps.ContractBasedPerp, urgentClose, 0)
	if oerr != nil || res.ExecutedQty <= executionQtyTolerance {
		// 交易所明确回报「该方向无持仓可平」(OKX 51169 等严格 allowlist)→ 主动按方向核验交易所持仓后清理本地幽灵,
		// 不再每 2s 无限重试(根治 GLW 幽灵仓刷屏 + 连带限频)。任一防护不满足 → 走原「下轮重试」。
		if exchangePositionAbsent(oerr) {
			cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
			poss, perr := e.v.FetchPositions(cctx)
			ccancel()
			if perr == nil { // 查询失败=不确定=不清理
				absent, oppositeOnly := positionDirectionAbsent(poss, sym, p.Side, executionQtyTolerance)
				if oppositeOnly {
					e.logger.Printf("[fast_live][warn] %s 本地=%s 但交易所仅有反向仓,判定本地该方向为幽灵,允许清理", sym, p.Side)
				}
				if absent {
					e.fastCleanupAbsentPosition(p)
					return
				}
			}
		}
		e.logger.Printf("[fast_live] %s 平仓未成交(下轮重试,防孤儿): err=%v executed=%.8f", sym, oerr, res.ExecutedQty)
		return
	}
	exitPrice, _ := sanitizeFillPrice(res.AvgPrice, mid, e.cfg.FillPriceMaxDevPct)
	realized := directionalUPnLPct(dside, p.EntryPrice, exitPrice)*p.NotionalUSDT - p.OpenFeeUSDT - math.Abs(res.Fee)
	now := e.nowFn().UTC()
	holdSec := int(now.Sub(p.OpenedAt).Seconds())
	if holdSec < 0 {
		holdSec = 0
	}
	settle := okxDirectionalSettlement{
		Time: now, Symbol: sym, Side: p.Side, Entry: p.EntryPrice, Exit: exitPrice,
		NotionalUSDT: p.NotionalUSDT, RealizedUSDT: realized, HoldMinutes: holdSec / 60, HoldSeconds: holdSec,
		Regime: "fast", Reason: reason, PositionID: p.PositionID,
		OpenViaTaker: p.OpenViaTaker, CloseViaTaker: legInfo.tookTaker,
		FeeUSDT:         p.OpenFeeUSDT + math.Abs(res.Fee),
		SignalLayer:     fastSignalLayer,
		EntryLayer:      fastEntryLayer,
		ManagementLayer: fastManagementLayer,
		// 复盘字段(按 score 分桶)
		FastCandidateScore:  p.Score,
		FastCandidateSide:   p.Side,
		FastCandidateReason: p.ScoreReason,
		FastCandidateSource: p.CandidateSource,
		FastEventID:         p.EventID,
		SourceEventTime:     p.EventTime,
		EntrySpreadBps:      p.EntrySpreadBps,
		EntryImbalance:      p.EntryImbalance,
		EntryMidImpulse15s:  p.EntryImpulse15s,
		EntryMidImpulse30s:  p.EntryImpulse30s,
		EntryMidImpulse60s:  p.EntryImpulse60s,
		// v3 maker 执行
		MakerEntry:      p.MakerEntry,
		MakerOrderPrice: p.MakerOrderPrice,
		MakerWaitMs:     p.MakerWaitMs,
		FillMid:         p.FillMid,
		MakerEdgeBps:    p.MakerEdgeBps,
	}
	// seen-aware 成交后水位:仅采样到的窗口写指针(nil=未采样,非nil含0=采样到)
	ptr := func(seen bool, v float64) *float64 {
		if !seen {
			return nil
		}
		x := v
		return &x
	}
	settle.PostFillMfe3sBps, settle.PostFillMae3sBps = ptr(p.Seen3, p.Mfe3), ptr(p.Seen3, p.Mae3)
	settle.PostFillMfe5sBps, settle.PostFillMae5sBps = ptr(p.Seen5, p.Mfe5), ptr(p.Seen5, p.Mae5)
	settle.PostFillMfe10sBps, settle.PostFillMae10sBps = ptr(p.Seen10, p.Mfe10), ptr(p.Seen10, p.Mae10)
	settle.PostFillMfe15sBps, settle.PostFillMae15sBps = ptr(p.Seen15, p.Mfe15), ptr(p.Seen15, p.Mae15)
	settle.PostFillMfe30sBps, settle.PostFillMae30sBps = ptr(p.Seen30, p.Mfe30), ptr(p.Seen30, p.Mae30)
	settle.PostFillMfe60sBps, settle.PostFillMae60sBps = ptr(p.Seen60, p.Mfe60), ptr(p.Seen60, p.Mae60)
	settle.FastExitVersion = "v2" // 出场时序 v2(minHold+灾难线+日熔断);离线分析与 v1 学费数据分代对照
	settle.EntryAdaptiveTags = p.EntryAdaptiveTags
	if err := appendJSONLRecord(e.settlementsPath, settle); err != nil {
		e.logger.Printf("[fast_live] 结算落盘失败: %v", err)
	}
	// v2 独立日志(用户要求):fast 结算另存一份专用文件,后续分析不用从混合 settlements 里过滤。
	if err := appendJSONLRecord(filepath.Join(e.recordDir, "fast_v2_settlements.jsonl"), settle); err != nil {
		e.logger.Printf("[fast_live] v2 结算副本落盘失败: %v", err)
	}
	e.fastDailyAdd(now, sym, realized)
	fastLiveMu.Lock()
	delete(fastLivePos[e.venueName], sym)
	e.fastLivePersistLocked()
	fastLiveMu.Unlock()
	// 坏出场(反向止损/无反馈/早砍)→ 该 symbol fast 层短冷却,防同一坏信号在同一行情片段反复刷单。
	if reason == "fast_stop_reversal" || reason == "fast_no_feedback" || reason == "fast_early_cut" || reason == "fast_species_a_cut" {
		e.fastSetCooldown(sym, reason, now)
	}
	e.logger.Printf("[fast_live] 已平 %s %s 持有%ds entry=%.6f exit=%.6f realized=%.4fU 原因=%s", sym, p.Side, holdSec, p.EntryPrice, exitPrice, realized, reason)
}

// exchangePositionAbsent 严格判定:平仓错误是否代表交易所侧「该方向已无持仓可 reduce/close」。
// 只认明确的「无持仓」码/文案(窄 allowlist);网络/限频(50011)/超时/泛化拒单一律 false → 继续重试,绝不误删真实仓。
func exchangePositionAbsent(err error) bool {
	if err == nil {
		return false
	}
	le := strings.ToLower(err.Error())
	if strings.Contains(le, "51169") { // OKX: 无该方向持仓可 reduce/close(数字码最稳)
		return true
	}
	for _, s := range []string{
		"empty position",
		"position not found",
		"no position",
		"no open position",
		"position is zero",
	} {
		if strings.Contains(le, s) {
			return true
		}
	}
	return false
}

// positionDirectionAbsent 纯判定:交易所持仓快照里,本地 side 方向的 sym 仓是否已不存在。
// absent=true → 可清理本地该方向 ghost;oppositeOnly=true → 仅存在反向仓(仍可清理,但调用方应 warning)。
// PositionAmt: 正=多、负=空。仅当交易所仍有「同向」非零仓时返回 absent=false(禁止清理)。
func positionDirectionAbsent(poss []venue.Position, sym, side string, tol float64) (absent bool, oppositeOnly bool) {
	for _, pp := range poss {
		if !strings.EqualFold(string(pp.Symbol), sym) {
			continue
		}
		if side == "long" {
			if pp.PositionAmt > tol {
				return false, false // 仍有同向多仓 → 禁止清理
			}
			if pp.PositionAmt < -tol {
				oppositeOnly = true // 仅反向空仓
			}
		} else { // short
			if pp.PositionAmt < -tol {
				return false, false // 仍有同向空仓 → 禁止清理
			}
			if pp.PositionAmt > tol {
				oppositeOnly = true // 仅反向多仓
			}
		}
	}
	return true, oppositeOnly
}

// fastCleanupAbsentPosition 幽灵清理:非成交,不臆造盈亏。写独立 fast_position_cleanup.jsonl 留痕
// (realized=0/fee=0,保留 hold_seconds 与分层字段),移除本地跟踪。绝不写 settlements.jsonl。
func (e *OKXDirectionalEngine) fastCleanupAbsentPosition(p *fastLivePosition) {
	now := e.nowFn().UTC()
	holdSec := int(now.Sub(p.OpenedAt).Seconds())
	if holdSec < 0 {
		holdSec = 0
	}
	rec := okxDirectionalSettlement{
		Time:         now,
		Symbol:       p.Symbol,
		Side:         p.Side,
		Entry:        p.EntryPrice,
		Exit:         0, // 无真实成交价
		NotionalUSDT: p.NotionalUSDT,
		RealizedUSDT: 0, // 不臆造盈亏
		FeeUSDT:      0, // 不臆造费用
		HoldMinutes:  holdSec / 60,
		HoldSeconds:  holdSec,
		Regime:       "fast",
		Reason:       fastCleanupReason,
		PositionID:   p.PositionID,
		// 分层字段正常写(便于归因脚本按 reason 排除)
		SignalLayer:     fastSignalLayer,
		EntryLayer:      fastEntryLayer,
		ManagementLayer: fastManagementLayer,
		// 入场复盘字段(追这条 ghost 的来源;realized=0 不影响 PnL)
		FastCandidateScore:  p.Score,
		FastCandidateSide:   p.Side,
		FastCandidateReason: p.ScoreReason,
		FastEventID:         p.EventID,
		OpenViaTaker:        p.OpenViaTaker,
		MakerEntry:          p.MakerEntry,
	}
	if err := appendJSONLRecord(filepath.Join(e.recordDir, fastPositionCleanupFile), rec); err != nil {
		e.logger.Printf("[fast_live] 幽灵清理留痕失败: %v", err)
	}
	fastLiveMu.Lock()
	delete(fastLivePos[e.venueName], p.Symbol)
	e.fastLivePersistLocked()
	fastLiveMu.Unlock()
	e.logger.Printf("[fast_live] %s 交易所已确认该方向(%s)无持仓(幽灵),清理本地跟踪 reason=%s hold=%ds realized=0 fee=0",
		p.Symbol, p.Side, fastCleanupReason, holdSec)
}

// makerFillState 纯判定 maker 单状态:必须 executed>tol 且 status∈{filled,partially_filled} 才算成交;
// executed>tol 但 status 非 fill 类 = 可疑(疑似撤单/回报竞态,不能当普通成交建仓);否则未成交。
// 返回:"fill" / "suspicious" / "not_filled"。
func makerFillState(executed float64, status string, tol float64) string {
	st := strings.ToLower(strings.TrimSpace(status))
	filled := st == "filled" || st == "partially_filled" || st == "partial_filled"
	if executed > tol {
		if filled {
			return "fill"
		}
		return "suspicious"
	}
	return "not_filled"
}

// fastMakerEntry v3 单发 post-only 入场(不追价):挂被动价,等 wait_ms,成交则返回;超时撤单(不重挂)。
// 返回 (成交结果, 挂单价, 等待ms, confirmPath, error)。confirmPath ∈
//   immediate_fill / poll_fill / cancel_then_query_fill / suspicious_exec_qty_no_fill_status / maker_not_filled。
// 成交判定加严:仅 makerFillState=="fill"(executed>tol 且 status filled/partial) 才返回成交;
// executed>tol 但 status 非 fill 类 → suspicious(调用方不建仓);QueryOrder 失败/未成交 → maker_not_filled。
func (e *OKXDirectionalEngine) fastMakerEntry(ctx context.Context, sym string, side venue.Side, qty float64, instr venue.Instrument, contractBased bool) (venue.OrderResult, float64, int, string, error) {
	var acc venue.OrderResult
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	book, berr := e.v.FetchBookTicker(bctx, venue.MarketPerp, venue.Symbol(sym))
	cancel()
	if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
		return acc, 0, 0, "maker_not_filled", berr
	}
	price := makerPassivePrice(side, book)
	price, q := quantizeOrderPriceQty(price, qty, side, instr, contractBased, e.logger, sym)
	clientID := okxDirectionalClientID(sym, "fastmaker")
	start := e.nowFn().UTC()
	placed, perr := e.v.PlaceOrder(ctx, venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: venue.Symbol(sym), Side: side,
		Type: venue.OrderTypePostOnly, Qty: q, Price: price, ClientOrderID: clientID, ReduceOnly: false,
	})
	if perr != nil {
		return acc, price, 0, "maker_not_filled", perr
	}
	elapsed := func() int { return int(e.nowFn().UTC().Sub(start).Milliseconds()) }
	if makerFillState(placed.ExecutedQty, placed.Status, executionQtyTolerance) == "fill" {
		return placed, price, elapsed(), "immediate_fill", nil
	}
	waitMs := e.cfg.FastMakerWaitMs
	if waitMs <= 0 {
		waitMs = 2000
	}
	pollMs := e.cfg.FastMakerPollMs
	if pollMs <= 0 {
		pollMs = 300
	}
	deadline := start.Add(time.Duration(waitMs) * time.Millisecond)
	for e.nowFn().UTC().Before(deadline) {
		select {
		case <-ctx.Done():
			_ = e.v.CancelOrder(ctx, venue.MarketPerp, venue.Symbol(sym), clientID)
			return acc, price, elapsed(), "maker_not_filled", ctx.Err()
		case <-time.After(time.Duration(pollMs) * time.Millisecond):
		}
		if r, qerr := e.v.QueryOrder(ctx, venue.MarketPerp, venue.Symbol(sym), clientID); qerr == nil {
			switch makerFillState(r.ExecutedQty, r.Status, executionQtyTolerance) {
			case "fill":
				return r, price, elapsed(), "poll_fill", nil
			case "suspicious":
				return r, price, elapsed(), "suspicious_exec_qty_no_fill_status", nil
			}
			if strings.EqualFold(r.Status, "canceled") || strings.EqualFold(r.Status, "rejected") {
				return acc, price, elapsed(), "maker_not_filled", nil // 明确未成交,不追价
			}
		}
	}
	// 超时:撤单(不追价),查最终成交(可能等待期内部分成交)
	_ = e.v.CancelOrder(ctx, venue.MarketPerp, venue.Symbol(sym), clientID)
	wms := elapsed()
	if r, qerr := e.v.QueryOrder(ctx, venue.MarketPerp, venue.Symbol(sym), clientID); qerr == nil {
		switch makerFillState(r.ExecutedQty, r.Status, executionQtyTolerance) {
		case "fill":
			return r, price, wms, "cancel_then_query_fill", nil
		case "suspicious":
			return r, price, wms, "suspicious_exec_qty_no_fill_status", nil
		}
	}
	return acc, price, wms, "maker_not_filled", nil // 含 QueryOrder 失败 → 不建仓
}

// fastLiveOpen 命中分数即真实开仓。各 guard:flat不开/异常盘口不开/量化不可交易不开/halt不开/已持(fast或slow)不重复不反手/下单失败不重试。
func (e *OKXDirectionalEngine) fastLiveOpen(ctx context.Context, ev *fastExecShadowEvent) string {
	if ev == nil {
		return "nil"
	}
	if ev.CandidateSource != "" {
		// v3② fade 实盘灰度(2026-07-03 16:44 用户拍板:秒级策略 2h=数万决策周期,机制已核实即动,禁止日历攒数据):
		// 机制=接力竭的 FOMO 冲量(我们做"被迫一次性taker"的对手方=唯一毛正物种 B 的镜像);仅限成熟 OK 池塘。
		// liq_catch 永远硬闸:已证伪(被迫流持续 1-3 分钟,60s 内接刀三桶全负,力竭/吸收确认都救不了)。
		if !(e.cfg.FastFadeLiveEnabled && ev.CandidateSource == "fade_exhaustion" && ev.PondOK && ev.PondSamples >= fastPondWarmupSamples) {
			return "record_only_source"
		}
	}
	if ev.FastCandidateSide != "long" && ev.FastCandidateSide != "short" { // flat 不开
		return "flat"
	}
	if ev.Mid <= 0 || ev.Bid <= 0 || ev.Ask <= 0 { // 异常盘口不开
		return "bad_book"
	}
	if e.controlPlane != nil && e.controlPlane.IsEmergencyHaltActive() { // 唯一急停
		return "halt"
	}
	// v3① 池塘过滤(独立灰度开关;默认仅打标不拦):死水/薄盘不下场,不跟机构抢镜面盘
	if e.cfg.FastPondFilterEnabled && !ev.PondOK {
		return "pond_reject"
	}
	sym := ev.Symbol
	// v2.2 自适应门槛(替代禁开):单币当日亏损档位抬高再入场分数,弱时段(UTC 弱段)提标+降名义。
	// fade 接型候选无 scorer 分数(机制条件即资格:冲量≥8bps+15s力竭+OK池塘),跳过分数闸;
	// 冷却/单币退避降名义/日熔断/持仓互斥等其余闸照过。
	effThreshold, notionalFrac, adaptiveTags := e.fastEntryAdaptive(sym, e.nowFn().UTC())
	if ev.CandidateSource == "" && ev.FastCandidateScore < effThreshold {
		if adaptiveTags != "" {
			return "low_score_adaptive"
		}
		return "low_score"
	}
	// fast 短冷却中 → 跳过(只影响 fast 层;非永久黑名单)
	if e.fastInCooldown(sym, e.nowFn().UTC()) {
		return "cooldown"
	}
	// fast 层日亏熔断(学费上限):当日 fast 净亏达阈值 → 当日停开新 fast 仓(在场仓照常管理;北京日切自动恢复)。
	if lim := e.effectiveFastDailyLossLimit(); lim > 0 {
		if realized := e.fastDailyRealized(e.nowFn().UTC()); realized <= -lim {
			return "daily_loss_halt"
		}
	}
	// 同 symbol 已有未平 fast 仓 → 不重复、不反手
	fastLiveMu.Lock()
	_, hasFast := fastLivePos[e.venueName][sym]
	fastLiveMu.Unlock()
	if hasFast {
		return "held_fast"
	}
	// 慢引擎已持该 symbol → 跳过(避免同标的冲突)
	e.mu.RLock()
	_, hasSlow := e.positions[sym]
	e.mu.RUnlock()
	if hasSlow {
		return "held_slow"
	}
	notional := e.cfg.FastLiveNotionalUSDT
	if notional <= 0 {
		notional = e.cfg.PerPositionNotionalUSDT
	}
	if notional <= 0 {
		return "no_notional"
	}
	notional *= notionalFrac // 弱时段降名义(便宜地继续采样)
	lev := float64(e.leverage())
	if lev <= 0 {
		lev = 1
	}
	perPosCapital := notional / lev
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(sym), venue.MarketPerp)
	if !ok {
		return "no_instrument"
	}
	caps := e.v.Capabilities()
	mid := ev.Mid
	qty := normalizeVenueQty(notional/mid, instr, caps.ContractBasedPerp)
	if !openLegTradableAfterQuantize(qty, mid, instr) { // 量化不可交易不开
		return "not_tradable"
	}
	if avail, merr := fetchVenuePerpAvailableUSDT(ctx, e.v); merr == nil && avail+1e-9 < perPosCapital {
		return "low_margin"
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[fast_live][OFF] 拟开 %s %s 名义=%.2f score=%.2f", sym, ev.FastCandidateSide, notional, ev.FastCandidateScore)
		return "off"
	}
	if err := e.v.SetLeverage(ctx, venue.Symbol(sym), e.leverage()); err != nil {
		e.logger.Printf("[fast_live] %s 设杠杆失败: %v", sym, err)
		return "set_lev_fail"
	}
	var side venue.Side
	if ev.FastCandidateSide == "long" {
		side = venue.SideBuy
	} else {
		side = venue.SideSell
	}
	makerMode := !strings.EqualFold(strings.TrimSpace(e.cfg.FastEntryMode), "taker") // 默认 maker(v3)
	var res venue.OrderResult
	var makerOrderPrice float64
	var makerWaitMs int
	var openViaTaker bool
	var confirmPath string
	if makerMode {
		var oerr error
		res, makerOrderPrice, makerWaitMs, confirmPath, oerr = e.fastMakerEntry(ctx, sym, side, qty, instr, caps.ContractBasedPerp)
		// 成交仅认 status filled/partial 的确认路径;suspicious 不算成交(不建仓)。
		filled := oerr == nil && (confirmPath == "immediate_fill" || confirmPath == "poll_fill" || confirmPath == "cancel_then_query_fill")
		// fill_mid + maker_edge,并记一条 maker 尝试(成交/未成交都记,供分桶)
		fillMidNow := e.fastCurrentMid(ctx, sym)
		if fillMidNow <= 0 {
			fillMidNow = mid
		}
		var fillPx, edgeBps float64
		if filled {
			fillPx, _ = sanitizeFillPrice(res.AvgPrice, fillMidNow, e.cfg.FillPriceMaxDevPct)
			if fillMidNow > 0 {
				if ev.FastCandidateSide == "long" {
					edgeBps = (fillMidNow - fillPx) / fillMidNow * 10000 // 买在 mid 之下=正
				} else {
					edgeBps = (fillPx - fillMidNow) / fillMidNow * 10000 // 卖在 mid 之上=正
				}
			}
		}
		_ = appendJSONLRecord(filepath.Join(e.recordDir, fastMakerAttemptFile), fastMakerAttemptEvent{
			RecordedAt: e.nowFn().UTC(), Venue: e.venueName, Symbol: sym, Side: ev.FastCandidateSide,
			Score: ev.FastCandidateScore, ScoreReason: ev.FastCandidateReason,
			MakerOrderPrice: makerOrderPrice, MakerWaitMs: makerWaitMs, MakerFilled: filled,
			FillPrice: fillPx, FillMid: fillMidNow, MakerEdgeBps: edgeBps,
			SpreadBps: ev.SpreadBps, Imbalance: ev.OrderbookImbalance,
			EntryConfirmPath: confirmPath,
		})
		if oerr != nil {
			e.logger.Printf("[fast_live] %s maker下单失败(不重试): %v", sym, oerr)
			return "order_fail"
		}
		// 可疑单:executed>tol 但 status 非 filled/partial → 不建幽灵本地仓,只告警(Phase1;不做 pending 核验)。
		if confirmPath == "suspicious_exec_qty_no_fill_status" {
			e.logger.Printf("[fast_live][warn] %s 可疑成交(不建本地仓): status=%q executed=%.8f exchangeOrderID=%s clientID=%s path=%s",
				sym, res.Status, res.ExecutedQty, res.ExchangeOrderID, nonEmpty(res.ClientOrderID, okxDirectionalClientID(sym, "fastmaker")), confirmPath)
			return "suspicious_no_build"
		}
		if !filled {
			return "maker_not_filled"
		}
		openViaTaker = false
	} else {
		clientID := okxDirectionalClientID(sym, "fastopen")
		var legInfo legExecInfo
		var oerr error
		res, legInfo, oerr = e.placeDirectionalLeg(ctx, venue.Symbol(sym), side, qty, clientID, false, instr, caps.ContractBasedPerp, e.cfg.FastLiveEntryUrgent, e.cfg.FastLiveTakerSlippageBps)
		if oerr != nil {
			e.logger.Printf("[fast_live] %s 开仓失败(不重试): %v", sym, oerr)
			return "order_fail"
		}
		if res.ExecutedQty <= executionQtyTolerance {
			return "no_fill"
		}
		openViaTaker = legInfo.tookTaker
	}
	fillMid := e.fastCurrentMid(ctx, sym)
	if fillMid <= 0 {
		fillMid = mid
	}
	fillPrice, _ := sanitizeFillPrice(res.AvgPrice, fillMid, e.cfg.FillPriceMaxDevPct)
	var makerEdge float64
	if makerMode && fillMid > 0 {
		if ev.FastCandidateSide == "long" {
			makerEdge = (fillMid - fillPrice) / fillMid * 10000
		} else {
			makerEdge = (fillPrice - fillMid) / fillMid * 10000
		}
	}
	now := e.nowFn().UTC()
	pos := &fastLivePosition{
		Symbol: sym, Side: ev.FastCandidateSide, EntryPrice: fillPrice, Qty: res.ExecutedQty,
		NotionalUSDT: notional, OpenedAt: now, PositionID: fmt.Sprintf("%s-%d", sym, now.UnixNano()),
		OpenFeeUSDT: math.Abs(res.Fee), Score: ev.FastCandidateScore, ScoreReason: ev.FastCandidateReason,
		OpenViaTaker:   openViaTaker,
		EventID:        ev.SnapshotID, EventTime: ev.RecordedAt,
		EntrySpreadBps: ev.SpreadBps, EntryImbalance: ev.OrderbookImbalance,
		EntryImpulse15s: ev.MidImpulse15sBps, EntryImpulse30s: ev.MidImpulse30sBps, EntryImpulse60s: ev.MidImpulse60sBps,
		MakerEntry: makerMode, MakerOrderPrice: makerOrderPrice, MakerWaitMs: makerWaitMs, FillMid: fillMid, MakerEdgeBps: makerEdge,
		EntryAdaptiveTags: adaptiveTags,
		CandidateSource:   ev.CandidateSource,
	}
	fastLiveMu.Lock()
	if fastLivePos[e.venueName] == nil {
		fastLivePos[e.venueName] = map[string]*fastLivePosition{}
	}
	fastLivePos[e.venueName][sym] = pos
	e.fastLivePersistLocked()
	fastLiveMu.Unlock()
	e.logger.Printf("[fast_live] 已开 %s %s qty=%.8f @%.6f 名义=%.2f score=%.2f maker=%v edge=%.1fbps wait=%dms", sym, pos.Side, pos.Qty, pos.EntryPrice, notional, pos.Score, pos.MakerEntry, pos.MakerEdgeBps, pos.MakerWaitMs)
	return "opened"
}
