package app

import (
	"path/filepath"
	"testing"
)

// TestRecoverActiveOpenHedgesForFlatten 钉死应急强平的台账恢复:从落盘台账重建的裸仓必须携带强平所需的关键字段——
// 负 carry 还币数量(SpotBaseQty)、名义校验参考价(MarkPrice)、carry 方向与生命周期态,缺一会导致强平下错数量/方向。
func TestRecoverActiveOpenHedgesForFlatten(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), liveOpenHedgesStateName)
	entries := []persistedOpenHedgeEntry{
		{
			Symbol:             "ETHUSDT",
			Pair:               "ETHUSDT",
			Direction:          fundingCarryDirectionNegative,
			SpotBaseAsset:      "ETH",
			HedgeQty:           1.5,
			SpotBaseQty:        1.5,
			ReferenceSpotPrice: 3200,
		},
		{
			Symbol:             "BTCUSDT",
			Pair:               "BTCUSDT",
			Direction:          fundingCarryDirectionPositive,
			SpotBaseAsset:      "BTC",
			HedgeQty:           0.02,
			SpotBaseQty:        0.02,
			ReferenceSpotPrice: 100000,
		},
	}
	if err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{Hedges: entries}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	engine := &FundingRateSniperEngine{openHedgeStatePath: statePath}
	hedges, err := engine.recoverActiveOpenHedgesForFlatten()
	if err != nil {
		t.Fatalf("recoverActiveOpenHedgesForFlatten error: %v", err)
	}
	if len(hedges) != 2 {
		t.Fatalf("expected 2 hedges, got %d", len(hedges))
	}

	bySymbol := make(map[string]OpenHedge, len(hedges))
	for _, h := range hedges {
		bySymbol[h.Target.Symbol] = h
	}

	neg := bySymbol["ETHUSDT"]
	if neg.carryDirection() != fundingCarryDirectionNegative {
		t.Fatalf("ETHUSDT 应恢复为负 carry, got %v", neg.carryDirection())
	}
	if parseFloat(neg.SpotBaseQty) != 1.5 {
		t.Fatalf("负 carry 还币数量应恢复为 1.5, got %s", neg.SpotBaseQty)
	}
	if neg.LastKnownSpotPrice() != 3200 {
		t.Fatalf("参考现货价应恢复为 3200, got %.2f", neg.LastKnownSpotPrice())
	}
	if neg.lifecycleOrderGate() != OpenHedgeLifecycleActive {
		t.Fatalf("恢复的裸仓生命周期应为 active, got %q", neg.lifecycleOrderGate())
	}

	pos := bySymbol["BTCUSDT"]
	if pos.carryDirection() != fundingCarryDirectionPositive {
		t.Fatalf("BTCUSDT 应恢复为正 carry, got %v", pos.carryDirection())
	}
}

// TestRecoverActiveOpenHedgesForFlattenEmpty 钉死:无台账文件时恢复返回空集且不报错(对应 flatten「无裸仓」路径)。
func TestRecoverActiveOpenHedgesForFlattenEmpty(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), liveOpenHedgesStateName)
	engine := &FundingRateSniperEngine{openHedgeStatePath: statePath}
	hedges, err := engine.recoverActiveOpenHedgesForFlatten()
	if err != nil {
		t.Fatalf("empty ledger should not error: %v", err)
	}
	if len(hedges) != 0 {
		t.Fatalf("expected 0 hedges from empty ledger, got %d", len(hedges))
	}
}
