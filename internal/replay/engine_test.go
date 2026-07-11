package replay

import (
	"math"
	"testing"
	"time"
)

var testBase = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

func almostEqual(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestReplayTradeFilledWithFeeFundingSlippage(t *testing.T) {
	gross := 10.0
	r := ReplayTrade(Trade{
		OpenTime: testBase, CloseTime: testBase.Add(time.Minute),
		Symbol: "BTC-USDT-SWAP", Side: SideLong, Style: ExecTaker,
		EntryMid: 100, EntryPrice: 100.1, ExitMid: 101.2, ExitPrice: 101,
		NotionalUSDT: 1000, FillRatio: 1,
		FeeUSDT: 1.2, FundingUSDT: 0.3, GrossPnLUSDT: &gross,
	})
	if r.Unfilled {
		t.Fatalf("成交交易不应标记 Unfilled")
	}
	if r.GrossUSDT != 10 {
		t.Fatalf("GrossPnLUSDT 覆盖应生效: got %v", r.GrossUSDT)
	}
	if !almostEqual(r.NetUSDT, 8.5, 1e-9) {
		t.Fatalf("net = gross - fee - funding = 8.5, got %v", r.NetUSDT)
	}
	if !almostEqual(r.EntrySlipUSDT, 1.0, 1e-9) {
		t.Fatalf("入场滑点应为 1.0 USDT(10bps×1000×0.1),got %v", r.EntrySlipUSDT)
	}
	wantExitSlip := (101.2 - 101.0) / 101.2 * 1000
	if !almostEqual(r.ExitSlipUSDT, wantExitSlip, 1e-9) {
		t.Fatalf("出场滑点 want %v got %v", wantExitSlip, r.ExitSlipUSDT)
	}
}

func TestReplayTradePartialFill(t *testing.T) {
	r := ReplayTrade(Trade{
		Side: SideLong, EntryPrice: 100, ExitPrice: 102,
		NotionalUSDT: 1000, FillRatio: 0.5,
	})
	if !almostEqual(r.GrossUSDT, 10, 1e-9) {
		t.Fatalf("部分成交按比例名义计 PnL:want 10 got %v", r.GrossUSDT)
	}
	if !almostEqual(r.NetUSDT, 10, 1e-9) {
		t.Fatalf("无费用时 net=gross,got %v", r.NetUSDT)
	}
}

func TestReplayTradeShortSideDerivedPnL(t *testing.T) {
	r := ReplayTrade(Trade{
		Side: SideShort, EntryPrice: 100, ExitPrice: 98,
		NotionalUSDT: 500, FillRatio: 1, FeeUSDT: 0.5,
	})
	// 空头价格下跌 2% × 500 = +10 毛利。
	if !almostEqual(r.GrossUSDT, 10, 1e-9) {
		t.Fatalf("空头毛利 want 10 got %v", r.GrossUSDT)
	}
	if !almostEqual(r.NetUSDT, 9.5, 1e-9) {
		t.Fatalf("net want 9.5 got %v", r.NetUSDT)
	}
}

func TestReplayTradeUnfilledCounterfactual(t *testing.T) {
	r := ReplayTrade(Trade{
		Side: SideLong, EntryMid: 100, ExitMid: 103,
		NotionalUSDT: 500, FillRatio: 0, FeeUSDT: 99, // 未成交不应扣费
	})
	if !r.Unfilled {
		t.Fatalf("FillRatio=0 应标记 Unfilled")
	}
	if r.NetUSDT != 0 || r.FeeUSDT != 0 {
		t.Fatalf("未成交净 PnL 与费用都应为 0: net=%v fee=%v", r.NetUSDT, r.FeeUSDT)
	}
	if !almostEqual(r.CounterfactualGrossUSDT, 15, 1e-9) {
		t.Fatalf("未成交反事实 mid-to-mid 应为 +15,got %v", r.CounterfactualGrossUSDT)
	}
}

func TestBuildEquityPathAndMetrics(t *testing.T) {
	results := []TradeResult{
		{Trade: Trade{CloseTime: testBase.Add(1 * time.Minute)}, NetUSDT: 10},
		{Trade: Trade{CloseTime: testBase.Add(2 * time.Minute)}, NetUSDT: -30},
		{Trade: Trade{CloseTime: testBase.Add(3 * time.Minute)}, NetUSDT: 5},
	}
	path := BuildEquityPath(100, results)
	if len(path) != 4 {
		t.Fatalf("路径点数 want 4 got %d", len(path))
	}
	m := ComputePathMetrics(path)
	if !almostEqual(m.NetUSDT, -15, 1e-9) {
		t.Fatalf("净 USDT want -15 got %v", m.NetUSDT)
	}
	if !almostEqual(m.LogGrowth, math.Log(0.85), 1e-12) {
		t.Fatalf("log growth want ln(0.85) got %v", m.LogGrowth)
	}
	wantDD := (110.0 - 80.0) / 110.0
	if !almostEqual(m.MaxDrawdownPct, wantDD, 1e-12) {
		t.Fatalf("最大回撤 want %v got %v", wantDD, m.MaxDrawdownPct)
	}
	if m.Ruined {
		t.Fatalf("未触及 0 不应判破产")
	}
}

func TestComputePathMetricsRuin(t *testing.T) {
	results := []TradeResult{{Trade: Trade{CloseTime: testBase}, NetUSDT: -12}}
	m := ComputePathMetrics(BuildEquityPath(10, results))
	if !m.Ruined {
		t.Fatalf("权益 -2 应判破产")
	}
	if !math.IsInf(m.LogGrowth, -1) {
		t.Fatalf("破产路径 log growth 应为 -Inf,got %v", m.LogGrowth)
	}
}

func TestGroupEpisodesExplicitClusterAndSolo(t *testing.T) {
	mk := func(open time.Time, net float64, episodeID, cluster string, unfilled bool) TradeResult {
		fill := 1.0
		if unfilled {
			fill = 0
		}
		return TradeResult{
			Trade:    Trade{OpenTime: open, CloseTime: open.Add(time.Minute), EpisodeID: episodeID, Cluster: cluster, FillRatio: fill},
			NetUSDT:  net,
			Unfilled: unfilled,
		}
	}
	results := []TradeResult{
		// 显式 episode:同一强平事件两笔。
		mk(testBase, -5, "liq_evt_1", "btc_liq", false),
		mk(testBase.Add(2*time.Minute), -7, "liq_evt_1", "btc_liq", false),
		// 同簇链式相邻(间隔 5m ≤ 窗口 10m)→ 同一 episode;第三笔间隔 30m → 新 episode。
		mk(testBase.Add(time.Hour), 3, "", "eth_liq", false),
		mk(testBase.Add(time.Hour+5*time.Minute), 4, "", "eth_liq", true),
		mk(testBase.Add(time.Hour+35*time.Minute), 8, "", "eth_liq", false),
		// 无 ID 无簇 → 独立。
		mk(testBase.Add(2*time.Hour), 1, "", "", false),
	}
	eps := GroupEpisodes(results, 10*time.Minute)
	if len(eps) != 4 {
		t.Fatalf("episode 数 want 4 got %d: %+v", len(eps), eps)
	}
	if eps[0].Key != "id:liq_evt_1" || !almostEqual(eps[0].NetUSDT, -12, 1e-9) || eps[0].Trades != 2 {
		t.Fatalf("显式 episode 聚合错误: %+v", eps[0])
	}
	if eps[1].Trades != 2 || !almostEqual(eps[1].NetUSDT, 7, 1e-9) || eps[1].Unfilled != 1 {
		t.Fatalf("链式簇 episode 聚合错误: %+v", eps[1])
	}
	if eps[2].Trades != 1 || !almostEqual(eps[2].NetUSDT, 8, 1e-9) {
		t.Fatalf("超窗应开新 episode: %+v", eps[2])
	}
	if eps[3].Trades != 1 || !almostEqual(eps[3].NetUSDT, 1, 1e-9) {
		t.Fatalf("无簇交易应独立成 episode: %+v", eps[3])
	}
}

func TestEpisodeCVaR(t *testing.T) {
	nets := []float64{-100, -50, 10, 20, 30, 40, 50, 60, 70, 80}
	eps := make([]Episode, len(nets))
	for i, v := range nets {
		eps[i] = Episode{NetUSDT: v}
	}
	if got := EpisodeCVaR(eps, 0.2); !almostEqual(got, 75, 1e-9) {
		t.Fatalf("alpha=0.2 取最差 2 个均值 75,got %v", got)
	}
	if got := EpisodeCVaR(eps, 0.05); !almostEqual(got, 100, 1e-9) {
		t.Fatalf("alpha=0.05 取最差 1 个 100,got %v", got)
	}
	if got := EpisodeCVaR(nil, 0.05); got != 0 {
		t.Fatalf("空样本 CVaR 应为 0,got %v", got)
	}
}
