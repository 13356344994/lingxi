package app

// directional_regime_edge_test.go 验证「range 反转单的前向 edge 是否随效率比升高(=正在转趋势)而变负」。
// 直接检验假设:震荡→趋势切换时,range 反转把仓开在了反方向。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalRangeEdgeByEfficiency -v -timeout 600s

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"lingxi/internal/core"
)

func TestDirectionalRangeEdgeByEfficiency(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 90
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	closesByc := map[string][]float64{}
	for _, s := range syms {
		c, e := edgeBTCandles(s, "5m", 300)
		if e == nil && len(c) >= 60 {
			closesByc[s] = c
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Logf("%d 币种 K线", len(closesByc))

	cfg := core.OKXDirectionalConfig{
		SignalWindowBars: 24, TrendEfficiencyThreshold: 0.30,
		EMAFast: 9, EMASlow: 30, ZScoreEntry: 1.2,
		RequireFundingAlignment: false,
		// 关掉防陷阱/回踩闸,纯看「信号方向 × 效率比」的前向 edge。
		SpikeBarPct: 1, TrendMaxExtensionPct: 1, VolExpansionMaxRatio: 1e9, ReversionKnifePct: 1,
		TrendEntryPullbackMaxBarPct: 0,
	}
	win := cfg.SignalWindowBars
	const horizon = 12 // 60min

	type bk struct {
		n, w int
		sum  float64
	}
	buckets := map[string]*bk{}
	add := func(key string, ret float64) {
		b := buckets[key]
		if b == nil {
			b = &bk{}
			buckets[key] = b
		}
		b.n++
		b.sum += ret
		if ret > 0 {
			b.w++
		}
	}
	for _, closes := range closesByc {
		n := len(closes)
		for i := 30; i < n-horizon; i++ {
			w := closes[:i+1]
			sig := computeDirectionalSignal(w, 0, cfg)
			if sig.Side == DirectionFlat {
				continue
			}
			sc := w
			if len(w) > win {
				sc = w[len(w)-win:]
			}
			er := efficiencyRatio(sc)
			band := fmt.Sprintf("%.1f-%.1f", float64(int(er*10))/10, float64(int(er*10)+1)/10)
			ret := (closes[i+horizon] - closes[i]) / closes[i]
			if sig.Side == DirectionShort {
				ret = -ret
			}
			add(sig.Regime.String()+" er"+band, ret)
		}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("\n=== 信号前向edge(+60min) 按 regime×效率比带(n>=20)===\n")
	for _, k := range keys {
		b := buckets[k]
		if b.n < 20 {
			continue
		}
		fmt.Printf("  %-16s n=%-5d 命中%.0f%% 均值%+.3f%%\n", k, b.n, 100*float64(b.w)/float64(b.n), b.sum/float64(b.n)*100)
	}
}
