package eventmap

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/profitmap"
)

const (
	quantileLowLiquidity = 0.25
	quantileRMSHigh      = 0.75
	quantileExtreme      = 0.90
)

type featureSample struct {
	ReturnAbsBPS    float64
	RMSBPS          float64
	AvgQuoteVolume  float64
	BTCRelativeAbs  float64
	BTCShockReturn  float64
}

// FitThresholds collects discovery-partition feature samples and freezes cutpoints.
// Outcomes are never read during fit.
func FitThresholds(spec ProfileSpec, boundaries Boundaries, symbolBars map[string][]Bar, btcBars []Bar) (FrozenThresholds, error) {
	var samples []featureSample
	windowBars := profitmap.RollingWindowBars(spec)
	primaryBack := primaryReturnBars(spec)

	for symbol, bars := range symbolBars {
		_ = symbol
		for i := primaryBack; i < len(bars); i++ {
			if profitmap.RejectCurrentBarOpen(bars, i, spec.BarDuration) {
				continue
			}
			decision := profitmap.DecisionTime(bars, i, spec.BarDuration)
			if !profitmap.InDiscovery(boundaries, decision) {
				continue
			}
			ret := logReturnBPS(bars, i, primaryBack)
			if !isFinite(ret) {
				continue
			}
			rms := realizedRMSBPS(bars, i, primaryBack)
			avgQuote := profitmap.AvgQuoteVolumeUSDT(bars, i, windowBars)
			btcIdx := profitmap.BarIndexAt(btcBars, decision, spec.BarDuration)
			var btcRel float64
			if btcIdx >= primaryBack {
				symRet := ret
				btcRet := logReturnBPS(btcBars, btcIdx, primaryBack)
				if isFinite(btcRet) {
					btcRel = math.Abs(symRet - btcRet)
				}
			}
			btcShock := 0.0
			if btcIdx >= 1 {
				btcShock = math.Abs(logReturnBPS(btcBars, btcIdx, 1))
			}
			samples = append(samples, featureSample{
				ReturnAbsBPS:   math.Abs(ret),
				RMSBPS:         rms,
				AvgQuoteVolume: avgQuote,
				BTCRelativeAbs: btcRel,
				BTCShockReturn: btcShock,
			})
		}
	}
	if len(samples) < 10 {
		return FrozenThresholds{}, errf("insufficient discovery samples for threshold fit (%d)", len(samples))
	}
	th := FrozenThresholds{
		ProfileID:            spec.ID,
		FrozenAt:             time.Now().UTC(),
		ExtremeReturnAbsBPS:  quantile(samples, func(s featureSample) float64 { return s.ReturnAbsBPS }, quantileExtreme),
		ExtremeRMSBPS:        quantile(samples, func(s featureSample) float64 { return s.RMSBPS }, quantileRMSHigh),
		LowLiquidityQuote:    quantile(samples, func(s featureSample) float64 { return s.AvgQuoteVolume }, quantileLowLiquidity),
		VolumeSpikeQuote:     quantile(samples, func(s featureSample) float64 { return s.AvgQuoteVolume }, quantileRMSHigh),
		BTCRelativeAbsBPS:    quantile(samples, func(s featureSample) float64 { return s.BTCRelativeAbs }, quantileExtreme),
		BTCShockReturnAbsBPS: quantile(samples, func(s featureSample) float64 { return s.BTCShockReturn }, quantileExtreme),
		DiscoverySampleCount: len(samples),
	}
	return th, nil
}

// HashThresholds returns a tamper-evident hash of frozen thresholds.
func HashThresholds(th FrozenThresholds) (string, error) {
	hash, _, err := datatruth.HashJSON(th)
	return hash, err
}

func quantile(samples []featureSample, pick func(featureSample) float64, q float64) float64 {
	values := make([]float64, 0, len(samples))
	for _, s := range samples {
		v := pick(s)
		if isFinite(v) {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	pos := q * float64(len(values)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(values) {
		return values[len(values)-1]
	}
	frac := pos - float64(lo)
	return values[lo] + frac*(values[lo+1]-values[lo])
}

func primaryReturnBars(spec ProfileSpec) int {
	if len(spec.Horizons) == 0 {
		return 1
	}
	return spec.Horizons[0].BarCount
}

func logReturnBPS(bars []Bar, idx, barsBack int) float64 {
	start := idx - barsBack
	if start < 0 || idx >= len(bars) || bars[start].Close <= 0 || bars[idx].Close <= 0 {
		return math.NaN()
	}
	return (bars[idx].Close/bars[start].Close - 1) * 10000
}

func realizedRMSBPS(bars []Bar, idx, barsBack int) float64 {
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
	return protocol.SymbolRealizedVolRMSBPS(returns)
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
