package personalvalidation

import (
	g4protocol "lingxi/internal/routerg4/protocol"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalplaybook"
)

const maxSingleEventContribution = 0.35
const maxSingleDayContribution = 0.40

// EvaluateTierGates applies frozen G10P validation gates to one capital tier replay.
func EvaluateTierGates(replay TierReplayStats, scenarios []TierScenarioResult, pit PITContaminationReport, maker MakerPathStats, exitClear bool) (status string, checks TierGateChecks, reason string) {
	checks = TierGateChecks{
		MinEventCountMet:               replay.Count >= ValidationMinEventCount,
		MinIndependentDaysMet:          replay.UniqueDays >= ValidationMinIndependentDays,
		MinShockBlocksMet:              replay.UniqueShockBlocks >= ValidationMinNonOverlapShockBlocks,
		MaxContributionOK:              replay.MaxPositiveContributionFraction <= maxSingleEventContribution+1e-12,
		MaxDayContributionOK:           replay.MaxDayPositiveContributionFraction <= maxSingleDayContribution+1e-12,
		BaseNetReturnNonNegative:       true,
		AllDelayNetReturnNonNegative:   true,
		CostStressNetReturnNonNegative: true,
		P90SlippageNetReturnNonNegative: true,
		NoLeverageSolvencyBreach:       true,
		TerminalRiskCeilingNotTarget:   true,
		PITContaminationClear:          !pit.ContaminationDetected,
		MakerSelectionBiasClear:        !maker.MakerGateForbid,
		ExitProofClear:                 exitClear,
	}
	if !checks.MinEventCountMet || !checks.MinIndependentDaysMet || !checks.MinShockBlocksMet {
		checks.SummaryNote = InconclusiveNotZeroEdgeNote
		return StatusInconclusive, checks, ReasonInsufficientEvidence
	}
	for _, s := range scenarios {
		if !s.NetReturnNonNegative {
			if s.P90SlippageStress {
				checks.P90SlippageNetReturnNonNegative = false
			} else if s.CostStressMultiplier > 1.0+1e-9 {
				checks.CostStressNetReturnNonNegative = false
			} else if s.DelayBars > 0 {
				checks.AllDelayNetReturnNonNegative = false
			} else {
				checks.BaseNetReturnNonNegative = false
			}
		}
		if s.DelayBars == 0 && s.CostStressMultiplier <= 1.0+1e-9 && !s.P90SlippageStress {
			if s.LeverageBreach || s.SolvencyBreach {
				checks.NoLeverageSolvencyBreach = false
			}
			if s.TerminalRiskCeilingHit {
				checks.TerminalRiskCeilingNotTarget = false
			}
		}
	}
	if !checks.PITContaminationClear {
		return StatusValidationFail, checks, ReasonPITContamination
	}
	if !checks.MakerSelectionBiasClear {
		return StatusValidationFail, checks, ReasonMakerSelectionBias
	}
	if !checks.ExitProofClear {
		return StatusValidationFail, checks, ReasonExitUnproven
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
	if !checks.P90SlippageNetReturnNonNegative {
		return StatusValidationFail, checks, ReasonCost
	}
	if !checks.BaseNetReturnNonNegative {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	if replay.NonzeroShockBlocks >= ValidationMinNonOverlapShockBlocks {
		if _, err := g4protocol.ExactOneSidedPositiveBinomialSignP(replay.PositiveShockBlocks, replay.NonzeroShockBlocks-replay.PositiveShockBlocks); err != nil {
			return StatusValidationFail, checks, ReasonCoverage
		}
	}
	return StatusValidationPass, checks, ""
}

// BuildPlaybookResults evaluates all playbooks with per-tier gates and layered reports.
func BuildPlaybookResults(
	playbooks []personalplaybook.PersonalWildPlaybook,
	tierOutcomes map[string][]TierResult,
	degeneracyByPlaybook map[string]DegeneracySummary,
	makerByPlaybook map[string]MakerPathStats,
	pitByPlaybook map[string]PITContaminationReport,
	crowdingByPlaybook map[string]CrowdingReport,
	leaveOneByPlaybook map[string]LeaveOneReport,
	eventsByPlaybook map[string][]TriggerEvent,
) []PlaybookResult {
	results := make([]PlaybookResult, len(playbooks))
	for i, pb := range playbooks {
		tiers := tierOutcomes[pb.PlaybookHash]
		deg := degeneracyByPlaybook[pb.PlaybookHash]
		maker := makerByPlaybook[pb.PlaybookHash]
		pit := pitByPlaybook[pb.PlaybookHash]
		crowding := crowdingByPlaybook[pb.PlaybookHash]
		leaveOne := leaveOneByPlaybook[pb.PlaybookHash]
		eligible := eligibleTiersFromResults(tiers)
		overall, reason := collapsePlaybookStatus(tiers)
		if deg.Degenerate {
			overall = StatusValidationFail
			reason = ReasonBrainDegeneracy
			eligible = nil
		}
		if crowding.AnyCrowdingFail {
			if overall == StatusValidationPass {
				overall = StatusValidationFail
				reason = ReasonCrowding
			}
		}
		layers := EvaluateLayers(tiers, maker, pit, deg, leaveOne, len(eventsByPlaybook[pb.PlaybookHash]) > 0, eventsByPlaybook[pb.PlaybookHash])
		maxCap := maxCapacityFromTiers(tiers)
		if maxCap <= 0 {
			maxCap = pb.RiskNarrative.MaxCapacityUSDT
		}
		results[i] = PlaybookResult{
			Ordinal: pb.Ordinal, PlaybookHash: pb.PlaybookHash, ExperimentHash: pb.ExperimentHash,
			ProfileID: pb.ProfileID, EventFamily: pb.EventFamily, PersonalTapeFamily: pb.PersonalTapeFamily,
			Horizon: pb.Horizon, Status: overall, FailReason: reason, Layers: layers,
			TierResults: tiers, EligibleCapitalTiers: eligible, MaxCapacityUSDT: maxCap,
			MakerStats: maker, PITReport: pit, CrowdingReport: crowding, LeaveOne: leaveOne,
			BrainDegeneracy: deg, Playbook: pb,
			TradeAllowed: false, ExamEligible: false, ExamActions: g10protocolv2.ExamActions,
		}
	}
	return results
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
	if tier == int(CapitalTiersUSDT[2]) {
		return "actual"
	}
	return itoa(tier)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func collapsePlaybookStatus(tiers []TierResult) (status, reason string) {
	if len(tiers) == 0 {
		return StatusInconclusive, ReasonInsufficientEvidence
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
	if hasPass {
		return StatusValidationPass, ""
	}
	return worst, reason
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
