package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ===== ㊳ 页面数据①:作战地图(2026-07-08)=====
// 回答用户五问:在做什么/做到哪一步/下一步是什么/目标是什么/怎么进入下一步 + 整个策略长什么样。
// 一切数字由 Go 现算:配置开关=进程内真值;考试进度/账本读数=particle_ledger.jsonl(㊱铁律的
// 判决货币);探针吞吐=fast_full_burst_probe.jsonl;慢线状态=control.json。
// 唯一的"登记态"(非计算)=持证格子清单与预注册门槛数字,它们是项目的官方决策记录(HANDOFF),
// 在代码里作为常量登记并标注出处。

// missionSealPathAnnotate ㊳① 路径字段部署时刻(此前无该字段=天然封样)。
const missionSealPathAnnotate = "2026-07-07T17:50:00Z"

// missionSealResidual 残余带闸封样(HANDOFF ㉝,12:10Z)。
const missionSealResidual = "2026-07-07T12:10:00Z"

type missionGate struct {
	Name     string `json:"name"`
	Current  string `json:"current"`
	Required string `json:"required"`
	Pass     bool   `json:"pass"`
	Note     string `json:"note,omitempty"`
}

type missionWorkstream struct {
	Name   string `json:"name"`
	Metric string `json:"metric"`
	Note   string `json:"note,omitempty"`
}

type missionStage struct {
	Name  string `json:"name"`
	State string `json:"state"` // done / current / pending / off
	Note  string `json:"note,omitempty"`
}

type missionResponse struct {
	Now         time.Time           `json:"now"`
	Phase       string              `json:"phase"`
	PhaseDetail string              `json:"phase_detail"`
	Goal        []string            `json:"goal"`
	Doing       []missionWorkstream `json:"doing"`
	Gates       []missionGate       `json:"gates"`
	Ladder      []missionStage      `json:"ladder"`   // 真钱阶梯
	Pipeline    []missionStage      `json:"pipeline"` // 六层流水线(整个策略)
	Certified   []string            `json:"certified"`
}

// missionLedgerStats particle_ledger 现算读数。
type missionLedgerStats struct {
	PathN       int     // rest+path_ok 封样后行数(路径阅卷进度,门=100)
	ResidualN   int     // 残余带闸账本口径行数(门=30)
	ResidualMed float64 // 其净中位
	ResidualPF  float64
	MkrFilled   int
	MkrUnfilled int
	MkrFirstAt  time.Time // maker 口径起算时刻(裁决=攒2-3天)
	BurstTagged int       // burst_during_hold 标注命中行
	RestN24h    int       // 近24h rest 行(E 触发吞吐)
}

func missionScanLedger(recordDir string, now time.Time) missionLedgerStats {
	var st missionLedgerStats
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(recordDir, "okx_directional", fastParticleLedgerFile), time.Time{}, 0)
	if err != nil {
		return st
	}
	sealPath, _ := time.Parse(time.RFC3339, missionSealPathAnnotate)
	sealRes, _ := time.Parse(time.RFC3339, missionSealResidual)
	var resNets []float64
	for _, r := range rows {
		switch r.Src {
		case "rest":
			if r.Exit == "unfilled" {
				continue
			}
			if r.PathOK && !r.T0.Before(sealPath) {
				st.PathN++
			}
			if !r.Time.Before(now.Add(-24 * time.Hour)) {
				st.RestN24h++
			}
			// 残余带闸账本口径:REST残余∈[-40,-20] × 新鲜 × 派班(位与粒子账本同源)
			if !r.T0.Before(sealRes) && r.Bits&pbResidual2040 != 0 && r.Bits&pbFresh != 0 && r.Bits&pbDutyXgap != 0 {
				resNets = append(resNets, r.NetBps)
			}
		case "mkr":
			if r.Exit == "unfilled" {
				st.MkrUnfilled++
			} else {
				st.MkrFilled++
			}
			if st.MkrFirstAt.IsZero() || r.T0.Before(st.MkrFirstAt) {
				st.MkrFirstAt = r.T0
			}
		}
		if r.BurstDuringHold {
			st.BurstTagged++
		}
	}
	st.ResidualN = len(resNets)
	if len(resNets) > 0 {
		sorted := append([]float64(nil), resNets...)
		sort.Float64s(sorted)
		st.ResidualMed = sorted[len(sorted)/2]
		gp, gl := 0.0, 0.0
		for _, x := range resNets {
			if x > 0 {
				gp += x
			} else {
				gl -= x
			}
		}
		if gl > 0 {
			st.ResidualPF = gp / gl
		} else if gp > 0 {
			st.ResidualPF = 99
		}
	}
	return st
}

// missionProbeStats fast_full_burst_probe 近1h吞吐与增量视野占比 + 累计时长(24h裁决进度)。
func missionProbeStats(recordDir string, now time.Time) (n1h, inc1h int, hoursAccum float64) {
	rows, err := loadTailJSONLFile[fastFullBurstProbeEvent](filepath.Join(recordDir, "okx_directional", fastFullBurstProbeFile), time.Time{}, 0)
	if err != nil || len(rows) == 0 {
		return 0, 0, 0
	}
	first := rows[0].RecordedAt
	cut := now.Add(-time.Hour)
	for _, r := range rows {
		if r.RecordedAt.Before(first) {
			first = r.RecordedAt
		}
		if r.RecordedAt.After(cut) {
			n1h++
			if !r.InHotWS {
				inc1h++
			}
		}
	}
	return n1h, inc1h, now.Sub(first).Hours()
}

// missionSlowActive 各所慢线 control.json Active 状态。
func missionSlowActive(recordDir string) map[string]bool {
	out := map[string]bool{}
	for _, vn := range directionalVenueNames {
		var c struct {
			Active bool `json:"active"`
		}
		data, err := os.ReadFile(filepath.Join(directionalRecordDir(recordDir, vn), "control.json"))
		if err != nil {
			continue
		}
		if json.Unmarshal(data, &c) == nil {
			out[vn] = c.Active
		}
	}
	return out
}

func onOff(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}

// handleMissionAPI GET /api/mission(只读鉴权由路由包)。
func (s *DashboardServer) handleMissionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UTC()
	dir := s.recordDirOrDefault()
	eng := s.directionalEngine("okx")
	if eng == nil {
		http.Error(w, "okx 引擎未注入", http.StatusServiceUnavailable)
		return
	}
	cfg := eng.cfg
	led := missionScanLedger(dir, now)
	probeN, probeInc, probeHours := missionProbeStats(dir, now)
	slow := missionSlowActive(dir)

	probeSrc := strings.TrimSpace(cfg.FastProbeSource)
	anyLive := cfg.FastXgapLiveEnabled || cfg.FastTapeLiveEnabled || cfg.FastLiqFollowLiveEnabled ||
		cfg.FastScorerLiveEnabled || cfg.FastFadeLiveEnabled || probeSrc != ""
	anySlow := false
	for _, a := range slow {
		anySlow = anySlow || a
	}
	phase := "阶段A·发现期(零实盘敞口)"
	detail := "fast 层 07-04 停火、慢线 07-07 全停;全系统 record-only,搜索边际成本=0 真钱。"
	if probeSrc != "" {
		phase = "阶段B·探针期(" + probeSrc + ")"
		detail = "探针预算 ≤" + fmt.Sprintf("%d 笔 / 累亏 ≤%.1fU", cfg.FastProbeMaxTrades, cfg.FastProbeMaxLossUSDT) + " 自动缴械。"
	} else if anyLive {
		phase = "⚠️ 有实弹开关开启(请核对)"
	}

	mkrDays := 0.0
	if !led.MkrFirstAt.IsZero() {
		mkrDays = now.Sub(led.MkrFirstAt).Hours() / 24
	}

	resp := missionResponse{
		Now: now, Phase: phase, PhaseDetail: detail,
		Goal: []string{
			"使命:分拣器分拣后,派给排列好的粒子组合交易盈利(费率飞轮只是次要放大器)。",
			"两道门(登记,HANDOFF §-7C):利润引擎门=中位≥18bps;飞轮引擎门=中位≥12bps。",
			"近期目标:第一个通过探针真门票的策略族拿 5U 探针实测真滑点(须用户明示批准)。",
		},
		Certified: []string{"E×平静海 格子(07-08 发证考试过检:n≥30 净中位+12.6 PF1.80;探针前须过残余带闸)"},
	}

	resp.Doing = []missionWorkstream{
		{Name: "全市场磁带探针(㊳②)", Metric: fmt.Sprintf("近1h burst=%d,增量视野=%d(%.0f%%),已攒 %.1fh/24h", probeN, probeInc, pct(int64(probeInc), int64(probeN)), probeHours),
			Note: "裁决=24h 按州分桶:盲区 burst 是强平驱动还是主动暴力"},
		{Name: "E 路径信息集标注(㊳①)", Metric: fmt.Sprintf("rest+path_ok 已攒 %d/100", led.PathN),
			Note: "到样后按生龄×速度×归因分桶,净正桶走冠军梯子"},
		{Name: "maker 口径账本(曲线①)", Metric: fmt.Sprintf("成交 %d / 未成交 %d,已攒 %.1f 天/2 天", led.MkrFilled, led.MkrUnfilled, mkrDays),
			Note: "裁决=逆选择税是否<省下的 3-6bps;过→坟场按 6bps 地板批量重审"},
		{Name: "burst 弹射标注(曲线②)", Metric: fmt.Sprintf("持仓中命中 %d 行", led.BurstTagged),
			Note: "E 持仓遇同币卖方 burst 提前弹出的反事实,只记不弹"},
		{Name: "E 触发吞吐", Metric: fmt.Sprintf("近24h 可执行触发 %d 次", led.RestN24h)},
	}

	resPass := led.ResidualN >= 30 && led.ResidualMed > 0 && led.ResidualPF >= 1.3
	resp.Gates = []missionGate{
		{Name: "残余带闸(E 探针真门票)", Pass: resPass,
			Current:  fmt.Sprintf("n=%d 净中位=%+.1f PF=%.2f(Go账本口径)", led.ResidualN, led.ResidualMed, led.ResidualPF),
			Required: "n≥30 且 净中位>0 且 PF≥1.3",
			Note:     "过检→连同执行层证据递用户裁探针;不过→E 回炉"},
		{Name: "maker 化裁决(曲线①)", Pass: mkrDays >= 2,
			Current: fmt.Sprintf("已攒 %.1f 天", mkrDays), Required: "≥2 天样本",
			Note: "过→半个坟场按 6bps 门票复活重审"},
		{Name: "路径分桶到样(㊳①)", Pass: led.PathN >= 100,
			Current: fmt.Sprintf("%d", led.PathN), Required: "≥100",
			Note: "回答『价差怎么诞生的』值多少 bps"},
		{Name: "探针开火操作(等用户批准)", Pass: false,
			Current:  onOff(cfg.FastXgapLiveEnabled, "live=开(异常!)", "live=锁定") + " / probe_source=" + onOff(probeSrc != "", probeSrc, "空"),
			Required: "上方门全过 + 用户明示批准",
			Note:     "操作=fast_xgap_live_enabled + fast_probe_source 两行配置+重启,零代码"},
	}

	resp.Ladder = []missionStage{
		{Name: "① 格子持证", State: "done", Note: "E×平静海 已持证(record-only 口径)"},
		{Name: "② 探针真门票(残余带闸)", State: "current", Note: fmt.Sprintf("进度 %d/30", led.ResidualN)},
		{Name: "③ 探针 5U(须用户批)", State: "pending", Note: "≤20 笔/累亏≤0.5U 自动缴械"},
		{Name: "④ 灰度→放大", State: "pending", Note: "探针 PF>1 才谈规模"},
	}

	nCollectors := 0
	for _, on := range []bool{cfg.FastMarketMidsEnabled, cfg.FastHotWSEnabled, cfg.FastXMidEnabled,
		cfg.FastXvenueLiqEnabled, cfg.FastLiqCollectorEnabled, cfg.FastFullTapeEnabled} {
		if on {
			nCollectors++
		}
	}
	resp.Pipeline = []missionStage{
		{Name: "层1·数据感知", State: "done", Note: fmt.Sprintf("%d/6 采集器在跑(全市场mid/热币WS/跨所锚/爆仓流×2/全市场磁带)", nCollectors)},
		{Name: "层2·事件触发三条枪", State: onOff(anyLive, "current", "off"),
			Note: "tape=" + onOff(cfg.FastTapeLiveEnabled, "实弹", "shadow") + " E=" + onOff(cfg.FastXgapLiveEnabled, "实弹", onOff(cfg.FastXgapShadowEnabled, "shadow", "关")) + " 跟瀑布=" + onOff(cfg.FastLiqFollowLiveEnabled, "实弹", "shadow(停火)")},
		{Name: "层3·当刻资格闸", State: "done", Note: "点差≤12/深度5k-15万/新鲜度/冷却退避,fail-closed"},
		{Name: "层4·司天监分拣", State: "done", Note: "燃料×潮汐×广度查表派班,海况刹车凌驾;影子运行"},
		{Name: "层5·判决工厂", State: "done", Note: "Go 粒子账本三口径(det/rest/mkr)=判决货币;预注册考试自动阅卷"},
		{Name: "层6·真钱阶梯", State: "current", Note: "慢线=" + onOff(anySlow, "有所在跑(核对!)", "四所全停") + ";fast 实盘=零;哨兵/探针治理器在岗"},
	}

	writeJSON(w, resp)
}
