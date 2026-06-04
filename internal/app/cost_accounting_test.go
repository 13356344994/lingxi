package app

import (
	"testing"
)

// M-S3.0:完整净利影子口径 = NetPnL + 已测执行摩擦(带符号:负=逆向成本)。纯观测,不改 NetPnL。
func TestApplyFrictionObservationFoldsSignedFrictions(t *testing.T) {
	s := CycleSettlement{
		NetPnL: 10,
		Attributions: []SettlementAttribution{
			{Symbol: "BTCUSDT", OpenSlippage: -0.5, CloseSlippage: -0.25, RecoveryFriction: 0},
			{Symbol: "ETHUSDT", OpenSlippage: -0.3, CloseSlippage: 0.1, RecoveryFriction: -0.4},
		},
	}
	s.applyFrictionObservation()

	if !approxEqual(s.TotalOpenSlippageUSDT, -0.8) {
		t.Fatalf("开仓滑点聚合=%v want -0.8", s.TotalOpenSlippageUSDT)
	}
	if !approxEqual(s.TotalCloseSlippageUSDT, -0.15) {
		t.Fatalf("平仓滑点聚合=%v want -0.15", s.TotalCloseSlippageUSDT)
	}
	if !approxEqual(s.TotalRecoveryFrictionUSDT, -0.4) {
		t.Fatalf("恢复摩擦聚合=%v want -0.4", s.TotalRecoveryFrictionUSDT)
	}
	// 10 + (-0.8) + (-0.15) + (-0.4) = 8.65
	if !approxEqual(s.NetPnLAfterFrictions, 8.65) {
		t.Fatalf("完整净利影子口径=%v want 8.65", s.NetPnLAfterFrictions)
	}
	// 纯观测:NetPnL 不应被改动。
	if !approxEqual(s.NetPnL, 10) {
		t.Fatalf("NetPnL 不应被观测改动, got %v", s.NetPnL)
	}
}

// M-S3.1(R6):模型预期手续费 = Σ 各腿(开+平)名义 × 配置费率;名义 = Σ 开仓两腿名义。
func TestExpectedHedgeCostObservation(t *testing.T) {
	e := &FundingRateSniperEngine{}
	e.cfg.HistoricalConfig.SpotTakerFeeRate = 0.0010
	e.cfg.HistoricalConfig.FuturesTakerFeeRate = 0.0005
	hedges := []OpenHedge{{
		HedgeQty:                1,
		OpenSpotQuoteQty:        100,
		OpenFuturesAvgPrice:     100,
		CloseSpotQuoteQty:       100,
		CloseFuturesExecutedQty: 1,
		CloseFuturesAvgPrice:    100,
	}}
	fees, notional := e.expectedHedgeCostObservation(hedges)
	// spot:(100+100)×0.0010=0.20  futures:(100+100)×0.0005=0.10  合计 0.30
	if !approxEqual(fees, 0.30) {
		t.Fatalf("模型预期手续费=%v want 0.30", fees)
	}
	// 名义:开仓现货 100 + 开仓合约 100 = 200
	if !approxEqual(notional, 200) {
		t.Fatalf("本轮名义=%v want 200", notional)
	}
}

func TestExpectedHedgeCostObservationFeeRateFallback(t *testing.T) {
	e := &FundingRateSniperEngine{}
	hedges := []OpenHedge{{HedgeQty: 1, OpenSpotQuoteQty: 100, OpenFuturesAvgPrice: 100, CloseSpotQuoteQty: 100, CloseFuturesAvgPrice: 100, CloseFuturesExecutedQty: 1}}
	fees, _ := e.expectedHedgeCostObservation(hedges)
	// 费率为 0 时回退默认 spot0.0010/futures0.0005 → 同上 0.30
	if !approxEqual(fees, 0.30) {
		t.Fatalf("费率回退后模型预期手续费=%v want 0.30", fees)
	}
}

func TestApplyFrictionObservationNoAttributionsIsNetPnL(t *testing.T) {
	s := CycleSettlement{NetPnL: -3.2}
	s.applyFrictionObservation()
	if !approxEqual(s.NetPnLAfterFrictions, -3.2) {
		t.Fatalf("无归因时完整净利应等于 NetPnL, got %v", s.NetPnLAfterFrictions)
	}
	if s.TotalOpenSlippageUSDT != 0 || s.TotalCloseSlippageUSDT != 0 || s.TotalRecoveryFrictionUSDT != 0 {
		t.Fatalf("无归因时摩擦聚合应为 0")
	}
}
