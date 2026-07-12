package features

import (
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
	g4protocol "lingxi/internal/routerg4/protocol"
)

type SymbolLocal struct {
	Symbol              string
	Return              float64
	RV                  float64
	Beta                float64
	BTCRelativeReturn   float64
	AvgQuoteVolume      float64
	LiquidityScore      float64
	VolCostPenalty      float64
	CapacityHeadroom    float64
	CostEstimate        float64
}

// ComputeSymbolLocal derives symbol-local continuous features at decisionTime.
func ComputeSymbolLocal(
	symbol string,
	symbolBars []profitmap.Bar,
	btcBars []profitmap.Bar,
	profile Profile,
	decisionTime time.Time,
	capitalUSDT float64,
) (SymbolLocal, bool) {
	idx := profitmap.BarIndexAt(symbolBars, decisionTime, profile.BarDuration)
	if idx < 0 || profitmap.RejectCurrentBarOpen(symbolBars, idx, profile.BarDuration) {
		return SymbolLocal{}, false
	}
	btcIdx := profitmap.BarIndexAt(btcBars, decisionTime, profile.BarDuration)
	if btcIdx < 0 {
		return SymbolLocal{}, false
	}
	window := profile.RollingQuoteWindowBars()
	retBars := profile.PrimaryReturnBars()
	out := SymbolLocal{Symbol: symbol}
	out.Return = logReturn(symbolBars, idx, retBars)
	out.RV = realizedVolBPS(symbolBars, idx, retBars)
	betaWindow := minInt(24, idx)
	if profile.ID == ProfileOKX5M {
		betaWindow = minInt(48, idx)
	}
	out.Beta = betaProxy(symbolBars, btcBars, idx, btcIdx, betaWindow)
	btcRet := logReturn(btcBars, btcIdx, retBars)
	if isFinite(out.Return) && isFinite(btcRet) {
		out.BTCRelativeReturn = out.Return - btcRet
	}
	out.AvgQuoteVolume = profitmap.AvgQuoteVolumeUSDT(symbolBars, idx, window)
	out.LiquidityScore = liquidityScore(out.AvgQuoteVolume)
	out.VolCostPenalty = volCostPenalty(out.RV, profile.ID)
	bar := symbolBars[idx]
	cost := profitmap.CostInputs{
		CapitalUSDT:            capitalUSDT,
		RollingQuoteVolumeUSDT: profitmap.RollingQuoteVolumeUSDT(symbolBars, idx, window),
		BarHigh:                bar.High,
		BarLow:                 bar.Low,
		BarMid:                 bar.Mid(),
	}
	_, conservative, capacityFail := profitmap.ComputeReturns(0, cost)
	out.CostEstimate = conservative * -1
	if capacityFail {
		out.CapacityHeadroom = 0
	} else {
		impact := g4protocol.CapacityImpactScaleBPS * capitalUSDT / math.Max(cost.RollingQuoteVolumeUSDT, 1)
		if impact > g4protocol.CapacityImpactCapBPS {
			impact = g4protocol.CapacityImpactCapBPS
		}
		out.CapacityHeadroom = 1 - impact/g4protocol.CapacityImpactCapBPS
	}
	return out, true
}

func logReturn(bars []profitmap.Bar, idx, barsBack int) float64 {
	start := idx - barsBack
	if start < 0 || bars[start].Close <= 0 || bars[idx].Close <= 0 {
		return math.NaN()
	}
	return (bars[idx].Close/bars[start].Close - 1) * 10000
}

func realizedVolBPS(bars []profitmap.Bar, idx, barsBack int) float64 {
	start := idx - barsBack
	if start < 0 {
		return math.NaN()
	}
	returns := make([]float64, 0, barsBack)
	for i := start + 1; i <= idx; i++ {
		if bars[i-1].Close <= 0 || bars[i].Close <= 0 {
			continue
		}
		returns = append(returns, bars[i].Close/bars[i-1].Close-1)
	}
	if len(returns) == 0 {
		return math.NaN()
	}
	return g4protocol.SymbolRealizedVolRMSBPS(returns)
}

func betaProxy(symBars, btcBars []profitmap.Bar, symIdx, btcIdx, window int) float64 {
	if symIdx < window || btcIdx < window {
		return math.NaN()
	}
	symRets := make([]float64, 0, window)
	btcRets := make([]float64, 0, window)
	for i := 0; i < window; i++ {
		si := symIdx - i
		bi := btcIdx - i
		if si <= 0 || bi <= 0 {
			return math.NaN()
		}
		if symBars[si-1].Close <= 0 || symBars[si].Close <= 0 || btcBars[bi-1].Close <= 0 || btcBars[bi].Close <= 0 {
			return math.NaN()
		}
		symRets = append(symRets, symBars[si].Close/symBars[si-1].Close-1)
		btcRets = append(btcRets, btcBars[bi].Close/btcBars[bi-1].Close-1)
	}
	if len(symRets) < 2 {
		return math.NaN()
	}
	cov := covariance(symRets, btcRets)
	varB := variance(btcRets)
	if varB <= 0 {
		return math.NaN()
	}
	return cov / varB
}

func covariance(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.NaN()
	}
	var sumA, sumB float64
	for i := range a {
		sumA += a[i]
		sumB += b[i]
	}
	meanA := sumA / float64(len(a))
	meanB := sumB / float64(len(b))
	var cov float64
	for i := range a {
		cov += (a[i] - meanA) * (b[i] - meanB)
	}
	return cov / float64(len(a))
}

func variance(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	var sum, sumSq float64
	for _, v := range values {
		sum += v
		sumSq += v * v
	}
	mean := sum / float64(len(values))
	return sumSq/float64(len(values)) - mean*mean
}

func liquidityScore(avgQuote float64) float64 {
	if avgQuote <= 0 {
		return 0
	}
	return math.Log10(avgQuote)
}

func volCostPenalty(rvBPS float64, profileID string) float64 {
	if !isFinite(rvBPS) || rvBPS <= 0 {
		return math.NaN()
	}
	ratio := g4protocol.VolatilityCostRatioFromRMS(rvBPS)
	if ratio <= 0 {
		return math.NaN()
	}
	return 1 / ratio
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
