package app

// ===== 自动挖掘器 v1(2026-07-09 用户拍板"自动匹配和优化组合寻找粒子,全自动升级维护")=====
//
// 职责:每 4h 在【账本字段空间】自动做 K 折组合挖掘(6 片,≥5/6 同号+门槛),
// 过检组合自动登记为动态考试(exam_dynamic.json)交给阅卷人前向确认;判负组合记指纹,
// 永不自动复活(坟场纪律)。
//
// 物理预算(章程②):组合深度≤2;动态在考≤3;谓词库=有限已验证维度(不自由生成=防
// 元路由器);单币≤30%;好组 n≥40 才配登记。
// 边界:挖掘→考试全自动;考试过门后的"接实枪闸"涉及写代码,推送人工(诚实边界)。

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	fastMinerDynFile     = "exam_dynamic.json"
	fastMinerHistFile    = "exam_dynamic_history.json"
	fastMinerMaxInFlight = 3
	fastMinerKFolds      = 6
	fastMinerMinFoldOK   = 5
	fastMinerMinN        = 40
)

// ===== 圣日封存制(2026-07-09 用户拍板"将过去某一天的数据拆出来,不参与系统/策略制定,只用来验证")=====
// 圣日数据对一切挖掘/换届/切点标定不可见;唯一用途=候选过完 K 折+前向后的【一次性】终验,
// 每个假设只许消费圣日一次(验证登记在 HANDOFF,复用=作弊)。
// 名单只许追加不许移除(移除=把考卷塞回题库);每周日 UTC 自动入圣日集合(随数据增长滚动扩充)。
var fastVaultDays = map[string]bool{
	"2026-07-08": true, // 首个圣日(仪器覆盖最全的一天:全市场磁带+多模块账本+market_mids 全量)
}

// fastInVault 纯判定:该时刻是否属于圣日(名单日 或 周日 UTC)。
func fastInVault(t time.Time) bool {
	u := t.UTC()
	return fastVaultDays[u.Format("2006-01-02")] || u.Weekday() == time.Sunday
}

// fastMinerRule 谓词(字段规则 DSL;字段集有限=数据地板内)。
type fastMinerRule struct {
	Field string  `json:"field"` // fuel_5m/breadth_30m/spread_bps/gap_det_bps/gap_rest_bps/prior_fw_24h/fw_exhaust_ratio/fw_sym_liq_5m/bit
	Op    string  `json:"op"`    // lt/ge/eq/bit_set/bit_clear
	Val   float64 `json:"val"`
}

// fastMinerExam 动态考试(阅卷人加载)。
type fastMinerExam struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Module   string          `json:"module"` // ""=e
	Src      string          `json:"src"`
	Seal     time.Time       `json:"seal"`
	Rules    []fastMinerRule `json:"rules"`
	MineNote string          `json:"mine_note"` // 挖掘期读数(证据留痕)
}

// fastMinerFieldVal 行字段取值(有限集;未知字段=NaN 永不命中)。
func fastMinerFieldVal(r fastLedgerRow, field string) (float64, bool) {
	switch field {
	case "fuel_5m":
		return r.Fuel5m, true
	case "breadth_30m":
		return r.Breadth30, true
	case "spread_bps":
		return r.SpreadBps, true
	case "gap_det_bps":
		return r.GapDetBps, true
	case "gap_rest_bps":
		return r.GapRestBps, true
	case "prior_fw_24h":
		return float64(r.PriorFw24h), true
	case "fw_sym_liq_5m":
		return float64(r.FwSymLiq5m), true
	case "fw_exhaust_ratio": // ret15/ret60(烟花力竭比;ret60<=0 不可算)
		if r.FwRet60Bps <= 0 {
			return 0, false
		}
		return r.FwRet15Bps / r.FwRet60Bps, true
	case "bits":
		return float64(r.Bits), true
	case "liq_echo": // L1 回声(59 号研究):-1 不适用/0 无回声(毒)/1 同行确认
		return float64(r.LiqEcho), true
	case "wx_grid": // 07-10 天气格(2 号 v1;0-7,-1 不可算):格5=暴动·跌·放量
		return float64(r.WxGrid), true
	case "prior_fw_24h_habit": // 惯犯池(24h 同币烟花触发数;WHERE 钥匙 AUC0.91)
		return float64(r.PriorFw24h), true
	case "confirm_after_t0": // 起点后确认(1=进场后 t0+Δ 出现同向二次确认;0=无)
		return float64(r.ConfirmAfterT0), true
	}
	return 0, false
}

// fastMinerMatch 谓词命中判定。
func fastMinerMatch(r fastLedgerRow, rules []fastMinerRule) bool {
	for _, ru := range rules {
		v, ok := fastMinerFieldVal(r, ru.Field)
		if !ok {
			return false
		}
		switch ru.Op {
		case "lt":
			if !(v < ru.Val) {
				return false
			}
		case "ge":
			if !(v >= ru.Val) {
				return false
			}
		case "eq":
			if v != ru.Val {
				return false
			}
		case "bit_set":
			if int(v)&int(ru.Val) == 0 {
				return false
			}
		case "bit_clear":
			if int(v)&int(ru.Val) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// fastMinerFingerprint 组合指纹(module|src|规则排序序列化)。
func fastMinerFingerprint(module, src string, rules []fastMinerRule) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, fmt.Sprintf("%s|%s|%g", r.Field, r.Op, r.Val))
	}
	sort.Strings(parts)
	h := sha1.Sum([]byte(module + "#" + src + "#" + strings.Join(parts, "&")))
	return hex.EncodeToString(h[:8])
}

// fastMinerPredicates 谓词库(有限;每条带对手模型注释=不自由生成)。
func fastMinerPredicates(module string) []fastMinerRule {
	switch module {
	case "firework":
		return []fastMinerRule{
			{Field: "prior_fw_24h", Op: "lt", Val: 3},     // 非惯犯(躲控盘反复拉砸)
			{Field: "fw_exhaust_ratio", Op: "lt", Val: 0.05}, // 完全力竭(买力枯竭)
			{Field: "fw_sym_liq_5m", Op: "eq", Val: 0},    // 无本币爆仓(非轧空回补)
			{Field: "spread_bps", Op: "lt", Val: 3},       // 厚盘(执行面)
			{Field: "fuel_5m", Op: "lt", Val: 20},         // 死水州(无燃料助推二波)
			{Field: "breadth_30m", Op: "ge", Val: 0},      // 大盘不跌(反手不逆大势)
		}
	case "fire":
		return []fastMinerRule{
			{Field: "fuel_5m", Op: "ge", Val: 40},     // 风暴州(瀑布接力环境)
			{Field: "breadth_30m", Op: "lt", Val: -15}, // 广跌(全场恐慌)
			{Field: "spread_bps", Op: "lt", Val: 6},   // 厚盘
		}
	default: // e 模块
		return []fastMinerRule{
			{Field: "bits", Op: "bit_set", Val: float64(pbFresh)},     // 新鲜裂口
			{Field: "bits", Op: "bit_set", Val: float64(pbDutyXgap)},  // 平静海派班
			{Field: "bits", Op: "bit_clear", Val: float64(pbHabitual)}, // 非惯犯
			{Field: "gap_det_bps", Op: "ge", Val: -60},                // 非深折价(深=真崩盘)
			{Field: "spread_bps", Op: "lt", Val: 3},                   // 厚盘
		}
	}
}

// fastMinerEval K 折评估一个组合;过检返回 (true, 摘要)。
func fastMinerEval(rows []fastLedgerRow, rules []fastMinerRule) (bool, string) {
	var hit []fastLedgerRow
	for _, r := range rows {
		if fastMinerMatch(r, rules) {
			hit = append(hit, r)
		}
	}
	if len(hit) < fastMinerMinN {
		return false, ""
	}
	// 单币主导
	bySym := map[string]int{}
	for _, r := range hit {
		bySym[r.Symbol]++
	}
	for _, c := range bySym {
		if float64(c) > 0.30*float64(len(hit)) {
			return false, ""
		}
	}
	// 汇总门:中位>0 且 PF≥1.3
	nets := make([]float64, 0, len(hit))
	gp, gl := 0.0, 0.0
	for _, r := range hit {
		nets = append(nets, r.NetBps)
		if r.NetBps > 0 {
			gp += r.NetBps
		} else {
			gl -= r.NetBps
		}
	}
	med := medianOf(nets)
	pf := 99.0
	if gl > 0 {
		pf = gp / gl
	}
	if med <= 0 || pf < 1.3 {
		return false, ""
	}
	// K 折:6 片留出,好组-其余中位差 ≥5/6 同号
	sort.Slice(rows, func(i, j int) bool { return rows[i].T0.Before(rows[j].T0) })
	okFolds, validFolds := 0, 0
	for i := 0; i < fastMinerKFolds; i++ {
		lo, hi := i*len(rows)/fastMinerKFolds, (i+1)*len(rows)/fastMinerKFolds
		var a, b []float64
		for j := lo; j < hi; j++ {
			if fastMinerMatch(rows[j], rules) {
				a = append(a, rows[j].NetBps)
			} else {
				b = append(b, rows[j].NetBps)
			}
		}
		if len(a) < 3 || len(b) < 3 {
			continue
		}
		validFolds++
		if medianOf(a) > medianOf(b) {
			okFolds++
		}
	}
	if validFolds < fastMinerMinFoldOK || okFolds < fastMinerMinFoldOK {
		return false, ""
	}
	return true, fmt.Sprintf("n=%d 中位%+.1f PF%.2f 同号%d/%d", len(hit), med, pf, okFolds, validFolds)
}

// fastMinerLoop 常驻(4h/轮):挖掘→过检→登记动态考试→推送。
func (e *OKXDirectionalEngine) fastMinerLoop(ctx interface{ Done() <-chan struct{} }) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[auto_miner] panic 已恢复: %v", r)
		}
	}()
	e.logger.Printf("[auto_miner] 自动挖掘器上岗(4h/轮,账本字段空间,深度≤2,动态在考≤%d)", fastMinerMaxInFlight)
	t := time.NewTicker(4 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.fastMinerRunOnce()
		}
	}
}

// fastMinerRunOnce 单轮挖掘。
func (e *OKXDirectionalEngine) fastMinerRunOnce() {
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(e.recordDir, fastParticleLedgerFile), time.Time{}, 0)
	if err != nil || len(rows) == 0 {
		return
	}
	// 现有动态考试与历史指纹
	var dyn []fastMinerExam
	examReadJSONFile(filepath.Join(e.recordDir, fastMinerDynFile), &dyn)
	hist := map[string]string{} // fingerprint -> verdict/在考
	examReadJSONFile(filepath.Join(e.recordDir, fastMinerHistFile), &hist)
	for _, d := range dyn {
		hist[fastMinerFingerprint(d.Module, d.Src, d.Rules)] = "in_flight"
	}
	inFlight := len(dyn)
	found := 0
	for _, module := range []string{"", "fire", "firework"} {
		if inFlight >= fastMinerMaxInFlight {
			break
		}
		var sub []fastLedgerRow
		for _, r := range rows {
			if fastInVault(r.T0) {
				continue // 圣日封存:挖掘永远看不见这一天(终验专用,一次性消费)
			}
			m := r.Module
			if m == "e" {
				m = ""
			}
			if m == module && r.Src == "rest" && r.Exit != "unfilled" {
				sub = append(sub, r)
			}
		}
		if len(sub) < fastMinerMinN*2 {
			continue
		}
		preds := fastMinerPredicates(module)
		// 深度1+深度2(两两组合)
		var cands [][]fastMinerRule
		for i := range preds {
			cands = append(cands, []fastMinerRule{preds[i]})
			for j := i + 1; j < len(preds); j++ {
				cands = append(cands, []fastMinerRule{preds[i], preds[j]})
			}
		}
		for _, rules := range cands {
			if inFlight >= fastMinerMaxInFlight {
				break
			}
			fp := fastMinerFingerprint(module, "rest", rules)
			if _, seen := hist[fp]; seen {
				continue // 在考或有案底(判负永不自动复活)
			}
			ok, note := fastMinerEval(sub, rules)
			if !ok {
				continue
			}
			modName := module
			if modName == "" {
				modName = "e"
			}
			ex := fastMinerExam{
				Key: "dyn_" + fp, Module: module, Src: "rest",
				Label:    fmt.Sprintf("[自动挖掘]%s·%s", modName, fastMinerRulesLabel(rules)),
				Seal:     e.nowFn().UTC(),
				Rules:    rules,
				MineNote: note,
			}
			dyn = append(dyn, ex)
			hist[fp] = "in_flight"
			inFlight++
			found++
			e.logger.Printf("[auto_miner] 🔍 过检登记动态考试 %s:%s(挖掘期 %s;封样后前向确认)", ex.Key, ex.Label, note)
			newAlertDispatcher(e.netCfg.AlertingConfig, e.logger).Notify(Alert{Level: EventLevelInfo,
				Title:  "🔍 自动挖掘:新组合进考场 " + ex.Label,
				Detail: "挖掘期(K折过检):" + note + "\n已自动预注册前向考试,过门后推送(接实枪闸需人工)。", OccurredAt: e.nowFn().UTC()})
		}
	}
	if found > 0 {
		_ = writeJSONFile(filepath.Join(e.recordDir, fastMinerDynFile), dyn)
		_ = writeJSONFile(filepath.Join(e.recordDir, fastMinerHistFile), hist)
	}
}

// fastMinerRulesLabel 组合人话名。
func fastMinerRulesLabel(rules []fastMinerRule) string {
	names := map[string]string{
		"prior_fw_24h|lt": "非惯犯", "fw_exhaust_ratio|lt": "完全力竭", "fw_sym_liq_5m|eq": "无本币爆仓",
		"spread_bps|lt": "厚盘", "fuel_5m|lt": "死水", "fuel_5m|ge": "风暴", "breadth_30m|ge": "大盘不跌",
		"breadth_30m|lt": "广跌", "gap_det_bps|ge": "非深折", "bits|bit_set": "粒子位", "bits|bit_clear": "非粒子位",
	}
	var parts []string
	for _, r := range rules {
		if n, ok := names[r.Field+"|"+r.Op]; ok {
			parts = append(parts, n)
		} else {
			parts = append(parts, r.Field)
		}
	}
	return strings.Join(parts, "×")
}

// fastMinerDynamicExams 阅卷人加载动态考试(转为 fastExamSpec;判负后回写案底)。
func (e *OKXDirectionalEngine) fastMinerDynamicExams() []fastExamSpec {
	var dyn []fastMinerExam
	examReadJSONFile(filepath.Join(e.recordDir, fastMinerDynFile), &dyn)
	out := make([]fastExamSpec, 0, len(dyn))
	for _, d := range dyn {
		d := d
		out = append(out, fastExamSpec{
			Key: d.Key, Label: d.Label, Seal: d.Seal,
			MinN: 30, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.30,
			Rows: func(r fastLedgerRow) bool {
				m := r.Module
				if m == "e" {
					m = ""
				}
				return m == d.Module && r.Src == d.Src && fastMinerMatch(r, d.Rules)
			},
			Question: "自动挖掘组合前向确认(挖掘期 " + d.MineNote + ");过门=推送接闸方案",
		})
	}
	return out
}

// fastMinerRetireDynamic 动态考试出分后归档:从在考文件移除,案底回写(判负永不自动复活)。
func (e *OKXDirectionalEngine) fastMinerRetireDynamic(key, verdict string) {
	var dyn []fastMinerExam
	examReadJSONFile(filepath.Join(e.recordDir, fastMinerDynFile), &dyn)
	hist := map[string]string{}
	examReadJSONFile(filepath.Join(e.recordDir, fastMinerHistFile), &hist)
	kept := dyn[:0]
	for _, d := range dyn {
		if d.Key == key {
			hist[fastMinerFingerprint(d.Module, d.Src, d.Rules)] = verdict
			continue
		}
		kept = append(kept, d)
	}
	_ = writeJSONFile(filepath.Join(e.recordDir, fastMinerDynFile), kept)
	_ = writeJSONFile(filepath.Join(e.recordDir, fastMinerHistFile), hist)
}
