package app

// ===== 自动阅卷人(2026-07-08 21:30 BJ 用户拍板"前向重考→发准考证→递你裁,完全可以全自动")=====
//
// 职责:常驻循环盯"在考考试注册表",按预注册冻结口径自动阅卷——
//   到线且过门 → 自动发"准考证" + Telegram 推送(递用户裁探针);
//   到线且不过 → 自动判负 + 推送(死因解剖排队)。
// 自动化边界(铁律,永不越界):机器只【发证+通知】;设 fast_probe_source / live 开关 /
// 写持证名单 = 永远是用户的手。阅卷状态持久化(重启不重复推送)。
//
// 考试注册表=登记态(项目官方决策记录,新预注册考试在此登记,与 HANDOFF 同步;
// 口径冻结在代码=改判决门必过 code review 留痕)。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// examReadJSONFile 读 JSON 状态文件(不存在=空,不报错)。
func examReadJSONFile(path string, into interface{}) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, into)
}

const fastExamStateFile = "exam_supervisor_state.json"

// fastAutoProbeFile 自动探针名单(07-09 用户"考证通过就接入,全自动"拍板):
// 阅卷人过门自动写入,fastProbeHas 将其与 config 名单合并生效。
// 安全边界:每源预算 20笔/0.5U 由探针治理器封死;并发≤fastAutoProbeMaxConcurrent;
// 加钱(灰度/持证)永远人工。文件可人工删条目=手动收回。
const fastAutoProbeFile = "auto_probe_sources.json"
const fastAutoProbeMaxConcurrent = 2

// fastAutoProbeState 自动接入记录。
type fastAutoProbeState struct {
	Sources map[string]time.Time `json:"sources"` // source -> 接入时刻
}

var (
	fastAutoProbeMu    sync.Mutex
	fastAutoProbeCache fastAutoProbeState
	fastAutoProbeAt    time.Time // 缓存加载时刻(5s TTL,热读)
)

// fastAutoProbeList 读自动探针名单(带 TTL 缓存;引擎判闸热路径用)。
func (e *OKXDirectionalEngine) fastAutoProbeList() map[string]time.Time {
	fastAutoProbeMu.Lock()
	defer fastAutoProbeMu.Unlock()
	if time.Since(fastAutoProbeAt) > 5*time.Second {
		st := fastAutoProbeState{Sources: map[string]time.Time{}}
		examReadJSONFile(filepath.Join(e.recordDir, fastAutoProbeFile), &st)
		if st.Sources == nil {
			st.Sources = map[string]time.Time{}
		}
		fastAutoProbeCache = st
		fastAutoProbeAt = time.Now()
	}
	out := map[string]time.Time{}
	for k, v := range fastAutoProbeCache.Sources {
		out[k] = v
	}
	return out
}

// fastProbeAllowed 探针身份合并判定:config 名单(用户手动) ∪ 自动名单(阅卷人过门)。
// 全部下游防线(预算/哨兵/熔断/冷却)对两种身份一视同仁。
func (e *OKXDirectionalEngine) fastProbeAllowed(source string) bool {
	if fastProbeHas(e.cfg.FastProbeSource, source) {
		return true
	}
	_, ok := e.fastAutoProbeList()[strings.TrimSpace(source)]
	return ok
}

// fastAutoProbeAdmit 阅卷人过门时自动接入探针(并发上限内)。返回是否接入。
func (e *OKXDirectionalEngine) fastAutoProbeAdmit(source string) bool {
	fastAutoProbeMu.Lock()
	defer fastAutoProbeMu.Unlock()
	st := fastAutoProbeState{Sources: map[string]time.Time{}}
	examReadJSONFile(filepath.Join(e.recordDir, fastAutoProbeFile), &st)
	if st.Sources == nil {
		st.Sources = map[string]time.Time{}
	}
	if _, ok := st.Sources[source]; ok {
		return false // 已在名单
	}
	if len(st.Sources) >= fastAutoProbeMaxConcurrent {
		return false // 并发满:排队等人工或某源缴械后人工腾位
	}
	st.Sources[source] = e.nowFn().UTC()
	if err := writeJSONFile(filepath.Join(e.recordDir, fastAutoProbeFile), st); err != nil {
		e.logger.Printf("[auto_probe] 名单落盘失败: %v", err)
		return false
	}
	fastAutoProbeCache = st
	fastAutoProbeAt = time.Now()
	return true
}

// fastExamSpec 一场在考考试(冻结口径)。
type fastExamSpec struct {
	Key      string    // 状态/推送键
	Label    string    // 人话名
	Seal     time.Time // 封样:只阅此后出生的样本
	MinN     int
	MinMed   float64 // 净中位下限(bps,严格大于)
	MinPF    float64
	MinWin   float64 // 胜率下限(刮痕中性化,|net|≤5 剔除);0=不查
	MaxTop   float64 // 单币主导上限(0=不查)
	Rows     func(r fastLedgerRow) bool
	Question string // 推送里说明这场考试裁什么
	// ProbeSource 非空=过门自动接入探针的 candidate_source(07-09 用户"全自动"拍板;
	// 前提=实枪口径与考试口径同源,否则留空走人工)。预算 20笔/0.5U 由治理器封死。
	ProbeSource string
}

// fastExamRegistry 当前在考(v1 三场;登记时刻 2026-07-08 21:35 BJ)。
func fastExamRegistry() []fastExamSpec {
	return []fastExamSpec{
		{
			Key: "e_ticket_r2", Label: "E 探针门票·二轮(残余带闸)",
			Seal: time.Date(2026, 7, 8, 13, 30, 0, 0, time.UTC), // 二轮封样=阅卷人上线时刻
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.20,
			Rows: func(r fastLedgerRow) bool {
				return (r.Module == "" || r.Module == "e") && r.Src == "rest" &&
					r.Bits&pbResidual2040 != 0 && r.Bits&pbFresh != 0 && r.Bits&pbDutyXgap != 0
			},
			Question: "E 首轮判负后的改良口径,前向能否转正?过=递探针发证材料",
		},
		{
			Key: "e_next_pkg", Label: "E 改良包v2(棘轮+弹射)",
			Seal: time.Date(2026, 7, 8, 9, 40, 0, 0, time.UTC), // 改良版上线 17:40 BJ
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.20,
			Rows: func(r fastLedgerRow) bool {
				return (r.Module == "" || r.Module == "e") && r.Src == "rest2"
			},
			Question: "改良出场能否把 E 拧回正期望?→ 07-09 判负(死因=弹射粒子级反向)",
		},
		{
			Key: "e_next2_ratchet", Label: "E 棘轮版v3(只棘轮不弹射)",
			Seal: time.Date(2026, 7, 9, 3, 10, 0, 0, time.UTC), // v3 上线时刻封样(07-09 11:10 BJ)
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.20,
			Rows: func(r fastLedgerRow) bool {
				return (r.Module == "" || r.Module == "e") && r.Src == "rest3"
			},
			Question: "v2 死因解剖(弹射-249/棘轮+57 配对)后的重考:棘轮单独能否过门",
		},
		{
			Key: "fw_pure", Label: "烟花·纯FOMO顶(非惯犯×完全力竭×无本币爆仓)",
			Seal: time.Date(2026, 7, 9, 4, 30, 0, 0, time.UTC), // 三特征字段上线时刻封样
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.30,
			Rows: func(r fastLedgerRow) bool {
				return r.Module == "firework" && r.Src == "rest" && r.PriorFw24h < 3 &&
					r.FwRet60Bps > 0 && r.FwRet15Bps < 0.05*r.FwRet60Bps && r.FwSymLiq5m == 0
			},
			Question: "K折 8/8 过检(n=106 中位-0.1 PF1.36):三特征能否把中位拧过 0=烟花的门票考试",
			// 烟花枪三特征闸与本考试口径同源(fastFireworkPureSkip)→ 过门自动接探针
			ProbeSource: "firework_pure",
		},
		{
			Key: "fw_nohabit", Label: "烟花×非惯犯(24h同币<3枪;holdout 校验过)",
			Seal: time.Date(2026, 7, 9, 3, 30, 0, 0, time.UTC), // 惯犯计数字段上线时刻封样
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.30,
			Rows: func(r fastLedgerRow) bool {
				return r.Module == "firework" && r.Src == "rest" && r.PriorFw24h < 3
			},
			Question: "train PF0.69→holdout 非惯犯 PF1.75/惯犯 0.19(躲毒确认);前向能否把中位拧过 0",
		},
		{
			Key: "fw_es_exhaust", Label: "烟花·能量出场×完全力竭(坟场翻案头号线)",
			Seal: time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC), // rest2 能量口径上线时刻封样
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.30,
			Rows: func(r fastLedgerRow) bool {
				return r.Module == "firework" && r.Src == "rest2" &&
					r.FwRet60Bps > 0 && r.FwRet15Bps < 0.05*r.FwRet60Bps
			},
			Question: "烟花被-40固定线杀死(双向中位-53=止损在噪声里);能量出场重测 train+21.2/holdout+22.7 PF1.32/1.36;前向监控能否复现",
		},
		{
			Key: "fw_es_pure", Label: "烟花·能量出场×三特征(非惯犯×完全力竭×无爆仓)",
			Seal: time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.30,
			Rows: func(r fastLedgerRow) bool {
				return r.Module == "firework" && r.Src == "rest2" && r.PriorFw24h < 3 &&
					r.FwRet60Bps > 0 && r.FwRet15Bps < 0.05*r.FwRet60Bps && r.FwSymLiq5m == 0
			},
			Question: "原三特征×能量出场(train +27.6/PF1.55 n=49,holdout n=9 仅记形);过门=枪出场换能量刻度的探针材料(须用户批)",
		},
		{
			Key: "firework_fwd", Label: "烟花反手刀·前向(Go账本口径)",
			Seal: time.Date(2026, 7, 8, 7, 9, 0, 0, time.UTC), // 烟花模块通电时刻
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.50, // 预注册原文:单币>50%作废
			Rows: func(r fastLedgerRow) bool {
				return r.Module == "firework" && r.Src == "rest"
			},
			Question: "力竭×拉升≥60×点差≤6 反手做空固定300s,前向过门=第二个探针候选",
		},
	}
}

// fastExamStats 阅卷读数。
type fastExamStats struct {
	N       int     `json:"n"`
	Med     float64 `json:"med_bps"`
	PF      float64 `json:"pf"`
	WinNeu  float64 `json:"win_neutralized"`
	TopSym  string  `json:"top_sym"`
	TopShr  float64 `json:"top_share"`
	NetSum  float64 `json:"net_sum_bps"`
}

// fastExamEvaluate 纯函数:算一场考试的读数(样本=Seal 后且匹配 Rows 的账本行)。
func fastExamEvaluate(rows []fastLedgerRow, ex fastExamSpec) fastExamStats {
	var st fastExamStats
	var nets []float64
	gp, gl := 0.0, 0.0
	wins, scratch := 0, 0
	bySym := map[string]int{}
	for _, r := range rows {
		if r.Exit == "unfilled" || r.T0.Before(ex.Seal) || !ex.Rows(r) {
			continue
		}
		st.N++
		nets = append(nets, r.NetBps)
		st.NetSum += r.NetBps
		if r.NetBps > 0 {
			gp += r.NetBps
		} else {
			gl -= r.NetBps
		}
		if r.NetBps > 5 {
			wins++
		} else if r.NetBps >= -5 {
			scratch++ // 刮痕中性化(用户哲学:止错≠失败)
		}
		bySym[r.Symbol]++
	}
	st.Med = medianOf(nets)
	if gl > 0 {
		st.PF = gp / gl
	} else if gp > 0 {
		st.PF = 99
	}
	if eff := st.N - scratch; eff > 0 {
		st.WinNeu = float64(wins) / float64(eff)
	}
	for s, c := range bySym {
		if float64(c)/float64(max(st.N, 1)) > st.TopShr {
			st.TopShr = float64(c) / float64(st.N)
			st.TopSym = s
		}
	}
	return st
}

// fastExamVerdict 纯函数:到线判定("":未到线;"pass"/"fail")。
func fastExamVerdict(st fastExamStats, ex fastExamSpec) string {
	if st.N < ex.MinN {
		return ""
	}
	pass := st.Med > ex.MinMed && st.PF >= ex.MinPF
	if ex.MinWin > 0 && st.WinNeu < ex.MinWin {
		pass = false
	}
	if ex.MaxTop > 0 && st.TopShr > ex.MaxTop {
		pass = false // 单币主导作废口径:计为不过(需人工解剖是否重考)
	}
	if pass {
		return "pass"
	}
	return "fail"
}

// fastExamRecord 已出分记录(持久化,防重复推送)。
type fastExamRecord struct {
	Verdict   string        `json:"verdict"`
	VerdictAt time.Time     `json:"verdict_at"`
	Stats     fastExamStats `json:"stats"`
}

// fastExamSupervisorLoop 常驻阅卷循环(10min 一轮;record-only+通知,零交易副作用)。
func (e *OKXDirectionalEngine) fastExamSupervisorLoop(ctx interface{ Done() <-chan struct{} }) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[exam] 阅卷人 panic 已恢复: %v", r)
		}
	}()
	alerts := newAlertDispatcher(e.netCfg.AlertingConfig, e.logger)
	statePath := filepath.Join(e.recordDir, fastExamStateFile)
	state := map[string]fastExamRecord{}
	examReadJSONFile(statePath, &state)
	e.logger.Printf("[exam] 自动阅卷人上岗:%d 场在考,10min 一轮;边界=只发证+通知,开火永远在用户", len(fastExamRegistry()))
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(e.recordDir, fastParticleLedgerFile), time.Time{}, 0)
		if err != nil {
			continue
		}
		changed := false
		exams := append(fastExamRegistry(), e.fastMinerDynamicExams()...) // 静态+自动挖掘的动态考试
		for _, ex := range exams {
			if _, done := state[ex.Key]; done {
				continue // 已出分:重考=登记新 Key(口径断代),不复用旧键
			}
			st := fastExamEvaluate(rows, ex)
			v := fastExamVerdict(st, ex)
			if v == "" {
				continue
			}
			state[ex.Key] = fastExamRecord{Verdict: v, VerdictAt: e.nowFn().UTC(), Stats: st}
			changed = true
			if strings.HasPrefix(ex.Key, "dyn_") { // 动态考试出分即归档(判负记案底永不自动复活)
				e.fastMinerRetireDynamic(ex.Key, v)
			}
			detail := fmt.Sprintf("n=%d 净中位%+.1fbps PF%.2f 胜率(中性化)%.0f%% 顶币%s(%.0f%%) 净合计%+.0fbps\n门=n≥%d 中位>%.0f PF≥%.1f\n%s",
				st.N, st.Med, st.PF, st.WinNeu*100, st.TopSym, st.TopShr*100, st.NetSum,
				ex.MinN, ex.MinMed, ex.MinPF, ex.Question)
			if v == "pass" {
				admitted := ""
				if ex.ProbeSource != "" && e.fastAutoProbeAdmit(ex.ProbeSource) {
					admitted = "\n🚀 已自动接入探针(" + ex.ProbeSource + "):5U×20笔×累亏0.5U 自动缴械;人工收回=删 " + fastAutoProbeFile + " 条目"
					e.logger.Printf("[auto_probe] 考试 %s 过门 → %s 自动接入探针(预算封顶)", ex.Key, ex.ProbeSource)
				}
				e.logger.Printf("[exam] 🎫 准考证已发:%s %s", ex.Label, strings.ReplaceAll(detail, "\n", " | "))
				alerts.Notify(Alert{Level: EventLevelWarning, Title: "🎫 准考证:" + ex.Label + " 前向过门",
					Detail: detail + admitted + "\n(加钱/灰度永远由你拍板)", OccurredAt: e.nowFn().UTC()})
			} else {
				e.logger.Printf("[exam] ✗ 判负:%s %s", ex.Label, strings.ReplaceAll(detail, "\n", " | "))
				alerts.Notify(Alert{Level: EventLevelWarning, Title: "✗ 判负:" + ex.Label,
					Detail: detail + "\n→ 死因解剖排队(五级归因后决定回炉方向)", OccurredAt: e.nowFn().UTC()})
			}
		}
		if changed {
			if err := writeJSONFile(statePath, state); err != nil {
				e.logger.Printf("[exam] 状态落盘失败: %v", err)
			}
		}
	}
}

// fastSkySnapshot 面板用:司天监此刻读数(全部现算,与派班闸同源函数)。
func (e *OKXDirectionalEngine) fastSkySnapshot(now time.Time) map[string]interface{} {
	fuel := fastMarketLiqRate(now, 300)
	br, brOK := fastBreadth30(now)
	long, short := 0, 0
	fastLiqBufMu.Lock()
	cut := now.Add(-5 * time.Minute)
	for _, hs := range fastLiqBuf {
		for _, l := range hs {
			if l.ts.Before(cut) {
				continue
			}
			if l.posSide == "long" {
				long++ // 多头爆=被迫卖(跌潮方向)
			} else if l.posSide == "short" {
				short++
			}
		}
	}
	fastLiqBufMu.Unlock()
	fuelState := "死水"
	if fuel >= 40 {
		fuelState = "风暴"
	} else if fuel >= 20 {
		fuelState = "中间"
	}
	// E 值班判定=派班闸同源口径(fastXgapCellSkip 的②③条件)
	eDuty, why := true, "平静×非广跌 → E 值班"
	if fuel >= 40 {
		eDuty, why = false, "风暴态(燃料≥40)→ E 歇业"
	} else if !brOK {
		eDuty, why = false, "广度不可算(fail-closed)→ E 歇业"
	} else if br <= -15 {
		eDuty, why = false, "广跌态(广度≤-15)→ E 歇业"
	}
	return map[string]interface{}{
		"fuel": fuel, "fuel_state": fuelState, "breadth": br, "breadth_ok": brOK,
		"tide_long": long, "tide_short": short,
		"e_duty": eDuty, "e_duty_why": why,
	}
}

// fastProbeSnapshot 面板用:探针名单各源实时预算(现算,与预算闸同源;含自动接入名单)。
func (e *OKXDirectionalEngine) fastProbeSnapshot() []map[string]interface{} {
	var out []map[string]interface{}
	srcs := []string{}
	seen := map[string]bool{}
	for _, p := range strings.Split(e.cfg.FastProbeSource, ",") {
		if s := strings.TrimSpace(p); s != "" && !seen[s] {
			srcs = append(srcs, s)
			seen[s] = true
		}
	}
	for s := range e.fastAutoProbeList() { // 自动接入的源(07-09 历史考试制)
		if !seen[s] {
			srcs = append(srcs, s)
			seen[s] = true
		}
	}
	sort.Strings(srcs)
	for _, src := range srcs {
		fastProbeMu.Lock()
		st := *e.fastProbeStateLocked(src)
		fastProbeMu.Unlock()
		out = append(out, map[string]interface{}{
			"source": src, "trades": st.Trades, "max_trades": e.cfg.FastProbeMaxTrades,
			"realized": st.Realized, "max_loss": e.cfg.FastProbeMaxLossUSDT,
			"exhausted": fastProbeExhausted(st, e.cfg.FastProbeMaxTrades, e.cfg.FastProbeMaxLossUSDT),
			"started_at": st.StartedAt,
		})
	}
	return out
}

// fastExamSnapshot 面板用:全部在考考试当前读数+已出分记录(现算)。
func (e *OKXDirectionalEngine) fastExamSnapshot() []map[string]interface{} {
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(e.recordDir, fastParticleLedgerFile), time.Time{}, 0)
	if err != nil {
		return nil
	}
	state := map[string]fastExamRecord{}
	examReadJSONFile(filepath.Join(e.recordDir, fastExamStateFile), &state)
	var out []map[string]interface{}
	for _, ex := range append(fastExamRegistry(), e.fastMinerDynamicExams()...) {
		st := fastExamEvaluate(rows, ex)
		item := map[string]interface{}{
			"key": ex.Key, "label": ex.Label, "n": st.N, "min_n": ex.MinN,
			"med": st.Med, "pf": st.PF, "question": ex.Question,
		}
		if rec, done := state[ex.Key]; done {
			item["verdict"] = rec.Verdict
			item["verdict_at"] = rec.VerdictAt
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["key"].(string) < out[j]["key"].(string) })
	return out
}
