package app

import (
	"testing"
	"time"
)

// TestDeclaredFundFlow ㊳④ 申报账本:窗口求和 + "解释缺口"判定(≥60% 量级)。
func TestDeclaredFundFlow(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	for _, rec := range []declaredFundFlow{
		{DeclaredAt: base.Add(-2 * time.Hour), AmountUSDT: -42, Note: "提现"},
		{DeclaredAt: base.Add(-1 * time.Hour), AmountUSDT: +10, Note: "充值"},
		{DeclaredAt: base.Add(-30 * time.Hour), AmountUSDT: -999, Note: "窗外"},
	} {
		if err := appendDeclaredFundFlow(dir, rec); err != nil {
			t.Fatalf("落盘失败: %v", err)
		}
	}
	got := sumDeclaredFundFlowsUSDT(dir, base.Add(-24*time.Hour), base)
	if got != -32 {
		t.Fatalf("窗口净额应 -32(窗外剔除),得 %.2f", got)
	}
	if !declaredFlowExplainsOutflow(-32, 40) {
		t.Fatal("申报净转出 32 应解释 40 的缺口(80%>60%)")
	}
	if declaredFlowExplainsOutflow(-10, 40) {
		t.Fatal("申报净转出 10 不足以解释 40 的缺口(25%<60%)")
	}
	if declaredFlowExplainsOutflow(+32, 40) {
		t.Fatal("净转入不解释流出")
	}
	if sumDeclaredFundFlowsUSDT(t.TempDir(), base.Add(-24*time.Hour), base) != 0 {
		t.Fatal("无申报文件应返回 0(fail-safe 不消音)")
	}
}

// TestAccountRiskWatch_DeclaredFlowSilencesAlert 全链路:42U 场景 + 用户已申报转出 →
// 连"未解释资金流出"告警都不发(消音),且依然不硬停。
func TestAccountRiskWatch_DeclaredFlowSilencesAlert(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 35
	cfg.RiskConfig.MaxDailyLossPct = 0
	cfg.RiskConfig.AccountRiskMaxEquityTickDropUSDT = 1000
	cfg.RiskConfig.AccountRiskMinFundFlowStepUSDT = 500
	w, notified := newWiringTestWatcher(t, cfg)
	w.equityFn = func(now time.Time) accountEquityObservation {
		return accountEquityObservation{
			TotalUSDT:     57,
			ByVenue:       map[string]float64{"okx": 57},
			RawDailyDelta: -43.4,
			ReadOK:        true,
		}
	}
	haltCalls := 0
	w.haltFn = func(reason string) error { haltCalls++; return nil }
	w.haltActiveFn = func() bool { return false }
	if err := appendDeclaredFundFlow(w.recordDir, declaredFundFlow{
		DeclaredAt: time.Now().UTC().Add(-time.Minute), AmountUSDT: -42, Source: "panel",
	}); err != nil {
		t.Fatalf("申报落盘失败: %v", err)
	}
	w.tickOnce()
	if haltCalls != 0 {
		t.Fatalf("不应硬停,haltFn 被调用 %d 次", haltCalls)
	}
	for _, title := range *notified {
		if title == "未解释资金流出(对账告警,未停机)" {
			t.Fatal("申报已解释缺口,不应再发未解释流出告警")
		}
	}
}
