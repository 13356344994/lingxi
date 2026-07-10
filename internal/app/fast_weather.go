package app

// 天气格引擎(2026-07-10;2 号天气学 v1 报告 weather_v1_report.md 的盘上实时化)。
// 8 格 = riot×breadth×volx(bit2/bit1/bit0),切点冻结自 2 号挖掘集(改=作弊,护栏锚定):
//   CUT_RIOT=0.1611(全市场 1h |ret|≥100bps 占比)
//   CUT_BREADTH=-0.01538(全市场 1h 涨占比−跌占比)
//   CUT_VOLX=1.039(全市场 24h 量 / 其 14 天中位)
// 用途(预注册,record-only):casc_fade 目标格5(暴动·跌·放量 riot高∧breadth低∧volx高)、
//   sqz_fade 目标死水·跌格;实时打标落 shadow 当对照组,营业收窄行为由 config 开关控制(默认关)。
// 数据源:60min 全市场 mid 环(mid 标尺循环喂)+ 全市场 24h 量的 14 天小时中位(持久化)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	fastWxCutRiot    = 0.1611
	fastWxCutBreadth = -0.01538
	fastWxCutVolx    = 1.039
)

// ---- 60min 全市场 mid 环(riot/breadth 的数据地板;15s 降采样,每币 ≤240 点)----
var (
	fastWx60Mu   sync.Mutex
	fastWx60Hist = map[string][]fastMidPoint{}
	fastWx60Last = map[string]time.Time{}
)

func fastWxNote60(sym string, now time.Time, mid float64) {
	fastWx60Mu.Lock()
	defer fastWx60Mu.Unlock()
	if last, ok := fastWx60Last[sym]; ok && now.Sub(last) < 15*time.Second {
		return
	}
	fastWx60Last[sym] = now
	h := append(fastWx60Hist[sym], fastMidPoint{now, mid})
	cut := now.Add(-3900 * time.Second) // 65min 留余量
	i := 0
	for i < len(h) && h[i].ts.Before(cut) {
		i++
	}
	fastWx60Hist[sym] = h[i:]
	if len(fastWx60Hist) > 4096 {
		for k, v := range fastWx60Hist {
			if len(v) == 0 || now.Sub(v[len(v)-1].ts) > time.Hour {
				delete(fastWx60Hist, k)
				delete(fastWx60Last, k)
			}
		}
	}
}

// fastWxRiotBreadth 全市场 1h riot(|ret|≥100bps 占比)与 breadth(涨占比−跌占比);n<20 → ok=false。
func fastWxRiotBreadth(now time.Time) (riot, breadth float64, ok bool) {
	fastWx60Mu.Lock()
	defer fastWx60Mu.Unlock()
	n, big, up, dn := 0, 0, 0, 0
	for _, h := range fastWx60Hist {
		if len(h) < 2 {
			continue
		}
		old := h[0]
		newest := h[len(h)-1]
		if newest.ts.Sub(old.ts) < 3000*time.Second || old.mid <= 0 { // 至少 50min 跨度
			continue
		}
		r := (newest.mid/old.mid - 1) * 1e4
		n++
		if r >= 100 || r <= -100 {
			big++
		}
		if r > 0 {
			up++
		} else if r < 0 {
			dn++
		}
	}
	if n < 20 {
		return 0, 0, false
	}
	fn := float64(n)
	return float64(big) / fn, float64(up)/fn - float64(dn)/fn, true
}

// ---- volx:全市场 24h 量 / 14 天小时中位(持久化小时序列)----
const fastWxVolSeriesFile = "fast_wx_volseries.json"

var (
	fastWxVolMu     sync.Mutex
	fastWxVolSeries []float64 // 每小时一点:全市场 24h 量(volCcy24h 之和);保留 ~14 天=336 点
	fastWxVolHour   int64     // 最后写入的小时(unix/3600)
	fastWxVolNow    float64   // 最新 24h 量
	fastWxVolLoaded bool
	fastWxVolPollAt time.Time // 全市场量轮询节流(标尺循环单 goroutine 访问)
)

func (e *OKXDirectionalEngine) fastWxVolPath() string {
	return filepath.Join(e.recordDir, fastWxVolSeriesFile)
}

func (e *OKXDirectionalEngine) fastWxVolLoad() {
	if fastWxVolLoaded {
		return
	}
	fastWxVolLoaded = true
	if data, err := os.ReadFile(e.fastWxVolPath()); err == nil {
		var st struct {
			Series []float64 `json:"series"`
			Hour   int64     `json:"hour"`
		}
		if json.Unmarshal(data, &st) == nil {
			fastWxVolSeries = st.Series
			fastWxVolHour = st.Hour
		}
	}
}

// fastWxNoteVol 全市场 24h 量入小时序列(标尺循环周期性调用;每整点滚一格)。
func (e *OKXDirectionalEngine) fastWxNoteVol(now time.Time, totalVol24h float64) {
	if totalVol24h <= 0 {
		return
	}
	fastWxVolMu.Lock()
	defer fastWxVolMu.Unlock()
	e.fastWxVolLoad()
	fastWxVolNow = totalVol24h
	hr := now.Unix() / 3600
	if hr != fastWxVolHour {
		fastWxVolHour = hr
		fastWxVolSeries = append(fastWxVolSeries, totalVol24h)
		if len(fastWxVolSeries) > 400 {
			fastWxVolSeries = fastWxVolSeries[len(fastWxVolSeries)-400:]
		}
		_ = writeJSONFile(e.fastWxVolPath(), struct {
			Series []float64 `json:"series"`
			Hour   int64     `json:"hour"`
		}{fastWxVolSeries, fastWxVolHour})
	}
}

// fastWxVolx 当前 volx=最新24h量/14天(336h)中位;点数<72(3天) → ok=false。
func fastWxVolx() (float64, bool) {
	fastWxVolMu.Lock()
	defer fastWxVolMu.Unlock()
	if fastWxVolNow <= 0 || len(fastWxVolSeries) < 72 {
		return 0, false
	}
	cp := append([]float64(nil), fastWxVolSeries...)
	if len(cp) > 336 {
		cp = cp[len(cp)-336:]
	}
	sort.Float64s(cp)
	med := cp[len(cp)/2]
	if med <= 0 {
		return 0, false
	}
	return fastWxVolNow / med, true
}

// fastWeatherGrid 纯判定:8 格标签(bit2 riot高/bit1 breadth高/bit0 volx高);ok=false=某轴不可算。
// 与 2 号 weather_v2.py lab8 口径逐位一致(切点同源冻结)。
func fastWeatherGrid(riot, breadth, volx float64, riotOK, volxOK bool) (int, bool) {
	if !riotOK || !volxOK {
		return -1, false
	}
	g := 0
	if riot > fastWxCutRiot {
		g += 4
	}
	if breadth > fastWxCutBreadth {
		g += 2
	}
	if volx > fastWxCutVolx {
		g += 1
	}
	return g, true
}

// 格常量(2 号命名):格5=暴动·跌·放量(riot高∧breadth低∧volx高)=casc_fade 营业格;
// 死水·跌两格=格0(riot低∧breadth低∧volx低)/格2? 报告 P2 好格=死水·跌(riot低∧breadth低)不分 volx。
const (
	fastWxGrid5CascFade = 5 // riot高 breadth低 volx高
)

// fastWxGridNow 引擎侧:算当前全市场天气格(record-only 打标与营业判定共用)。
func (e *OKXDirectionalEngine) fastWxGridNow(now time.Time) (grid int, riot, breadth, volx float64, ok bool) {
	riot, breadth, rok := fastWxRiotBreadth(now)
	volx, vok := fastWxVolx()
	grid, ok = fastWeatherGrid(riot, breadth, volx, rok, vok)
	return
}

// fastWxCascFadeInGrid casc_fade 营业格判定:格5(暴动·跌·放量)。
func fastWxCascFadeInGrid(grid int, ok bool) bool { return ok && grid == fastWxGrid5CascFade }

// fastWxSqzFadeInGrid sqz_fade 营业格判定:死水·跌(riot低 ∧ breadth低,不看 volx)=格0/格1。
func fastWxSqzFadeInGrid(grid int, ok bool) bool { return ok && (grid == 0 || grid == 1) }
