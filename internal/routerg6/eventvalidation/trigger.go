package eventvalidation

import (
	"time"

	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

// FeatureSnapshot holds decision-bar features for trigger matching.
type FeatureSnapshot struct {
	ReturnBPS      float64
	PriorReturnBPS float64
	RMSBPS         float64
	AvgQuoteVolume float64
	VolumeSpike    float64
	BTCRelativeBPS float64
	SessionBoundary bool
}

// MatchCardFamily reports whether detected families include the card family with trigger conditions.
func MatchCardFamily(
	card reverseevent.FrozenEventCard,
	families []string,
	features FeatureSnapshot,
) bool {
	want := card.EventFamily
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
	return MatchTriggerConditions(card.TriggerConditions, features)
}

// MatchTriggerConditions evaluates frozen reverse-derived trigger predicates.
func MatchTriggerConditions(spec reverseevent.TriggerSpec, features FeatureSnapshot) bool {
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

func matchOneCondition(cond reverseevent.TriggerCondition, features FeatureSnapshot) bool {
	switch cond.Field {
	case "abs_return_bps":
		return compareFloat(features.ReturnBPS, cond.Op, cond.Value)
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

// FeaturesFromBar extracts trigger features at one decision bar.
func FeaturesFromBar(
	bars []eventmap.Bar,
	idx int,
	spec eventmap.ProfileSpec,
	btcBars []eventmap.Bar,
	windowBars, primaryBack int,
	decision time.Time,
	barDuration time.Duration,
) FeatureSnapshot {
	ret, priorRet, rms, avgQuote, btcRel := featureContextAt(bars, idx, spec, btcBars, windowBars, primaryBack)
	spikeWindow := windowBars / 4
	if spikeWindow < 1 {
		spikeWindow = 1
	}
	return FeatureSnapshot{
		ReturnBPS:       ret,
		PriorReturnBPS:  priorRet,
		RMSBPS:          rms,
		AvgQuoteVolume:  avgQuote,
		VolumeSpike:     avgQuoteAt(bars, idx, spikeWindow),
		BTCRelativeBPS:  btcRel,
		SessionBoundary: isSessionBoundaryBar(decision, barDuration),
	}
}

func featureContextAt(
	bars []eventmap.Bar,
	idx int,
	spec eventmap.ProfileSpec,
	btcBars []eventmap.Bar,
	windowBars, primaryBack int,
) (returnBPS, priorReturnBPS, rmsBPS, avgQuote, btcRel float64) {
	_ = spec
	returnBPS = logReturnBPS(bars, idx, primaryBack)
	priorReturnBPS = logReturnBPS(bars, idx-primaryBack, primaryBack)
	rmsBPS = realizedRMSBPS(bars, idx, primaryBack)
	avgQuote = avgQuoteAt(bars, idx, windowBars)
	btcIdx := barIndexAt(btcBars, decisionTimeAt(bars, idx, spec.BarDuration), spec.BarDuration)
	if btcIdx >= primaryBack {
		symRet := returnBPS
		btcRet := logReturnBPS(btcBars, btcIdx, primaryBack)
		if isFinite(btcRet) && isFinite(symRet) {
			btcRel = symRet - btcRet
		}
	}
	return returnBPS, priorReturnBPS, rmsBPS, avgQuote, btcRel
}

func isSessionBoundaryBar(decision time.Time, barDuration time.Duration) bool {
	utc := decision.UTC()
	switch utc.Hour() {
	case 0, 8, 16:
		barMinutes := int(barDuration / time.Minute)
		if barMinutes <= 0 {
			barMinutes = 60
		}
		return utc.Minute() < 2*barMinutes
	default:
		return false
	}
}
