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
