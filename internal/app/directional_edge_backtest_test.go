package app

// directional_edge_backtest_test.go 离线「方向 edge」验证框架(研究用,默认跳过)。
//
// 目的:在加任何新信号逻辑前,先用历史 K 线量化「生产信号 computeDirectionalSignal 发出后,
// 价格朝信号方向走的前向命中率/期望」是否显著 > 抛硬币。复用生产纯函数,保证测的就是实盘信号。
//
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalEdgeBacktest -v -timeout 600s
// 需要外网访问 OKX 公共行情(无需鉴权)。
//
// 方法:对成交量 top-N 的 USDT 永续,各拉 300 根 K 线;在每根 K 上跑 computeDirectionalSignal,
// 信号非空则记录其后 +h 根的前向收益(按信号方向取符号)。聚合命中率(净>0 占比)、均值、期望。
// 与「无条件方向命中率」(同一批样本里价格上行占比)对比——信号命中率须显著高于该基线才算有 edge。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
)

const okxPublicBase = "https://www.okx.com"

type edgeBTHorizon struct {
	bars int
	// 信号方向前向收益样本(已按方向取符号,正=朝信号方向走)。
	signalRets []float64
	// 无条件「下一段上行」基线样本(用于对比:long-always 的命中率)。
	upRets []float64
}

func edgeBTHTTPGet(path string, q string, out any) error {
	url := okxPublicBase + path
	if q != "" {
		url += "?" + q
	}
	cli := &http.Client{Timeout: 20 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// edgeBTTopSymbols 取成交额 top-N 的 USDT 永续 instId(去稳定币/极小盘由量自然过滤)。
func edgeBTTopSymbols(n int) ([]string, error) {
	var resp struct {
		Data []struct {
			InstID   string `json:"instId"`
			VolCcy24 string `json:"volCcy24h"`
		} `json:"data"`
	}
	if err := edgeBTHTTPGet("/api/v5/market/tickers", "instType=SWAP", &resp); err != nil {
		return nil, err
	}
	type sv struct {
		id  string
		vol float64
	}
	var list []sv
	for _, d := range resp.Data {
		if !strings.HasSuffix(d.InstID, "-USDT-SWAP") {
			continue
		}
		var v float64
		fmt.Sscanf(d.VolCcy24, "%g", &v)
		list = append(list, sv{d.InstID, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].vol > list[j].vol })
	out := make([]string, 0, n)
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, list[i].id)
	}
	return out, nil
}

func edgeBTCandles(instID, bar string, limit int) ([]float64, error) {
	var resp struct {
		Data [][]string `json:"data"`
	}
	q := fmt.Sprintf("instId=%s&bar=%s&limit=%d", instID, bar, limit)
	if err := edgeBTHTTPGet("/api/v5/market/candles", q, &resp); err != nil {
		return nil, err
	}
	// OKX newest-first → 转 oldest-first 收盘序列。
	closes := make([]float64, 0, len(resp.Data))
	for i := len(resp.Data) - 1; i >= 0; i-- {
		row := resp.Data[i]
		if len(row) < 5 {
			continue
		}
		var c float64
		fmt.Sscanf(row[4], "%g", &c)
		if c > 0 {
			closes = append(closes, c)
		}
	}
	return closes, nil
}

func edgeBTReport(label string, h *edgeBTHorizon, barMin int) {
	hit := func(xs []float64) (float64, float64) {
		if len(xs) == 0 {
			return 0, 0
		}
		w, sum := 0, 0.0
		for _, x := range xs {
			if x > 0 {
				w++
			}
			sum += x
		}
		return float64(w) / float64(len(xs)) * 100, sum / float64(len(xs)) * 100
	}
	sigHit, sigMean := hit(h.signalRets)
	baseHit, baseMean := hit(h.upRets)
	fmt.Printf("  [%s] +%d根(%d分): 信号 n=%d 命中=%.1f%% 均值=%+.3f%% | 基线(无条件上行) 命中=%.1f%% 均值=%+.3f%% | edge=%+.1f%%\n",
		label, h.bars, h.bars*barMin, len(h.signalRets), sigHit, sigMean, baseHit, baseMean, sigHit-baseHit)
}

func edgeBTRunConfig(t *testing.T, label string, cfg core.OKXDirectionalConfig, symbolCloses map[string][]float64, horizons []int, barMin int) {
	hs := make(map[int]*edgeBTHorizon)
	for _, hb := range horizons {
		hs[hb] = &edgeBTHorizon{bars: hb}
	}
	warm := cfg.SignalWindowBars
	if warm < cfg.EMASlow {
		warm = cfg.EMASlow
	}
	if warm < 30 {
		warm = 30
	}
	for _, closes := range symbolCloses {
		n := len(closes)
		for i := warm; i < n; i++ {
			win := closes[:i+1]
			sig := computeDirectionalSignal(win, 0, cfg) // funding=0:对齐闸已关,纯测价格信号
			for _, hb := range horizons {
				if i+hb >= n {
					continue
				}
				ret := (closes[i+hb] - closes[i]) / closes[i]
				// 基线:无条件 long(同样本)。
				hs[hb].upRets = append(hs[hb].upRets, ret)
				if sig.Side == DirectionLong {
					hs[hb].signalRets = append(hs[hb].signalRets, ret)
				} else if sig.Side == DirectionShort {
					hs[hb].signalRets = append(hs[hb].signalRets, -ret)
				}
			}
		}
	}
	fmt.Printf("== 配置: %s ==\n", label)
	for _, hb := range horizons {
		edgeBTReport(label, hs[hb], barMin)
	}
}

func TestDirectionalEdgeBacktest(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	bar := "5m"
	if v := os.Getenv("EDGE_BT_BAR"); v != "" {
		bar = v
	}
	barMin := 5
	switch bar {
	case "15m":
		barMin = 15
	case "1H":
		barMin = 60
	}
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("取 symbols 失败: %v", err)
	}
	t.Logf("拉取 %d 个 USDT 永续 %s K线…", len(syms), bar)
	symbolCloses := make(map[string][]float64)
	for _, s := range syms {
		c, err := edgeBTCandles(s, bar, 300)
		if err != nil || len(c) < 60 {
			continue
		}
		symbolCloses[s] = c
		time.Sleep(60 * time.Millisecond) // 轻节流,避免被限频
	}
	t.Logf("成功 %d 个币种有效 K线", len(symbolCloses))

	horizons := []int{6, 12, 24}

	// 生产配置(与 config.yaml 对齐;含趋势回踩 + 防陷阱闸,对齐闸关以纯测价格信号)。
	prod := core.OKXDirectionalConfig{
		SignalWindowBars: 24, TrendEfficiencyThreshold: 0.30,
		EMAFast: 9, EMASlow: 30, ZScoreEntry: 1.2,
		RequireFundingAlignment:     false,
		SpikeBarPct:                 0.03,
		TrendMaxExtensionPct:        0.02,
		VolExpansionMaxRatio:        1.8,
		ReversionKnifePct:           0.015,
		TrendEntryPullbackMaxBarPct: 0.005,
	}
	edgeBTRunConfig(t, "生产信号(趋势顺势+震荡反转,含回踩/防陷阱闸)", prod, symbolCloses, horizons, barMin)

	// 裸信号(关闭所有防陷阱/回踩闸,看原始信号方向预测力)。
	raw := prod
	raw.SpikeBarPct = 1
	raw.TrendMaxExtensionPct = 1
	raw.VolExpansionMaxRatio = 1e9
	raw.ReversionKnifePct = 1
	raw.TrendEntryPullbackMaxBarPct = 0
	edgeBTRunConfig(t, "裸信号(无防陷阱闸)", raw, symbolCloses, horizons, barMin)

	// 信心分桶:只看不同 Conviction 档的前向 edge,扣往返成本后是否仍 > 0。
	// 这是把薄 edge 做厚的关键实验——若仅最高信心档扣费后为正,则应只做该档。
	legBps := 5.0
	if v := os.Getenv("EDGE_BT_LEGBPS"); v != "" {
		fmt.Sscanf(v, "%g", &legBps)
	}
	edgeBTConvictionBuckets(t, prod, symbolCloses, 12, barMin, legBps)
}

// edgeBTConvictionBuckets 按信心档统计:对 12 根(≈MaxHold)前向收益,分桶 [0,.3)/[.3,.5)/[.5,.7)/[.7,1],
// 报告每桶 n/命中率/均值,以及「扣往返成本(2×legBps)后的净均值」——净>0 才是真 edge。
func edgeBTConvictionBuckets(t *testing.T, cfg core.OKXDirectionalConfig, symbolCloses map[string][]float64, hb, barMin int, legBps float64) {
	type bucket struct {
		lo, hi   float64
		rets     []float64
	}
	bks := []*bucket{{0, 0.3, nil}, {0.3, 0.5, nil}, {0.5, 0.7, nil}, {0.7, 1.01, nil}}
	warm := cfg.SignalWindowBars
	if warm < cfg.EMASlow {
		warm = cfg.EMASlow
	}
	if warm < 30 {
		warm = 30
	}
	for _, closes := range symbolCloses {
		n := len(closes)
		for i := warm; i < n-hb; i++ {
			sig := computeDirectionalSignal(closes[:i+1], 0, cfg)
			if sig.Side == DirectionFlat {
				continue
			}
			ret := (closes[i+hb] - closes[i]) / closes[i]
			if sig.Side == DirectionShort {
				ret = -ret
			}
			for _, b := range bks {
				if sig.Conviction >= b.lo && sig.Conviction < b.hi {
					b.rets = append(b.rets, ret)
					break
				}
			}
		}
	}
	rtCostFrac := 2 * legBps / 10000 // 往返成本(名义比例)
	fmt.Printf("== 信心分桶 edge(+%d根/%d分,往返成本=%.0fbps)==\n", hb, hb*barMin, 2*legBps)
	for _, b := range bks {
		if len(b.rets) == 0 {
			fmt.Printf("  信心[%.1f,%.1f): n=0\n", b.lo, b.hi)
			continue
		}
		w, sum := 0, 0.0
		for _, x := range b.rets {
			if x > 0 {
				w++
			}
			sum += x
		}
		mean := sum / float64(len(b.rets))
		netMean := mean - rtCostFrac
		fmt.Printf("  信心[%.1f,%.1f): n=%d 命中=%.1f%% 毛均值=%+.3f%% 扣费净均值=%+.3f%% %s\n",
			b.lo, b.hi, len(b.rets), float64(w)/float64(len(b.rets))*100, mean*100, netMean*100,
			map[bool]string{true: "← 正 edge", false: ""}[netMean > 0])
	}
}
