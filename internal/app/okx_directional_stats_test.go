package app

import (
	"math"
	"testing"
	"time"
)

func TestDirectionalReasonKey(t *testing.T) {
	got := directionalReasonKey("快进快出止盈(浮盈12%本金,吃到就跑)")
	if got != "快进快出止盈" {
		t.Fatalf("got %q", got)
	}
	got = directionalReasonKey("移动保盈(峰值20%回吐至5%)")
	if got != "移动保盈" {
		t.Fatalf("got %q", got)
	}
	if directionalReasonKey("") != "未知" {
		t.Fatal("empty reason should be 未知")
	}
}

func TestComputeDirectionalStats(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	settles := []okxDirectionalSettlement{
		{Time: base, Reason: "快进快出止盈(浮盈10%本金,吃到就跑)", Regime: "trend", RealizedUSDT: 2.5},
		{Time: base.Add(time.Hour), Reason: "快进快出止盈(浮盈8%本金,吃到就跑)", Regime: "trend", RealizedUSDT: 1.0},
		{Time: base.Add(2 * time.Hour), Reason: "硬止损", Regime: "range", RealizedUSDT: -3.0},
		{Time: base.Add(3 * time.Hour), Reason: "超时离场", Regime: "range", RealizedUSDT: -1.0},
		{Time: base.Add(4 * time.Hour), Reason: "信号反转", Regime: "trend", RealizedUSDT: 0.5},
	}

	all := computeDirectionalStats(settles, time.Time{})
	if all.TradeCount != 5 {
		t.Fatalf("trade_count=%d want 5", all.TradeCount)
	}
	if all.Wins != 3 {
		t.Fatalf("wins=%d want 3", all.Wins)
	}
	if math.Abs(all.NetUSDT-0.0) > 1e-9 {
		t.Fatalf("net=%v want 0", all.NetUSDT)
	}
	if math.Abs(all.WinRate-0.6) > 1e-9 {
		t.Fatalf("win_rate=%v want 0.6", all.WinRate)
	}
	if math.Abs(all.AvgWinUSDT-(4.0/3.0)) > 1e-9 {
		t.Fatalf("avg_win=%v", all.AvgWinUSDT)
	}
	if math.Abs(all.AvgLossUSDT-(-2.0)) > 1e-9 {
		t.Fatalf("avg_loss=%v want -2", all.AvgLossUSDT)
	}

	if len(all.ByReason) < 3 {
		t.Fatalf("by_reason len=%d", len(all.ByReason))
	}
	if all.ByReason[0].Reason != "硬止损" || math.Abs(all.ByReason[0].NetUSDT+3.0) > 1e-9 {
		t.Fatalf("worst reason=%+v", all.ByReason[0])
	}
	if all.ByReason[len(all.ByReason)-1].Reason != "快进快出止盈" {
		t.Fatalf("best reason=%+v", all.ByReason[len(all.ByReason)-1])
	}
	for i := 1; i < len(all.ByReason); i++ {
		if all.ByReason[i].NetUSDT < all.ByReason[i-1].NetUSDT {
			t.Fatalf("by_reason not sorted ascending at %d", i)
		}
	}

	dayStart := beijingCSTDayStart(base)
	today := computeDirectionalStats(settles, dayStart)
	if today.TradeCount != 5 {
		t.Fatalf("today trade_count=%d want 5", today.TradeCount)
	}

	futureDay := computeDirectionalStats(settles, beijingCSTDayStart(base.Add(48*time.Hour)))
	if futureDay.TradeCount != 0 {
		t.Fatalf("future since should be empty, got %d", futureDay.TradeCount)
	}
}

// TestComputeDirectionalStatsPositionLevel 验证仓位级聚合:
// 同一 PositionID 的部分平 + 全平 合并成 1 仓;无 PositionID 的条目回退 symbol|side|entry 键。
func TestComputeDirectionalStatsPositionLevel(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	settles := []okxDirectionalSettlement{
		// 仓位 A: 部分止盈(2.0) + 全平止损(-5.0) → 合计 -3.0 (亏仓)
		{Time: base, Symbol: "BTC", Side: "long", Entry: 60000, PositionID: "pos-A", Reason: "部分止盈(部分)", RealizedUSDT: 2.0},
		{Time: base.Add(time.Hour), Symbol: "BTC", Side: "long", Entry: 60000, PositionID: "pos-A", Reason: "硬止损", RealizedUSDT: -5.0},
		// 仓位 B: 单次全平盈利 3.0 (盈仓)
		{Time: base.Add(2 * time.Hour), Symbol: "ETH", Side: "long", Entry: 3000, PositionID: "pos-B", Reason: "快进快出止盈", RealizedUSDT: 3.0},
		// 无 PositionID:回退键;两条相同 symbol|side|entry → 同一仓位 net=1.0 (盈仓)
		{Time: base.Add(3 * time.Hour), Symbol: "SOL", Side: "short", Entry: 100.0, Reason: "部分(部分)", RealizedUSDT: 0.5},
		{Time: base.Add(4 * time.Hour), Symbol: "SOL", Side: "short", Entry: 100.0, Reason: "超时离场", RealizedUSDT: 0.5},
	}

	st := computeDirectionalStats(settles, time.Time{})

	// 事件级保持不变(5 条)
	if st.TradeCount != 5 {
		t.Fatalf("trade_count=%d want 5", st.TradeCount)
	}

	// 仓位级:3 仓(A=亏, B=盈, SOL=盈)
	if st.PositionCount != 3 {
		t.Fatalf("position_count=%d want 3", st.PositionCount)
	}
	if st.PositionWins != 2 {
		t.Fatalf("position_wins=%d want 2", st.PositionWins)
	}
	if math.Abs(st.PositionWinRate-2.0/3.0) > 1e-9 {
		t.Fatalf("position_win_rate=%v want %.6f", st.PositionWinRate, 2.0/3.0)
	}
	// 盈仓合计 3.0+1.0=4.0;亏仓合计 -3.0 → PF=4/3
	wantPF := 4.0 / 3.0
	if math.Abs(st.PositionProfitFactor-wantPF) > 1e-9 {
		t.Fatalf("position_profit_factor=%v want %v", st.PositionProfitFactor, wantPF)
	}
	if math.Abs(st.PositionMaxLossUSDT-(-3.0)) > 1e-9 {
		t.Fatalf("position_max_loss=%v want -3", st.PositionMaxLossUSDT)
	}
	wantAvgWin := (3.0 + 1.0) / 2.0
	if math.Abs(st.PositionAvgWinUSDT-wantAvgWin) > 1e-9 {
		t.Fatalf("position_avg_win=%v want %v", st.PositionAvgWinUSDT, wantAvgWin)
	}
}
