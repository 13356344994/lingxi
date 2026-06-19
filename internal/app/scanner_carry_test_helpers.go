package app

import "time"

// scannerTestCarryNearFundingRate 供 carry 扫描器单测:在 30min 结算窗口内 first-window EV 仍为正。
const scannerTestCarryNearFundingRate = 0.0025

func scannerTestCarryNextFunding(now time.Time) time.Time {
	return now.Add(30 * time.Minute)
}

func scannerTestCarryLowFundingVol(perpVenue, symbol string) map[string]map[string]float64 {
	return map[string]map[string]float64{
		perpVenue: {symbol: 0.00001},
	}
}
