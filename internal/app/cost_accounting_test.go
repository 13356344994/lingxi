package app

import (
	"testing"
)

// R5 审定语义:NetPnLAfterFrictions = 零滑点理想净利基准 = NetPnL − Σ滑点(滑点带符号、负=成本,
// 减法把已在 NetPnL 内的滑点成本加回 → 得到「零滑点理想净利」)。纯观测,不改 NetPnL。
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
	// 零滑点理想净利 = NetPnL − Σ滑点 = 10 − (-0.8 -0.15 -0.4) = 10 + 1.35 = 11.35。
	if !approxEqual(s.NetPnLAfterFrictions, 11.35) {
		t.Fatalf("零滑点理想净利基准=%v want 11.35", s.NetPnLAfterFrictions)
	}
	// 执行拖累 = NetPnL − 基准 = Σ滑点 = -1.35(已在 NetPnL 内,此处仅量化)。
	if !approxEqual(s.NetPnL-s.NetPnLAfterFrictions, -1.35) {
		t.Fatalf("执行拖累=%v want -1.35", s.NetPnL-s.NetPnLAfterFrictions)
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
		t.Fatalf("无滑点时零滑点理想净利应等于 NetPnL, got %v", s.NetPnLAfterFrictions)
	}
	if s.TotalOpenSlippageUSDT != 0 || s.TotalCloseSlippageUSDT != 0 || s.TotalRecoveryFrictionUSDT != 0 {
		t.Fatalf("无归因时摩擦聚合应为 0")
	}
}
