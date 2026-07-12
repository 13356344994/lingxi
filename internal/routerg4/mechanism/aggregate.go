package mechanism

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/validation"
)

const maxConservativeReservoir = 256

type wfSample struct {
	Symbol                string
	DecisionTime          time.Time
	GrossBPS              float64
	FeeNetBPS             float64
	ConservativeBPS       float64
	DelayConservativeBPS  float64
	StressConservativeBPS float64
	CapacityFail          bool
}

type conservativeReservoirItem struct {
	Priority string
	Value    float64
}

type wfAccumulator struct {
	count         int
	days          map[string]bool
	symbols       map[string]bool
	grossSum      float64
	feeSum        float64
	consSum       float64
	delaySum      float64
	stressSum     float64
	capacityFails int
	positiveSum   float64
	maxPositive   float64
	horizon       time.Duration
	blockSums     map[string]float64
	blockCounts   map[string]int
	reservoir     []conservativeReservoirItem
}

func newWFAccumulator(horizon ...time.Duration) *wfAccumulator {
	var candidateHorizon time.Duration
	if len(horizon) > 0 {
		candidateHorizon = horizon[0]
	}
	return &wfAccumulator{
		days:        map[string]bool{},
		symbols:     map[string]bool{},
		horizon:     candidateHorizon,
		blockSums:   map[string]float64{},
		blockCounts: map[string]int{},
	}
}

func (a *wfAccumulator) add(sample wfSample, sampleID string) {
	a.count++
	a.days[sample.DecisionTime.UTC().Format("2006-01-02")] = true
	a.symbols[sample.Symbol] = true
	a.grossSum += sample.GrossBPS
	a.feeSum += sample.FeeNetBPS
	a.consSum += sample.ConservativeBPS
	a.delaySum += sample.DelayConservativeBPS
	a.stressSum += sample.StressConservativeBPS
	if block, ok := independentBlockKey(sample.DecisionTime, a.horizon); ok {
		a.blockSums[block] += sample.ConservativeBPS
		a.blockCounts[block]++
	}
	a.addReservoir(sampleID, sample.ConservativeBPS)
	if sample.ConservativeBPS > 0 {
		a.positiveSum += sample.ConservativeBPS
		if sample.ConservativeBPS > a.maxPositive {
			a.maxPositive = sample.ConservativeBPS
		}
	}
	if sample.CapacityFail {
		a.capacityFails++
	}
}

func (a *wfAccumulator) finalize() validation.FiringStats {
	if a.count == 0 {
		return validation.FiringStats{}
	}
	n := float64(a.count)
	stats := validation.FiringStats{
		Count:                     a.count,
		UniqueDays:                len(a.days),
		UniqueSymbols:             len(a.symbols),
		GrossMeanBPS:              a.grossSum / n,
		FeeNetMeanBPS:             a.feeSum / n,
		ConservativeMeanBPS:       a.consSum / n,
		DelayConservativeMeanBPS:  a.delaySum / n,
		StressConservativeMeanBPS: a.stressSum / n,
		CapacityFailRate:          float64(a.capacityFails) / n,
	}
	if a.positiveSum > 0 {
		stats.MaxPositiveContributionFraction = a.maxPositive / a.positiveSum
	}
	if len(a.reservoir) > 0 {
		values := make([]float64, len(a.reservoir))
		for i := range a.reservoir {
			values[i] = a.reservoir[i].Value
		}
		sort.Float64s(values)
		stats.ConservativeP10BPS = percentile(values, 0.10)
		stats.WorstConservativeBPS = values[0]
	}
	return stats
}

func (a *wfAccumulator) blockMeans() map[string]float64 {
	out := map[string]float64{}
	for block, sum := range a.blockSums {
		count := a.blockCounts[block]
		if count > 0 {
			out[block] = sum / float64(count)
		}
	}
	return out
}

// independentBlockKey creates UTC-epoch-anchored candidate-specific blocks.
// Each 24h observation window is followed by a full candidate-horizon embargo.
func independentBlockKey(at time.Time, horizon time.Duration) (string, bool) {
	if horizon <= 0 {
		return "", false
	}
	window := time.Duration(protocol.StrictWFObservationWindowHours) * time.Hour
	step := window + horizon
	unixNanos := at.UTC().UnixNano()
	stepNanos := int64(step)
	blockIndex := unixNanos / stepNanos
	if unixNanos < 0 && unixNanos%stepNanos != 0 {
		blockIndex--
	}
	startNanos := blockIndex * stepNanos
	offset := unixNanos - startNanos
	if offset < 0 || offset >= int64(window) {
		return "", false
	}
	return time.Unix(0, startNanos).UTC().Format(time.RFC3339Nano), true
}

func (a *wfAccumulator) addReservoir(sampleID string, value float64) {
	sum := sha256.Sum256([]byte(sampleID))
	item := conservativeReservoirItem{Priority: hex.EncodeToString(sum[:]), Value: value}
	if len(a.reservoir) < maxConservativeReservoir {
		a.reservoir = append(a.reservoir, item)
		return
	}
	maxIndex := 0
	for i := 1; i < len(a.reservoir); i++ {
		if a.reservoir[i].Priority > a.reservoir[maxIndex].Priority {
			maxIndex = i
		}
	}
	if item.Priority < a.reservoir[maxIndex].Priority {
		a.reservoir[maxIndex] = item
	}
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

func sampleFromWFEvent(ev validationEventContext) wfSample {
	return wfSample{
		Symbol:                ev.Symbol,
		DecisionTime:          ev.DecisionTime,
		GrossBPS:              ev.GrossBPS,
		FeeNetBPS:             ev.FeeNetBPS,
		ConservativeBPS:       ev.ConservativeBPS,
		DelayConservativeBPS:  ev.DelayConservativeBPS,
		StressConservativeBPS: ev.StressConservativeBPS,
		CapacityFail:          ev.CapacityFail,
	}
}

func evaluatePreliminaryWFGates(stats validation.FiringStats, independentBlocks ...int) (WFGateChecks, string) {
	blockCount := 0
	if len(independentBlocks) > 0 {
		blockCount = independentBlocks[0]
	}
	checks := WFGateChecks{
		MinFiringsMet:            stats.Count >= protocol.StrictWFMinFirings,
		MinUniqueDaysMet:         stats.UniqueDays >= protocol.StrictWFMinUniqueDays,
		MinUniqueSymbolsMet:      stats.UniqueSymbols >= protocol.StrictWFMinUniqueSymbols,
		MinIndependentBlocksMet:  blockCount >= protocol.StrictWFMinIndependentBlocks,
		FeeNetMeanPositive:       stats.FeeNetMeanBPS > 0,
		ConservativeMeanPositive: stats.ConservativeMeanBPS > 0,
		DelayMeanPositive:        stats.DelayConservativeMeanBPS > 0,
		StressMeanPositive:       stats.StressConservativeMeanBPS > 0,
		MaxContributionOK:        stats.MaxPositiveContributionFraction <= protocol.StrictWFMaxSingleEventContributionFraction,
		CapacityFailFractionOK:   stats.CapacityFailRate < protocol.StrictWFMaxCapacityFailFraction,
	}
	if !checks.MinFiringsMet || !checks.MinUniqueDaysMet || !checks.MinUniqueSymbolsMet ||
		!checks.MinIndependentBlocksMet {
		return checks, protocol.StrictWFInsufficientSampleState
	}
	if !checks.MaxContributionOK {
		return checks, protocol.StrictWFGateFailureState
	}
	if !checks.CapacityFailFractionOK {
		return checks, protocol.StrictWFGateFailureState
	}
	if !checks.FeeNetMeanPositive || !checks.ConservativeMeanPositive ||
		!checks.DelayMeanPositive || !checks.StressMeanPositive {
		return checks, protocol.StrictWFGateFailureState
	}
	checks.SignTestEligible = true
	return checks, ""
}

func nonzeroBlockSignStats(blockMeans map[string]float64) (nonzeroBlocks, positiveBlocks int) {
	for _, mean := range blockMeans {
		if mean == 0 {
			continue
		}
		nonzeroBlocks++
		if mean > 0 {
			positiveBlocks++
		}
	}
	return nonzeroBlocks, positiveBlocks
}

func twoSidedBinomialSignPValue(nonzeroBlocks, positiveBlocks int) float64 {
	if nonzeroBlocks == 0 {
		return 1
	}
	negativeBlocks := nonzeroBlocks - positiveBlocks
	extreme := positiveBlocks
	if negativeBlocks > extreme {
		extreme = negativeBlocks
	}
	logTail := math.Inf(-1)
	for i := extreme; i <= nonzeroBlocks; i++ {
		logTail = logAddExp(logTail, logBinomialHalfPMF(nonzeroBlocks, i))
	}
	p := math.Exp(math.Ln2 + logTail)
	if p > 1 {
		return 1
	}
	return p
}

func logBinomialHalfPMF(n, k int) float64 {
	logN, _ := math.Lgamma(float64(n + 1))
	logK, _ := math.Lgamma(float64(k + 1))
	logNK, _ := math.Lgamma(float64(n - k + 1))
	return logN - logK - logNK - float64(n)*math.Ln2
}

func logAddExp(a, b float64) float64 {
	if math.IsInf(a, -1) {
		return b
	}
	if math.IsInf(b, -1) {
		return a
	}
	if a < b {
		a, b = b, a
	}
	return a + math.Log1p(math.Exp(b-a))
}

func applyHolm(results []WFResult, alpha float64) {
	indexes := make([]int, 0)
	for i, result := range results {
		if result.GateChecks.SignTestEligible && result.Status == "" {
			indexes = append(indexes, i)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		return results[indexes[i]].SignPValue < results[indexes[j]].SignPValue
	})
	m := len(indexes)
	rejected := false
	for rank, index := range indexes {
		limit := alpha / float64(m-rank)
		results[index].HolmLimit = limit
		if rejected || results[index].SignPValue > limit {
			rejected = true
			results[index].GateChecks.HolmPass = false
			results[index].GateChecks.SignTestPass = results[index].SignPValue <= limit
			results[index].Status = protocol.StrictWFGateFailureState
			results[index].Reason = "HOLM_FAMILY_REJECTION"
			continue
		}
		results[index].GateChecks.HolmPass = true
		results[index].GateChecks.SignTestPass = true
		results[index].Status = protocol.StrictWFPassState
		results[index].Reason = "PASS"
	}
}
