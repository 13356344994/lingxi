package app

// directional_btc_edge_test.go 验证「逆 BTC 近期方向 的 alt 信号是否前向 edge 为负」。
// 检验根因假设:alts 跟随 BTC,逆 BTC 开仓系统性亏;策略却无 BTC 方向门控。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalBTCAlignmentEdge -v -timeout 600s

import (
	"fmt"
	"os"
	"testing"
	"time"

	"lingxi/internal/core"
)

func TestDirectionalBTCAlignmentEdge(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:EDGE_BT=1 + 外网")
	}
	// BTC 基准 K 线
	btc, err := edgeBTCandles("BTC-USDT-SWAP", "5m", 300)
	if err != nil || len(btc) < 60 {
		t.Fatalf("BTC K线失败: %v len=%d", err, len(btc))
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
		if s == "BTC-USDT-SWAP" {
			continue
		}
		c, e := edgeBTCandles(s, "5m", 300)
		if e == nil && len(c) >= 60 {
			closesByc[s] = c
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Logf("BTC %d根, %d 币种", len(btc), len(closesByc))

	cfg := core.OKXDirectionalConfig{
		SignalWindowBars: 24, TrendEfficiencyThreshold: 0.30,
		EMAFast: 9, EMASlow: 30, ZScoreEntry: 1.2,
		RequireFundingAlignment: false,
		SpikeBarPct: 1, TrendMaxExtensionPct: 1, VolExpansionMaxRatio: 1e9, ReversionKnifePct: 1,
		TrendEntryPullbackMaxBarPct: 0,
	}
	const horizon = 12 // 60min
	const btcLook = 6  // BTC 近30min 方向

	type bk struct {
		n, w int
		sum  float64
	}
	stat := map[string]*bk{}
	add := func(k string, ret float64) {
		b := stat[k]
		if b == nil {
			b = &bk{}
			stat[k] = b
		}
		b.n++
		b.sum += ret
		if ret > 0 {
			b.w++
		}
	}
	// BTC K线与 alt 同为最近 300 根、同周期、近似对齐(同源 OKX 5m)。用末尾对齐索引。
	for _, closes := range closesByc {
		n := len(closes)
		// 对齐:alt 索引 i 对应 btc 索引 bi（两者都 oldest-first，长度可能差几根，用末端对齐)
		off := len(btc) - n
		for i := 30; i < n-horizon; i++ {
			bi := i + off
			if bi-btcLook < 0 || bi >= len(btc) {
				continue
			}
			sig := computeDirectionalSignal(closes[:i+1], 0, cfg)
			if sig.Side == DirectionFlat {
				continue
			}
			btcMove := (btc[bi] - btc[bi-btcLook]) / btc[bi-btcLook]
			btcDir := 0
			if btcMove > 0.0008 {
				btcDir = 1
			} else if btcMove < -0.0008 {
				btcDir = -1
			}
			sigDir := 1
			if sig.Side == DirectionShort {
				sigDir = -1
			}
			ret := (closes[i+horizon] - closes[i]) / closes[i]
			if sig.Side == DirectionShort {
				ret = -ret
			}
			var key string
			if btcDir == 0 {
				key = "BTC横盘"
			} else if btcDir == sigDir {
				key = "顺BTC方向"
			} else {
				key = "逆BTC方向"
			}
			add(key, ret)
		}
	}
	fmt.Printf("\n=== alt信号前向edge(+60min) 按 与BTC近30min方向 的关系 ===\n")
	for _, k := range []string{"顺BTC方向", "逆BTC方向", "BTC横盘"} {
		b := stat[k]
		if b == nil || b.n == 0 {
			continue
		}
		fmt.Printf("  %-10s n=%-5d 命中%.0f%% 均值%+.3f%%\n", k, b.n, 100*float64(b.w)/float64(b.n), b.sum/float64(b.n)*100)
	}
}
