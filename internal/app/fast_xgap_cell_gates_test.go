package app

import "testing"

// TestFastXgapCellSkip 格子口径三闸纯判定(2026-07-08 实盘日硬化;口径=㉝残余带+格子考试,冻结)。
func TestFastXgapCellSkip(t *testing.T) {
	cases := []struct {
		name     string
		fresh    bool
		fuel     float64
		breadth  float64
		brOK     bool
		gapNow   float64
		want     string
	}{
		{"全过", true, 10, -3, true, -30, ""},
		{"陈年价差", false, 10, -3, true, -30, "cell_stale_gap"},
		{"风暴州", true, 80, -3, true, -30, "cell_off_duty_storm"},
		{"广跌州", true, 10, -20, true, -30, "cell_off_duty_breadth"},
		{"广度不可算fail-closed", true, 10, 0, false, -30, "cell_off_duty_breadth"},
		{"残余太浅(肉没了)", true, 10, -3, true, -15, "cell_residual_shallow"},
		{"残余太深(没人抢有原因)", true, 10, -3, true, -80, "cell_residual_deep"},
		{"残余带边界-20", true, 10, -3, true, -20, ""},
		{"残余带边界-40", true, 10, -3, true, -40, ""},
	}
	for _, c := range cases {
		if got := fastXgapCellSkip(c.fresh, c.fuel, c.breadth, c.brOK, c.gapNow); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	// 闸序断言:三闸互斥优先级=新鲜度>派班>残余带(审计口径稳定)
	if got := fastXgapCellSkip(false, 80, -20, true, -80); got != "cell_stale_gap" {
		t.Fatalf("多重违规应报第一闸,got %q", got)
	}
}
