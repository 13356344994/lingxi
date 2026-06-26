package app

import (
	"math"
	"testing"
)

func TestScoreUniverseCandidate(t *testing.T) {
	calmBTC := okxBTCContext{TrendStrength: 0.1, RetAbsPct: 0.01}
	hotBTC := okxBTCContext{TrendStrength: 0.95, RetAbsPct: 0.05}

	good := okxUniverseFeatures{
		Symbol: "SOL-USDT", FundingRate: 0.003, AtrPct: 0.015, VolUSDT: 1e7, BtcCorr: 0.2,
	}
	weak := okxUniverseFeatures{
		Symbol: "WEAK-USDT", FundingRate: 0.0001, AtrPct: 0.002, VolUSDT: 1e4, BtcCorr: 0.1,
	}
	goodScore, _ := scoreUniverseCandidate(good, calmBTC)
	weakScore, _ := scoreUniverseCandidate(weak, calmBTC)
	if goodScore <= weakScore {
		t.Fatalf("高资费+波动+热度应高于低资费低量: good=%.4f weak=%.4f", goodScore, weakScore)
	}

	corrHigh := good
	corrHigh.BtcCorr = 0.95
	scoreCalm, _ := scoreUniverseCandidate(corrHigh, calmBTC)
	scoreHot, _ := scoreUniverseCandidate(corrHigh, hotBTC)
	if scoreHot >= scoreCalm {
		t.Fatalf("BTC 趋势猛且高相关应扣分: calm=%.4f hot=%.4f", scoreCalm, scoreHot)
	}
}

func TestRankUniverse(t *testing.T) {
	btc := okxBTCContext{}
	feats := []okxUniverseFeatures{
		{Symbol: "A-USDT", FundingRate: 0.001, AtrPct: 0.01, VolUSDT: 1e6, BtcCorr: 0.3},
		{Symbol: "B-USDT", FundingRate: 0.003, AtrPct: 0.02, VolUSDT: 1e7, BtcCorr: 0.2},
		{Symbol: "C-USDT", FundingRate: 0.0005, AtrPct: 0.005, VolUSDT: 1e5, BtcCorr: 0.1},
	}
	out := rankUniverse(feats, btc, 2)
	if len(out) != 2 {
		t.Fatalf("shortlistN=2 应截断为 2, got %d", len(out))
	}
	if out[0].Score < out[1].Score {
		t.Fatalf("应按综合分降序: first=%.4f second=%.4f", out[0].Score, out[1].Score)
	}
	if out[0].Symbol != "B-USDT" {
		t.Fatalf("B 应排第一, got %s", out[0].Symbol)
	}
}

func TestPearson(t *testing.T) {
	a := []float64{1, 2, 3, 4, 5}
	b := []float64{1, 2, 3, 4, 5}
	if r := pearson(a, b); math.Abs(r-1) > 0.01 {
		t.Fatalf("同向序列相关应≈1, got %.4f", r)
	}
	c := []float64{5, 4, 3, 2, 1}
	if r := pearson(a, c); math.Abs(r+1) > 0.01 {
		t.Fatalf("反向序列相关应≈-1, got %.4f", r)
	}
	if pearson([]float64{1, 1, 1}, []float64{2, 3, 4}) != 0 {
		t.Fatal("零方差序列应返回 0")
	}
}
