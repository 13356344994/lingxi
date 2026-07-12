package executionpolicy

// EvaluateMakerFillSelectionGate forbids maker when adverse selection dominates.
func EvaluateMakerFillSelectionGate(stats MakerStats) MakerFillSelectionGate {
	gate := MakerFillSelectionGate{}
	if stats.ExpectedNetGivenFilled < 0 {
		gate.ForbidMaker = true
		gate.Reasons = append(gate.Reasons, "expected_net_given_filled_negative")
	}
	if stats.FastFillExpectedNet < stats.SlowFillExpectedNet && stats.FastFillExpectedNet < 0 {
		gate.ForbidMaker = true
		gate.Reasons = append(gate.Reasons, "fast_fill_worse_than_slow")
	}
	missedProfit := stats.Quadrants.UnfilledSubsequentlyProfitable
	filledProfit := stats.Quadrants.FilledProfitable
	if missedProfit > filledProfit && missedProfit > stats.Quadrants.FilledUnprofitable {
		gate.ForbidMaker = true
		gate.Reasons = append(gate.Reasons, "profit_concentrated_in_missed_group")
	}
	return gate
}
