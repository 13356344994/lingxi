package eventvalidation

import (
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

func primaryReturnBars(spec eventmap.ProfileSpec) int {
	if spec.ID == eventmap.ProfileOKX5M {
		return 12
	}
	return 1
}

func rollingWindowBars(spec eventmap.ProfileSpec) int {
	return profitmap.RollingWindowBars(spec)
}

func decisionTimeAt(bars []eventmap.Bar, idx int, barDuration time.Duration) time.Time {
	return profitmap.DecisionTime(bars, idx, barDuration)
}

func barIndexAt(bars []eventmap.Bar, decision time.Time, barDuration time.Duration) int {
	return profitmap.BarIndexAt(bars, decision, barDuration)
}

func avgQuoteAt(bars []eventmap.Bar, idx, window int) float64 {
	return profitmap.AvgQuoteVolumeUSDT(bars, idx, window)
}

func logReturnBPS(bars []eventmap.Bar, idx, lookback int) float64 {
	if idx < lookback || idx >= len(bars) {
		return math.NaN()
	}
	prev := bars[idx-lookback].Close
	cur := bars[idx].Close
	if prev <= 0 || cur <= 0 {
		return math.NaN()
	}
	return (cur/prev - 1) * 10000
}

func realizedRMSBPS(bars []eventmap.Bar, idx, lookback int) float64 {
	if idx < lookback {
		return math.NaN()
	}
	var sum float64
	for i := idx - lookback + 1; i <= idx; i++ {
		r := logReturnBPS(bars, i, 1)
		if !isFinite(r) {
			continue
		}
		sum += r * r
	}
	if lookback <= 0 {
		return math.NaN()
	}
	return math.Sqrt(sum / float64(lookback))
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func horizonSpec(spec eventmap.ProfileSpec, label string) (profitmap.HorizonSpec, bool) {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h, true
		}
	}
	return profitmap.HorizonSpec{}, false
}

func classifyOutcome(conservativeBPS float64) string {
	if conservativeBPS > 0 {
		return OutcomeWinner
	}
	if conservativeBPS < 0 {
		return OutcomeLoss
	}
	return OutcomeNeutral
}
