package eventmap

import (
	"fmt"
	"time"

	"lingxi/internal/routerg4/profitmap"
)

// BuildEventID freezes event_id = symbol + decision_time + event_family.
func BuildEventID(symbol string, decision time.Time, family string) string {
	return fmt.Sprintf("%s|%s|%s", symbol, decision.UTC().Format(time.RFC3339Nano), family)
}

// DetectFamilies evaluates frozen feature thresholds at one decision bar.
// Outcomes are never used for detection.
func DetectFamilies(
	symbol string,
	bars []Bar,
	idx int,
	spec ProfileSpec,
	boundaries Boundaries,
	th FrozenThresholds,
	btcBars []Bar,
	windowBars int,
) []string {
	if profitmap.RejectCurrentBarOpen(bars, idx, spec.BarDuration) {
		return nil
	}
	decision := profitmap.DecisionTime(bars, idx, spec.BarDuration)
	partition := boundaries.PartitionAt(decision)
	if partition == "" {
		return nil
	}
	primaryBack := primaryReturnBars(spec)
	if idx < primaryBack*2 {
		return nil
	}
	ret, priorRet, rms, avgQuote, btcRel := featureContext(bars, idx, spec, btcBars, windowBars, primaryBack)
	if !isFinite(ret) || !isFinite(priorRet) || !isFinite(rms) {
		return nil
	}

	var families []string
	absRet := abs(ret)
	if absRet >= th.ExtremeReturnAbsBPS && rms >= th.ExtremeRMSBPS {
		if sign(ret) == sign(priorRet) && priorRet != 0 {
			families = append(families, FamilyExtremeContinuation)
		}
		if sign(ret) != sign(priorRet) && priorRet != 0 {
			families = append(families, FamilyExtremeReversal)
		}
	}
	if isSessionBoundaryBar(decision, spec.BarDuration) &&
		avgQuote <= th.LowLiquidityQuote &&
		profitmap.AvgQuoteVolumeUSDT(bars, idx, maxInt(1, windowBars/4)) >= th.VolumeSpikeQuote {
		families = append(families, FamilySessionLiquidity)
	}
	if isFinite(btcRel) && abs(btcRel) >= th.BTCRelativeAbsBPS {
		families = append(families, FamilyBTCRelative)
	}
	return families
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
