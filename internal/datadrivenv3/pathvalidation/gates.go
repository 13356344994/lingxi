package pathvalidation

import (
	"crypto/sha256"
	"math"
	"sort"
	"time"
)

// Frozen g1 validation-gate thresholds, copied verbatim from
// research/firstbattle/data_driven_router_v3/protocol.json so this package
// carries its own auditable copy rather than parsing the protocol file at
// gate-evaluation time (the protocol file's hash is still independently
// verified in sealed.go).
const (
	MinimumEvents                  = 50
	MinimumActiveDays              = 20
	MinimumNonoverlapRegimeBlocks  = 10
	SymbolHashFolds                = 10
	MinimumPositiveSymbolFolds     = 8
	MinimumPositiveMonthFraction   = 0.80
	MinimumPositiveRegimeBlocks    = 8
	MaximumTopTwoDayFraction       = 0.35
	MaximumSingleSymbolFraction    = 0.35
	ValidationBHFDRQ               = 0.10
	LeafWFHolmAlpha                = 0.01
	DegenerateFixedBaselineMaxFrac = 0.99
	CostStressMultiplier           = 1.5
)

// PathRow is one matched, replayed validation/leaf_wf row for a single
// candidate path (one policy leaf). All delay/stress variants are computed
// once per row so every gate below shares identical inputs.
type PathRow struct {
	Instrument      string
	DecisionMS      int64
	BTCReturn24h    float64
	BTCVol24h       float64
	NetRewardBase   float64    // leaf's own delay, base cost
	NetRewardStress float64    // leaf's own delay, 1.5x cost stress
	NetRewardDelay  [4]float64 // delay 0,1,2,3, base cost (same fraction/horizon)
	DelayOK         [4]bool    // whether each delay variant matured (vs ruin/unknown-cost)
	Ruin            bool
	UnknownCost     bool
}

func monthKey(decisionMS int64) string {
	return time.UnixMilli(decisionMS).UTC().Format("2006-01")
}
func dayKey(decisionMS int64) string {
	return time.UnixMilli(decisionMS).UTC().Format("2006-01-02")
}

// GateReport is the complete, itemized g1 validation-gate evidence for one
// candidate path in one partition (validation or leaf_wf).
type GateReport struct {
	Support                int        `json:"support"`
	ActiveDays             int        `json:"active_days"`
	RuinCount              int        `json:"ruin_count"`
	UnknownCostCount       int        `json:"unknown_cost_count"`
	Solvent                bool       `json:"solvent"`
	AllScenariosExecutable bool       `json:"all_scenarios_executable"`
	NetRewardBaseSum       float64    `json:"net_reward_base_sum"`
	NetRewardStressSum     float64    `json:"net_reward_cost_stress_sum"`
	NetRewardDelaySum      [4]float64 `json:"net_reward_delay_0_3_sum"`
	SymbolFoldPositive     int        `json:"symbol_fold_positive"`
	SymbolFoldTotal        int        `json:"symbol_fold_total"`
	MonthLOOPositive       int        `json:"month_loo_positive"`
	MonthLOOTotal          int        `json:"month_loo_total"`
	RegimeBlockPositive    int        `json:"btc_regime_block_positive"`
	RegimeBlockTotal       int        `json:"btc_regime_block_total"`
	TopTwoDayFraction      float64    `json:"top_two_day_positive_fraction"`
	SingleSymbolFraction   float64    `json:"single_symbol_positive_fraction"`
	SignTestPValue         float64    `json:"sign_test_p_value"`
	MeetsMinimumSupport    bool       `json:"meets_minimum_support"`
	MeetsSymbolFolds       bool       `json:"meets_symbol_folds"`
	MeetsMonthLOO          bool       `json:"meets_month_loo"`
	MeetsRegimeBlocks      bool       `json:"meets_btc_regime_blocks"`
	MeetsTopTwoDay         bool       `json:"meets_top_two_day"`
	MeetsSingleSymbol      bool       `json:"meets_single_symbol"`
	MeetsAllDelaysPositive bool       `json:"meets_all_delays_positive"`
	MeetsCostStress        bool       `json:"meets_cost_stress"`
	MeetsBaseNetPositive   bool       `json:"meets_base_net_positive"`
}

// EvaluateGates computes every g1 robustness/candidate-statistics gate for
// one path's matched rows within one partition. globalRegimeBlockOf maps a
// decision timestamp to a partition-wide, market-wide BTC regime block id
// (see AssignRegimeBlocks); it is computed once per partition from every row
// in that partition, not just the rows matched by this specific leaf, so the
// regime definition itself cannot be selection-biased by which rows matched.
func EvaluateGates(rows []PathRow, globalRegimeBlockOf map[int64]int) GateReport {
	report := GateReport{Support: len(rows)}
	if len(rows) == 0 {
		return report
	}
	days := map[string]bool{}
	monthTotals := map[string]float64{}
	symbolTotals := map[string]float64{}
	dayTotals := map[string]float64{}
	blockTotals := map[int]float64{}
	foldTotals := make([]float64, SymbolHashFolds)
	foldSeen := make([]bool, SymbolHashFolds)
	positiveRows := 0
	for _, r := range rows {
		if r.Ruin {
			report.RuinCount++
		}
		if r.UnknownCost {
			report.UnknownCostCount++
		}
		days[dayKey(r.DecisionMS)] = true
		monthTotals[monthKey(r.DecisionMS)] += r.NetRewardBase
		symbolTotals[r.Instrument] += r.NetRewardBase
		dayTotals[dayKey(r.DecisionMS)] += r.NetRewardBase
		if block, ok := globalRegimeBlockOf[r.DecisionMS]; ok {
			blockTotals[block] += r.NetRewardBase
		}
		fold := symbolFold(r.Instrument)
		foldTotals[fold] += r.NetRewardBase
		foldSeen[fold] = true
		report.NetRewardBaseSum += r.NetRewardBase
		report.NetRewardStressSum += r.NetRewardStress
		for d := 0; d < 4; d++ {
			if r.DelayOK[d] {
				report.NetRewardDelaySum[d] += r.NetRewardDelay[d]
			}
		}
		if r.NetRewardBase > 0 {
			positiveRows++
		}
	}
	report.ActiveDays = len(days)

	for f := 0; f < SymbolHashFolds; f++ {
		if !foldSeen[f] {
			continue
		}
		report.SymbolFoldTotal++
		if foldTotals[f] > 0 {
			report.SymbolFoldPositive++
		}
	}

	report.MonthLOOTotal = len(monthTotals)
	total := report.NetRewardBaseSum
	for _, monthSum := range monthTotals {
		if total-monthSum > 0 {
			report.MonthLOOPositive++
		}
	}

	report.RegimeBlockTotal = len(blockTotals)
	for _, sum := range blockTotals {
		if sum > 0 {
			report.RegimeBlockPositive++
		}
	}

	report.TopTwoDayFraction = topNFraction(dayTotals, 2)
	report.SingleSymbolFraction = topNFraction(symbolTotals, 1)

	report.SignTestPValue = oneSidedSignTestPValue(positiveRows, len(rows))

	report.MeetsMinimumSupport = report.Support >= MinimumEvents && report.ActiveDays >= MinimumActiveDays
	// Frozen protocol thresholds are literal minimums out of the fixed fold
	// count (10) / total months / total blocks, so a fold/month/block with no
	// matched rows counts against the "positive" tally, never in favor of it.
	report.MeetsSymbolFolds = report.SymbolFoldPositive >= MinimumPositiveSymbolFolds
	report.MeetsMonthLOO = report.MonthLOOTotal > 0 && float64(report.MonthLOOPositive)/float64(report.MonthLOOTotal) >= MinimumPositiveMonthFraction
	report.MeetsRegimeBlocks = report.RegimeBlockTotal >= MinimumNonoverlapRegimeBlocks && report.RegimeBlockPositive >= MinimumPositiveRegimeBlocks
	report.MeetsTopTwoDay = report.TopTwoDayFraction <= MaximumTopTwoDayFraction
	report.MeetsSingleSymbol = report.SingleSymbolFraction <= MaximumSingleSymbolFraction
	allDelaysPositive := true
	for d := 0; d < 4; d++ {
		if report.NetRewardDelaySum[d] <= 0 {
			allDelaysPositive = false
		}
	}
	report.MeetsAllDelaysPositive = allDelaysPositive
	report.MeetsCostStress = report.NetRewardStressSum > 0
	report.MeetsBaseNetPositive = report.NetRewardBaseSum > 0
	report.Solvent = report.RuinCount == 0
	report.AllScenariosExecutable = report.RuinCount == 0 && report.UnknownCostCount == 0
	return report
}

// topNFraction returns the fraction of total positive contribution accounted
// for by the top n positive keys (by value). If there is no positive
// contribution at all, it returns 0 (the gate then passes trivially, and the
// path fails on net-positive-reward grounds instead).
func topNFraction(totals map[string]float64, n int) float64 {
	positives := make([]float64, 0, len(totals))
	var positiveSum float64
	for _, v := range totals {
		if v > 0 {
			positives = append(positives, v)
			positiveSum += v
		}
	}
	if positiveSum <= 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(positives)))
	if n > len(positives) {
		n = len(positives)
	}
	var top float64
	for i := 0; i < n; i++ {
		top += positives[i]
	}
	return top / positiveSum
}

func symbolFold(instrument string) int {
	sum := sha256.Sum256([]byte("g1-symbol-hash-fold\x00" + instrument))
	// Use the low 8 bytes as an unsigned modulus; deterministic and stable.
	var v uint64
	for _, b := range sum[24:32] {
		v = v<<8 | uint64(b)
	}
	return int(v % uint64(SymbolHashFolds))
}

// oneSidedSignTestPValue tests H0: P(reward>0)=0.5 against the one-sided
// alternative P(reward>0)>0.5, using a normal approximation to the binomial
// sign test. This is the single, documented test statistic this validation
// owner uses for the BH-FDR (validation) and Holm (leaf_wf) multiple-
// comparison corrections; the frozen protocol fixes the q/alpha thresholds
// but not a specific test statistic.
func oneSidedSignTestPValue(positive, n int) float64 {
	if n == 0 {
		return 1
	}
	phat := float64(positive) / float64(n)
	se := math.Sqrt(0.25 / float64(n))
	if se == 0 {
		if phat > 0.5 {
			return 0
		}
		return 1
	}
	z := (phat - 0.5) / se
	return 1 - standardNormalCDF(z)
}

func standardNormalCDF(z float64) float64 {
	return 0.5 * math.Erfc(-z/math.Sqrt2)
}

// BenjaminiHochberg returns, for each input p-value (in caller order), true
// if it is rejected (i.e., "statistically significant") under the BH-FDR
// procedure at level q.
func BenjaminiHochberg(pValues []float64, q float64) []bool {
	m := len(pValues)
	reject := make([]bool, m)
	if m == 0 {
		return reject
	}
	type indexed struct {
		p   float64
		idx int
	}
	ordered := make([]indexed, m)
	for i, p := range pValues {
		ordered[i] = indexed{p, i}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].p < ordered[j].p })
	largestK := -1
	for k := m; k >= 1; k-- {
		threshold := float64(k) / float64(m) * q
		if ordered[k-1].p <= threshold {
			largestK = k
			break
		}
	}
	for k := 0; k < largestK; k++ {
		reject[ordered[k].idx] = true
	}
	return reject
}

// HolmBonferroni returns, for each input p-value (in caller order), true if
// it is rejected under the Holm step-down procedure at family-wise level
// alpha.
func HolmBonferroni(pValues []float64, alpha float64) []bool {
	m := len(pValues)
	reject := make([]bool, m)
	if m == 0 {
		return reject
	}
	type indexed struct {
		p   float64
		idx int
	}
	ordered := make([]indexed, m)
	for i, p := range pValues {
		ordered[i] = indexed{p, i}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].p < ordered[j].p })
	for k, item := range ordered {
		threshold := alpha / float64(m-k)
		if item.p <= threshold {
			reject[item.idx] = true
		} else {
			break
		}
	}
	return reject
}

// AssignRegimeBlocks computes a partition-wide, market-wide BTC-return/vol
// regime id (0-3, via a within-partition median split on each dimension,
// computed once per distinct decision timestamp) and then run-length-encodes
// the chronologically sorted timestamps into contiguous block ids. It is
// computed independently for each partition (validation, leaf_wf) using only
// that partition's own rows, so it never reads the sealed discovery
// partition to define a market regime.
func AssignRegimeBlocks(btcByMS map[int64][2]float64) map[int64]int {
	if len(btcByMS) == 0 {
		return map[int64]int{}
	}
	timestamps := make([]int64, 0, len(btcByMS))
	returns := make([]float64, 0, len(btcByMS))
	vols := make([]float64, 0, len(btcByMS))
	for ms, v := range btcByMS {
		timestamps = append(timestamps, ms)
		returns = append(returns, v[0])
		vols = append(vols, v[1])
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	medianReturn := median(returns)
	medianVol := median(vols)

	out := make(map[int64]int, len(timestamps))
	blockID := -1
	previousRegime := -1
	for _, ms := range timestamps {
		v := btcByMS[ms]
		regime := 0
		if v[0] >= medianReturn {
			regime |= 1
		}
		if v[1] >= medianVol {
			regime |= 2
		}
		if regime != previousRegime {
			blockID++
			previousRegime = regime
		}
		out[ms] = blockID
	}
	return out
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
