package app

import (
	"testing"
	"time"
)

// TestStrategyKeyForSettlement 策略键派生:fast 按 candidate_source 细分,慢线按 venue。
func TestStrategyKeyForSettlement(t *testing.T) {
	cases := []struct {
		venue, regime, src, want string
	}{
		{"okx", "fast", "", "fast_scorer"},
		{"okx", "fast", "liq_follow", "fast_liq_follow"},
		{"okx", "fast", "xgap_rev", "fast_xgap_rev"},
		{"bybit", "", "", "slow_bybit"},
		{"OKX", "slow", "", "slow_okx"},
	}
	for _, c := range cases {
		if got := strategyKeyForSettlement(c.venue, c.regime, c.src); got != c.want {
			t.Fatalf("(%s,%s,%s) → %s want %s", c.venue, c.regime, c.src, got, c.want)
		}
	}
}

// TestAggregateStratTrades 聚合数学:胜率/净和/中位/PF/最大回撤。
func TestAggregateStratTrades(t *testing.T) {
	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	trades := []stratTrade{
		{Time: base.Add(1 * time.Minute), Strategy: "s", Net: +10},
		{Time: base.Add(2 * time.Minute), Strategy: "s", Net: -4},
		{Time: base.Add(3 * time.Minute), Strategy: "s", Net: -6},
		{Time: base.Add(4 * time.Minute), Strategy: "s", Net: +8},
	}
	aggs := aggregateStratTrades(trades)
	if len(aggs) != 1 {
		t.Fatalf("应 1 个策略,得 %d", len(aggs))
	}
	a := aggs[0]
	if a.N != 4 || a.WinRate != 0.5 {
		t.Fatalf("n/胜率错: %d/%.2f", a.N, a.WinRate)
	}
	if a.NetSum != 8 {
		t.Fatalf("净和应 +8,得 %.1f", a.NetSum)
	}
	if a.PF != 1.8 { // 18/10
		t.Fatalf("PF 应 1.8,得 %.2f", a.PF)
	}
	// 曲线:10→6→0→8;峰10,谷0 → maxDD=10
	if a.MaxDD != 10 {
		t.Fatalf("maxDD 应 10,得 %.1f", a.MaxDD)
	}
	// 中位:[-6,-4,8,10] 偶数取中两均值 = 2
	if a.NetMed != 2 {
		t.Fatalf("中位应 2,得 %.1f", a.NetMed)
	}
}

// TestBuildStratCurve 曲线:升序累计 + 降采样保尾点。
func TestBuildStratCurve(t *testing.T) {
	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	var trades []stratTrade
	for i := 0; i < 1000; i++ {
		trades = append(trades, stratTrade{Time: base.Add(time.Duration(i) * time.Second), Strategy: "s", Net: 1})
	}
	pts := buildStratCurve(trades, 400)
	if len(pts) != 400 {
		t.Fatalf("应降采样到 400,得 %d", len(pts))
	}
	if pts[len(pts)-1].Cum != 1000 {
		t.Fatalf("尾点累计应 1000,得 %.0f", pts[len(pts)-1].Cum)
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].Cum < pts[i-1].Cum {
			t.Fatal("全正收益曲线应单调不降")
		}
	}
	if buildStratCurve(nil, 400) != nil {
		t.Fatal("空输入应返回 nil")
	}
}
