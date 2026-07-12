package eventvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"

	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

type shockBlockAgg struct {
	Cluster string
	Sum     float64
	Count   int
}

type tierAccumulator struct {
	count         int
	days          map[string]bool
	symbols       map[string]bool
	shockClusters map[string]bool
	grossSum      float64
	feeSum        float64
	consSum       float64
	stressSum     float64
	capacityFails int
	positiveSum   float64
	maxPositive   float64
	dayPositive   map[string]float64
	delaySum      map[int]float64
	delayCount    map[int]int
	winnerCount   int
	lossCount     int
	neutralCount  int
	shockBlocks   map[string]*shockBlockAgg
}

func newTierAccumulator() *tierAccumulator {
	return &tierAccumulator{
		days:          map[string]bool{},
		symbols:       map[string]bool{},
		shockClusters: map[string]bool{},
		dayPositive:   map[string]float64{},
		delaySum:      map[int]float64{},
		delayCount:    map[int]int{},
		shockBlocks:   map[string]*shockBlockAgg{},
	}
}

func (a *tierAccumulator) add(sample FiringSample) {
	a.count++
	a.days[sample.Day] = true
	a.symbols[sample.Symbol] = true
	if sample.ShockCluster != "" && sample.ShockCluster != "none" {
		a.shockClusters[sample.ShockCluster] = true
	}
	a.grossSum += sample.GrossBPS
	a.feeSum += sample.FeeNetBPS
	a.consSum += sample.ConservativeBPS
	a.stressSum += sample.StressConservativeBPS
	a.delaySum[sample.DelayBars] += sample.ConservativeBPS
	a.delayCount[sample.DelayBars]++
	if sample.CapacityFail {
		a.capacityFails++
	}
	switch sample.OutcomeClass {
	case OutcomeWinner:
		a.winnerCount++
		if sample.ConservativeBPS > 0 {
			a.positiveSum += sample.ConservativeBPS
			if sample.ConservativeBPS > a.maxPositive {
				a.maxPositive = sample.ConservativeBPS
			}
			a.dayPositive[sample.Day] += sample.ConservativeBPS
		}
	case OutcomeLoss:
		a.lossCount++
	case OutcomeNeutral:
		a.neutralCount++
	}
	if sample.ShockCluster != "" && sample.ShockCluster != "none" {
		block := a.shockBlocks[sample.ShockCluster]
		if block == nil {
			block = &shockBlockAgg{Cluster: sample.ShockCluster}
			a.shockBlocks[sample.ShockCluster] = block
		}
		if sample.DelayBars == 0 {
			block.Sum += sample.ConservativeBPS
			block.Count++
		}
	}
}

func (a *tierAccumulator) finalize() FiringStats {
	if a.count == 0 {
		return FiringStats{DelayMeanBPS: map[int]float64{}}
	}
	n := float64(a.count)
	stats := FiringStats{
		Count:                     a.count,
		UniqueDays:                len(a.days),
		UniqueSymbols:             len(a.symbols),
		UniqueShockBlocks:         len(a.shockClusters),
		GrossMeanBPS:              a.grossSum / n,
		FeeNetMeanBPS:             a.feeSum / n,
		ConservativeMeanBPS:       a.consSum / n,
		StressConservativeMeanBPS: a.stressSum / n,
		CapacityFailRate:          float64(a.capacityFails) / n,
		WinnerCount:               a.winnerCount,
		LossCount:                 a.lossCount,
		NeutralCount:              a.neutralCount,
		DelayMeanBPS:              map[int]float64{},
	}
	for delay, count := range a.delayCount {
		if count > 0 {
			stats.DelayMeanBPS[delay] = a.delaySum[delay] / float64(count)
		}
	}
	if a.positiveSum > 0 {
		stats.MaxPositiveContributionFraction = a.maxPositive / a.positiveSum
	}
	stats.MaxDayPositiveContributionFraction =
		g4protocol.MaxDayPositiveContributionFraction(a.dayPositive)
	stats.NonzeroShockBlocks, stats.PositiveShockBlocks = a.shockBlockSigns()
	return stats
}

func (a *tierAccumulator) shockBlockSigns() (nonzero, positive int) {
	for _, block := range a.shockBlocks {
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

func (a *tierAccumulator) snapshot() checkpointTierStats {
	delayMean := map[int]float64{}
	for delay, count := range a.delayCount {
		if count > 0 {
			delayMean[delay] = a.delaySum[delay] / float64(count)
		}
	}
	blocks := make([]checkpointShockBlock, 0, len(a.shockBlocks))
	for _, block := range a.shockBlocks {
		blocks = append(blocks, checkpointShockBlock{
			Cluster: block.Cluster, Sum: block.Sum, Count: block.Count,
		})
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Cluster < blocks[j].Cluster })
	return checkpointTierStats{
		Count: a.count, Days: keys(a.days), Symbols: keys(a.symbols),
		ShockClusters: keys(a.shockClusters), GrossSum: a.grossSum, FeeSum: a.feeSum,
		ConsSum: a.consSum, StressSum: a.stressSum, CapacityFails: a.capacityFails,
		PositiveSum: a.positiveSum, MaxPositive: a.maxPositive,
		DayPositive: cloneFloatMap(a.dayPositive), DelaySum: cloneFloatDelayMap(a.delaySum),
		DelayCount: cloneIntMap(a.delayCount), WinnerCount: a.winnerCount,
		LossCount: a.lossCount, NeutralCount: a.neutralCount, ShockBlocks: blocks,
		DelayMeanBPS: delayMean,
	}
}

func restoreTierAccumulator(s checkpointTierStats) *tierAccumulator {
	acc := newTierAccumulator()
	acc.count = s.Count
	for _, day := range s.Days {
		acc.days[day] = true
	}
	for _, sym := range s.Symbols {
		acc.symbols[sym] = true
	}
	for _, cluster := range s.ShockClusters {
		acc.shockClusters[cluster] = true
	}
	acc.grossSum = s.GrossSum
	acc.feeSum = s.FeeSum
	acc.consSum = s.ConsSum
	acc.stressSum = s.StressSum
	acc.capacityFails = s.CapacityFails
	acc.positiveSum = s.PositiveSum
	acc.maxPositive = s.MaxPositive
	acc.dayPositive = cloneFloatMap(s.DayPositive)
	acc.delaySum = cloneFloatDelayMap(s.DelaySum)
	acc.delayCount = cloneIntMap(s.DelayCount)
	acc.winnerCount = s.WinnerCount
	acc.lossCount = s.LossCount
	acc.neutralCount = s.NeutralCount
	for _, block := range s.ShockBlocks {
		acc.shockBlocks[block.Cluster] = &shockBlockAgg{
			Cluster: block.Cluster, Sum: block.Sum, Count: block.Count,
		}
	}
	return acc
}

type cardAccumulator struct {
	tiers map[int]*tierAccumulator
}

func newCardAccumulator() *cardAccumulator {
	tiers := map[int]*tierAccumulator{}
	for _, tier := range protocol.ValidationInitialEquities {
		tiers[int(tier)] = newTierAccumulator()
	}
	return &cardAccumulator{tiers: tiers}
}

func (c *cardAccumulator) add(sample FiringSample) {
	if acc := c.tiers[sample.CapitalTier]; acc != nil {
		acc.add(sample)
	}
}

func (c *cardAccumulator) tierStats() map[int]FiringStats {
	out := map[int]FiringStats{}
	for tier, acc := range c.tiers {
		out[tier] = acc.finalize()
	}
	return out
}

func (c *cardAccumulator) shockBlockSigns() (nonzero, positive int) {
	merged := map[string]float64{}
	for _, acc := range c.tiers {
		for cluster, block := range acc.shockBlocks {
			if block.Count == 0 {
				continue
			}
			merged[cluster] += block.Sum
		}
	}
	for _, sum := range merged {
		if sum == 0 {
			continue
		}
		nonzero++
		if sum > 0 {
			positive++
		}
	}
	return nonzero, positive
}

func addReservoirSample(id string, value float64, reservoir *[]conservativeSample) {
	sum := sha256.Sum256([]byte(id))
	item := conservativeSample{Priority: hex.EncodeToString(sum[:]), Value: value}
	if len(*reservoir) < 256 {
		*reservoir = append(*reservoir, item)
		return
	}
	maxIndex := 0
	for i := 1; i < len(*reservoir); i++ {
		if (*reservoir)[i].Priority > (*reservoir)[maxIndex].Priority {
			maxIndex = i
		}
	}
	if item.Priority < (*reservoir)[maxIndex].Priority {
		(*reservoir)[maxIndex] = item
	}
}

type conservativeSample struct {
	Priority string
	Value    float64
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneIntMap(in map[int]int) map[int]int {
	out := make(map[int]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneFloatDelayMap(in map[int]float64) map[int]float64 {
	out := make(map[int]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func computeOutcome(
	bars []eventmap.Bar,
	decisionIndex int,
	horizon profitmap.HorizonSpec,
	direction string,
	tier int,
	costIn profitmap.CostInputs,
	funding []profitmap.FundingPoint,
	delayBars int,
) (gross, feeNet, conservative, stress float64, capacityFail bool, ok bool) {
	entryIndex := decisionIndex + 1 + delayBars
	exitIndex := entryIndex + horizon.BarCount
	if entryIndex >= len(bars) || exitIndex >= len(bars) {
		return 0, 0, 0, 0, false, false
	}
	entryTime := bars[entryIndex].OpenTime
	exitTime := bars[exitIndex].OpenTime
	entryPrice := bars[entryIndex].Open
	exitPrice := bars[exitIndex].Open
	if entryPrice <= 0 || exitPrice <= 0 {
		return 0, 0, 0, 0, false, false
	}
	wantExit := entryTime.Add(horizon.Duration)
	if !exitTime.Equal(wantExit) {
		return 0, 0, 0, 0, false, false
	}
	costIn.CapitalUSDT = float64(tier)
	costIn.FundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
	gross = profitmap.SignedGrossBPS(entryPrice, exitPrice, direction)
	feeNet, conservative, capacityFail = profitmap.ComputeReturns(gross, costIn)
	stress = profitmap.StressConservativeNet(gross, feeNet, conservative)
	if math.IsNaN(stress) || math.IsInf(stress, 0) {
		return 0, 0, 0, 0, false, false
	}
	return gross, feeNet, conservative, stress, capacityFail, true
}
