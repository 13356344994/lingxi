package app

import (
	"math"
	"strings"
	"testing"
	"time"
)

// TestBuildSettlementAttributionReport 验证归因台:funding/direction/cost 拆解、可信过滤、edge 判据。
func TestBuildSettlementAttributionReport(t *testing.T) {
	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC) // 晚于 legacy 截断日,保证可信
	recs := make([]strategySettlementRecord, 0, 40)
	// 40 笔:每笔资金费很小(+0.01),方向 PnL 大幅波动且聚合有显著净敞口(even +2.5/odd -1.5,sum=+20),
	// 成本 0.05 → 方向/基差敞口在聚合口径上远大于资金费 → 应判"edge 不在 carry"。
	for i := 0; i < 40; i++ {
		dir := -1.5
		if i%2 == 0 {
			dir = 2.5
		}
		recs = append(recs, strategySettlementRecord{
			StrategyType:       UnifiedStrategyPerpPerp,
			Symbol:             "H-USDT",
			CloseTime:          base.Add(time.Duration(i) * time.Hour),
			NotionalUSDT:       100,
			RealizedFunding:    0.01,
			TradePnL:           dir,
			TotalFees:          0.05,
			NetPnL:             0.01 + dir - 0.05,
			FundingTruthStatus: "complete",
			CloseReason:        "计划离场:目标结算+缓冲已到",
		})
	}
	rep := BuildSettlementAttributionReport(recs, time.Time{})

	if rep.Trusted.TradeCount != 40 {
		t.Fatalf("trusted count=%d want 40", rep.Trusted.TradeCount)
	}
	if math.Abs(rep.Trusted.FundingUSDT-0.4) > 1e-6 {
		t.Fatalf("funding sum=%.4f want 0.40", rep.Trusted.FundingUSDT)
	}
	// 方向中位绝对值(2/100*10000=200bps)远大于资金费中位(0.01/100*10000=1bps)→ edge 不在 carry。
	if rep.EdgeRatio >= 0.5 {
		t.Fatalf("edge ratio=%.4f 应远 <0.5(方向主导)", rep.EdgeRatio)
	}
	if rep.EdgeVerdict == "" || rep.FundingPerNotionalMedianBps <= 0 {
		t.Fatalf("verdict/median 缺失: %+v", rep)
	}
	t.Logf("edge_verdict=%s funding_bps=%.2f dir_bps=%.2f ratio=%.4f",
		rep.EdgeVerdict, rep.FundingPerNotionalMedianBps, rep.DirectionAbsPerNotionalMedianBps, rep.EdgeRatio)

	// 可信/不可信拆分:加 5 笔无 funding_truth_status 的 legacy perp-perp,应进 untrusted。
	for i := 0; i < 5; i++ {
		recs = append(recs, strategySettlementRecord{
			StrategyType: UnifiedStrategyPerpPerp,
			Symbol:       "LEGACY-USDT",
			CloseTime:    base.Add(time.Duration(i) * time.Hour),
			NotionalUSDT: 100, NetPnL: 1.0, RealizedFunding: 0.5, TradePnL: 0.6, TotalFees: 0.1,
		})
	}
	rep2 := BuildSettlementAttributionReport(recs, time.Time{})
	if rep2.Untrusted.TradeCount != 5 {
		t.Fatalf("untrusted count=%d want 5", rep2.Untrusted.TradeCount)
	}
	if rep2.Trusted.TradeCount != 40 {
		t.Fatalf("trusted count after legacy=%d want 40", rep2.Trusted.TradeCount)
	}
}

// TestSettlementAttributionInsufficientSample 样本不足时判据应说"证据不足"。
func TestSettlementAttributionInsufficientSample(t *testing.T) {
	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	recs := []strategySettlementRecord{{
		StrategyType: UnifiedStrategyPerpPerp, Symbol: "H-USDT", CloseTime: base,
		NotionalUSDT: 100, RealizedFunding: 0.01, TradePnL: 0.5, TotalFees: 0.05,
		NetPnL: 0.46, FundingTruthStatus: "complete",
	}}
	rep := BuildSettlementAttributionReport(recs, time.Time{})
	if rep.EdgeVerdict == "" || !strings.Contains(rep.EdgeVerdict, "证据不足") {
		t.Fatalf("期望证据不足,实得: %s", rep.EdgeVerdict)
	}
}
