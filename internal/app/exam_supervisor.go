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
			Key: "e_next_pkg", Label: "E 改良包(三闸+棘轮+弹射真跑)",
			Seal: time.Date(2026, 7, 8, 9, 40, 0, 0, time.UTC), // 改良版上线 17:40 BJ
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.20,
			Rows: func(r fastLedgerRow) bool {
				return (r.Module == "" || r.Module == "e") && r.Src == "rest2"
			},
			Question: "改良出场能否把 E 拧回正期望?E 下一版口径的裁决文件",
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
		rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(e.recordDir, "okx_directional", fastParticleLedgerFile), time.Time{}, 0)
		if err != nil {
			continue
		}
		changed := false
		for _, ex := range fastExamRegistry() {
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
			detail := fmt.Sprintf("n=%d 净中位%+.1fbps PF%.2f 胜率(中性化)%.0f%% 顶币%s(%.0f%%) 净合计%+.0fbps\n门=n≥%d 中位>%.0f PF≥%.1f\n%s",
				st.N, st.Med, st.PF, st.WinNeu*100, st.TopSym, st.TopShr*100, st.NetSum,
				ex.MinN, ex.MinMed, ex.MinPF, ex.Question)
			if v == "pass" {
				e.logger.Printf("[exam] 🎫 准考证已发:%s %s", ex.Label, strings.ReplaceAll(detail, "\n", " | "))
				alerts.Notify(Alert{Level: EventLevelWarning, Title: "🎫 准考证:" + ex.Label + " 前向过门",
					Detail: detail + "\n→ 是否裁探针/发证由你拍板(agent 只递数字)", OccurredAt: e.nowFn().UTC()})
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

// fastExamSnapshot 面板用:全部在考考试当前读数+已出分记录(现算)。
func (e *OKXDirectionalEngine) fastExamSnapshot() []map[string]interface{} {
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(e.recordDir, "okx_directional", fastParticleLedgerFile), time.Time{}, 0)
	if err != nil {
		return nil
	}
	state := map[string]fastExamRecord{}
	examReadJSONFile(filepath.Join(e.recordDir, fastExamStateFile), &state)
	var out []map[string]interface{}
	for _, ex := range fastExamRegistry() {
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
