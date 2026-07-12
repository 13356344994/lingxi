package constitution

import "testing"

func TestTerminalRiskCeilingsAreHardCapsNotTargets(t *testing.T) {
	if !RiskCeilingIsNotSizingTarget {
		t.Fatal("risk ceiling must not be treated as sizing target")
	}
	if MaxEventTerminalLossFraction != 0.40 {
		t.Fatalf("max event terminal loss = %v, want 0.40", MaxEventTerminalLossFraction)
	}
	if MaxAccountTerminalRiskFraction != 0.70 {
		t.Fatalf("max account terminal risk = %v, want 0.70", MaxAccountTerminalRiskFraction)
	}
	if SizingDirection != "downward_only_from_evidence_and_liquidation_distance" {
		t.Fatalf("unexpected sizing direction: %q", SizingDirection)
	}
}

func TestOKXOnlyExecutionCrossVenuePITOnly(t *testing.T) {
	if ExecutionVenueOKXOnly != "okx" {
		t.Fatalf("execution venue = %q, want okx", ExecutionVenueOKXOnly)
	}
	want := []string{"binance", "bybit", "gate"}
	if len(CrossVenuePITVenues) != len(want) {
		t.Fatalf("cross venue pit venues = %v, want %v", CrossVenuePITVenues, want)
	}
	for i := range want {
		if CrossVenuePITVenues[i] != want[i] {
			t.Fatalf("cross venue pit venues[%d] = %q, want %q", i, CrossVenuePITVenues[i], want[i])
		}
	}
}

func TestPersonalToleranceAndProhibitions(t *testing.T) {
	if !AllowLargeFloatingLoss || !AllowOvernightHold || !AllowCrossFundingSettlement {
		t.Fatal("floating loss / overnight / funding tolerance must be allowed")
	}
	if !MultiVenuePresetCapitalFuture || !NoManualConfirmationRequired || !LongTermIdleCapacityAllowed {
		t.Fatal("multi-venue preset / no manual confirm / idle capacity must be allowed")
	}
	if !SolvencyIndependentRequired || !ProhibitLossChasing || !ProhibitUpwardRounding || !ProhibitTopUpAfterToxicFill {
		t.Fatal("solvency independence and loss-chasing prohibitions must be frozen")
	}
}

func TestCapitalTiersAndConcurrency(t *testing.T) {
	if len(CapitalTiersUSDT) != 3 || CapitalTiersUSDT[0] != 30 || CapitalTiersUSDT[1] != 100 || CapitalTiersUSDT[2] != 1000 {
		t.Fatalf("capital tiers = %v", CapitalTiersUSDT)
	}
	if MaxConcurrentByCapital[30] != 1 || MaxConcurrentByCapital[100] != 2 || MaxConcurrentByCapital[1000] != 3 {
		t.Fatalf("max concurrent = %v", MaxConcurrentByCapital)
	}
}
