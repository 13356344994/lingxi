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

// missionJourneyStop 旅程图一站(给用户的白话版"进行到哪一步")。
type missionJourneyStop struct {
	Name      string `json:"name"`
	Plain     string `json:"plain"`     // 这一站是什么(人话)
	Condition string `json:"condition"` // 进入下一站的条件(人话+数字)
	State     string `json:"state"`     // done / current / pending
}

// missionPlainCard 白话数字卡:一个数+一句解释。
type missionPlainCard struct {
	Title   string `json:"title"`
	Value   string `json:"value"`
	Explain string `json:"explain"`
	Tone    string `json:"tone"` // good / bad / neutral
}

// missionCandidate 管线全员位置表一行(回答"候选在哪一步/为什么现在谈的是谁")。
// 位置与状态=项目官方决策记录(HANDOFF 同步维护);进度数字尽量 Go 现算。
type missionCandidate struct {
	Name  string `json:"name"`
	Stage string `json:"stage"` // 替补席 / ①影子海选 / ②格子发证 / ③探针 / ④灰度 / ⑤走量
	Note  string `json:"note"`
	Front bool   `json:"front"` // 是否站在探针门口(当前唯一实盘候选)
}

// missionBaseline 当前最新策略基准数据(2026-07-08 用户重心指令:"页面数据一定以当前最新策略
// 为基准"+"历史学费不上页面")。基准=E×格子三闸口径(04:34Z 部署后 rest 行=全闸通过的可执行
// 口径,与探针开火时真枪同一批);杠杆=改良包三件的配对反事实现算(工程师口径:只答"哪个杠杆
// 能把结果拧好多少",不记分)。
type missionBaseline struct {
	SinceLabel string  `json:"since_label"`
	N          int     `json:"n"`
	NetMedBps  float64 `json:"net_med_bps"`
	NetSumBps  float64 `json:"net_sum_bps"`
	PF         float64 `json:"pf"`
	WinRate    float64 `json:"win_rate"`
	// 原版出场对照(同触发同入场,出场=收敛/600s/-60;改良版差值=杠杆真实兑现)
	OrigN      int     `json:"orig_n"`
	OrigMedBps float64 `json:"orig_med_bps"`
	OrigPF     float64 `json:"orig_pf"`
	// 吐回税监视(E 头号漏点:冲上去又吐回来的钱)
	GivebackN      int     `json:"giveback_n"`     // 峰值≥5bps 的单数
	GivebackNegN   int     `json:"giveback_neg_n"` // 其中最终收负
	GivebackMedBps float64 `json:"giveback_med_bps"`
	// 棘轮杠杆(配对反事实:同一笔 棘轮出场-原版出场,正=棘轮拧回的钱)
	RatchetRestN      int     `json:"ratchet_rest_n"`
	RatchetRestMedBps float64 `json:"ratchet_rest_med_bps"`
	RatchetDetN       int     `json:"ratchet_det_n"` // det 提名口径参考(rest 供给低时先看方向)
	RatchetDetMedBps  float64 `json:"ratchet_det_med_bps"`
	// 弹射杠杆(配对反事实:burst 时刻弹出 vs 实际拿到,正=弹射省下的钱)
	EjectN         int     `json:"eject_n"`
	EjectSaveMedBps float64 `json:"eject_save_med_bps"`
}

func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// missionBaselineFromRows 纯函数:账本行 → 当前策略基准(可单测)。
// sinceCell=三闸口径起点(04:34Z 部署);E 模块行 only,unfilled 剔除。
func missionBaselineFromRows(rows []fastLedgerRow, sinceCell time.Time) missionBaseline {
	b := missionBaseline{SinceLabel: "★★★E·棘轮版v3(只棘轮不弹射,07-09 11:10 BJ 上线)可执行前向"}
	var nets, origNets []float64
	var giveback []float64
	var ratchetRest, ratchetDet, ejectSave []float64
	gp, gl, wins := 0.0, 0.0, 0
	ogp, ogl := 0.0, 0.0
	for _, r := range rows {
		if (r.Module != "" && r.Module != "e") || r.Exit == "unfilled" {
			continue
		}
		// 杠杆反事实:标注存在即可配对(不限窗;标注字段 07-08 14:59 BJ 起才有=天然封样)
		if r.TrailFired {
			d := r.TrailExitBps - r.NetBps
			switch r.Src {
			case "rest":
				ratchetRest = append(ratchetRest, d)
			case "det":
				ratchetDet = append(ratchetDet, d)
			}
		}
		if r.BurstDuringHold && r.Src == "rest" {
			ejectSave = append(ejectSave, r.RetAtBurstBps-r.GrossBps)
		}
		if r.T0.Before(sinceCell) {
			continue
		}
		switch r.Src {
		case "rest3": // 主口径:棘轮版 v3(07-09 死因解剖后的最新口径)
			b.N++
			nets = append(nets, r.NetBps)
			b.NetSumBps += r.NetBps
			if r.NetBps > 0 {
				wins++
				gp += r.NetBps
			} else {
				gl -= r.NetBps
			}
			if r.PeakBps >= 5 {
				b.GivebackN++
				giveback = append(giveback, r.PeakBps-r.NetBps)
				if r.NetBps < 0 {
					b.GivebackNegN++
				}
			}
		case "rest": // 对照:原版出场
			b.OrigN++
			origNets = append(origNets, r.NetBps)
			if r.NetBps > 0 {
				ogp += r.NetBps
			} else {
				ogl -= r.NetBps
			}
		}
	}
	b.NetMedBps = medianOf(nets)
	if gl > 0 {
		b.PF = gp / gl
	} else if gp > 0 {
		b.PF = 99
	}
	if b.N > 0 {
		b.WinRate = float64(wins) / float64(b.N)
	}
	b.OrigMedBps = medianOf(origNets)
	if ogl > 0 {
		b.OrigPF = ogp / ogl
	} else if ogp > 0 {
		b.OrigPF = 99
	}
	b.GivebackMedBps = medianOf(giveback)
	b.RatchetRestN, b.RatchetRestMedBps = len(ratchetRest), medianOf(ratchetRest)
	b.RatchetDetN, b.RatchetDetMedBps = len(ratchetDet), medianOf(ratchetDet)
	b.EjectN, b.EjectSaveMedBps = len(ejectSave), medianOf(ejectSave)
	return b
}

// missionFlowStep E 策略流程一步(带今日实数=漏斗计数器现算)。
type missionFlowStep struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`          // 规则(冻结口径数字)
	Today  string `json:"today,omitempty"` // 今日实数
	State  string `json:"state"`           // on / locked
}

// missionStrategyCard 一军策略完整流程卡("对整个策略流程认识不清晰"的直接回答)。
type missionStrategyCard struct {
	Name      string            `json:"name"`
	Mechanism string            `json:"mechanism"` // 机制一句话(对手模型)
	Steps     []missionFlowStep `json:"steps"`     // 进场漏斗(触发→闸→下单)
	Exits     []missionFlowStep `json:"exits"`     // 出场机器
	Upgrade   []missionFlowStep `json:"upgrade"`   // 改良包三件(进行时资产)
	Status    string            `json:"status"`    // 现在卡在哪(一句话)
}

// ===== 策略解剖书(07-08 用户"哲学/粒子组合/流程对不上,一脸懵逼"之诉)=====
// 登记态数据=项目官方决策记录(与 PARTICLE_LIBRARY.md / HANDOFF 同步维护):
// 回答"我灌进去的哲学在哪一步生效/当前策略由哪些粒子组合成/每个粒子从哪具尸体上拆来"。

// missionPhilosophyRow 一句哲学纲领 → 系统里的具体化身。
type missionPhilosophyRow struct {
	Tenet   string   `json:"tenet"`   // 纲领原文
	Meaning string   `json:"meaning"` // 一句白话
	Embodim []string `json:"embodiments"` // 化身机制(标注在流程第几步)
}

// missionAnatomyStep 流程解剖一步:规则数字 + 粒子组合(出身) + 哲学出处。
type missionAnatomyStep struct {
	Step      string   `json:"step"`
	Rule      string   `json:"rule"`
	Particles []string `json:"particles"`  // 粒子名(出身:从哪具尸体/哪次研究拆来)
	Tenet     string   `json:"tenet"`      // 对应哲学
}

// missionFlowLayer 全系统纵剖面一层(07-08 用户"这种格式我很喜欢,要详细"拍板的页面化)。
type missionFlowLayer struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	Note  string   `json:"note,omitempty"`
}

// missionBuildFlowLayers 九层纵剖面:规则=登记态,数字/锁态=现算。
func missionBuildFlowLayers(cfg OKXDirectionalConfig, fn fastXgapFunnelStats) []missionFlowLayer {
	sk := func(k string) int { return fn.Skips[k] }
	lock := func(b bool) string { return onOff(b, "✅开", "🔒锁") }
	return []missionFlowLayer{
		{Title: "第0层 · 数据地基(仪器,全部 record-only)", Lines: []string{
			"全市场 mid 标尺(~750币,3s一拍)← E 的眼睛 " + lock(cfg.FastMarketMidsEnabled),
			"Bybit 价格锚 xmid(价差的另一半)" + lock(cfg.FastXMidEnabled) + " · 热币 WS 40坑(books5+逐笔,怪兽优先)" + lock(cfg.FastHotWSEnabled),
			"burst 探测器(1s名义≥5×基线且≥500U,识别延迟中位38ms)← 火刀/弹射的耳朵",
			"OKX 官方爆仓推送(~490ms)+ Bybit 爆仓流 " + lock(cfg.FastXvenueLiqEnabled) + " · 全市场磁带探针(401币)" + lock(cfg.FastFullTapeEnabled),
			"由此现算三个州变量:燃料=5min爆仓数(<20死水/20-40中间/≥40风暴) · 潮汐=30min净向 · 广度=30min收益中位(≤-15=广跌)",
		}},
		{Title: "第1层 · 各模块触发器(各看各的事件;触发即写 det 无闸对照账)", Lines: []string{
			fmt.Sprintf("E·捡便宜:OKX 比 Bybit 锚便宜≥%.0fbps(锚新鲜≤8s),同币 %d 分钟一枪 → 今日触发 %d 次", cfg.FastXgapEntryGapBps, cfg.FastXgapCooldownSeconds/60, fn.Trigger),
			"火刀·跟瀑布:卖方 burst → 挂起队列 → 官方爆仓确认(≤3s窗)→ 方向对号(多数票)",
			"烟花·反手:暴力拉升≥60bps × 力竭(15s涨幅<25%×60s)× 点差≤6 → 反手做空",
		}},
		{Title: "第2层 · 司天监(州→值班表→派班;慢变量允许滞后,扳机不允许)", Lines: []string{
			"当前持证格 = 0 → 宪法口径:全体⓪歇业(真钱 0 枪 0 亏,发证期)",
			fmt.Sprintf("E 已真接线:派班闸(风暴/广跌歇业)→ 今日拦 %d 次", sk("cell_off_duty_storm")+sk("cell_off_duty_breadth")),
			"火刀/烟花:账本行带州标注,供判决工厂发证阅卷 · 海况刹车=护栏(只停枪不选组合)",
		}},
		{Title: "第3层 · 当刻资格闸(E 完整闸序,今日实拦数)", Lines: []string{
			fmt.Sprintf("①REST复核价差已收敛 gap_gone×%d ②点差>12bps spread_wide×%d", sk("gap_gone"), sk("spread_wide")),
			fmt.Sprintf("③新鲜度(回看1800s须现过>-20,陈年=标价)×%d ④派班闸×%d", sk("cell_stale_gap"), sk("cell_off_duty_storm")+sk("cell_off_duty_breadth")),
			fmt.Sprintf("⑤残余带∈[-40,-20](浅=肉没了×%d,深=没人抢有原因×%d)→ 今日全闸通过 %d 枪", sk("cell_residual_shallow"), sk("cell_residual_deep"), fn.Pass),
		}, Note: "若探针开火,打的就是「全闸通过」这批枪——推荐书裁的就是它们的影子成绩"},
		{Title: "第4层 · 下单与预算机器(真钱防线)", Lines: []string{
			"来源闸 fast_xgap_live_enabled=" + onOff(cfg.FastXgapLiveEnabled, "⚠️true(实弹!)", "false 🔒 全闸通过也只记影子"),
			"持证闸(硬代码):非探针且不在持证名单 → no_cert 拒;当前持证名单=" + onOff(strings.TrimSpace(cfg.FastCertifiedSources) != "", cfg.FastCertifiedSources, "空(0张证)"),
			fmt.Sprintf("开火形态(你批准后两行配置):探针 %.0fU 名义 → 治理器 ≤%d笔/累亏≤%.1fU 自动缴械(重启不失忆)", cfg.FastLiveNotionalUSDT, cfg.FastProbeMaxTrades, cfg.FastProbeMaxLossUSDT),
			"净值哨兵(滚50中位<0粘停)→ fast日熔断(本金30%)→ 账户级35U/日 → 急停三级(面板/文件/systemd)",
		}},
		{Title: "第5层 · 各策略专属出场(出场刀配游戏类型)", Lines: []string{
			fmt.Sprintf("E·原版(回归局):收敛≥-%.0f=论点兑现 / %ds=论点过期 / -%.0f灾难+学费冷却", cfg.FastXgapExitGapBps, cfg.FastXgapMaxHoldSeconds, cfg.FastXgapDisasterBps),
			"E·改良版(影子真跑):burst弹射 > 灾难 > 棘轮(峰≥10回吐8落袋) > 收敛 > 到点 —— 与原版并排记账",
			"火刀·程序S(动量局):trail棘轮(10/20) / 二波止错(20s无接力=离场) / -40灾难 / cap120s",
			"烟花(回归局):固定300s(trail在回归局=绞肉机,实证) / -40灾难",
		}},
		{Title: "第6层 · 账本(判决货币;每枪四口径并行)", Lines: []string{
			"det=触发时刻(无闸对照) / rest=REST可执行·原版出场 / rest2=改良版出场 / mkr=挂单变体",
			"每行带:八粒子位(新鲜/燃料/广度/深度档/币种/惯犯/派班/残余带)+ 路径(生龄/速度/归因)+ 反事实标注(棘轮/弹射)+ 州 + module",
		}},
		{Title: "第7层 · 判决工厂(唯一发证通道;真钱只能从这道门出来)", Lines: []string{
			"假设登记(必须带对手模型)→ 预注册(冻结切点,改=作弊;封样在先)",
			"→ 前向新样本 n≥30 且 净中位>0 且 PF≥1.3 且 胜率≥45% 且 单币≤20% → 格子持证",
			"→ 连数字递你裁探针 → 探针PF>1 → 灰度5→50→200U → 走量升费率档",
		}},
		{Title: "第8层 · 认知机器(30min自刷;只提名不判决)", Lines: []string{
			"kNN事件空间(相似事件→相似结局)/ 邻域引力场(局部海况)/ 后验织网(60格亮度)/ 结构图(粒子承重)",
			"产出=重组假设 → 回到第7层排队;智能层永不拥有最后防线(宪法附则)",
		}},
	}
}

func missionBuildPhilosophy() []missionPhilosophyRow {
	return []missionPhilosophyRow{
		{Tenet: "调研定方向", Meaning: "方向=打哪场仗(选池塘/选被迫流),不是猜下一秒价格;只有过了证伪审查的调研才配定方向",
			Embodim: []string{"E 的方向来自 54h 离线研究+预注册前向判决(第⑧步判决工厂)", "一切新想法先影子考试,过门才见真钱(裁决日历)", "信息集审查:入场规则只用入场时刻可知的数(全流程)"}},
		{Tenet: "极端定仓位", Meaning: "期望藏在极端事件的肥尾里,平时仓位应为零;该定仓位的是只有我们测得到的极端",
			Embodim: []string{"平时零仓位,价差裂开≥40bps(第②步触发)才动", "探针 5U×20笔×0.5U=赌注与验证严格度成正比(第⑥步)", "风暴/广跌州直接歇业(第④步派班),不在人人看见的极端里凑热闹"}},
		{Tenet: "天道定信心", Meaning: "信心锚在不变量上(套利商不会放着价差不搬),不锚在近期盈亏;信念定期缴可证伪预言税",
			Embodim: []string{"E 的论点=天道条款:同一张永续两所价差必被搬平(第②步)", "预言税=前向判决:预注册数字没兑现就是错(第⑧步)", "页面基准数据=当前口径前向,不粉饰(成绩区)"}},
		{Tenet: "止错,不止损", Meaning: "价格逆行不是错,论点死亡才是错;'错'必须在进场前定义、有半衰期,另留一条不讲道理的灾难线",
			Embodim: []string{"收敛≥-10=论点兑现离场(第⑦步)", "600s 没收敛=论点过期,到点离场=止错不是止损(第⑦步)", "-60bps 灾难线+学费冷却=数据看不见的错的保险费,杠杆下偿付能力优先于哲学(第⑦步)"}},
	}
}

func missionBuildAnatomy() []missionAnatomyStep {
	return []missionAnatomyStep{
		{Step: "① 数据地基(仪器层)", Rule: "全市场 mid 标尺 3s 一拍 + Bybit 锚(新鲜≤8s)+ 爆仓流/燃料/广度",
			Particles: []string{"全市场标尺(出身:liq_follow 尸体的'盲区闸迟到'根治)", "跨所锚 xmid(出身:跨所价差分类器尸体的数据,它死了但数据孕育了 E)"},
			Tenet: "调研定方向"},
		{Step: "② 触发", Rule: "OKX mid 比 Bybit 锚便宜 ≥40bps → 候选;同币 1800s 冷却一枪",
			Particles: []string{"跨所价差触发粒子(天道条款)", "单币冷却粒子(出身:CELO 两天 1124 笔重复锤教训)"},
			Tenet: "极端定仓位 / 天道定信心"},
		{Step: "③ 执行守卫", Rule: "REST 现拉盘口复核:价差已收敛→放弃(gap_gone);盘口点差>12bps→放弃(吃不到)",
			Particles: []string{"点差闸粒子(资格族,出身:scorer 尸体的 shadow 数据)", "机会已死不追(fade 尸体教训:追迟到的机会=付逆选择税)"},
			Tenet: "止错(错在进场前定义)"},
		{Step: "④ 格子三闸(司天监派班)", Rule: "新鲜度(回看1800s价差现过>-20)× 派班(燃料<40 且 广度>-15)× 残余带∈[-40,-20]",
			Particles: []string{"新鲜度粒子(出身:E-v1 尸体——LAB 摆半小时的陈年价差=标价不是错价)", "燃料/广度州粒子(司天监:E 毒州=广跌,UTC02 血窗实证)", "残余带粒子(出身:㉝执行层解剖——浅=肉没了,深=没人抢有原因,[-40,-20]是唯一活区)"},
			Tenet: "调研定方向(州=打哪场仗)"},
		{Step: "⑤ 下单", Rule: "taker 买 OKX;当前锁死(record-only),开火=探针 5U",
			Particles: []string{"taker 进场粒子(出身:fade 尸体——回归局挂 maker 等成交=力竭诊断被证伪)", "小池塘体型优势(铁律:几千U 不跟机构抢镜面盘)"},
			Tenet: "极端定仓位"},
		{Step: "⑥ 预算与哨兵(组织层)", Rule: "探针≤20笔/累亏≤0.5U 自动缴械;净值哨兵滚50中位<0 粘性停火;日熔断本金30%",
			Particles: []string{"探针预算粒子(章程§-5:买离线买不到的真滑点)", "净值哨兵粒子(测'游戏此刻给不给钱'本身)", "死机器层:智能层永不拥有最后防线(宪法附则)"},
			Tenet: "止错(认错成本上限)"},
		{Step: "⑦ 持有与出场", Rule: "收敛≥-10 离场 / 600s 到点离场 / -60 灾难砍+学费冷却 / burst弹射(锁)",
			Particles: []string{"收敛出场粒子(回归局专用:论点是闭合不是动量——烟花刀教训:出场刀必须配游戏类型)", "max_hold=错的半衰期", "灾难线+灾难入冷却(出身:LAB 灾难后 44s 再进再灾难)", "弹射粒子(出身:38ms burst 探测器,军火商思维:探测器不当枪当盾)"},
			Tenet: "止错,不止损"},
		{Step: "⑧ 学习闭环(判决工厂)", Rule: "每枪进粒子账本三口径(det提名/rest判决/mkr变体)+棘轮/弹射反事实逐tick标注 → 预注册考试自动阅卷 → 过门才递你批",
			Particles: []string{"粒子账本(判决货币:Go 单一来源,消灭口径漂移)", "预注册=死亡证明书模板(错在上岗前定义)", "配对反事实标注(改良包三杠杆的证据工厂)"},
			Tenet: "调研定方向 + 天道的预言税"},
	}
}

// missionRosterGroup 编制四抽屉一格(㊘四抽屉收纳制的页面化)。
type missionRosterGroup struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
	Note  string   `json:"note,omitempty"`
}

// missionVerdict 裁决日历一行:接下来会"出分"的考试,按到线先后排。
// Scope 口径徽标(07-08 用户"考试结果全是最新策略的吗"之问):
// strategy=最新策略口径战绩 / variant=下一版候选变体研究 / instrument=仪器与数据基建裁决。
type missionVerdict struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	ETA      string `json:"eta"`      // 预计到线(墙钟或样本条件)
	Progress string `json:"progress"` // 当前进度(现算)
	Question string `json:"question"` // 这场考试裁什么
	Done     bool   `json:"done"`     // 已到线出分
}

type missionResponse struct {
	Now         time.Time           `json:"now"`
	Phase       string              `json:"phase"`
	PhaseDetail string              `json:"phase_detail"`
	Candidates  []missionCandidate  `json:"candidates"` // 管线全员位置表
	// ===== 一页看懂(2026-07-08 用户"页面还是看不懂"反馈的重做;讲故事优先,细节表格下沉)=====
	Headline   string               `json:"headline"`    // 大字一句话:现在在做什么
	TodayFocus string               `json:"today_focus"` // 今日焦点:现在唯一值得看的一件事(机器自动挑)
	Journey    []missionJourneyStop `json:"journey"`     // 六站旅程图:你在这里
	PlainCards []missionPlainCard   `json:"plain_cards"` // 白话数字卡
	// ===== 找回方向四件套(2026-07-08 用户"试错太多,流程认识不清晰"之诉;
	// 同日重心纠偏:页面数据以当前最新策略为基准,历史学费不上页面)=====
	Baseline     missionBaseline     `json:"baseline"`      // 当前策略基准+改良包杠杆现算
	// ===== 作战实时状态条(07-08 21:40 探针实弹化后,"看页面了解系统全部"之诉)=====
	Sky    map[string]interface{}   `json:"sky"`    // 司天监此刻:燃料/潮汐/广度/E值班(现算)
	Probes []map[string]interface{} `json:"probes"` // 探针名单各源实时预算
	Exams  []map[string]interface{} `json:"exams"`  // 自动阅卷人:在考考试进度/已出分
	// ===== 策略解剖书(同日"哲学/粒子/流程连不上"之诉;"详细分层图"同日追加)=====
	Philosophy []missionPhilosophyRow `json:"philosophy"`  // 四句纲领→机制化身
	Anatomy    []missionAnatomyStep   `json:"anatomy"`     // 八步流程解剖(规则/粒子出身/哲学)
	FlowLayers []missionFlowLayer     `json:"flow_layers"` // 九层纵剖面(数字/锁态现算)
	StrategyCard missionStrategyCard `json:"strategy_card"` // 一军 E 完整流程(带今日漏斗实数)
	Roster       []missionRosterGroup `json:"roster"`       // 编制四抽屉(一军/仪器/冷藏/坟场)
	VerdictQueue []missionVerdict    `json:"verdict_queue"` // 裁决日历(接下来谁出分)
	// ===== 细节区(下沉,给深查用)=====
	Goal      []string            `json:"goal"`
	Doing     []missionWorkstream `json:"doing"`
	Gates     []missionGate       `json:"gates"`
	Ladder    []missionStage      `json:"ladder"`   // 真钱阶梯
	Pipeline  []missionStage      `json:"pipeline"` // 六层流水线(整个策略)
	Certified []string            `json:"certified"`
}

// missionTodayFocus 纯函数:从当前门状态里挑"现在唯一值得看的一件事"。
// 优先级:需要用户拍板 > 刚判负的善后 > 最近的裁决点倒计时 > 默认(机器攒样本,无需操作)。
func missionTodayFocus(resDone, resPass bool, resN int, resMed, resPF float64, mkrDays float64, pathN int, probeHours float64) string {
	if resDone && resPass {
		return "【等你拍板】E 的探针真门票(残余带闸)已过检——数字已备好,裁 5U 探针开不开。"
	}
	var waits []string
	if !resDone {
		waits = append(waits, fmt.Sprintf("残余带闸还差 %d 笔到线", 30-resN))
	}
	if mkrDays < 2 {
		waits = append(waits, fmt.Sprintf("maker裁决还差 %.1f 天", 2-mkrDays))
	}
	if pathN < 100 {
		waits = append(waits, fmt.Sprintf("路径分桶还差 %d 行", 100-pathN))
	}
	if probeHours > 0 && probeHours < 24 {
		waits = append(waits, fmt.Sprintf("全市场磁带 24h 裁决还差 %.0f 小时", 24-probeHours))
	}
	head := "今天没有需要你做的事。"
	if resDone && !resPass {
		// 工程师口径(07-08 用户重心指令):不停在"判负",直接指向改良杠杆。
		head = fmt.Sprintf("E 门票首轮未过门(n=%d 中位%+.1f PF%.2f)→当前重心=改良包三杠杆(三闸/弹射/棘轮)前向对比,页面基准数据即该口径。", resN, resMed, resPF)
	}
	if len(waits) == 0 {
		return head + " 机器在无人值守攒证据。"
	}
	next := waits[0]
	return head + " 最近的裁决点:" + next + ";其余在排队(" + strings.Join(waits, " / ") + ")。"
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
		if r.Module != "" && r.Module != "e" {
			continue // 多模块化后 fire/firework 行禁止漏入 E 考试/吞吐统计
		}
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

// missionSealCellGates 三闸格子口径部署时刻(㊷,07-08 04:34Z)= 当前策略基准窗起点。
var missionSealCellGates = time.Date(2026, 7, 8, 4, 34, 0, 0, time.UTC)

// missionScanBaseline 读粒子账本 → 当前策略基准+改良包杠杆(页面主展示,现算)。
func missionScanBaseline(recordDir string) missionBaseline {
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(recordDir, "okx_directional", fastParticleLedgerFile), time.Time{}, 0)
	if err != nil {
		return missionBaseline{SinceLabel: "账本不可读"}
	}
	return missionBaselineFromRows(rows, missionSealCellGates)
}

// missionBuildStrategyCard 一军 E 完整流程卡:规则=冻结口径(config 真值),今日实数=漏斗计数器。
func missionBuildStrategyCard(cfg OKXDirectionalConfig, fn fastXgapFunnelStats) missionStrategyCard {
	skipLabel := map[string]string{
		"gap_gone":              "REST复核时价差已收敛(机会没了)",
		"spread_wide":           "盘口点差>12bps(mid价差吃不到)",
		"cell_stale_gap":        "新鲜度闸:裂开超过半小时=标价不是错价",
		"cell_off_duty_storm":   "派班闸:风暴州(燃料≥40)歇业",
		"cell_off_duty_breadth": "派班闸:广跌州(广度≤-15)歇业",
		"cell_residual_shallow": "残余带闸:残余>-20,肉没了",
		"cell_residual_deep":    "残余带闸:残余<-40,没人抢有原因",
	}
	var skips []string
	// 按次数降序列今日拦截
	type kv struct {
		k string
		n int
	}
	var ks []kv
	for k, n := range fn.Skips {
		ks = append(ks, kv{k, n})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].n > ks[j].n })
	for _, x := range ks {
		lb := skipLabel[x.k]
		if lb == "" {
			lb = x.k
		}
		skips = append(skips, fmt.Sprintf("%s ×%d", lb, x.n))
	}
	todaySkips := "今日未拦截"
	if len(skips) > 0 {
		todaySkips = strings.Join(skips, ";")
	}
	live := cfg.FastXgapLiveEnabled
	return missionStrategyCard{
		Name:      "E·跨所捡便宜货(唯一主攻)",
		Mechanism: "同一张永续 OKX 比 Bybit 便宜太多时,搬砖商必然把价差搬平(天道条款)。我们买便宜的 OKX,等价差闭合。赚的是慢所追赶快所的时滞。",
		Steps: []missionFlowStep{
			{Name: "① 触发", State: "on", Detail: fmt.Sprintf("全市场标尺每3s扫:OKX mid 比 Bybit 锚便宜 ≥%.0fbps(锚新鲜≤8s),同币 %d 分钟一枪", cfg.FastXgapEntryGapBps, cfg.FastXgapCooldownSeconds/60),
				Today: fmt.Sprintf("今日触发 %d 次", fn.Trigger)},
			{Name: "② 执行守卫", State: "on", Detail: "REST 现拉盘口复核:价差还在(没收敛)+点差≤12bps(吃得到)"},
			{Name: "③ 格子三闸", State: onOff(cfg.FastXgapCellGatesEnabled, "on", "locked"), Detail: "新鲜度(半小时内正常过)×派班(非风暴×非广跌)×残余带∈[-40,-20](唯一活区)",
				Today: todaySkips},
			{Name: "④ 下单", State: onOff(live, "on", "locked"), Detail: onOff(live, "⚠️ 实弹开启", "锁死(record-only):全闸通过也只记影子账,不下单"),
				Today: fmt.Sprintf("今日全闸通过 %d 枪(若实弹,这就是打出去的数)", fn.Pass)},
		},
		Exits: []missionFlowStep{
			{Name: "burst 弹射(改良)", State: "on", Detail: "持仓中同币卖方爆量→立即弹出(治灾难单)。影子主口径已真跑;实枪采用=前向过检+你批" + onOff(cfg.FastXgapBurstEjectEnabled, "(实枪已开)", "")},
			{Name: "棘轮锁盈(改良)", State: "on", Detail: "峰值冲上≥10bps 后回吐≥8bps→落袋离场(治吐回税)。影子主口径已真跑;实枪采用=前向过检+你批"},
			{Name: "收敛离场", State: "on", Detail: fmt.Sprintf("价差回到 ≥-%.0fbps=论点兑现,离场", cfg.FastXgapExitGapBps)},
			{Name: "到点/灾难", State: "on", Detail: fmt.Sprintf("%d 秒没闭合=论点过期离场;-%.0fbps 无条件砍+该币入学费冷却", cfg.FastXgapMaxHoldSeconds, cfg.FastXgapDisasterBps)},
		},
		Upgrade: []missionFlowStep{
			{Name: "三闸(已装已开)", State: "on", Detail: "把 E 从全时段乱打收敛到唯一活区;代价=风暴日几乎不开枪(fail-closed 如设计)"},
			{Name: "burst 弹射(已装锁死)", State: "locked", Detail: "治最大单项亏损源(崩盘单);等前向数据+用户批"},
			{Name: "棘轮出场(账本标注中)", State: "locked", Detail: "治吐回税(53%冲正单收负,吐回中位40.6bps);账本逐tick攒精确反事实,不动实枪"},
		},
		Status: "页面主口径=★E·改良版(三闸+棘轮+弹射,影子真跑);原版出场同步并行作对照——两条曲线的差=改良杠杆真实兑现的钱。实枪出场变更须前向过检+你批准。",
	}
}

// missionBuildRoster 编制四抽屉(㊘;登记态=项目官方决策记录,HANDOFF 同步维护)。
func missionBuildRoster() []missionRosterGroup {
	return []missionRosterGroup{
		{Title: "一军(唯一主攻,汇报只讲它)", Items: []string{"E·跨所捡便宜货 —— 在探针门口回炉改良中"}},
		{Title: "自动后台(仪器,测量不交易)", Items: []string{
			"13 条数据流采集(全市场mid/热币WS/双所爆仓/磁带…)",
			"Go 粒子账本三口径(det/rest/mkr)=判决货币",
			"三层认知机器(kNN空间/引力场/后验织网,30min 自刷)",
			"监视循环+阅卷人(考试自动出分)",
		}, Note: "异常才报,平时不占你注意力"},
		{Title: "冷藏(影子排队,越过门槛才许出现名字)", Items: []string{
			"烟花反手刀 / 火灾瀑布刀 / #8b资费后崩 / 开盘反隔夜 —— 全部影子记账攒判决,零成本",
			"替补席:死水回补 / 死水做市 / E2双腿 / M程序",
		}},
		{Title: "坟场(21具,不复活不占注意力)", Items: []string{
			"scorer/fade/liq_catch/动量全档/吸收率/沉默钟…(死因五级已归档 PARTICLE_LIBRARY.md)",
			"≥9 具直接死因=12bps 门票而非方向错 → maker化/费率降档若成,按 6bps 地板批量重审",
		}},
	}
}
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
	autoSrcs := make([]string, 0, 2) // 07-09 历史考试制:自动接入名单同样=探针期
	for s := range eng.fastAutoProbeList() {
		autoSrcs = append(autoSrcs, s)
	}
	sort.Strings(autoSrcs)
	allProbe := probeSrc
	if len(autoSrcs) > 0 {
		if allProbe != "" {
			allProbe += ","
		}
		allProbe += strings.Join(autoSrcs, ",")
	}
	anyLive := cfg.FastXgapLiveEnabled || cfg.FastTapeLiveEnabled || cfg.FastLiqFollowLiveEnabled ||
		cfg.FastScorerLiveEnabled || cfg.FastFadeLiveEnabled || allProbe != ""
	anySlow := false
	for _, a := range slow {
		anySlow = anySlow || a
	}
	phase := "阶段A·发现期(零实盘敞口)"
	detail := "fast 层 07-04 停火、慢线 07-07 全停;全系统 record-only,搜索边际成本=0 真钱。"
	if allProbe != "" {
		phase = "阶段B·探针期(" + allProbe + ")"
		detail = "每源独立预算(自动资金阶梯 L0 起步)自动缴械/晋升;页面天花板+急停凌驾。"
	} else if anyLive {
		phase = "⚠️ 有实弹开关开启(请核对)"
	}

	mkrDays := 0.0
	if !led.MkrFirstAt.IsZero() {
		mkrDays = now.Sub(led.MkrFirstAt).Hours() / 24
	}

	// ===== 找回方向四件套 =====
	baseline := missionScanBaseline(dir)
	funnel := fastXgapFunnelSnapshot(now)
	card := missionBuildStrategyCard(cfg, funnel)
	roster := missionBuildRoster()
	verdicts := []missionVerdict{
		{Name: "改良包 vs 原版 E(前向对比)——头号", Scope: "strategy",
			ETA:      "rest 供给恢复后攒样本(风暴日 fail-closed 供给极低)",
			Progress: fmt.Sprintf("棘轮配对 rest=%d/det=%d 笔,弹射配对 %d 笔,自动积累中", baseline.RatchetRestN, baseline.RatchetDetN, baseline.EjectN),
			Question: "三闸+弹射+棘轮各拧回多少 bps?E 下一版口径的裁决文件"},
		{Name: "E 探针门票(残余带闸)", Scope: "strategy", Done: led.ResidualN >= 30,
			ETA:      onOff(led.ResidualN >= 30, "首轮已出分", fmt.Sprintf("还差 %d 笔到 30", 30-led.ResidualN)),
			Progress: fmt.Sprintf("n=%d 净中位%+.1f PF%.2f", led.ResidualN, led.ResidualMed, led.ResidualPF),
			Question: "首轮未过门;改良口径继续攒,过门才谈探针"},
		{Name: "全市场磁带 24h 裁决", Scope: "instrument",
			ETA:      fmt.Sprintf("还差 %.0f 小时(满 24h)", max(0, 24-probeHours)),
			Progress: fmt.Sprintf("已攒 %.1f/24h", probeHours),
			Question: "全市场磁带比 40 热币多看见的爆量,值不值得接进确认管线(门=增量≥40%)"},
		{Name: "maker 化裁决(曲线①)", Scope: "variant",
			ETA:      fmt.Sprintf("还差 %.1f 天(满 2 天)", max(0, 2-mkrDays)),
			Progress: fmt.Sprintf("mkr 口径已攒 %.1f 天", mkrDays),
			Question: "挂单省的 6bps > 逆选择税吗?过→坟场半数按 6bps 地板复活重审"},
		{Name: "E 路径分桶(生龄×速度×归因)", Scope: "variant", Done: led.PathN >= 100,
			ETA:      onOff(led.PathN >= 100, "已到样,待阅卷", fmt.Sprintf("还差 %d 行到 100", 100-led.PathN)),
			Progress: fmt.Sprintf("rest+path_ok 已攒 %d", led.PathN),
			Question: "『价差怎么诞生的』这个路径信息值多少 bps"},
	}

	resp := missionResponse{
		Now: now, Phase: phase, PhaseDetail: detail,
		Baseline: baseline, StrategyCard: card, Roster: roster, VerdictQueue: verdicts,
		Philosophy: missionBuildPhilosophy(), Anatomy: missionBuildAnatomy(),
		FlowLayers: missionBuildFlowLayers(cfg, funnel),
		Sky:        eng.fastSkySnapshot(now),
		Probes:     eng.fastProbeSnapshot(),
		Exams:      eng.fastExamSnapshot(),
		Goal: []string{
			"使命:分拣器分拣后,派给排列好的粒子组合交易盈利(费率飞轮只是次要放大器)。",
			"两道门(登记,HANDOFF §-7C):利润引擎门=中位≥18bps;飞轮引擎门=中位≥12bps。",
			"近期目标:第一个通过探针真门票的策略族拿 5U 探针实测真滑点(须用户明示批准)。",
		},
		Certified: []string{"E×平静海 格子(07-08 发证考试过检:n≥30 净中位+12.6 PF1.80;探针前须过残余带闸)"},
	}

	// ===== 一页看懂区(讲故事优先) =====
	resDone := led.ResidualN >= 30
	resPassNow := resDone && led.ResidualMed > 0 && led.ResidualPF >= 1.3
	resp.Headline = "我们在用零真钱的影子机器海选策略:谁能连过三关(格子考试→探针门票→5U探针),谁才配用真钱。目前最接近的是 E(跨所捡便宜货)。"
	resp.TodayFocus = missionTodayFocus(resDone, resPassNow, led.ResidualN, led.ResidualMed, led.ResidualPF, mkrDays, led.PathN, probeHours)
	journeyState := func(idx int) string { // 0..5;当前=②影子考试(除非探针开着)
		cur := 1
		if probeSrc != "" {
			cur = 3
		}
		switch {
		case idx < cur:
			return "done"
		case idx == cur:
			return "current"
		}
		return "pending"
	}
	resp.Journey = []missionJourneyStop{
		{Name: "① 建机器", Plain: "把数据流/分拣器/考场/账本全部建好,让试错免费", Condition: "已完成:13流+司天监+Go三口径账本在跑", State: journeyState(0)},
		{Name: "② 影子海选(现在在这)", Plain: "所有策略先在影子里用真数据考试,死掉不花一分钱", Condition: fmt.Sprintf("出线条件=格子考试过+探针门票过;E 已过格子,门票 %d/30", led.ResidualN), State: journeyState(1)},
		{Name: "③ 5U 探针", Plain: "第一个双过的策略拿 5U 真钱测真滑点(你拍板才开)", Condition: "≤20笔/亏≤0.5U 自动缴械,买的是离线买不到的真实成交数据", State: journeyState(2)},
		{Name: "④ 灰度", Plain: "探针赚钱才加码,单票 5→50→200U 阶梯放大", Condition: "探针 PF>1 且哨兵不报警", State: journeyState(3)},
		{Name: "⑤ 走量升级", Plain: "单票 500U×几百笔/天刷成交量,费率档 VIP2,门票 12→9bps", Condition: "月成交量 $10M(机器算术:500U×330笔/天×30天)", State: journeyState(4)},
		{Name: "⑥ 利润引擎", Plain: "门票降了,坟场边缘策略批量复活,多引擎全天候营业", Condition: "任一策略净中位≥新门票的1.5倍", State: journeyState(5)},
	}
	// 管线全员位置表:左半段(海选/发证)多车道并行,右半段(探针起)当前单车道。
	// E 在门口不是钦点,是唯一"枪已造好+三闸硬化+考试走完几轮"的候选;其余照跑未被压制。
	resp.Candidates = []missionCandidate{
		{Name: "E 跨所捡便宜货", Stage: "②→③ 探针门口(改良中)", Front: true,
			Note: fmt.Sprintf("当前=三闸口径攒前向(门票 n=%d);三杠杆(三闸/弹射/棘轮)对比表出数定下一版", led.ResidualN)},
		{Name: "烟花反手刀", Stage: "①影子海选", Note: "力竭×拉升≥60×点差≤6;前向判决攒样本(阅卷人自动)"},
		{Name: "开盘反隔夜(股票perp)", Stage: "①影子海选", Note: "按簇攒(周一开盘=肥簇假说);每天自动+1-2簇"},
		{Name: "火灾瀑布刀", Stage: "①影子海选", Note: "组合判决屡败;等真风暴锚或费率降档,值班表内影子记账"},
		{Name: "#8b 资费后崩", Stage: "①影子海选", Note: "07-08 判负(跨日翻向);三问解剖后决定重考口径"},
		{Name: "死水回补/死水做市/E2双腿/M程序", Stage: "替补席", Note: "等考位与前置条件,预注册后进①"},
	}
	resTone := "neutral"
	if resDone {
		resTone = onOff(resPassNow, "good", "bad")
	}
	mkrTone := "neutral"
	if led.MkrFilled+led.MkrUnfilled >= 50 {
		mkrTone = "bad" // 首批读数 PF 偏低;到 2 天正式裁决
	}
	resp.PlainCards = []missionPlainCard{
		{Title: "真钱敞口", Value: "0 U", Tone: "good",
			Explain: "慢线已全停、fast 全锁,现在亏损上限=0。烧的是数据不是钱。"},
		{Title: "E 的探针门票", Value: fmt.Sprintf("%d/30 笔,中位 %+.1f", led.ResidualN, led.ResidualMed), Tone: resTone,
			Explain: "E 要拿真钱,必须先在『REST残余20-40bps』这个唯一活区里前向证明自己(中位>0 且 PF≥1.3)。"},
		{Title: "maker 曲线(省门票实验)", Value: fmt.Sprintf("已攒 %.1f/2 天", mkrDays), Tone: mkrTone,
			Explain: "如果挂单比吃单省的钱>逆选择亏的钱,坟场里一半死者复活。首批读数偏差,等满 2 天正式裁。"},
		{Title: "第一波盲区探针", Value: fmt.Sprintf("已攒 %.1f/24 小时", probeHours), Tone: "neutral",
			Explain: "以前只看得见 40 个热币,现在 401 币全market磁带——裁决『我们此前错过的第一波爆量值不值得接』。"},
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
