package eventvalidation

import (
	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// EvaluateTierGates applies frozen G6 validation gates to one capital tier.
func EvaluateTierGates(stats FiringStats) (status string, checks TierGateChecks, reason string) {
	checks = TierGateChecks{
		MinEventCountMet:            stats.Count >= protocol.ValidationMinEventCount,
		MinIndependentDaysMet:       stats.UniqueDays >= protocol.ValidationMinIndependentDays,
		MinShockBlocksMet:           stats.UniqueShockBlocks >= protocol.ValidationMinNonOverlapShockBlocks,
		BaseNetReturnPositive:       delayMeanPositive(stats.DelayMeanBPS, 0),
		AllDelayNetReturnPositive:   allDelaysPositive(stats.DelayMeanBPS),
		CostStressNetReturnPositive: stats.StressConservativeMeanBPS > 0,
		MaxContributionOK:           stats.MaxPositiveContributionFraction <= protocol.ValidationMaxSingleEventPositiveContributionFraction+1e-12,
		MaxDayContributionOK:        stats.MaxDayPositiveContributionFraction <= protocol.ValidationMaxSingleDayPositiveContributionFraction+1e-12,
	}
	if !checks.MinEventCountMet || !checks.MinIndependentDaysMet || !checks.MinShockBlocksMet {
		checks.SummaryNote = protocol.ValidationMinEventCountInconclusiveNote
		return StatusInconclusive, checks, ReasonCoverage
	}
	if !checks.MaxContributionOK || !checks.MaxDayContributionOK {
		return StatusValidationFail, checks, ReasonConcentration
	}
	if !checks.AllDelayNetReturnPositive {
		return StatusValidationFail, checks, ReasonDelay
	}
	if !checks.CostStressNetReturnPositive {
		return StatusValidationFail, checks, ReasonCost
	}
	if !checks.BaseNetReturnPositive {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	if stats.ConservativeMeanBPS <= 0 || stats.FeeNetMeanBPS <= 0 {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	return StatusValidationPass, checks, ""
}

func allDelaysPositive(delayMeans map[int]float64) bool {
	for _, delay := range protocol.ValidationDelayBars {
		if !delayMeanPositive(delayMeans, delay) {
			return false
		}
	}
	return true
}

func delayMeanPositive(delayMeans map[int]float64, delay int) bool {
	mean, ok := delayMeans[delay]
	return ok && mean > 0
}

// BuildCardResults evaluates all cards with per-tier gates and BH-FDR discovery screen.
func BuildCardResults(
	cards []reverseevent.FrozenEventCard,
	accumulators []*cardAccumulator,
	ledger reverseevent.ExperimentLedger,
) []CardResult {
	results := make([]CardResult, len(cards))
	cardPValues := make([]float64, len(cards))
	deterministicPass := make([]bool, len(cards))

	for i, card := range cards {
		tierResults := make([]TierResult, 0, len(protocol.ValidationInitialEquities))
		worstStatus := StatusValidationPass
		worstReason := ""

		for _, equity := range protocol.ValidationInitialEquities {
			tier := int(equity)
			var stats FiringStats
			if i < len(accumulators) && accumulators[i] != nil {
				if acc := accumulators[i].tiers[tier]; acc != nil {
					stats = acc.finalize()
				}
			}
			status, checks, reason := EvaluateTierGates(stats)
			tierResults = append(tierResults, TierResult{
				CapitalTier: tier,
				Status:      status,
				FailReason:  reason,
				Firing:      stats,
				GateChecks:  checks,
			})
			if statusRank(status) > statusRank(worstStatus) {
				worstStatus = status
				worstReason = reason
			} else if status == worstStatus && reason != "" && worstReason == "" {
				worstReason = reason
			}
		}

		signP := 1.0
		if worstStatus == StatusValidationPass {
			var nonzero, positive int
			if i < len(accumulators) && accumulators[i] != nil {
				nonzero, positive = accumulators[i].shockBlockSigns()
			}
			if p, err := g4protocol.ExactOneSidedPositiveBinomialSignP(positive, nonzero-positive); err == nil {
				signP = p
				deterministicPass[i] = true
			} else {
				worstStatus = StatusValidationFail
				worstReason = ReasonMultipleTesting
			}
		}
		cardPValues[i] = signP

		results[i] = CardResult{
			Ordinal:        card.Ordinal,
			EventCardHash:  card.EventCardHash,
			ExperimentHash: card.ExperimentHash,
			ProfileID:      card.ProfileID,
			EventFamily:    card.EventFamily,
			Horizon:        card.Horizon,
			Direction:      card.Direction,
			Status:         worstStatus,
			FailReason:     worstReason,
			TierResults:    tierResults,
			SignP:          signP,
			TradeAllowed:   false,
			ExamEligible:   false,
			PayerStatus:    reverseevent.PayerStatusUnexplained,
			Card:           card,
		}
	}

	familyPValues := discoveryScreenFamilyPValues(ledger, cards, cardPValues)
	bhPass, err := g4protocol.BenjaminiHochbergFDR(familyPValues, DiscoveryScreenFDRQ)
	if err != nil {
		return results
	}
	bhLimit := 0.0
	for i, pass := range bhPass {
		if pass && familyPValues[i] > bhLimit {
			bhLimit = familyPValues[i]
		}
	}
	attemptBHPass := map[string]bool{}
	for i, attempt := range ledger.Attempts {
		if attempt.EventCardHash != "" {
			attemptBHPass[attempt.EventCardHash] = bhPass[i]
		}
	}
	for i := range results {
		results[i].BHLimit = bhLimit
		results[i].BHPass = deterministicPass[i] && attemptBHPass[results[i].EventCardHash]
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

func discoveryScreenFamilyPValues(
	ledger reverseevent.ExperimentLedger,
	cards []reverseevent.FrozenEventCard,
	cardPValues []float64,
) []float64 {
	cardPByHash := map[string]float64{}
	for i, card := range cards {
		if i < len(cardPValues) {
			cardPByHash[card.EventCardHash] = cardPValues[i]
		}
	}
	if len(ledger.Attempts) == 0 {
		out := make([]float64, len(cards))
		copy(out, cardPValues)
		return out
	}
	out := make([]float64, len(ledger.Attempts))
	for i, attempt := range ledger.Attempts {
		if attempt.EventCardHash == "" {
			out[i] = 1.0
			continue
		}
		if p, ok := cardPByHash[attempt.EventCardHash]; ok {
			out[i] = p
		} else {
			out[i] = 1.0
		}
	}
	return out
}

func statusRank(status string) int {
	switch status {
	case StatusValidationPass:
		return 0
	case StatusValidationFail:
		return 1
	default:
		return 2
	}
}

// CardStatusFromTiers collapses tier outcomes to one card status.
func CardStatusFromTiers(tiers []TierResult) (status, reason string) {
	status = StatusValidationPass
	for _, tier := range tiers {
		if statusRank(tier.Status) > statusRank(status) {
			status = tier.Status
			reason = tier.FailReason
		}
	}
	return status, reason
}
