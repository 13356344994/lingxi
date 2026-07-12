package eventmap

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
)

func sessionBucket(decision time.Time) string {
	hour := decision.UTC().Hour()
	switch {
	case hour >= 0 && hour < 8:
		return "asia"
	case hour >= 8 && hour < 16:
		return "europe"
	default:
		return "us"
	}
}

func isSessionBoundaryBar(decision time.Time, barDuration time.Duration) bool {
	utc := decision.UTC()
	// First two bars after UTC session opens at 0/8/16.
	switch utc.Hour() {
	case 0, 8, 16:
		minute := utc.Minute()
		barMinutes := int(barDuration / time.Minute)
		if barMinutes <= 0 {
			barMinutes = 60
		}
		return minute < 2*barMinutes
	default:
		return false
	}
}

func featureContext(bars []Bar, idx int, spec ProfileSpec, btcBars []Bar, windowBars, primaryBack int) (returnBPS, priorReturnBPS, rmsBPS, avgQuote, btcRel float64) {
	decision := profitmap.DecisionTime(bars, idx, spec.BarDuration)
	returnBPS = logReturnBPS(bars, idx, primaryBack)
	priorReturnBPS = logReturnBPS(bars, idx-primaryBack, primaryBack)
	rmsBPS = realizedRMSBPS(bars, idx, primaryBack)
	avgQuote = profitmap.AvgQuoteVolumeUSDT(bars, idx, windowBars)
	btcIdx := profitmap.BarIndexAt(btcBars, decision, spec.BarDuration)
	if btcIdx >= primaryBack {
		symRet := returnBPS
		btcRet := logReturnBPS(btcBars, btcIdx, primaryBack)
		if isFinite(btcRet) && isFinite(symRet) {
			btcRel = symRet - btcRet
		}
	}
	return returnBPS, priorReturnBPS, rmsBPS, avgQuote, btcRel
}
