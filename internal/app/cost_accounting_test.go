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
