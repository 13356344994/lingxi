package constitution

import "lingxi/internal/datatruth"

// FrozenSnapshot returns the sealed personal constitution for downstream hashing.
func FrozenSnapshot() map[string]any {
	return map[string]any{
		"schema_version":                      SchemaVersion,
		"capital_tiers_usdt":                  CapitalTiersUSDT,
		"max_concurrent_by_capital":           MaxConcurrentByCapital,
		"max_event_terminal_loss_fraction":    MaxEventTerminalLossFraction,
		"max_account_terminal_risk_fraction":  MaxAccountTerminalRiskFraction,
		"risk_ceiling_is_not_sizing_target":   RiskCeilingIsNotSizingTarget,
		"sizing_direction":                    SizingDirection,
		"execution_venue_okx_only":            ExecutionVenueOKXOnly,
		"cross_venue_pit_info_venues":         CrossVenuePITVenues,
		"allow_large_floating_loss":           AllowLargeFloatingLoss,
		"allow_overnight_hold":                AllowOvernightHold,
		"allow_cross_funding_settlement":      AllowCrossFundingSettlement,
		"multi_venue_preset_capital_future":   MultiVenuePresetCapitalFuture,
		"no_manual_confirmation_required":     NoManualConfirmationRequired,
		"long_term_idle_capacity_allowed":     LongTermIdleCapacityAllowed,
		"solvency_independent_required":       SolvencyIndependentRequired,
		"prohibit_loss_chasing":               ProhibitLossChasing,
		"prohibit_upward_rounding":            ProhibitUpwardRounding,
		"prohibit_top_up_after_toxic_fill":    ProhibitTopUpAfterToxicFill,
		"actual_equity_tier_label":            ActualEquityTierLabel,
	}
}

// Hash seals the frozen personal constitution snapshot.
func Hash() (string, error) {
	hash, _, err := datatruth.HashJSON(FrozenSnapshot())
	return hash, err
}
