package validation

import (
	"lingxi/internal/routerg10/playbook"
)

// FeatureSnapshot holds decision-bar features for trigger matching.
type FeatureSnapshot struct {
	ReturnBPS         float64
	PriorReturnBPS    float64
	RMSBPS            float64
	AvgQuoteVolume    float64
	VolumeSpike       float64
	BTCRelativeBPS    float64
	SessionBoundary   bool
}

// MatchPlaybookFamily reports whether detected families include the playbook family with trigger conditions.
func MatchPlaybookFamily(
	pb playbook.FrozenEventPlaybook,
	families []string,
	features FeatureSnapshot,
) bool {
	want := pb.EventFamily
	found := false
	for _, family := range families {
		if family == want {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	return MatchTriggerConditions(pb.TriggerConditions, features)
}

// MatchTriggerConditions evaluates frozen reverse-derived trigger predicates.
func MatchTriggerConditions(spec playbook.TriggerSpec, features FeatureSnapshot) bool {
	if len(spec.Conditions) == 0 {
		return false
	}
	for _, cond := range spec.Conditions {
		if !matchOneCondition(cond, features) {
			return false
		}
	}
	return true
}

func matchOneCondition(cond playbook.TriggerCondition, features FeatureSnapshot) bool {
	switch cond.Field {
	case "abs_return_bps":
		return compareFloat(abs(features.ReturnBPS), cond.Op, cond.Value)
	case "rms_bps":
		return compareFloat(features.RMSBPS, cond.Op, cond.Value)
	case "return_sign":
		switch cond.Op {
		case "eq_prior_sign":
			return sign(features.ReturnBPS) == sign(features.PriorReturnBPS) && features.PriorReturnBPS != 0
		case "ne_prior_sign":
			return sign(features.ReturnBPS) != sign(features.PriorReturnBPS) && features.PriorReturnBPS != 0
		}
	case "session_boundary":
		if cond.Op == "eq" && cond.Value == 1 {
			return features.SessionBoundary
		}
	case "avg_quote_volume":
		return compareFloat(features.AvgQuoteVolume, cond.Op, cond.Value)
	case "volume_spike":
		return compareFloat(features.VolumeSpike, cond.Op, cond.Value)
	case "btc_relative_abs_bps":
		return compareFloat(abs(features.BTCRelativeBPS), cond.Op, cond.Value)
	case "event_family":
		if cond.Op == "eq" {
			return true
		}
	}
	return false
}

func compareFloat(value float64, op string, threshold float64) bool {
	switch op {
	case "gte":
		return value >= threshold
	case "lte":
		return value <= threshold
	case "gt":
		return value > threshold
	case "lt":
		return value < threshold
	case "eq":
		return value == threshold
	default:
		return false
	}
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// InferDirection picks the realized branch direction for one matched trigger.
func InferDirection(pb playbook.FrozenEventPlaybook, features FeatureSnapshot) string {
	switch pb.EventFamily {
	case "ohlcv_extreme_continuation":
		if sign(features.ReturnBPS) == sign(features.PriorReturnBPS) && features.PriorReturnBPS != 0 {
			return "long"
		}
		if sign(features.ReturnBPS) != sign(features.PriorReturnBPS) && features.PriorReturnBPS != 0 {
			return "short"
		}
		return "flat"
	case "ohlcv_extreme_reversal":
		if features.ReturnBPS > 0 {
			return "short"
		}
		if features.ReturnBPS < 0 {
			return "long"
		}
		return "flat"
	default:
		if features.ReturnBPS >= 0 {
			return "long"
		}
		return "short"
	}
}
