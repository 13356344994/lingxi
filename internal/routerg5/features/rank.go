package features

import (
	"math"
	"sort"
)

type rankEntry struct {
	key   string
	value float64
}

// PercentileRanks assigns deterministic average ranks normalized to [0,1].
// Only finite values in values participate; missing keys are omitted from output.
func PercentileRanks(values map[string]float64, higherIsBetter bool) map[string]float64 {
	entries := make([]rankEntry, 0, len(values))
	for key, value := range values {
		if isFinite(value) {
			entries = append(entries, rankEntry{key: key, value: value})
		}
	}
	if len(entries) == 0 {
		return map[string]float64{}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].value == entries[j].value {
			return entries[i].key < entries[j].key
		}
		if higherIsBetter {
			return entries[i].value > entries[j].value
		}
		return entries[i].value < entries[j].value
	})
	ranks := averageRanks(entries)
	out := make(map[string]float64, len(ranks))
	// Convert average rank to percentile where 1.0 is best in the cross-section.
	denom := float64(len(entries) - 1)
	if denom <= 0 {
		for key := range ranks {
			out[key] = 0.5
		}
		return out
	}
	for key, rank := range ranks {
		out[key] = 1 - (rank-1)/denom
	}
	return out
}

func averageRanks(entries []rankEntry) map[string]float64 {
	out := make(map[string]float64, len(entries))
	i := 0
	for i < len(entries) {
		j := i + 1
		for j < len(entries) && entries[j].value == entries[i].value {
			j++
		}
		avgRank := (float64(i+1) + float64(j)) / 2
		for k := i; k < j; k++ {
			out[entries[k].key] = avgRank
		}
		i = j
	}
	return out
}

// ApplyCrossSectionRanks fills rank fields from visible locals at one timestamp.
func ApplyCrossSectionRanks(locals map[string]SymbolLocal) map[string]map[string]float64 {
	returns := map[string]float64{}
	vols := map[string]float64{}
	liquidity := map[string]float64{}
	betas := map[string]float64{}
	rel := map[string]float64{}
	for sym, loc := range locals {
		returns[sym] = loc.Return
		vols[sym] = loc.RV
		liquidity[sym] = loc.AvgQuoteVolume
		betas[sym] = loc.Beta
		rel[sym] = loc.BTCRelativeReturn
	}
	out := map[string]map[string]float64{}
	retRanks := PercentileRanks(returns, true)
	volRanks := PercentileRanks(vols, false)
	liqRanks := PercentileRanks(liquidity, true)
	betaRanks := PercentileRanks(betas, true)
	relRanks := PercentileRanks(rel, true)
	for sym := range locals {
		out[sym] = map[string]float64{
			"return_rank":                retRanks[sym],
			"vol_rank":                   volRanks[sym],
			"liquidity_rank":             liqRanks[sym],
			"beta_rank":                  betaRanks[sym],
			"quote_volume_rank":          liqRanks[sym],
			"liquidity_percentile":       liqRanks[sym],
			"btc_relative_strength_rank": relRanks[sym],
		}
	}
	return out
}

// SoftSkyScores exposes continuous sky values; no bucket classification.
func SoftSkyScores(btc BTCFeatures) map[string]float64 {
	out := map[string]float64{
		"btc_return":     btc.Return,
		"btc_vol":        btc.Vol,
		"btc_trend":      btc.Trend,
		"btc_drawdown":   btc.Drawdown,
		"sky_confidence": btc.Confidence,
	}
	if btc.FundingKnown {
		out["btc_funding_state"] = btc.Funding
		out["mask_btc_funding_missing"] = 0
	} else {
		out["btc_funding_state"] = math.NaN()
		out["mask_btc_funding_missing"] = 1
	}
	return out
}

// SoftSorterScores exposes continuous sorter penalties without hard rejection.
func SoftSorterScores(loc SymbolLocal) map[string]float64 {
	return map[string]float64{
		"liquidity_score":    loc.LiquidityScore,
		"vol_cost_penalty":   loc.VolCostPenalty,
		"capacity_headroom":  loc.CapacityHeadroom,
		"cost_estimate":      loc.CostEstimate,
		"symbol_return":      loc.Return,
		"symbol_rv":          loc.RV,
		"symbol_beta":        loc.Beta,
		"btc_relative_return": loc.BTCRelativeReturn,
	}
}
