package features

import (
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
)

// BTCFeatures holds continuous sky-style BTC state at one decision.
type BTCFeatures struct {
	Return       float64
	Vol          float64
	Trend        float64
	Drawdown     float64
	Funding      float64
	FundingKnown bool
	Confidence   float64
}

// ComputeBTCFeatures derives continuous BTC features from profile-frequency bars.
func ComputeBTCFeatures(bars []profitmap.Bar, funding []profitmap.FundingPoint, profile Profile, decisionTime time.Time) BTCFeatures {
	out := BTCFeatures{
		Return: math.NaN(), Vol: math.NaN(), Trend: math.NaN(), Drawdown: math.NaN(), Funding: math.NaN(),
	}
	idx := profitmap.BarIndexAt(bars, decisionTime, profile.BarDuration)
	if idx < 0 || profitmap.RejectCurrentBarOpen(bars, idx, profile.BarDuration) {
		return out
	}
	closes := closedBarCloses(bars, decisionTime, profile.BarDuration)
	if len(closes) == 0 {
		return out
	}
	lookback := profile.MaxLookbackBars()
	if v, ok := signedReturn(closes, lookback); ok {
		out.Return = v
	}
	if v, ok := trendEfficiency(closes, lookback); ok {
		out.Trend = v
	}
	if v, ok := realizedVolatility(closes, lookback); ok {
		out.Vol = v
	}
	if v, ok := drawdown(closes, profile.DrawdownLookbackBars()); ok {
		out.Drawdown = v
	}
	if v, ok := fundingState(funding, decisionTime, profile.MaxFeatureLookback); ok {
		out.Funding = v
		out.FundingKnown = true
	}
	out.Confidence = skyConfidence(out)
	return out
}

func closedBarCloses(bars []profitmap.Bar, decisionTime time.Time, barDuration time.Duration) []float64 {
	closes := make([]float64, 0, len(bars))
	for _, bar := range bars {
		if bar.OpenTime.Add(barDuration).After(decisionTime) {
			break
		}
		if bar.Confirmed && bar.Close > 0 {
			closes = append(closes, bar.Close)
		}
	}
	return closes
}

func lastN(values []float64, n int) []float64 {
	if len(values) < n {
		return nil
	}
	return values[len(values)-n:]
}

func signedReturn(closes []float64, barsBack int) (float64, bool) {
	window := lastN(closes, barsBack+1)
	if window == nil || window[0] <= 0 || window[len(window)-1] <= 0 {
		return 0, false
	}
	return window[len(window)-1]/window[0] - 1, true
}

func trendEfficiency(closes []float64, barsBack int) (float64, bool) {
	window := lastN(closes, barsBack+1)
	if window == nil {
		return 0, false
	}
	net := math.Abs(window[len(window)-1] - window[0])
	var path float64
	for i := 1; i < len(window); i++ {
		path += math.Abs(window[i] - window[i-1])
	}
	if path == 0 {
		return 0, true
	}
	return net / path, true
}

func realizedVolatility(closes []float64, barsBack int) (float64, bool) {
	window := lastN(closes, barsBack+1)
	if window == nil {
		return 0, false
	}
	var sumSq float64
	count := 0
	for i := 1; i < len(window); i++ {
		if window[i-1] <= 0 || window[i] <= 0 {
			return 0, false
		}
		r := math.Log(window[i] / window[i-1])
		sumSq += r * r
		count++
	}
	if count == 0 {
		return 0, false
	}
	return math.Sqrt(sumSq), true
}

func drawdown(closes []float64, barsBack int) (float64, bool) {
	window := lastN(closes, barsBack+1)
	if window == nil {
		return 0, false
	}
	peak := window[0]
	for _, c := range window {
		if c > peak {
			peak = c
		}
	}
	if peak <= 0 {
		return 0, false
	}
	last := window[len(window)-1]
	return (peak - last) / peak, true
}

func fundingState(funding []profitmap.FundingPoint, decisionTime time.Time, lookback time.Duration) (float64, bool) {
	windowStart := decisionTime.Add(-lookback)
	var sum float64
	var count int
	for _, point := range funding {
		if point.Time.After(decisionTime) {
			continue
		}
		if point.Time.After(windowStart) {
			sum += point.Rate
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

func skyConfidence(btc BTCFeatures) float64 {
	total := 4.0
	present := 0.0
	if isFinite(btc.Return) {
		present++
	}
	if isFinite(btc.Vol) {
		present++
	}
	if isFinite(btc.Trend) {
		present++
	}
	if isFinite(btc.Drawdown) {
		present++
	}
	if present == 0 {
		return 0
	}
	return present / total
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
