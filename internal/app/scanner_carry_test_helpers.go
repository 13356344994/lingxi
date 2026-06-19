package app

import "time"

// scannerTestCarryNearFundingRate 供 carry 扫描器单测:在 30min 结算窗口内 first-window EV 仍为正。
const scannerTestCarryNearFundingRate = 0.01

// scannerTestCarryNearNegativeFundingRate 供负 carry(perp-spot)单测:覆盖借币+滑点后 first-window EV 仍为正。
const scannerTestCarryNearNegativeFundingRate = 0.05

func scannerTestCarryNextFunding(now time.Time) time.Time {
	return now.Add(30 * time.Minute)
}

// scannerTestEstablishedFundingSnap 供单测:有 p90 历史+足样本 → 满置信 sizing。
func scannerTestEstablishedFundingSnap(rate float64, next time.Time) CrossVenueSpotPerpFundingSnap {
	return CrossVenueSpotPerpFundingSnap{
		Rate:                  rate,
		IntervalHours:         8,
		NextFundingTime:       next,
		SpikeThreshold:        rate * 0.5,
		FundingHistorySamples: 2,
	}
}

func scannerTestCarryLowFundingVol(perpVenue, symbol string) map[string]map[string]float64 {
	return map[string]map[string]float64{
		perpVenue: {symbol: 0.00001},
	}
}

func scannerTestCarryScannerWithVolHistory(venue, symbol string, rates []float64) *CrossVenueScanner {
	return &CrossVenueScanner{
		fundingHistory: map[string]map[string][]float64{
			venue: {symbol: append([]float64(nil), rates...)},
		},
	}
}
