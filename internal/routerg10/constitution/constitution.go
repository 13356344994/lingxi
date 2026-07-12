// Package constitution freezes the personal-exclusion Brain constitution for G10 v2.
// Risk ceilings (40% single-event terminal loss, 70% account simultaneous terminal risk)
// are hard caps, not sizing targets; Brain must size downward from opportunity evidence
// and liquidation distance. Legacy G10 0.5%/2% stress constants remain in protocol v1 only.
package constitution

const (
	SchemaVersion = "lingxi.routerg10.personal-constitution.v1"

	// Capital tiers for personal research (USDT).
	CapitalTier30U   = 30
	CapitalTier100U  = 100
	CapitalTier1000U = 1000

	MaxConcurrent30U   = 1
	MaxConcurrent100U  = 2
	MaxConcurrent1000U = 3

	// Hard terminal-loss ceilings — not sizing targets.
	MaxEventTerminalLossFraction   = 0.40
	MaxAccountTerminalRiskFraction = 0.70

	// Execution venue boundary.
	ExecutionVenueOKXOnly = "okx"

	// Cross-venue venues supply strict PIT announcement/info only; no order routing.
	CrossVenuePITInfoVenues = "binance,bybit,gate"

	RiskCeilingIsNotSizingTarget = true
	SizingDirection              = "downward_only_from_evidence_and_liquidation_distance"

	AllowLargeFloatingLoss       = true
	AllowOvernightHold           = true
	AllowCrossFundingSettlement  = true
	MultiVenuePresetCapitalFuture = true
	NoManualConfirmationRequired = true
	LongTermIdleCapacityAllowed  = true

	SolvencyIndependentRequired = true
	ProhibitLossChasing         = true
	ProhibitUpwardRounding      = true
	ProhibitTopUpAfterToxicFill = true

	ActualEquityTierLabel = "actual"
)

// CapitalTiersUSDT is the frozen personal capital ladder.
var CapitalTiersUSDT = []float64{CapitalTier30U, CapitalTier100U, CapitalTier1000U}

// CrossVenuePITVenues lists venues allowed for PIT announcement/info only.
var CrossVenuePITVenues = []string{"binance", "bybit", "gate"}

// MaxConcurrentByCapital maps initial equity tier to max concurrent event positions.
var MaxConcurrentByCapital = map[int]int{
	CapitalTier30U:   MaxConcurrent30U,
	CapitalTier100U:  MaxConcurrent100U,
	CapitalTier1000U: MaxConcurrent1000U,
}
