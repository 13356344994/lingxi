package brain

import (
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg6/eventmap"
)

// MatchPlaybooks evaluates trigger conditions and returns matched events for one bar.
func MatchPlaybooks(playbooks []playbook.FrozenEventPlaybook, feat BarFeatures, barIndex int) []MatchedEvent {
	var out []MatchedEvent
	for _, pb := range playbooks {
		if pb.ProfileID != feat.ProfileID {
			continue
		}
		if !triggerFires(pb.TriggerConditions, feat) {
			continue
		}
		phase := eventPhase(barIndex, barIndex, pb)
		health, longGross, shortGross := thesisHealth(pb, feat, phase)
		capUSDT := parseCapacityBand(pb.CapacityBand)
		deadline := barIndex + horizonBars(pb.Horizon, pb.ProfileID)
		me := MatchedEvent{
			Playbook:       pb,
			EventFamily:    pb.EventFamily,
			EventID:        eventID(feat.Symbol, feat.ProfileID, pb.EventFamily, barIndex),
			EventPhase:     phase,
			ThesisHealth:   health,
			GrossLongBPS:   longGross,
			GrossShortBPS:  shortGross,
			CompletionBars: horizonBars(pb.Horizon, pb.ProfileID),
			CapacityUSDT:   capUSDT,
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

func triggerFires(spec playbook.TriggerSpec, feat BarFeatures) bool {
	if len(spec.Conditions) == 0 {
		return false
	}
	for _, cond := range spec.Conditions {
		if !evalCondition(cond, feat) {
			return false
		}
	}
	return true
}

func evalCondition(cond playbook.TriggerCondition, feat BarFeatures) bool {
	field := strings.ToLower(strings.TrimSpace(cond.Field))
	switch field {
	case "abs_return_bps":
		return math.Abs(feat.ReturnBPS) >= cond.Value
	case "rms_bps":
		return feat.BarRangeBPS >= cond.Value
	case "return_sign":
		if cond.Op == "eq_prior_sign" {
			return sign(feat.ReturnBPS) == sign(feat.PriorReturnBPS) && feat.ReturnBPS != 0
		}
		if cond.Op == "ne_prior_sign" {
			return sign(feat.ReturnBPS) != sign(feat.PriorReturnBPS) && feat.PriorReturnBPS != 0
		}
	case "btc_relative_abs_bps":
		return math.Abs(feat.BTCRelativeBPS) >= cond.Value
	case "event_family":
		return strings.TrimSpace(cond.Note) != ""
	default:
		return false
	}
	return false
}

func thesisHealth(pb playbook.FrozenEventPlaybook, feat BarFeatures, phase float64) (health string, longGross, shortGross float64) {
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
	case eventmap.FamilyExtremeReversal:
		if sign(feat.ReturnBPS) != sign(feat.PriorReturnBPS) {
			health = ThesisHealthy
			shortGross = math.Abs(feat.ReturnBPS) * 0.75
			longGross = math.Abs(feat.ReturnBPS) * 0.25
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

func eventPhase(barIndex, triggerBar int, pb playbook.FrozenEventPlaybook) float64 {
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

func parseCapacityBand(band string) float64 {
	if band == "" || strings.Contains(band, "unbounded") {
		return 0
	}
	var cap float64
	_, _ = fmt.Sscanf(band, "0_%f_usdt", &cap)
	return cap
}

func eventID(symbol, profileID, family string, barIndex int) string {
	return fmt.Sprintf("%s|%s|%s|%d", symbol, profileID, family, barIndex)
}

func invalidatorBar(pb playbook.FrozenEventPlaybook, feat BarFeatures, bar int) int {
	if feat.OOD || thesisInvalidated(pb.EventFamily, feat) {
		return bar + 1
	}
	return 0
}

func completeBar(pb playbook.FrozenEventPlaybook, bar int) int {
	return bar + horizonBars(pb.Horizon, pb.ProfileID)
}

func payerGoneBar(pb playbook.FrozenEventPlaybook, bar int) int {
	if pb.PayerStatus == playbook.PayerStatusUnexplained {
		return 0
	}
	return bar + horizonBars(pb.Horizon, pb.ProfileID)/2
}

func oodBar(feat BarFeatures, bar int) int {
	if feat.OOD {
		return bar
	}
	return 0
}

func thesisInvalidated(family string, feat BarFeatures) bool {
	_, longGross, shortGross := thesisHealth(playbook.FrozenEventPlaybook{EventFamily: family}, feat, 0)
	return longGross <= 0 && shortGross <= 0
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

// EventDecisionTime returns canonical decision timestamp for hashing.
func EventDecisionTime(t time.Time) time.Time {
	return t.UTC()
}
