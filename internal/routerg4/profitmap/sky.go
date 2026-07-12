package profitmap

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/sky"
)

// CoarseSkyLabel collapses te/dd to wildcards per G4 protocol.
func CoarseSkyLabel(full sky.SkyState) string {
	if full.OOD || full.Label == "" {
		return "ood"
	}
	parts := strings.Split(full.Label, "/")
	out := make([]string, 0, 5)
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "ret", "rv", "fund":
			out = append(out, part)
		case "te", "dd":
			out = append(out, kv[0]+"=*")
		}
	}
	if len(out) == 0 {
		return "ood"
	}
	sort.Strings(out)
	return strings.Join(out, "/")
}

// FitSkyCutpoints fits coarse sky cutpoints on discovery-partition BTC 1h decisions.
func FitSkyCutpoints(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	boundaries Boundaries,
	maxLines int,
) (sky.Cutpoints, error) {
	absRoot := historyRoot
	if err := VerifyCatalogFile(catalog, absRoot, "okx_1h/BTC-USDT-SWAP.jsonl"); err != nil {
		return sky.Cutpoints{}, err
	}
	prices, err := LoadBars(filepath.Join(absRoot, "okx_1h", "BTC-USDT-SWAP.jsonl"), maxLines)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	funding, err := optionalCatalogFunding(catalog, absRoot, "BTC-USDT-SWAP", maxLines)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	series := sky.Series{}
	for _, bar := range prices {
		series.Bars = append(series.Bars, sky.Bar{OpenTime: bar.OpenTime, Close: bar.Close})
	}
	for _, point := range funding {
		series.Funding = append(series.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	var samples []sky.FeatureSet
	for i := range prices {
		decision := prices[i].OpenTime.Add(time.Hour)
		if decision.Before(boundaries.Start) || !decision.Before(boundaries.DiscoveryEnd) {
			continue
		}
		features := sky.ComputeFeatures(series, decision)
		if !features.OOD {
			samples = append(samples, features)
		}
	}
	if len(samples) < 10 {
		// Funding is optional and may not overlap the long discovery window.
		// Fit price dimensions and represent funding as an explicit unknown
		// bucket instead of disabling the OHLCV opportunity map.
		return priceOnlyCutpoints(series, prices, boundaries), nil
	}
	quantiles := []float64{1.0 / 3.0, 2.0 / 3.0}
	cuts, err := sky.Fit(sky.FitPartition, samples, quantiles)
	if err != nil {
		return sky.Cutpoints{}, err
	}
	return cuts.Freeze(), nil
}

// SkyAtDecision classifies coarse sky at decisionTime using frozen cutpoints.
func SkyAtDecision(cutpoints sky.Cutpoints, series sky.Series, decisionTime time.Time) string {
	features := sky.ComputeFeatures(series, decisionTime)
	ret, retOK := features.Values[sky.DimReturn24h]
	rv, rvOK := features.Values[sky.DimRealizedVol24h]
	if !retOK || !rvOK {
		return "ood"
	}
	retLabel, ok := coarseBucket(ret, cutpoints.Cuts[sky.DimReturn24h])
	if !ok {
		return "ood"
	}
	rvLabel, ok := coarseBucket(rv, cutpoints.Cuts[sky.DimRealizedVol24h])
	if !ok {
		return "ood"
	}
	fundLabel := "unknown"
	if fund, exists := features.Values[sky.DimFundingState]; exists {
		if label, valid := coarseBucket(fund, cutpoints.Cuts[sky.DimFundingState]); valid &&
			!degenerateZeroCuts(cutpoints.Cuts[sky.DimFundingState]) {
			fundLabel = label
		}
	}
	return fmt.Sprintf("ret=%s/te=*/rv=%s/dd=*/fund=%s", retLabel, rvLabel, fundLabel)
}

func coarseBucket(value float64, cuts []float64) (string, bool) {
	if len(cuts) != 2 {
		return "", false
	}
	switch {
	case value <= cuts[0]:
		return "low", true
	case value <= cuts[1]:
		return "mid", true
	default:
		return "high", true
	}
}

func degenerateZeroCuts(cuts []float64) bool {
	return len(cuts) == 2 && cuts[0] == 0 && cuts[1] == 0
}

// BuildBTCSkySeries loads BTC 1h bars and funding for sky classification.
func BuildBTCSkySeries(catalog historydata.HistoryCatalog, historyRoot string, maxLines int) (sky.Series, error) {
	prices, err := LoadBars(filepath.Join(historyRoot, "okx_1h", "BTC-USDT-SWAP.jsonl"), maxLines)
	if err != nil {
		return sky.Series{}, err
	}
	funding, err := optionalCatalogFunding(catalog, historyRoot, "BTC-USDT-SWAP", maxLines)
	if err != nil {
		return sky.Series{}, err
	}
	series := sky.Series{}
	for _, bar := range prices {
		series.Bars = append(series.Bars, sky.Bar{OpenTime: bar.OpenTime, Close: bar.Close})
	}
	for _, point := range funding {
		series.Funding = append(series.Funding, sky.FundingPoint{Time: point.Time, Rate: point.Rate})
	}
	return series, nil
}

func optionalCatalogFunding(catalog historydata.HistoryCatalog, root, instrument string, maxLines int) ([]FundingPoint, error) {
	rel := "okx_funding/" + instrument + ".jsonl"
	stats, declared := CatalogFile(catalog, rel)
	if !declared || stats.Empty {
		return nil, nil
	}
	if err := VerifyCatalogFile(catalog, root, rel); err != nil {
		return nil, err
	}
	return loadOKXFunding(filepath.Join(root, rel), maxLines)
}

func priceOnlyCutpoints(series sky.Series, prices []Bar, boundaries Boundaries) sky.Cutpoints {
	values := map[string][]float64{}
	used := 0
	for _, bar := range prices {
		decision := bar.OpenTime.Add(time.Hour)
		if decision.Before(boundaries.Start) || !decision.Before(boundaries.DiscoveryEnd) {
			continue
		}
		features := sky.ComputeFeatures(series, decision)
		for dim, value := range features.Values {
			values[dim] = append(values[dim], value)
		}
		used++
	}
	cuts := map[string][]float64{}
	for _, dim := range sky.Dimensions {
		if dim == sky.DimFundingState || len(values[dim]) == 0 {
			cuts[dim] = []float64{0, 0}
			continue
		}
		sort.Float64s(values[dim])
		cuts[dim] = []float64{quantile(values[dim], 1.0/3.0), quantile(values[dim], 2.0/3.0)}
	}
	return sky.Cutpoints{
		Partition: sky.FitPartition, Quantiles: []float64{1.0 / 3.0, 2.0 / 3.0},
		Cuts: cuts, FitSamples: used, Frozen: true,
	}
}

// SkyDecisionTimeFor5m maps a 5m decision to the latest closed 1h BTC bar.
func SkyDecisionTimeFor5m(decisionTime time.Time) time.Time {
	return decisionTime.Truncate(time.Hour)
}

// HashCutpoints returns cutpoint hash for manifest binding.
func HashCutpoints(c sky.Cutpoints) (string, error) {
	return c.Hash()
}

// SymbolClassCutpoints holds frozen discovery volume tertiles.
type SymbolClassCutpoints struct {
	Lower  float64 `json:"lower_tertile"`
	Upper  float64 `json:"upper_tertile"`
	Frozen bool    `json:"frozen"`
}

func (c SymbolClassCutpoints) Classify(avgVolume float64) string {
	if !c.Frozen {
		return SymbolClassMid
	}
	if avgVolume <= c.Lower {
		return SymbolClassLongTail
	}
	if avgVolume >= c.Upper {
		return SymbolClassMajor
	}
	return SymbolClassMid
}

func (c SymbolClassCutpoints) Hash() (string, error) {
	hash, _, err := datatruth.HashJSON(c)
	return hash, err
}

// FitSymbolClassCutpoints computes tertile boundaries from discovery avg volumes.
func FitSymbolClassCutpoints(volumes []float64) SymbolClassCutpoints {
	sorted := append([]float64(nil), volumes...)
	sort.Float64s(sorted)
	if len(sorted) == 0 {
		return SymbolClassCutpoints{Frozen: true}
	}
	lower := quantile(sorted, 1.0/3.0)
	upper := quantile(sorted, 2.0/3.0)
	return SymbolClassCutpoints{Lower: lower, Upper: upper, Frozen: true}
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	frac := pos - float64(lo)
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

// DiscoveryAvgQuoteVolume computes average quote volume over discovery partition bars.
func DiscoveryAvgQuoteVolume(bars []Bar, spec ProfileSpec, boundaries Boundaries) float64 {
	window := RollingWindowBars(spec)
	var sum float64
	var count int
	for i := range bars {
		decision := DecisionTime(bars, i, spec.BarDuration)
		if !InDiscovery(boundaries, decision) {
			continue
		}
		avg := AvgQuoteVolumeUSDT(bars, i, window)
		if avg > 0 {
			sum += avg
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func loadOKXFunding(path string, maxLines int) ([]FundingPoint, error) {
	return loadFundingJSONL(path, maxLines)
}
