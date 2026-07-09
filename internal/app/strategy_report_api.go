package app

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ===== ㊳ 页面数据②:时间段策略报告(2026-07-08)=====
// 全部数字由 Go 从真实落盘数据现算:真金=四所 settlements.jsonl(交易所实扣口径,USDT);
// 影子=particle_ledger.jsonl(Go 账本虚拟仓,bps)。双口径报告制(㉕-补):真金与影子永不混算,
// 前端分区展示。API: GET /api/strategy-report?from=RFC3339&to=RFC3339&strategy=key。

// stratTrade 统一交易行(真金 unit=USDT;影子 unit=bps,Virtual=true)。
type stratTrade struct {
	Time     time.Time `json:"time"`
	Venue    string    `json:"venue"`
	Symbol   string    `json:"symbol"`
	Side     string    `json:"side"`
	Strategy string    `json:"strategy"`
	Net      float64   `json:"net"` // USDT(真金)/ bps(影子)
	Notional float64   `json:"notional,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	HoldSec  int       `json:"hold_sec,omitempty"`
	Virtual  bool      `json:"virtual"`
}

// stratAgg 单策略聚合。
type stratAgg struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Virtual  bool    `json:"virtual"`
	Unit     string  `json:"unit"` // USDT / bps
	N        int     `json:"n"`
	WinRate  float64 `json:"win_rate"`
	NetSum   float64 `json:"net_sum"`
	NetMed   float64 `json:"net_med"`
	PF       float64 `json:"pf"`
	MaxDD    float64 `json:"max_dd"` // 该策略自身净值曲线最大回撤(同单位)
	LastTime string  `json:"last_time,omitempty"`
}

type stratCurvePt struct {
	T   time.Time `json:"t"`
	Cum float64   `json:"cum"`
}

type strategyReportResponse struct {
	From       time.Time      `json:"from"`
	To         time.Time      `json:"to"`
	Strategy   string         `json:"strategy"` // 请求的过滤键("" = 全部)
	Strategies []stratAgg     `json:"strategies"`
	Curve      []stratCurvePt `json:"curve"`      // 选中策略(或真金合计)的累计净值
	CurveUnit  string         `json:"curve_unit"` // USDT / bps
	Trades     []stratTrade   `json:"trades"`     // 窗口内最新 ≤200 笔(倒序)
}

// strategyKeyForSettlement 纯函数:结算行 → 策略键。
// fast 行按 candidate_source 细分(scorer/liq_follow/fade_exhaustion/tape_burst/xgap_rev…),
// 慢线按 venue 分。
func strategyKeyForSettlement(venue string, regime, candidateSource string) string {
	if strings.EqualFold(strings.TrimSpace(regime), "fast") {
		src := strings.TrimSpace(candidateSource)
		if src == "" {
			src = "scorer"
		}
		return "fast_" + src
	}
	return "slow_" + strings.ToLower(strings.TrimSpace(venue))
}

// stratLabel 展示名(键找不到时原样返回)。
func stratLabel(key string) string {
	m := map[string]string{
		"slow_okx": "慢线·OKX(已停)", "slow_bybit": "慢线·Bybit(已停)",
		"slow_binance": "慢线·Binance(已停)", "slow_gate": "慢线·Gate(已停)",
		"fast_scorer": "fast·scorer(v1/v2 学费批)", "fast_liq_follow": "fast·跟瀑布(停火)",
		"fast_fade_exhaustion": "fast·fade(降级)", "fast_tape_burst": "fast·tape枪",
		"fast_xgap_rev": "fast·E捡便宜", "shadow_e_det": "E影子·探测口径",
		"shadow_e_rest": "E影子·REST可执行口径", "shadow_e_mkr": "E影子·maker挂单口径",
		"shadow_e_v0": "E影子·v0早期行(未标口径)",
		"e_next":         "★★E·改良包v2(棘轮+弹射)——07-09 判负,弹射粒子级反向,留作对照",
		"e_next2":        "★★★E·棘轮版v3(只棘轮不弹射)——当前最新口径,07-09 死因解剖后登记",
		"shadow_e_rest2": "E影子·v2出场原始流(=e_next 同数据)",
		"shadow_e_rest3": "E影子·v3出场原始流(=e_next2 同数据)",
		"e_current":      "★E·原版出场(三闸口径,改良版的对照组)",
		"exam_e_cell":   "【考试】E格子口径(新鲜×派班)",
		"exam_e_ticket": "【考试】E探针门票(格子×残余带)←门槛表那行数字的明细",
		"shadow_fire_rest":     "火刀影子·程序S(逐tick)",
		"shadow_firework_rest": "烟花刀影子·固定300s(逐tick)",
	}
	if v, ok := m[key]; ok {
		return v
	}
	return key
}

// aggregateStratTrades 纯函数:按策略聚合(输入须同单位一致——调用方保证按 Virtual 分好)。
func aggregateStratTrades(trades []stratTrade) []stratAgg {
	byKey := map[string][]stratTrade{}
	for _, t := range trades {
		byKey[t.Strategy] = append(byKey[t.Strategy], t)
	}
	out := make([]stratAgg, 0, len(byKey))
	for key, ts := range byKey {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Time.Before(ts[j].Time) })
		nets := make([]float64, 0, len(ts))
		wins, gp, gl := 0, 0.0, 0.0
		cum, peak, maxDD := 0.0, 0.0, 0.0
		for _, t := range ts {
			nets = append(nets, t.Net)
			if t.Net > 0 {
				wins++
				gp += t.Net
			} else {
				gl -= t.Net
			}
			cum += t.Net
			if cum > peak {
				peak = cum
			}
			if dd := peak - cum; dd > maxDD {
				maxDD = dd
			}
		}
		sort.Float64s(nets)
		med := 0.0
		if n := len(nets); n > 0 {
			if n%2 == 1 {
				med = nets[n/2]
			} else {
				med = (nets[n/2-1] + nets[n/2]) / 2
			}
		}
		pf := 0.0
		if gl > 0 {
			pf = gp / gl
		} else if gp > 0 {
			pf = 99
		}
		unit := "USDT"
		if ts[0].Virtual {
			unit = "bps"
		}
		out = append(out, stratAgg{
			Key: key, Label: stratLabel(key), Virtual: ts[0].Virtual, Unit: unit,
			N: len(ts), WinRate: float64(wins) / float64(len(ts)),
			NetSum: cum, NetMed: med, PF: pf, MaxDD: maxDD,
			LastTime: ts[len(ts)-1].Time.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Virtual != out[j].Virtual {
			return !out[i].Virtual // 真金在前
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// buildStratCurve 纯函数:累计净值曲线(按时间升序,降采样到 ≤maxPts)。
func buildStratCurve(trades []stratTrade, maxPts int) []stratCurvePt {
	if len(trades) == 0 {
		return nil
	}
	sorted := append([]stratTrade(nil), trades...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	pts := make([]stratCurvePt, 0, len(sorted))
	cum := 0.0
	for _, t := range sorted {
		cum += t.Net
		pts = append(pts, stratCurvePt{T: t.Time, Cum: cum})
	}
	if maxPts > 0 && len(pts) > maxPts {
		step := float64(len(pts)) / float64(maxPts)
		ds := make([]stratCurvePt, 0, maxPts)
		for i := 0; i < maxPts; i++ {
			ds = append(ds, pts[int(float64(i)*step)])
		}
		ds[len(ds)-1] = pts[len(pts)-1]
		pts = ds
	}
	return pts
}

// loadStratTradesReal 读四所 settlements → 真金交易行(时间窗过滤)。
func loadStratTradesReal(recordDir string, from, to time.Time) []stratTrade {
	var out []stratTrade
	for _, vn := range directionalVenueNames {
		path := filepath.Join(directionalRecordDir(recordDir, vn), okxDirectionalSettlementsFile)
		recs, err := loadTailJSONLFile[okxDirectionalSettlement](path, time.Time{}, 0)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.Time.Before(from) || r.Time.After(to) {
				continue
			}
			hold := r.HoldSeconds
			if hold == 0 {
				hold = r.HoldMinutes * 60
			}
			out = append(out, stratTrade{
				Time: r.Time, Venue: vn, Symbol: r.Symbol, Side: r.Side,
				Strategy: strategyKeyForSettlement(vn, r.Regime, r.FastCandidateSource),
				Net:      r.RealizedUSDT, Notional: r.NotionalUSDT,
				Reason: r.Reason, HoldSec: hold, Virtual: false,
			})
		}
	}
	return out
}

// examKeysForLedgerRow 纯函数:账本行额外归属的考试口径键(用户"考试数字看不到明细/选不了时段"
// 之诉的修复——考试口径成为一等策略键,与 shadow_e_* 并列进报告,可选时段/看逐笔/画曲线)。
// e_current     = ★当前版本 E(三闸口径部署 04:34Z 后的 rest 行;shadow_e_rest 键新旧口径混窗
//                 的根治——用户任选时段都不会混入旧口径,与面板基准区同源)。
// exam_e_cell   = E格子口径(rest × 新鲜 × 派班)
// exam_e_ticket = E探针门票/残余带口径(格子 × REST残余∈[-40,-20]),即面板门槛表那行数字的明细。
func examKeysForLedgerRow(src string, bits int, t0 time.Time) []string {
	if src == "rest2" { // 改良包 v2(棘轮+弹射;07-09 判负留作对照)
		return []string{"e_next"}
	}
	if src == "rest3" { // 棘轮版 v3(只棘轮不弹射)= 当前最新口径一等键
		return []string{"e_next2"}
	}
	if src != "rest" {
		return nil
	}
	var keys []string
	if !t0.Before(missionSealCellGates) {
		keys = append(keys, "e_current")
	}
	if bits&pbFresh != 0 && bits&pbDutyXgap != 0 {
		keys = append(keys, "exam_e_cell")
		if bits&pbResidual2040 != 0 {
			keys = append(keys, "exam_e_ticket")
		}
	}
	return keys
}

// loadStratTradesVirtual 读粒子账本 → 影子交易行(bps;unfilled 行剔除)。
// 考试口径行为同一笔的多键投影(聚合按键独立,真金/影子仍永不混算)。
func loadStratTradesVirtual(recordDir string, from, to time.Time) []stratTrade {
	path := filepath.Join(recordDir, "okx_directional", fastParticleLedgerFile)
	recs, err := loadTailJSONLFile[fastLedgerRow](path, time.Time{}, 0)
	if err != nil {
		return nil
	}
	var out []stratTrade
	for _, r := range recs {
		if r.Exit == "unfilled" || r.Time.Before(from) || r.Time.After(to) {
			continue
		}
		src := strings.TrimSpace(r.Src)
		if src == "" {
			src = "v0" // 账本 v0 早期行无口径标注(det 前身),单列不并桶
		}
		module := strings.TrimSpace(r.Module)
		if module == "" {
			module = "e"
		}
		side := r.Side
		if side == "" {
			side = "long"
		}
		base := stratTrade{
			Time: r.Time, Venue: "okx", Symbol: r.Symbol, Side: side,
			Strategy: "shadow_" + module + "_" + src, Net: r.NetBps,
			Reason: r.Exit, HoldSec: r.HoldSec, Virtual: true,
		}
		out = append(out, base)
		if module == "e" { // 考试口径只属于 E 模块(fire/firework 行禁止漏入)
			for _, k := range examKeysForLedgerRow(src, r.Bits, r.T0) {
				t := base
				t.Strategy = k
				out = append(out, t)
			}
		}
	}
	return out
}

// handleStrategyReportAPI GET /api/strategy-report(只读鉴权由路由包)。
func (s *DashboardServer) handleStrategyReportAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	to := now
	if v := strings.TrimSpace(r.URL.Query().Get("from")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t.UTC()
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("to")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t.UTC()
		}
	}
	if to.Before(from) {
		from, to = to, from
	}
	stratFilter := strings.TrimSpace(r.URL.Query().Get("strategy"))

	dir := s.recordDirOrDefault()
	real := loadStratTradesReal(dir, from, to)
	virtual := loadStratTradesVirtual(dir, from, to)

	all := append(append([]stratTrade(nil), real...), virtual...)
	// 聚合表恒展示全量(便于下拉框列出全部策略);曲线与明细按过滤键收窄。
	aggs := append(aggregateStratTrades(real), aggregateStratTrades(virtual)...)
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].Virtual != aggs[j].Virtual {
			return !aggs[i].Virtual
		}
		return aggs[i].Key < aggs[j].Key
	})

	selected := all
	curveUnit := "USDT"
	if stratFilter != "" {
		selected = selected[:0:0]
		for _, t := range all {
			if t.Strategy == stratFilter {
				selected = append(selected, t)
			}
		}
		if len(selected) > 0 && selected[0].Virtual {
			curveUnit = "bps"
		}
	} else {
		selected = real // 无过滤时曲线只画真金合计(双口径纪律:不与影子混算)
	}
	curve := buildStratCurve(selected, 400)

	trades := append([]stratTrade(nil), selected...)
	sort.Slice(trades, func(i, j int) bool { return trades[i].Time.After(trades[j].Time) })
	if len(trades) > 200 {
		trades = trades[:200]
	}

	writeJSON(w, strategyReportResponse{
		From: from, To: to, Strategy: stratFilter,
		Strategies: aggs, Curve: curve, CurveUnit: curveUnit, Trades: trades,
	})
}
