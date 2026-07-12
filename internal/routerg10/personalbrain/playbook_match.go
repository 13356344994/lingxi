package personalbrain

import (
	"fmt"
	"math"
	"strings"

	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg6/eventmap"
)

// MatchPersonalPlaybooks evaluates public trigger conditions for certified playbooks.
func MatchPersonalPlaybooks(playbooks []personalplaybook.PersonalWildPlaybook, feat BarFeatures, barIndex int) []MatchedPersonalEvent {
	var out []MatchedPersonalEvent
	for _, pb := range playbooks {
		if pb.ProfileID != feat.ProfileID {
			continue
		}
		if !publicTriggerFires(pb.PublicTrigger, feat) {
			continue
		}
		proofs := proofsFromPlaybook(pb)
		phase := eventPhase(barIndex, barIndex, pb)
		health, longGross, shortGross := thesisHealth(pb, feat, phase)
		capUSDT := pb.RiskNarrative.MaxCapacityUSDT
		maxSize := pb.RiskNarrative.MaxSizeUSDT
		deadline := barIndex + horizonBars(pb.Horizon, pb.ProfileID)
		me := MatchedPersonalEvent{
			Playbook:       pb,
			Proofs:         proofs,
			EventID:        eventID(feat.Symbol, feat.ProfileID, pb.EventFamily, barIndex),
			EventFamily:    pb.EventFamily,
			EventPhase:     phase,
			ThesisHealth:   health,
			Direction:      pb.Direction,
			GrossLongBPS:   longGross,
			GrossShortBPS:  shortGross,
			CompletionBars: horizonBars(pb.Horizon, pb.ProfileID),
			CapacityUSDT:   capUSDT,
			MaxSizeUSDT:    maxSize,
			DeadlineBar:    deadline,
			InvalidatorBar: invalidatorBar(pb, feat, barIndex),
			CompleteBar:    completeBar(pb, barIndex),
			PayerGoneBar:   payerGoneBar(pb, barIndex),
			OODBar:         oodBar(feat, barIndex),
		}
		out = append(out, me)
	}
	return out
}

func proofsFromPlaybook(pb personalplaybook.PersonalWildPlaybook) VerifiedProofs {
	exclusionAccepted := personalplaybook.ValidateExclusionAccepted(pb.ProofBinding, pb.ExclusionNarrative) == nil
	makerForbid := strings.Contains(pb.ExecutionNarrative.MakerSelectionConclusion, "forbid_maker") ||
		strings.Contains(pb.ExecutionNarrative.MakerSelectionConclusion, "adverse_selection")
	return VerifiedProofs{
		ConstitutionHash:      pb.ProofBinding.ConstitutionHash,
		ExclusionProofHash:    pb.ProofBinding.ExclusionProofHash,
		InstitutionAxisHash:   pb.ProofBinding.InstitutionAxisHash,
		RetailAxisHash:        pb.ProofBinding.RetailAxisHash,
		BotAxisHash:           pb.ProofBinding.BotAxisHash,
		EntryProofHash:        pb.ProofBinding.EntryProofHash,
		ExecutionPolicyHash:   pb.ProofBinding.ExecutionPolicyHash,
		ExitProofHash:         pb.ProofBinding.ExitProofHash,
		AnnouncementTapeHash:  pb.ProofBinding.AnnouncementTapeHash,
		EntryGrade:            entryGradeFromPlaybook(pb),
		EntryCertified:        false,
		ExitCertified:         false,
		ExclusionAccepted:     exclusionAccepted,
		CrowdingPassed:        pb.StopNarrative.CrowdingStopRule != "crowding_failed",
		MakerGateForbid:       makerForbid,
		DynamicMinNotional:    pb.EntryNarrative.DynamicMinNotionalUSDT,
		ExecutableDepthUSDT:   pb.EntryNarrative.ExecutableDepthUSDT,
	}
}

func entryGradeFromPlaybook(pb personalplaybook.PersonalWildPlaybook) string {
	return ""
}

func publicTriggerFires(spec personalplaybook.PublicTriggerSpec, feat BarFeatures) bool {
	if len(spec.Conditions) == 0 {
		return false
	}
	for _, cond := range spec.Conditions {
		if !evalPublicCondition(cond, feat) {
			return false
		}
	}
	return true
}

func evalPublicCondition(cond personalplaybook.PublicTriggerCondition, feat BarFeatures) bool {
	field := strings.ToLower(strings.TrimSpace(cond.Field))
	op := strings.ToLower(strings.TrimSpace(cond.Op))
	switch field {
	case "abs_return_bps":
		return compareFloat(op, math.Abs(feat.ReturnBPS), cond.Value)
	case "rms_bps":
		return compareFloat(op, feat.BarRangeBPS, cond.Value)
	case "return_sign":
		if op == "eq_prior_sign" {
			return sign(feat.ReturnBPS) == sign(feat.PriorReturnBPS) && feat.ReturnBPS != 0
		}
		if op == "ne_prior_sign" {
			return sign(feat.ReturnBPS) != sign(feat.PriorReturnBPS) && feat.PriorReturnBPS != 0
		}
	case "btc_relative_abs_bps":
		return compareFloat(op, math.Abs(feat.BTCRelativeBPS), cond.Value)
	case "visible_announcement_count":
		return compareFloat(op, float64(feat.VisibleAnnouncementCount), cond.Value)
	case "cross_venue_shock_cluster":
		return compareStringEq(op, feat.CrossVenueShockCluster, cond.Note)
	case "dynamic_min_notional_usdt":
		return compareFloat(op, feat.DynamicMinNotionalUSDT, cond.Value)
	case "min_lot_nonlinearity":
		return compareFloat(op, feat.MinLotNonlinearity, cond.Value)
	case "event_family":
		return compareStringEq(op, feat.EventFamily, cond.Note)
	case "mechanical_flow_exhaustion":
		return compareFloat(op, feat.MechanicalFlowExhaustion, cond.Value)
	case "prepositioned_capital_window":
		return compareFloat(op, feat.PrepositionedCapitalWindow, cond.Value)
	case "custody_authorization_limit":
		return compareFloat(op, feat.CustodyAuthorizationLimit, cond.Value)
	case "event_sparsity":
		return compareFloat(op, feat.EventSparsity, cond.Value)
	case "dynamic_threshold_shift":
		return compareFloat(op, feat.DynamicThresholdShift, cond.Value)
	case "personal_tape_family":
		return compareStringEq(op, feat.PersonalTapeFamily, cond.Note)
	case "horizon":
		return compareStringEq(op, feat.Horizon, cond.Note)
	default:
		return false
	}
	return false
}

func compareFloat(op string, actual, threshold float64) bool {
	switch op {
	case "gte":
		return actual >= threshold
	case "gt":
		return actual > threshold
	case "lte":
		return actual <= threshold
	case "lt":
		return actual < threshold
	case "eq":
		return actual == threshold
	default:
		return false
	}
}

func compareStringEq(op, actual, expected string) bool {
	if op != "eq" {
		return false
	}
	return strings.TrimSpace(actual) == strings.TrimSpace(expected) && expected != ""
}

func thesisHealth(pb personalplaybook.PersonalWildPlaybook, feat BarFeatures, phase float64) (health string, longGross, shortGross float64) {
	longGross = feat.ReturnBPS
	shortGross = -feat.ReturnBPS
	switch pb.EventFamily {
	case eventmap.FamilyExtremeContinuation:
		if sign(feat.ReturnBPS) == sign(feat.PriorReturnBPS) && math.Abs(feat.ReturnBPS) > 10 {
			health = ThesisHealthy
			longGross = math.Abs(feat.ReturnBPS) * 0.8
			shortGross = -math.Abs(feat.ReturnBPS) * 0.3
		} else if sign(feat.ReturnBPS) != sign(feat.PriorReturnBPS) {
			health = ThesisInvalidated
			longGross = 5
			shortGross = math.Abs(feat.ReturnBPS) * 0.7
		} else {
			health = ThesisWeakening
		}
	default:
		if phase < 0.7 {
			health = ThesisHealthy
		} else {
			health = ThesisWeakening
		}
	}
	if feat.OOD {
		health = ThesisInvalidated
	}
	if phase >= 1.0 {
		health = ThesisInvalidated
	}
	return health, longGross, shortGross
}

func eventPhase(barIndex, triggerBar int, pb personalplaybook.PersonalWildPlaybook) float64 {
	span := float64(horizonBars(pb.Horizon, pb.ProfileID))
	if span <= 0 {
		return 0
	}
	elapsed := float64(barIndex - triggerBar)
	if elapsed < 0 {
		return 0
	}
	p := elapsed / span
	if p > 1 {
		return 1
	}
	return p
}

func horizonBars(horizon, profileID string) int {
	switch horizon {
	case "5m":
		return 1
	case "15m":
		return 3
	case "1h":
		return 1
	case "4h":
		return 4
	case "24h":
		return 24
	default:
		if profileID == eventmap.ProfileOKX5M {
			return 12
		}
		return 4
	}
}

func eventID(symbol, profileID, family string, barIndex int) string {
	return fmt.Sprintf("%s|%s|%s|%d", symbol, profileID, family, barIndex)
}

func invalidatorBar(pb personalplaybook.PersonalWildPlaybook, feat BarFeatures, bar int) int {
	if feat.OOD {
		return bar + 1
	}
	return 0
}

func completeBar(pb personalplaybook.PersonalWildPlaybook, bar int) int {
	return bar + horizonBars(pb.Horizon, pb.ProfileID)
}

func payerGoneBar(pb personalplaybook.PersonalWildPlaybook, bar int) int {
	return bar + horizonBars(pb.Horizon, pb.ProfileID)/2
}

func oodBar(feat BarFeatures, bar int) int {
	if feat.OOD {
		return bar
	}
	return 0
}

func sign(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

func pickPrimaryEvent(events []MatchedPersonalEvent) MatchedPersonalEvent {
	if len(events) == 0 {
		return MatchedPersonalEvent{}
	}
	best := events[0]
	bestScore := personalRankScore(events[0])
	for _, ev := range events[1:] {
		score := personalRankScore(ev)
		if score > bestScore {
			best = ev
			bestScore = score
		}
	}
	return best
}

func personalRankScore(ev MatchedPersonalEvent) float64 {
	if !ev.Playbook.Certified {
		return 0
	}
	score := ev.Playbook.RiskNarrative.MaxCapacityUSDT
	if ev.CompletionBars > 0 {
		score = score / float64(ev.CompletionBars)
	}
	if ev.Direction == "long" {
		score += ev.GrossLongBPS
	} else {
		score += ev.GrossShortBPS
	}
	return score
}
