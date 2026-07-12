package exclusionmap

import "testing"

func TestCustodyNotHardcodedPass(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.ConstitutionHash = ""
	ctx.Tiers = cliffTiers()
	inst := evaluateInstitution(ctx)
	binding := inst.EvidenceBindings["custody_authorization_limit"]
	if binding.Passed {
		t.Fatal("custody must not pass without personal constitution binding")
	}
}

func TestBestDirectionGrossNoOracleInjection(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	result := EvaluateFromGross(ctx, 0)
	if result.Accepted {
		t.Fatal("zero gross must fail closed, not inject oracle gross")
	}
}

func TestMinLotUsesPITDynamicMinNotional(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	ctx.DynamicMinNotionalUSDT = 10
	retail := evaluateRetail(ctx)
	ratio := retail.EvidenceBindings["min_lot_nonlinearity"].Value
	want := 10.0 / 30.0
	if ratio < want-1e-9 || ratio > want+1e-9 {
		t.Fatalf("min lot ratio = %v want %v", ratio, want)
	}
	if retail.EvidenceBindings["min_lot_nonlinearity"].Metric != "pit_dynamic_min_notional_to_capital_ratio_30" {
		t.Fatal("expected PIT dynamic min notional metric")
	}
}

func TestMinLotFailsClosedWithoutPIT(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	ctx.DynamicMinNotionalUSDT = 0
	retail := evaluateRetail(ctx)
	if retail.EvidenceBindings["min_lot_nonlinearity"].Passed {
		t.Fatal("min lot nonlinearity must fail closed without PIT dynamic min")
	}
}
