package fillmodel

import (
	"math"
	"testing"
)

func TestResidualCostChargesPartialFillAndLegReject(t *testing.T) {
	params := ResidualParams{
		Enabled:                  true,
		ExpectedLegFillRatio:     0.99,
		LegRejectProbability:     0.01,
		EmergencyHedgeMultiplier: 4.0,
	}
	got := ResidualCost(params, RoundTrip{
		OpenNotional:      1000,
		CloseNotional:     1000,
		OpenFrictionRate:  0.003,
		CloseFrictionRate: 0.003,
		OpenBasisRate:     0.001,
		CloseBasisRate:    0.001,
	})

	if math.Abs(got.OpenResidualUSDT-10) > 1e-9 || math.Abs(got.CloseResidualUSDT-10) > 1e-9 {
		t.Fatalf("expected 1%% residual on each leg (10 USDT), got open=%.6f close=%.6f", got.OpenResidualUSDT, got.CloseResidualUSDT)
	}
	if math.Abs(got.PartialFillCostUSDT-0.26) > 1e-9 {
		t.Fatalf("expected partial-fill cost 0.26, got %.6f", got.PartialFillCostUSDT)
	}
	if math.Abs(got.LegRejectCostUSDT-0.26) > 1e-9 {
		t.Fatalf("expected leg-reject cost 0.26, got %.6f", got.LegRejectCostUSDT)
	}
	if math.Abs(got.HaircutUSDT-0.52) > 1e-9 {
		t.Fatalf("expected total haircut 0.52, got %.6f", got.HaircutUSDT)
	}
}

func TestResidualCostDisabledIsZero(t *testing.T) {
	got := ResidualCost(ResidualParams{Enabled: false, ExpectedLegFillRatio: 0.99, LegRejectProbability: 0.01, EmergencyHedgeMultiplier: 4.0},
		RoundTrip{OpenNotional: 1000, CloseNotional: 1000, OpenFrictionRate: 0.003, CloseFrictionRate: 0.003, OpenBasisRate: 0.001, CloseBasisRate: 0.001})
	if got.HaircutUSDT != 0 || got.PartialFillCostUSDT != 0 || got.LegRejectCostUSDT != 0 {
		t.Fatalf("expected zero haircut when disabled, got %+v", got)
	}
}

func TestResidualCostMonotonicInFillRatio(t *testing.T) {
	base := ResidualParams{Enabled: true, ExpectedLegFillRatio: 0.99, LegRejectProbability: 0.01, EmergencyHedgeMultiplier: 4.0}
	worse := base
	worse.ExpectedLegFillRatio = 0.95
	rt := RoundTrip{OpenNotional: 1000, CloseNotional: 1000, OpenFrictionRate: 0.003, CloseFrictionRate: 0.003, OpenBasisRate: 0.001, CloseBasisRate: 0.001}
	if high, low := ResidualCost(worse, rt), ResidualCost(base, rt); high.HaircutUSDT <= low.HaircutUSDT {
		t.Fatalf("expected lower fill ratio to charge more, got worse=%.6f base=%.6f", high.HaircutUSDT, low.HaircutUSDT)
	}
}

func TestResidualCostModelImplementsCostModel(t *testing.T) {
	var model CostModel = ResidualCostModel{Params: ResidualParams{Enabled: true, ExpectedLegFillRatio: 0.99, LegRejectProbability: 0.01, EmergencyHedgeMultiplier: 4.0}}
	if got := model.ResidualFillCost(RoundTrip{OpenNotional: 1000, CloseNotional: 1000, OpenFrictionRate: 0.003, CloseFrictionRate: 0.003}); got.HaircutUSDT <= 0 {
		t.Fatalf("expected positive haircut from CostModel, got %.6f", got.HaircutUSDT)
	}
}
