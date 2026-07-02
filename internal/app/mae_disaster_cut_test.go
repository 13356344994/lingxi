package app

// MAE 灾难刹车(§11.2)与 runup 临门确认闸的行为测试。

import "testing"

func TestEffectiveFastStopFloor(t *testing.T) {
	cases := []struct {
		name       string
		dynFloor   float64
		stopPct    float64
		maeCut     float64
		armed      bool
		wantFloor  float64
		wantReason string
	}{
		{"未武装:回退硬止损", 0, 0.05, 0.03, false, -0.05, "硬止损(快速)"},
		{"武装:刹车比硬止损浅→刹车绑定", 0, 0.05, 0.03, true, -0.03, "mae_disaster_cut"},
		{"武装但阈值0:忽略", 0, 0.05, 0, true, -0.05, "硬止损(快速)"},
		{"动态锁利地板为正:绝不被刹车放宽", 0.02, 0.05, 0.03, true, 0.02, "硬止损(快速)"},
		{"动态地板比刹车浅(-2%):保持动态", -0.02, 0.05, 0.03, true, -0.02, "硬止损(快速)"},
		{"动态地板比刹车深(-4%):刹车绑定", -0.04, 0.05, 0.03, true, -0.03, "mae_disaster_cut"},
		{"刹车比硬止损深(误配6%>5%):不生效", 0, 0.05, 0.06, true, -0.05, "硬止损(快速)"},
	}
	for _, c := range cases {
		floor, reason := effectiveFastStopFloor(c.dynFloor, c.stopPct, c.maeCut, c.armed)
		if floor != c.wantFloor || reason != c.wantReason {
			t.Errorf("%s: got (%.4f,%s) want (%.4f,%s)", c.name, floor, reason, c.wantFloor, c.wantReason)
		}
	}
}

func TestMAEDisasterCutArmedVenueGating(t *testing.T) {
	mk := func(enabled bool, pct float64, venues, self string) *OKXDirectionalEngine {
		e := &OKXDirectionalEngine{venueName: self}
		e.cfg.MAEDisasterCutEnabled = enabled
		e.cfg.MAEDisasterCutPct = pct
		e.cfg.MAEDisasterCutVenues = venues
		return e
	}
	if mk(false, 0.03, "", "okx").maeDisasterCutArmed() {
		t.Error("开关关时不应武装")
	}
	if mk(true, 0, "", "okx").maeDisasterCutArmed() {
		t.Error("阈值<=0 时不应武装")
	}
	if !mk(true, 0.03, "", "okx").maeDisasterCutArmed() {
		t.Error("空名单=所有所,okx 应武装")
	}
	if !mk(true, 0.03, "okx, bybit", "bybit").maeDisasterCutArmed() {
		t.Error("bybit 在名单内应武装(容忍空格)")
	}
	if mk(true, 0.03, "okx,bybit", "binance").maeDisasterCutArmed() {
		t.Error("binance 不在名单内不应武装")
	}
}

func TestRunupConfirmArmedGating(t *testing.T) {
	mk := func(enabled bool, venues, self string, microOn bool) *OKXDirectionalEngine {
		e := &OKXDirectionalEngine{venueName: self}
		e.cfg.RunupConfirmFilterEnabled = enabled
		e.cfg.RunupConfirmVenues = venues
		e.cfg.OpenMicroRecordEnabled = microOn
		return e
	}
	if mk(false, "", "okx", true).runupConfirmArmed() {
		t.Error("开关关时不应武装")
	}
	if !mk(true, "", "okx", true).runupConfirmArmed() {
		t.Error("空名单=所有所,okx 应武装")
	}
	if mk(true, "okx", "bybit", true).runupConfirmArmed() {
		t.Error("bybit 不在名单不应武装")
	}
	if mk(true, "okx", "okx", false).runupConfirmArmed() {
		t.Error("OpenMicro 埋点关闭(无数据源)时不应武装")
	}
}

func TestVenueInAllowList(t *testing.T) {
	e := &OKXDirectionalEngine{venueName: "bybit"}
	for _, c := range []struct {
		allow string
		want  bool
	}{
		{"", true}, {"bybit", true}, {" okx , BYBIT ", true}, {"okx", false}, {"okx,gate", false},
	} {
		if got := e.venueInAllowList(c.allow); got != c.want {
			t.Errorf("allow=%q got=%v want=%v", c.allow, got, c.want)
		}
	}
}
