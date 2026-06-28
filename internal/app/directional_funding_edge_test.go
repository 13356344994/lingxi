package app

// directional_funding_edge_test.go 资费 carry edge 离线验证(研究用,默认跳过)。
//
// 假设:资费极正 → 多头拥挤付费给空头 → 做空既「收资费(carry)」又赌「价格均值回归」;
//       资费极负 → 反之做多。检验「按资费反向开仓」的 前向价格收益 + 实收资费 − 成本 是否净正。
//
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalFundingCarryEdge -v -timeout 600s
// 复用 directional_edge_backtest_test.go 的 OKX 公共行情拉取(edgeBTHTTPGet / edgeBTTopSymbols)。

import (
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

type fcCandle struct {
	ts    int64 // 毫秒
	close float64
}

// fcCandles 拉 K 线(带时间戳,oldest-first);bar 由参数指定(5m/1H 等)。
func fcCandles(instID, bar string, limit int) ([]fcCandle, error) {
	var resp struct {
		Data [][]string `json:"data"`
	}
	q := fmt.Sprintf("instId=%s&bar=%s&limit=%d", instID, bar, limit)
	if err := edgeBTHTTPGet("/api/v5/market/candles", q, &resp); err != nil {
		return nil, err
	}
	out := make([]fcCandle, 0, len(resp.Data))
	for i := len(resp.Data) - 1; i >= 0; i-- {
		row := resp.Data[i]
		if len(row) < 5 {
			continue
		}
		var ts int64
		var c float64
		fmt.Sscanf(row[0], "%d", &ts)
		fmt.Sscanf(row[4], "%g", &c)
		if c > 0 {
			out = append(out, fcCandle{ts, c})
		}
	}
	return out, nil
}

type fcFunding struct {
	ts   int64
	rate float64
}

// fcFundingHistory 拉资费历史(每 8h 一条,oldest-first)。
func fcFundingHistory(instID string, limit int) ([]fcFunding, error) {
	var resp struct {
		Data []struct {
			FundingRate string `json:"fundingRate"`
			FundingTime string `json:"fundingTime"`
		} `json:"data"`
	}
	q := fmt.Sprintf("instId=%s&limit=%d", instID, limit)
	if err := edgeBTHTTPGet("/api/v5/public/funding-rate-history", q, &resp); err != nil {
		return nil, err
	}
	out := make([]fcFunding, 0, len(resp.Data))
	for _, d := range resp.Data {
		var ts int64
		var r float64
		fmt.Sscanf(d.FundingTime, "%d", &ts)
		fmt.Sscanf(d.FundingRate, "%g", &r)
		out = append(out, fcFunding{ts, r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ts < out[j].ts })
	return out, nil
}

// rateAt 返回某时刻 ts 生效的资费(最近一次已结算的 ≤ ts)。
func rateAt(funds []fcFunding, ts int64) (float64, bool) {
	lo, hi, idx := 0, len(funds)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if funds[mid].ts <= ts {
			idx = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if idx < 0 {
		return 0, false
	}
	return funds[idx].rate, true
}

func TestDirectionalFundingCarryEdge(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 120
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	lev := 5.0
	bar := "5m"
	barMin := 5
	if v := os.Getenv("EDGE_BT_BAR"); v != "" {
		bar = v
	}
	switch bar {
	case "1H":
		barMin = 60
	case "15m":
		barMin = 15
	case "30m":
		barMin = 30
	}
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	type symData struct {
		candles []fcCandle
		funds   []fcFunding
	}
	data := map[string]symData{}
	for _, s := range syms {
		c, e1 := fcCandles(s, bar, 300)
		if e1 != nil || len(c) < 60 {
			continue
		}
		f, e2 := fcFundingHistory(s, 300)
		if e2 != nil || len(f) == 0 {
			continue
		}
		data[s] = symData{c, f}
		time.Sleep(80 * time.Millisecond)
	}
	t.Logf("有效币种 %d(含资费历史)bar=%s", len(data), bar)

	// 资费极性分桶(按 |当根生效资费| 的绝对值,单位 %/8h)。
	type bucket struct {
		lo, hi float64 // |rate| 区间(小数,如 0.0005=0.05%)
		label  string
	}
	buckets := []bucket{
		{0, 0.0003, "|f|<0.03%"},
		{0.0003, 0.0007, "0.03-0.07%"},
		{0.0007, 0.0015, "0.07-0.15%"},
		{0.0015, 1, "|f|>0.15%"},
	}
	horizons := []int{6, 12, 24} // 5m档:30/60/120min
	if barMin >= 60 {
		horizons = []int{1, 2, 4} // 1H档:1/2/4h
	} else if barMin >= 15 {
		horizons = []int{2, 4, 8}
	}
	legBps := 5.0
	if v := os.Getenv("EDGE_BT_LEGBPS"); v != "" {
		fmt.Sscanf(v, "%g", &legBps)
	}
	rtCostAcct := 2 * legBps / 10000 * lev // 往返成本(账户口径)

	type acc struct {
		nPrice, wPrice         int
		sumPriceAcct           float64 // 仅价格(账户%,carry方向,未扣费)
		sumCarryAcct           float64 // 实收资费(账户%)
		sumNetAcct             float64 // 价格+carry-成本
		wNet                   int
	}
	stats := map[string]map[int]*acc{}
	for _, b := range buckets {
		stats[b.label] = map[int]*acc{}
		for _, h := range horizons {
			stats[b.label][h] = &acc{}
		}
	}

	for _, sd := range data {
		cs := sd.candles
		n := len(cs)
		for i := 0; i < n; i++ {
			rate, ok := rateAt(sd.funds, cs[i].ts)
			if !ok || rate == 0 {
				continue
			}
			af := math.Abs(rate)
			var blab string
			for _, b := range buckets {
				if af >= b.lo && af < b.hi {
					blab = b.label
					break
				}
			}
			if blab == "" {
				continue
			}
			// carry 方向:资费>0 → 做空(收资费);资费<0 → 做多。
			shortDir := rate > 0
			for _, h := range horizons {
				if i+h >= n {
					continue
				}
				priceRet := (cs[i+h].close - cs[i].close) / cs[i].close
				if shortDir {
					priceRet = -priceRet
				}
				priceAcct := priceRet * lev
				// carry 收益:持有 h*barMin 分钟内跨过的结算次数 × |rate|(收方向恒为正)。
				hMin := float64(h * barMin)
				carryFrac := af * (hMin / 480.0) // 近似:按 8h=480min 比例累计
				carryAcct := carryFrac * lev
				netAcct := priceAcct + carryAcct - rtCostAcct
				a := stats[blab][h]
				a.nPrice++
				if priceAcct > 0 {
					a.wPrice++
				}
				a.sumPriceAcct += priceAcct
				a.sumCarryAcct += carryAcct
				a.sumNetAcct += netAcct
				if netAcct > 0 {
					a.wNet++
				}
			}
		}
	}

	fmt.Printf("\n=== 资费 carry edge(按资费方向开仓:f>0做空/f<0做多,账户口径×%.0f,往返成本%.0fbps)===\n", lev, 2*legBps)
	for _, h := range horizons {
		fmt.Printf("\n-- 持有 %d根(%dmin) --\n", h, h*barMin)
		fmt.Printf("  %-12s %-7s %-9s %-10s %-11s %-9s %-6s\n", "资费档", "n", "价格均值%", "carry均值%", "净均值%", "净胜率", "")
		for _, b := range buckets {
			a := stats[b.label][h]
			if a.nPrice == 0 {
				fmt.Printf("  %-12s n=0\n", b.label)
				continue
			}
			pm := a.sumPriceAcct / float64(a.nPrice) * 100
			cm := a.sumCarryAcct / float64(a.nPrice) * 100
			nm := a.sumNetAcct / float64(a.nPrice) * 100
			wr := float64(a.wNet) / float64(a.nPrice) * 100
			flag := ""
			if nm > 0 {
				flag = "← 净正"
			}
			fmt.Printf("  %-12s %-7d %+8.3f %+9.3f %+10.3f %7.0f%% %s\n", b.label, a.nPrice, pm, cm, nm, wr, flag)
		}
	}
}
