package app

import (
	"math"
	"testing"

	"lingxi/internal/core"
)

const testReferenceEquityUSDT = 435.0

func assertNear(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.4f want %.4f (tol %.4f)", label, got, want, tol)
	}
}

func TestEffectiveLossDrawdownThresholdUSDT_AtReferenceEquity(t *testing.T) {
	pct := core.DefaultMaxDailyLossPct
	got := effectiveLossDrawdownThresholdUSDT(25, pct, testReferenceEquityUSDT)
	assertNear(t, "daily@435", got, 25, 0.5)

	got = effectiveLossDrawdownThresholdUSDT(60, core.DefaultMaxAccountDrawdownPct, testReferenceEquityUSDT)
	assertNear(t, "drawdown@435", got, 60, 0.5)

	got = effectiveLossDrawdownThresholdUSDT(35, core.DefaultMaxStrategyCumulativeLossPct, testReferenceEquityUSDT)
	assertNear(t, "strategy@435", got, 35, 0.5)

	got = effectiveAutoFlattenAccountDrawdownUSDT(RiskConfig{
		AutoFlattenAccountDrawdownUSDT: 150,
		AutoFlattenAccountDrawdownPct:  core.DefaultAutoFlattenAccountDrawdownPct,
	}, testReferenceEquityUSDT)
	assertNear(t, "auto_flatten@435", got, 150, 0.5)
}

func TestEffectiveLossDrawdownThresholdUSDT_ScalesAt2000(t *testing.T) {
	pct := core.DefaultMaxDailyLossPct
	got := effectiveLossDrawdownThresholdUSDT(40, pct, 2000)
	assertNear(t, "daily@2000", got, pct*2000, 1.0)

	got = effectiveLossDrawdownThresholdUSDT(90, core.DefaultMaxAccountDrawdownPct, 2000)
	assertNear(t, "drawdown@2000", got, core.DefaultMaxAccountDrawdownPct*2000, 1.0)
}

func TestEffectiveLossDrawdownThresholdUSDT_PctZeroUsesUSDT(t *testing.T) {
	got := effectiveLossDrawdownThresholdUSDT(40, 0, 2000)
	if got != 40 {
		t.Fatalf("pct=0 regression got %.2f want 40", got)
	}
}

func TestEffectiveOpenNotionalLimitUSDT_GrossLeverage(t *testing.T) {
	got := effectiveOpenNotionalLimitUSDT(2200, core.DefaultMaxGrossLeverage, testReferenceEquityUSDT)
	assertNear(t, "notional@435", got, 2200, 1.0)

	got = effectiveOpenNotionalLimitUSDT(2200, core.DefaultMaxGrossLeverage, 2000)
	assertNear(t, "notional@2000", got, core.DefaultMaxGrossLeverage*2000, 1.0)
}

func TestEffectiveTotalDeployableUSDT(t *testing.T) {
	got := effectiveTotalDeployableUSDT(435, core.DefaultTotalDeployablePct, testReferenceEquityUSDT)
	assertNear(t, "deployable@435", got, 435, 1.0)

	got = effectiveTotalDeployableUSDT(435, core.DefaultTotalDeployablePct, 2000)
	assertNear(t, "deployable@2000", got, 2000, 1.0)

	got = effectiveTotalDeployableUSDT(435, 0, 2000)
	if got != 435 {
		t.Fatalf("pct=0 regression got %.2f want 435", got)
	}
}

func TestEvaluateAccountHardHaltIntent_AdaptiveDailyLoss(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 40
	cfg.MaxDailyLossPct = 40.0 / testReferenceEquityUSDT
	cfg.MaxAccountDrawdownUSDT = 0
	cfg.MaxStrategyCumulativeLossUSDT = 0
	cfg.MaxAccountOpenNotionalUSDT = 0
	cfg.MaxGrossLeverage = 0

	snap435 := testAccountRiskSnapshotForEval(-40, func(s *accountRiskSnapshot) {
		s.RealEquityTotalUSDT = testReferenceEquityUSDT
	})
	at435 := evaluateAccountHardHaltIntent(snap435, cfg)
	if !at435.ShouldHalt {
		t.Fatalf("expected halt at equity=435 with -40 loss, got %+v", at435)
	}

	snap2000 := testAccountRiskSnapshotForEval(-40, func(s *accountRiskSnapshot) {
		s.RealEquityTotalUSDT = 2000
	})
	at2000 := evaluateAccountHardHaltIntent(snap2000, cfg)
	if at2000.ShouldHalt {
		t.Fatalf("same -40 USDT loss must not halt at equity=2000 (scaled threshold), got %+v", at2000)
	}
}

func TestEvaluateAccountAutoFlattenIntent_AdaptivePeakDrawdown(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150
	cfg.AutoFlattenAccountDrawdownPct = 150.0 / testReferenceEquityUSDT

	below := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 149
		s.RealEquityDrawdownAdjustedUSDT = 149
		s.RealEquityPeakUSDT = testReferenceEquityUSDT
		s.RealEquityTotalUSDT = testReferenceEquityUSDT - 149
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if below.ShouldFlatten {
		t.Fatalf("expected no flatten below pct threshold at reference equity, got %+v", below)
	}

	at := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 150
		s.RealEquityDrawdownAdjustedUSDT = 150
		s.RealEquityPeakUSDT = testReferenceEquityUSDT
		s.RealEquityTotalUSDT = testReferenceEquityUSDT - 150
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if !at.ShouldFlatten {
		t.Fatalf("expected flatten at reference equity boundary, got %+v", at)
	}

	scaledPeak := 2000.0
	scaledLimit := 150.0 / testReferenceEquityUSDT * scaledPeak
	notYet := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = scaledLimit - 1
		s.RealEquityDrawdownAdjustedUSDT = scaledLimit - 1
		s.RealEquityPeakUSDT = scaledPeak
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if notYet.ShouldFlatten {
		t.Fatalf("drawdown below scaled limit must not flatten, got %+v", notYet)
	}
}

func TestRotationTotalDeployablePctScalesWithEquity(t *testing.T) {
	cfg := rotationBudgetTestCfg(435, 0)
	cfg.TotalDeployablePct = core.DefaultTotalDeployablePct

	at435 := newRotationBudgetState(map[string]float64{"binance": 800}, nil, cfg, testReferenceEquityUSDT)
	assertNear(t, "rotation budget@435", at435.totalLimitUSDT, 435, 1.0)

	at2000 := newRotationBudgetState(map[string]float64{"binance": 3000}, nil, cfg, 2000)
	assertNear(t, "rotation budget@2000", at2000.totalLimitUSDT, 2000, 1.0)
}
