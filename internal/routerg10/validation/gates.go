package validation

import (
	"fmt"

	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	g4protocol "lingxi/internal/routerg4/protocol"
)

// EvaluateTierGates applies frozen G10 validation gates to one capital tier replay.
func EvaluateTierGates(replay TierReplayStats, scenarios []TierScenarioResult) (status string, checks TierGateChecks, reason string) {
	return EvaluateTierGatesWithShockSign(replay, scenarios, -1, -1)
}

// EvaluateTierGatesWithShockSign applies tier gates plus an independent shock-block sign gate.
func EvaluateTierGatesWithShockSign(replay TierReplayStats, scenarios []TierScenarioResult, nonzeroShockBlocks, positiveShockBlocks int) (status string, checks TierGateChecks, reason string) {
	checks = TierGateChecks{
		MinEventCountMet:               replay.Count >= protocol.ValidationMinEventCount,
		MinIndependentDaysMet:          replay.UniqueDays >= protocol.ValidationMinIndependentDays,
		MinShockBlocksMet:              replay.UniqueShockBlocks >= protocol.ValidationMinNonOverlapShockBlocks,
		MaxContributionOK:              replay.MaxPositiveContributionFraction <= protocol.ValidationMaxSingleEventPositiveContributionFraction+1e-12,
		MaxDayContributionOK:           replay.MaxDayPositiveContributionFraction <= protocol.ValidationMaxSingleDayPositiveContributionFraction+1e-12,
		BaseNetReturnNonNegative:       true,
		AllDelayNetReturnNonNegative:   true,
		CostStressNetReturnNonNegative: true,
		NoLeverageSolvencyBreach:       true,
		ShockSignBlocksMet:             true,
	}
	if !checks.MinEventCountMet || !checks.MinIndependentDaysMet || !checks.MinShockBlocksMet {
		checks.SummaryNote = protocol.ValidationMinEventCountInconclusiveNote
		return StatusInconclusive, checks, ReasonCoverage
	}
	for _, s := range scenarios {
		if !s.NetReturnNonNegative {
			if s.CostStressMultiplier > 1.0+1e-9 {
				checks.CostStressNetReturnNonNegative = false
			} else if s.DelayBars > 0 {
				checks.AllDelayNetReturnNonNegative = false
			} else {
				checks.BaseNetReturnNonNegative = false
			}
		}
		if s.DelayBars == 0 && s.CostStressMultiplier <= 1.0+1e-9 {
			if s.LeverageBreach || s.SolvencyBreach {
				checks.NoLeverageSolvencyBreach = false
			}
		}
	}
	if !checks.MaxContributionOK || !checks.MaxDayContributionOK {
		return StatusValidationFail, checks, ReasonConcentration
	}
	if !checks.NoLeverageSolvencyBreach {
		return StatusValidationFail, checks, ReasonSolvency
	}
	if !checks.AllDelayNetReturnNonNegative {
		return StatusValidationFail, checks, ReasonDelay
	}
	if !checks.CostStressNetReturnNonNegative {
		return StatusValidationFail, checks, ReasonCost
	}
	if !checks.BaseNetReturnNonNegative {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	if nonzeroShockBlocks >= 0 {
		if nonzeroShockBlocks < protocol.ValidationMinNonOverlapShockBlocks {
			checks.ShockSignBlocksMet = false
			return StatusValidationFail, checks, ReasonMultipleTesting
		}
		if _, err := g4protocol.ExactOneSidedPositiveBinomialSignP(positiveShockBlocks, nonzeroShockBlocks-positiveShockBlocks); err != nil {
			checks.ShockSignBlocksMet = false
			return StatusValidationFail, checks, ReasonMultipleTesting
		}
	}
	return StatusValidationPass, checks, ""
}

// BuildPlaybookResults evaluates all playbooks with per-tier gates and BH-FDR discovery screen.
func BuildPlaybookResults(
	playbooks []playbook.FrozenEventPlaybook,
	tierOutcomes map[string][]TierResult,
	ledger playbook.ExperimentLedger,
	degeneracyByPlaybook map[string]DegeneracySummary,
) []PlaybookResult {
	results := make([]PlaybookResult, len(playbooks))
	pValues := make([]float64, len(playbooks))
	deterministicPass := make([]bool, len(playbooks))

	for i, pb := range playbooks {
		tiers := tierOutcomes[pb.PlaybookHash]
		deg := degeneracyByPlaybook[pb.PlaybookHash]
		eligible := eligibleTiersFromResults(tiers)
		overall, reason := collapsePlaybookStatus(tiers)
		if deg.Degenerate {
			overall = StatusValidationFail
			reason = ReasonBrainDegeneracy
			eligible = nil
		}
		signP := 1.0
		if overall == StatusValidationPass {
			nonzero, positive := shockSignsFromTiers(tiers)
			if p, err := g4protocol.ExactOneSidedPositiveBinomialSignP(positive, nonzero-positive); err == nil {
				signP = p
				deterministicPass[i] = true
			} else {
				overall = StatusValidationFail
				reason = ReasonMultipleTesting
			}
		}
		pValues[i] = signP
		maxCap := maxCapacityFromTiers(tiers)
		frozenEdge, frozenStress := frozenEdgeFromTiers(tiers)
		results[i] = PlaybookResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
			ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, Horizon: pb.Horizon,
			Status: overall, FailReason: reason, TierResults: tiers,
			EligibleCapitalTiers: eligible, MaxCapacityUSDT: maxCap,
			FrozenConservativeMeanBPS: frozenEdge, FrozenStressConservativeMeanBPS: frozenStress,
			SignP: signP, TradeAllowed: false, ExamEligible: false,
			PayerStatus: protocol.UnexplainedPersonalEdgeStatus, BrainDegeneracy: deg, Playbook: pb,
		}
	}

	familySize, testedP, testedHashes := discoveryScreenTestedHypotheses(ledger, playbooks, pValues, deterministicPass)
	bhPass, err := benjaminiHochbergFDRWithFamilyDenominator(testedP, familySize, DiscoveryScreenFDRQ)
	if err != nil {
		return results
	}
	bhLimit := 0.0
	for i, pass := range bhPass {
		if pass && testedP[i] > bhLimit {
			bhLimit = testedP[i]
		}
	}
	attemptBHPass := map[string]bool{}
	for i, hash := range testedHashes {
		if bhPass[i] {
			attemptBHPass[hash] = true
		}
	}
	for i := range results {
		results[i].BHLimit = bhLimit
		results[i].BHPass = deterministicPass[i] && attemptBHPass[results[i].PlaybookHash]
		if results[i].BrainDegeneracy.Degenerate {
			results[i].Status = StatusValidationFail
			results[i].FailReason = ReasonBrainDegeneracy
			results[i].EligibleCapitalTiers = nil
			results[i].BHPass = false
		} else if deterministicPass[i] && !results[i].BHPass {
			results[i].Status = StatusValidationFail
			results[i].FailReason = ReasonMultipleTesting
			results[i].EligibleCapitalTiers = nil
		} else if results[i].BHPass {
			results[i].Status = StatusValidationPass
			results[i].FailReason = ""
		}
	}
	return results
}

type shockSignCounts struct {
	nonzero  int
	positive int
}

func shockSignsFromTiers(tiers []TierResult) (nonzero, positive int) {
	for _, tr := range tiers {
		if tr.Status != StatusValidationPass {
			continue
		}
		if tr.Replay.NonzeroShockBlocks > nonzero {
			nonzero = tr.Replay.NonzeroShockBlocks
			positive = tr.Replay.PositiveShockBlocks
		}
	}
	return nonzero, positive
}

func discoveryScreenTestedHypotheses(
	ledger playbook.ExperimentLedger,
	playbooks []playbook.FrozenEventPlaybook,
	playbookPValues []float64,
	deterministicPass []bool,
) (familySize int, testedP []float64, testedHashes []string) {
	pByHash := map[string]float64{}
	for i, pb := range playbooks {
		if i < len(playbookPValues) {
			pByHash[pb.PlaybookHash] = playbookPValues[i]
		}
	}
	passByHash := map[string]bool{}
	for i, pb := range playbooks {
		if i < len(deterministicPass) {
			passByHash[pb.PlaybookHash] = deterministicPass[i]
		}
	}
	if len(ledger.Attempts) == 0 {
		for i, pb := range playbooks {
			if !deterministicPass[i] {
				continue
			}
			testedP = append(testedP, playbookPValues[i])
			testedHashes = append(testedHashes, pb.PlaybookHash)
		}
		return len(testedP), testedP, testedHashes
	}
	familySize = len(ledger.Attempts)
	seenTested := map[string]bool{}
	for _, attempt := range ledger.Attempts {
		if attempt.PlaybookHash == "" {
			continue
		}
		if !passByHash[attempt.PlaybookHash] {
			continue
		}
		if seenTested[attempt.PlaybookHash] {
			continue
		}
		seenTested[attempt.PlaybookHash] = true
		testedP = append(testedP, pByHash[attempt.PlaybookHash])
		testedHashes = append(testedHashes, attempt.PlaybookHash)
	}
	return familySize, testedP, testedHashes
}

func eligibleTiersFromResults(tiers []TierResult) []string {
	var out []string
	for _, tr := range tiers {
		if tr.Status == StatusValidationPass {
			out = append(out, tierLabel(tr.CapitalTier))
		}
	}
	return out
}

func tierLabel(tier int) string {
	return fmt.Sprintf("%d", tier)
}

func collapsePlaybookStatus(tiers []TierResult) (status, reason string) {
	if len(tiers) == 0 {
		return StatusValidationFail, ReasonCoverage
	}
	hasPass := false
	worst := StatusValidationPass
	for _, tr := range tiers {
		if tr.Status == StatusValidationPass {
			hasPass = true
		}
		if statusRank(tr.Status) > statusRank(worst) {
			worst = tr.Status
			reason = tr.FailReason
		}
	}
	if hasPass && !protocol.ValidationAllTiersMustPass {
		return StatusValidationPass, ""
	}
	if hasPass {
		return StatusValidationPass, ""
	}
	return worst, reason
}

func frozenEdgeFromTiers(tiers []TierResult) (edge, stress float64) {
	for _, tr := range tiers {
		if tr.Status != StatusValidationPass {
			continue
		}
		if tr.FrozenConservativeMeanBPS != 0 {
			edge = tr.FrozenConservativeMeanBPS
			stress = tr.StressConservativeMeanBPS
		}
	}
	return edge, stress
}

func maxCapacityFromTiers(tiers []TierResult) float64 {
	maxCap := 0.0
	for _, tr := range tiers {
		if tr.Status != StatusValidationPass {
			continue
		}
		if tr.Replay.CapacityCliffUSDT > 0 && (maxCap == 0 || tr.Replay.CapacityCliffUSDT < maxCap) {
			maxCap = tr.Replay.CapacityCliffUSDT
		}
	}
	return maxCap
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
