package app

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ===== 页面数据③:「此刻谁在给钱」脉搏(2026-07-09 面板极简化)=====
// GET /api/pulse:每个打法的最近 30 笔滚动净值中位/笔数/最后一笔时间/热冷标记,全部 Go 现算。
// 影子侧=particle_ledger.jsonl 按 (module|src) 分组(bps);
// 真金侧=四所 settlements.jsonl 按 strategyKeyForSettlement(fast 按 candidate_source 细分)分组(USDT)。
// 双口径纪律(㉕-补)不变:真金与影子分列,永不混算。

// pulseWindow 滚动窗口笔数(与净值哨兵同思路:只看最近的游戏给不给钱)。
const pulseWindow = 30

// pulseSample 一笔已了结净值样本(单位由来源决定:影子=bps,真金=USDT)。
type pulseSample struct {
	Key  string
	Time time.Time
	Net  float64
}

// pulseGroup 一个打法的脉搏读数。
type pulseGroup struct {
	Key      string    `json:"key"`       // module|src(影子)/ 策略键(真金)
	N        int       `json:"n"`         // 窗口内笔数(≤窗口)
	TotalN   int       `json:"total_n"`   // 全量笔数
	MedNet   float64   `json:"med_net"`   // 最近窗口净值中位(影子 bps / 真金 USDT)
	LastTime time.Time `json:"last_time"` // 最后一笔时间
	Hot      bool      `json:"hot"`       // 中位>0 = 热
}

// pulseGroupsFromSamples 纯函数:样本 → 按 Key 分组,各取时间序最近 window 笔算中位。
// 排序:中位降序(给钱的在前),同中位按 Key 升序,保证输出确定。
func pulseGroupsFromSamples(samples []pulseSample, window int) []pulseGroup {
	byKey := map[string][]pulseSample{}
	for _, s := range samples {
		byKey[s.Key] = append(byKey[s.Key], s)
	}
	out := make([]pulseGroup, 0, len(byKey))
	for key, ss := range byKey {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].Time.Before(ss[j].Time) })
		tail := ss
		if window > 0 && len(tail) > window {
			tail = tail[len(tail)-window:]
		}
		nets := make([]float64, 0, len(tail))
		for _, s := range tail {
			nets = append(nets, s.Net)
		}
		med := medianOf(nets)
		out = append(out, pulseGroup{
			Key: key, N: len(tail), TotalN: len(ss),
			MedNet: med, LastTime: ss[len(ss)-1].Time, Hot: med > 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MedNet != out[j].MedNet {
			return out[i].MedNet > out[j].MedNet
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// pulseLoadShadowSamples 读粒子账本尾部 → 影子样本(bps;unfilled 剔除,与报告 API 同口径)。
func pulseLoadShadowSamples(recordDir string) []pulseSample {
	rows, err := loadTailJSONLFile[fastLedgerRow](filepath.Join(recordDir, "okx_directional", fastParticleLedgerFile), time.Time{}, 0)
	if err != nil {
		return nil
	}
	out := make([]pulseSample, 0, len(rows))
	for _, r := range rows {
		if r.Exit == "unfilled" {
			continue
		}
		module := strings.TrimSpace(r.Module)
		if module == "" {
			module = "e"
		}
		src := strings.TrimSpace(r.Src)
		if src == "" {
			src = "v0"
		}
		out = append(out, pulseSample{Key: module + "|" + src, Time: r.Time, Net: r.NetBps})
	}
	return out
}

// pulseLoadRealSamples 读四所 settlements → 真金样本(USDT,交易所实扣口径)。
func pulseLoadRealSamples(recordDir string) []pulseSample {
	var out []pulseSample
	for _, vn := range directionalVenueNames {
		path := filepath.Join(directionalRecordDir(recordDir, vn), okxDirectionalSettlementsFile)
		recs, err := loadTailJSONLFile[okxDirectionalSettlement](path, time.Time{}, 0)
		if err != nil {
			continue
		}
		for _, rec := range recs {
			out = append(out, pulseSample{
				Key:  strategyKeyForSettlement(vn, rec.Regime, rec.FastCandidateSource),
				Time: rec.Time,
				Net:  rec.RealizedUSDT,
			})
		}
	}
	return out
}

// handlePulseAPI GET /api/pulse(只读鉴权由路由包)。
func (s *DashboardServer) handlePulseAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := s.recordDirOrDefault()
	writeJSON(w, map[string]any{
		"generated_at": time.Now().UTC(),
		"window":       pulseWindow,
		"shadow_bps":   pulseGroupsFromSamples(pulseLoadShadowSamples(dir), pulseWindow),
		"real_usdt":    pulseGroupsFromSamples(pulseLoadRealSamples(dir), pulseWindow),
	})
}
