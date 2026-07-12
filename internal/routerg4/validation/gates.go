package validation

import (
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

// EvaluateGates applies frozen protocol validation gates to firing stats.
func EvaluateGates(stats FiringStats) (status string, checks GateChecks, reason string) {
	checks = GateChecks{
		MinOpportunitiesMet:      stats.Count >= protocol.ValidationMinOpportunities,
		MinUniqueDaysMet:         stats.UniqueDays >= protocol.ValidationMinUniqueDays,
		MinUniqueSymbolsMet:      stats.UniqueSymbols >= protocol.ValidationMinUniqueSymbols,
		FeeNetMeanPositive:       stats.FeeNetMeanBPS > protocol.ValidationMinFeeNetMeanBPS,
		ConservativeMeanPositive: stats.ConservativeMeanBPS > protocol.ValidationMinConservativeNetMeanBPS,
		MaxContributionOK:        stats.MaxPositiveContributionFraction <= protocol.ValidationMaxSingleEventContributionFraction,
		MaxDayContributionOK:     stats.MaxDayPositiveContributionFraction <= protocol.ValidationMaxDayPositiveContributionFraction,
		MinIndependentBlocksMet:  stats.NonzeroBlocks >= protocol.ValidationMinIndependentBlocks,
		DelayMeanPositive:        stats.DelayConservativeMeanBPS > 0,
		StressMeanNonNegative:    stats.StressConservativeMeanBPS >= 0,
	}
	if !checks.MinOpportunitiesMet || !checks.MinUniqueDaysMet ||
		!checks.MinUniqueSymbolsMet || !checks.MinIndependentBlocksMet {
		return StatusInconclusive, checks, ReasonCoverage
	}
	if !checks.MaxContributionOK || !checks.MaxDayContributionOK {
		return StatusValidationFail, checks, ReasonConcentration
	}
	if !checks.DelayMeanPositive {
		return StatusValidationFail, checks, ReasonDelay
	}
	if !checks.StressMeanNonNegative {
		return StatusValidationFail, checks, ReasonCost
	}
	if stats.CapacityFailRate >= 1.0 && stats.ConservativeMeanBPS <= 0 {
		return StatusValidationFail, checks, ReasonCapacity
	}
	if !checks.FeeNetMeanPositive || !checks.ConservativeMeanPositive {
		if stats.CapacityFailRate > 0.5 {
			return StatusValidationFail, checks, ReasonCapacity
		}
		return StatusValidationFail, checks, ReasonNoEdge
	}
	return StatusValidationPass, checks, ""
}

// BuildCandidateResults evaluates all candidates from accumulators.
func BuildCandidateResults(candidates []reversemine.Candidate, accumulators []*firingAccumulator) []CandidateResult {
	results := make([]CandidateResult, len(candidates))
	pValues := make([]float64, len(candidates))
	deterministicPass := make([]bool, len(candidates))
	for i, c := range candidates {
		var stats FiringStats
		if i < len(accumulators) && accumulators[i] != nil {
			stats = accumulators[i].finalize()
		}
		status, checks, reason := EvaluateGates(stats)
		phenomenonID := phenomenonID(c)
		signP := 1.0
		if status == StatusValidationPass {
			negative := stats.NonzeroBlocks - stats.PositiveBlocks
			if p, err := protocol.ExactOneSidedPositiveBinomialSignP(
				stats.PositiveBlocks, negative,
			); err == nil {
				signP = p
				deterministicPass[i] = true
			} else {
				status, reason = StatusValidationFail, ReasonMultipleTesting
			}
		}
		pValues[i] = signP
		results[i] = CandidateResult{
			Ordinal:            c.Ordinal,
			CanonicalHash:      c.CanonicalHash,
			PhenomenonID:       phenomenonID,
			ProfileID:          c.ProfileID,
			CellKey:            c.CellKey,
			Status:             status,
			FailReason:         reason,
			Firing:             stats,
			GateChecks:         checks,
			NonzeroBlocks:      stats.NonzeroBlocks,
			PositiveBlocks:     stats.PositiveBlocks,
			SignP:              signP,
			MaxDayContribution: stats.MaxDayPositiveContributionFraction,
			Candidate:          c,
		}
	}
	bhPass, err := protocol.BenjaminiHochbergFDR(pValues, protocol.ValidationFDRQ)
	if err != nil {
		return results
	}
	bhLimit := 0.0
	for i, pass := range bhPass {
		if pass && pValues[i] > bhLimit {
			bhLimit = pValues[i]
		}
	}
	for i := range results {
		results[i].BHLimit = bhLimit
		results[i].BHPass = deterministicPass[i] && bhPass[i]
		if deterministicPass[i] && !results[i].BHPass {
			results[i].Status = StatusValidationFail
			results[i].FailReason = ReasonMultipleTesting
		} else if results[i].BHPass {
			results[i].Status = StatusValidationPass
			results[i].FailReason = ""
		}
	}
	return results
}

func phenomenonID(candidate reversemine.Candidate) string {
	conditions := make([]protocol.PhenomenonCondition, len(candidate.Conditions))
	for i, condition := range candidate.Conditions {
		conditions[i] = protocol.PhenomenonCondition{
			FeatureKey: condition.FeatureKey,
			Op:         string(condition.Op),
			Threshold:  condition.Threshold,
			Quantile:   condition.Quantile,
		}
	}
	id, err := protocol.PhenomenonID(protocol.PhenomenonIdentity{
		ProfileID:       candidate.ProfileID,
		SkyCoarseMarket: candidate.CellKey.SkyCoarse,
		Horizon:         candidate.CellKey.Horizon,
		Direction:       candidate.CellKey.Direction,
		Conditions:      conditions,
	})
	if err != nil {
		return ""
	}
	return id
}
