package reconcile

import "testing"

func makeRecord(key string, net, funding, trading, fees, notional float64) TradeRecord {
	return TradeRecord{Key: key, Symbol: key, NetPnL: net, FundingIncome: funding, TradingPnL: trading, TotalFees: fees, Notional: notional}
}

func TestReconcilePerfectMatchHealthy(t *testing.T) {
	live := []TradeRecord{makeRecord("BTC@1", 5, 8, 0, 3, 10000)}
	backtest := []TradeRecord{makeRecord("BTC@1", 5, 8, 0, 3, 10000)}
	report := Reconcile(live, backtest, Thresholds{AbsUSDT: 1, Rel: 0.1, TotalAbsUSDT: 10})
	if !report.Healthy {
		t.Fatalf("expected healthy report, got %+v", report)
	}
	if report.MatchedCount != 1 || report.BreachedCount != 0 || report.TotalNetAbsGap != 0 {
		t.Fatalf("unexpected summary: %+v", report)
	}
}

func TestReconcileBreachRequiresBothAbsAndRel(t *testing.T) {
	// 净收益偏差 2 USDT，相对回测值(5) = 0.4。阈值 abs=1 rel=0.1，两道都破 → breach。
	live := []TradeRecord{makeRecord("BTC@1", 7, 8, 0, 1, 10000)}
	backtest := []TradeRecord{makeRecord("BTC@1", 5, 8, 0, 3, 10000)}
	report := Reconcile(live, backtest, Thresholds{AbsUSDT: 1, Rel: 0.1})
	if report.BreachedCount != 1 || report.Healthy {
		t.Fatalf("expected one breach, got %+v", report)
	}

	// 同样 2 USDT 绝对差，但相对阈值放宽到 0.9 → 相对未破 → 不算 breach（噪声抑制）。
	report = Reconcile(live, backtest, Thresholds{AbsUSDT: 1, Rel: 0.9})
	if report.BreachedCount != 0 {
		t.Fatalf("expected no breach when rel within tolerance, got %+v", report)
	}
}

func TestReconcileTotalGapGuard(t *testing.T) {
	live := []TradeRecord{
		makeRecord("A", 10, 0, 10, 0, 1000),
		makeRecord("B", 10, 0, 10, 0, 1000),
	}
	backtest := []TradeRecord{
		makeRecord("A", 7, 0, 7, 0, 1000),
		makeRecord("B", 6, 0, 6, 0, 1000),
	}
	// 单笔放宽（不触发逐笔 breach），但净收益总偏差 |3|+|4| = 7 > TotalAbsUSDT=5 → 总闸触发。
	report := Reconcile(live, backtest, Thresholds{AbsUSDT: 100, Rel: 1, TotalAbsUSDT: 5})
	if report.TotalNetAbsGap != 7 {
		t.Fatalf("total net abs gap=%v want 7", report.TotalNetAbsGap)
	}
	if !report.TotalBreached || report.Healthy {
		t.Fatalf("expected total breach, got %+v", report)
	}
}

func TestReconcileUnmatchedKeysUnhealthy(t *testing.T) {
	live := []TradeRecord{makeRecord("A", 1, 0, 1, 0, 100), makeRecord("ONLY_LIVE", 1, 0, 1, 0, 100)}
	backtest := []TradeRecord{makeRecord("A", 1, 0, 1, 0, 100), makeRecord("ONLY_BT", 1, 0, 1, 0, 100)}
	report := Reconcile(live, backtest, Thresholds{AbsUSDT: 10, Rel: 1})
	if len(report.OnlyLiveKeys) != 1 || report.OnlyLiveKeys[0] != "ONLY_LIVE" {
		t.Fatalf("expected ONLY_LIVE unmatched, got %+v", report.OnlyLiveKeys)
	}
	if len(report.OnlyBacktestKeys) != 1 || report.OnlyBacktestKeys[0] != "ONLY_BT" {
		t.Fatalf("expected ONLY_BT unmatched, got %+v", report.OnlyBacktestKeys)
	}
	if report.Healthy {
		t.Fatalf("unmatched keys must make report unhealthy")
	}
}

func TestReconcileRelGapFallsBackToNotional(t *testing.T) {
	// 回测净收益为 0，相对差应以名义为分母：abs=2 / notional=10000 = 0.0002。
	live := []TradeRecord{makeRecord("A", 2, 2, 0, 0, 10000)}
	backtest := []TradeRecord{makeRecord("A", 0, 0, 0, 0, 10000)}
	report := Reconcile(live, backtest, Thresholds{AbsUSDT: 1, Rel: 0.001})
	var netGap FieldGap
	for _, g := range report.Records[0].Gaps {
		if g.Field == "net_pnl" {
			netGap = g
		}
	}
	if netGap.AbsGap != 2 {
		t.Fatalf("net abs gap=%v want 2", netGap.AbsGap)
	}
	if netGap.RelGap < 0.00019 || netGap.RelGap > 0.00021 {
		t.Fatalf("rel gap should fall back to notional denom, got %v", netGap.RelGap)
	}
	// abs 破(2>1) 但 rel 未破(0.0002<0.001) → 不 breach。
	if report.BreachedCount != 0 {
		t.Fatalf("expected no breach, got %+v", report.Records[0])
	}
}
