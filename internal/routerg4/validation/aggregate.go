package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg4/protocol"
)

const maxConservativeReservoir = 256

type conservativeSample struct {
	Priority string  `json:"priority"`
	Value    float64 `json:"value"`
}

type blockAggregate struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Sum   float64   `json:"sum"`
	Count int       `json:"count"`
}

// firingAccumulator collects firing samples for one candidate.
type firingAccumulator struct {
	count         int
	days          map[string]bool
	symbols       map[string]bool
	grossSum      float64
	feeSum        float64
	consSum       float64
	delaySum      float64
	stressSum     float64
	consPositive  int
	capacityFails int
	positiveSum   float64
	maxPositive   float64
	worst         float64
	reservoir     []conservativeSample
	dayPositive   map[string]float64
	blocks        []blockAggregate
}

func newFiringAccumulator(plans ...[]protocol.UTCAnchoredBlock) *firingAccumulator {
	acc := &firingAccumulator{
		days:        map[string]bool{},
		symbols:     map[string]bool{},
		dayPositive: map[string]float64{},
	}
	if len(plans) > 0 {
		acc.blocks = make([]blockAggregate, len(plans[0]))
		for i, block := range plans[0] {
			acc.blocks[i] = blockAggregate{Start: block.Start, End: block.End}
		}
	}
	return acc
}

func (a *firingAccumulator) add(sample FiringSample, sampleID string) {
	a.count++
	a.days[sample.Day] = true
	a.symbols[sample.Symbol] = true
	a.grossSum += sample.GrossBPS
	a.feeSum += sample.FeeNetBPS
	a.consSum += sample.ConservativeBPS
	a.delaySum += sample.DelayConservativeBPS
	a.stressSum += sample.StressConservativeBPS
	if a.count == 1 || sample.ConservativeBPS < a.worst {
		a.worst = sample.ConservativeBPS
	}
	a.addReservoir(sampleID, sample.ConservativeBPS)
	if sample.ConservativeBPS > 0 {
		a.consPositive++
		a.positiveSum += sample.ConservativeBPS
		if sample.ConservativeBPS > a.maxPositive {
			a.maxPositive = sample.ConservativeBPS
		}
		a.dayPositive[sample.Day] += sample.ConservativeBPS
	}
	a.addBlock(sample.DecisionTime, sample.ConservativeBPS)
	if sample.CapacityFail {
		a.capacityFails++
	}
}

func (a *firingAccumulator) finalize() FiringStats {
	if a.count == 0 {
		return FiringStats{}
	}
	n := float64(a.count)
	stats := FiringStats{
		Count:                     a.count,
		UniqueDays:                len(a.days),
		UniqueSymbols:             len(a.symbols),
		GrossMeanBPS:              a.grossSum / n,
		FeeNetMeanBPS:             a.feeSum / n,
		ConservativeMeanBPS:       a.consSum / n,
		DelayConservativeMeanBPS:  a.delaySum / n,
		StressConservativeMeanBPS: a.stressSum / n,
		ConservativePositiveRate:  float64(a.consPositive) / n,
		CapacityFailRate:          float64(a.capacityFails) / n,
	}
	if len(a.reservoir) > 0 {
		values := make([]float64, len(a.reservoir))
		for i := range a.reservoir {
			values[i] = a.reservoir[i].Value
		}
		sort.Float64s(values)
		stats.WorstConservativeBPS = a.worst
		stats.ConservativeP10BPS = percentile(values, 0.10)
	}
	if a.positiveSum > 0 {
		stats.MaxPositiveContributionFraction = a.maxPositive / a.positiveSum
	}
	stats.MaxDayPositiveContributionFraction =
		protocol.MaxDayPositiveContributionFraction(a.dayPositive)
	stats.NonzeroBlocks, stats.PositiveBlocks = a.blockSigns()
	return stats
}

func (a *firingAccumulator) addBlock(decision time.Time, conservative float64) {
	for i := range a.blocks {
		block := &a.blocks[i]
		if decision.Before(block.Start) {
			return
		}
		if !decision.Before(block.End) {
			continue
		}
		block.Sum += conservative
		block.Count++
		return
	}
}

func (a *firingAccumulator) blockSigns() (nonzero, positive int) {
	for _, block := range a.blocks {
		if block.Count == 0 || block.Sum == 0 {
			continue
		}
		nonzero++
		if block.Sum > 0 {
			positive++
		}
	}
	return nonzero, positive
}

func (a *firingAccumulator) addReservoir(sampleID string, value float64) {
	sum := sha256.Sum256([]byte(sampleID))
	item := conservativeSample{Priority: hex.EncodeToString(sum[:]), Value: value}
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

func sampleFromEvent(ev EventContext) FiringSample {
	return FiringSample{
		Symbol:                ev.Symbol,
		DecisionTime:          ev.DecisionTime,
		Day:                   ev.DecisionTime.UTC().Format("2006-01-02"),
		GrossBPS:              ev.GrossBPS,
		FeeNetBPS:             ev.FeeNetBPS,
		ConservativeBPS:       ev.ConservativeBPS,
		DelayConservativeBPS:  ev.DelayConservativeBPS,
		StressConservativeBPS: ev.StressConservativeBPS,
		CapacityFail:          ev.CapacityFail,
	}
}
