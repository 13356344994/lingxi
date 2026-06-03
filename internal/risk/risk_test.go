package risk

import "testing"

func TestEvaluateHedgeImbalance(t *testing.T) {
	cases := []struct {
		name     string
		spot     float64
		futures  float64
		absThr   float64
		rateThr  float64
		breached bool
		action   RiskAction
	}{
		{name: "balanced_within_abs", spot: 1000, futures: 995, absThr: 20, rateThr: 0, breached: false, action: RiskActionAllow},
		{name: "abs_breach", spot: 1000, futures: 950, absThr: 20, rateThr: 0, breached: true, action: RiskActionTripPlatformFuse},
		{name: "rate_breach", spot: 1000, futures: 900, absThr: 0, rateThr: 0.05, breached: true, action: RiskActionTripPlatformFuse},
		{name: "rate_within", spot: 1000, futures: 990, absThr: 0, rateThr: 0.05, breached: false, action: RiskActionAllow},
		{name: "either_threshold_trips", spot: 1000, futures: 970, absThr: 100, rateThr: 0.02, breached: true, action: RiskActionTripPlatformFuse},
		{name: "guard_disabled", spot: 1000, futures: 0, absThr: 0, rateThr: 0, breached: false, action: RiskActionAllow},
		{name: "naked_single_leg", spot: 1000, futures: 0, absThr: 50, rateThr: 0, breached: true, action: RiskActionTripPlatformFuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateHedgeImbalance(tc.spot, tc.futures, tc.absThr, tc.rateThr)
			if got.Breached != tc.breached {
				t.Fatalf("breached=%v, want %v (imbalance=%.2f rate=%.4f)", got.Breached, tc.breached, got.Imbalance, got.ImbalanceRate)
			}
			if got.Action != tc.action {
				t.Fatalf("action=%q, want %q", got.Action, tc.action)
			}
			if tc.breached && got.Severity != RiskSeverityLevelCritical {
				t.Fatalf("expected critical severity on breach, got %q", got.Severity)
			}
		})
	}
}

func TestEvaluateExecutionCoverageRisk(t *testing.T) {
	cases := []struct {
		name     string
		coverage float64
		action   RiskAction
		severity string
	}{
		{name: "healthy", coverage: 0.99, action: RiskActionObserve, severity: "healthy"},
		{name: "minor", coverage: 0.95, action: RiskActionObserve, severity: "minor"},
		{name: "major", coverage: 0.75, action: RiskActionTighten, severity: "major"},
		{name: "critical", coverage: 0.50, action: RiskActionPauseCycle, severity: "critical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateExecutionCoverageRisk("depth", tc.coverage)
			if got.Action != tc.action || got.Severity != tc.severity {
				t.Fatalf("unexpected decision: %+v", got)
			}
		})
	}
}

func TestEvaluateLiquidityShareRisk(t *testing.T) {
	cases := []struct {
		name        string
		share       float64
		limit       float64
		severeLimit float64
		action      RiskAction
		severity    string
	}{
		{name: "no_share", share: 0, limit: 0.1, severeLimit: 0.2, action: RiskActionObserve, severity: "healthy"},
		{name: "below_soft", share: 0.05, limit: 0.1, severeLimit: 0.2, action: RiskActionObserve, severity: "healthy"},
		{name: "near_soft", share: 0.09, limit: 0.1, severeLimit: 0.2, action: RiskActionObserve, severity: "minor"},
		{name: "over_soft", share: 0.15, limit: 0.1, severeLimit: 0.2, action: RiskActionTighten, severity: "major"},
		{name: "over_severe", share: 0.25, limit: 0.1, severeLimit: 0.2, action: RiskActionPauseCycle, severity: "critical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateLiquidityShareRisk(tc.share, tc.limit, tc.severeLimit)
			if got.Action != tc.action || got.Severity != tc.severity {
				t.Fatalf("unexpected decision: %+v", got)
			}
		})
	}
}

func TestMarketRiskDecisionNormalizedDefaultsAction(t *testing.T) {
	got := MarketRiskDecision{Category: "  depth ", Severity: " major "}.Normalized()
	if got.Category != "depth" || got.Severity != "major" {
		t.Fatalf("expected trimmed fields, got %+v", got)
	}
	if got.Action != RiskActionObserve {
		t.Fatalf("expected empty action to default to observe, got %q", got.Action)
	}
}
