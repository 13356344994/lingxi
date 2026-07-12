package reversemine

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

const (
	featureTimeOfDaySession  = "time_of_day_session"
	featureBTCRelativeReturn = "btc_relative_return"
	featureBTCBetaProxy      = "btc_beta_proxy"
)

// ProfileFeatureOrder returns the frozen derivation order for one profile.
func ProfileFeatureOrder(profileID string) []string {
	var out []string
	out = append(out, protocol.SharedFeatures...)
	switch profileID {
	case profitmap.ProfileOKX1H:
		out = append(out, protocol.OKX1HProfileFeatures...)
	case profitmap.ProfileOKX5M:
		out = append(out, protocol.OKX5MProfileFeatures...)
	}
	out = append(out, featureTimeOfDaySession, featureBTCRelativeReturn, featureBTCBetaProxy)
	seen := map[string]bool{}
	ordered := make([]string, 0, len(out))
	for _, key := range out {
		if seen[key] || IsForbiddenFeatureKey(key) {
			continue
		}
		seen[key] = true
		ordered = append(ordered, key)
	}
	return ordered
}

// ComputeFeatures rebuilds decision-before features for one symbol at decisionTime.
// Outcome and current-bar inputs are never used.
func ComputeFeatures(
	spec profitmap.ProfileSpec,
	symbolBars []profitmap.Bar,
	btcBars []profitmap.Bar,
	decisionTime time.Time,
	optional map[string]float64,
) (map[string]float64, error) {
	idx := profitmap.BarIndexAt(symbolBars, decisionTime, spec.BarDuration)
	if idx < 0 {
		return nil, errors.New("reversemine: decision bar not found")
	}
	if profitmap.RejectCurrentBarOpen(symbolBars, idx, spec.BarDuration) {
		return nil, errors.New("reversemine: current bar rejected")
	}
	features := map[string]float64{}
	windowBars := profitmap.RollingWindowBars(spec)
	avgQuote := profitmap.AvgQuoteVolumeUSDT(symbolBars, idx, windowBars)
	features["quote_volume_24h_log_bucket"] = quoteVolumeLogBucket(avgQuote)
	realizedVol := realizedVolBPS(symbolBars, idx, spec, []int{1, 3, 4, 12, 24, 48})
	for label, barsBack := range profileReturnHorizons(spec) {
		ret := logReturnBPS(symbolBars, idx, barsBack)
		features["symbol_return_"+label] = ret
		volKey := "symbol_realized_vol_" + label
		if v, ok := realizedVol[label]; ok {
			features[volKey] = v
		}
	}
	features["volatility_cost_ratio"] = volatilityCostRatio(realizedVol, spec.ID)
	features[featureTimeOfDaySession] = timeOfDaySession(decisionTime, spec.BarDuration)
	btcIdx := profitmap.BarIndexAt(btcBars, decisionTime, spec.BarDuration)
	if btcIdx >= 0 {
		primaryLabel := primaryReturnLabel(spec)
		barsBack := profileReturnHorizons(spec)[primaryLabel]
		symRet := logReturnBPS(symbolBars, idx, barsBack)
		btcRet := logReturnBPS(btcBars, btcIdx, barsBack)
		features[featureBTCRelativeReturn] = symRet - btcRet
		features[featureBTCBetaProxy] = betaProxy(symbolBars, btcBars, idx, btcIdx, 24)
	}
	for key, value := range optional {
		if IsForbiddenFeatureKey(key) {
			return nil, fmt.Errorf("reversemine: forbidden optional feature %q", key)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		features[key] = value
	}
	return features, nil
}

func profileReturnHorizons(spec profitmap.ProfileSpec) map[string]int {
	out := map[string]int{}
	for _, h := range spec.Horizons {
		label := h.Label
		if spec.ID == profitmap.ProfileOKX5M {
			switch label {
			case "60m":
				label = "1h"
			case "240m":
				label = "4h"
			}
		}
		out[label] = h.BarCount
	}
	return out
}

func primaryReturnLabel(spec profitmap.ProfileSpec) string {
	if len(spec.Horizons) == 0 {
		return "1h"
	}
	return spec.Horizons[0].Label
}

func quoteVolumeLogBucket(avgQuote float64) float64 {
	if avgQuote <= 0 {
		return 0
	}
	return math.Floor(math.Log10(avgQuote))
}

func logReturnBPS(bars []profitmap.Bar, idx, barsBack int) float64 {
	start := idx - barsBack
	if start < 0 || idx >= len(bars) || bars[start].Close <= 0 || bars[idx].Close <= 0 {
		return math.NaN()
	}
	return (bars[idx].Close/bars[start].Close - 1) * 10000
}

func realizedVolBPS(bars []profitmap.Bar, idx int, spec profitmap.ProfileSpec, windows []int) map[string]float64 {
	out := map[string]float64{}
	labels := profileReturnHorizons(spec)
	for label, barsBack := range labels {
		start := idx - barsBack
		if start < 0 {
			continue
		}
		returns := make([]float64, 0, barsBack)
		for i := start + 1; i <= idx; i++ {
			if bars[i-1].Close <= 0 || bars[i].Close <= 0 {
				continue
			}
			returns = append(returns, bars[i].Close/bars[i-1].Close-1)
		}
		if len(returns) == 0 {
			continue
		}
		out[label] = protocol.SymbolRealizedVolRMSBPS(returns)
	}
	_ = windows
	return out
}

func volatilityCostRatio(realized map[string]float64, profileID string) float64 {
	var vol float64
	switch profileID {
	case profitmap.ProfileOKX1H:
		vol = realized["1h"]
	case profitmap.ProfileOKX5M:
		vol = realized["5m"]
	}
	if !isFinite(vol) || vol <= 0 {
		return math.NaN()
	}
	return protocol.VolatilityCostRatioFromRMS(vol)
}

func timeOfDaySession(decision time.Time, barDuration time.Duration) float64 {
	utc := decision.UTC()
	hour := utc.Hour()
	switch {
	case hour >= 0 && hour < 8:
		return 0 // asia
	case hour >= 8 && hour < 16:
		return 1 // europe
	default:
		return 2 // us
	}
}

func betaProxy(symBars, btcBars []profitmap.Bar, symIdx, btcIdx, window int) float64 {
	if symIdx < window || btcIdx < window {
		return math.NaN()
	}
	var symRets, btcRets []float64
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

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func winnerID(w profitmap.WinnerEntry) string {
	return w.Symbol + "|" + w.DecisionTime.UTC().Format(time.RFC3339Nano)
}

func HashWinnerIDs(ids []string) (string, error) {
	hash, _, err := datatruth.HashJSON(ids)
	return hash, err
}

// EmpiricalQuantile uses linear interpolation (type-7).
func EmpiricalQuantile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

func featurePairKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + "\x00" + b
}

func dualFeaturePairs(profileID string) [][2]string {
	features := ProfileFeatureOrder(profileID)
	var pairs [][2]string
	for i := 0; i < len(features); i++ {
		for j := i + 1; j < len(features); j++ {
			pairs = append(pairs, [2]string{features[i], features[j]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs
}

func validateFeatureKey(key string) error {
	if IsForbiddenFeatureKey(key) {
		return fmt.Errorf("reversemine: forbidden feature %q", key)
	}
	lower := strings.ToLower(key)
	for _, allowed := range append(append(protocol.SharedFeatures, protocol.OKX1HProfileFeatures...), protocol.OKX5MProfileFeatures...) {
		if key == allowed {
			return nil
		}
	}
	switch key {
	case featureTimeOfDaySession, featureBTCRelativeReturn, featureBTCBetaProxy:
		return nil
	}
	_ = lower
	return fmt.Errorf("reversemine: unknown feature %q", key)
}
