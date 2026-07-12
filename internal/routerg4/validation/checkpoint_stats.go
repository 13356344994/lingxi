package validation

import "sort"

// checkpointCandidateStats is exact serializable accumulator state.
type checkpointCandidateStats struct {
	Count         int                  `json:"count"`
	Days          []string             `json:"days"`
	Symbols       []string             `json:"symbols"`
	GrossSum      float64              `json:"gross_sum"`
	FeeSum        float64              `json:"fee_sum"`
	ConsSum       float64              `json:"cons_sum"`
	DelaySum      float64              `json:"delay_sum"`
	StressSum     float64              `json:"stress_sum"`
	ConsPositive  int                  `json:"cons_positive"`
	CapacityFails int                  `json:"capacity_fails"`
	PositiveSum   float64              `json:"positive_sum"`
	MaxPositive   float64              `json:"max_positive"`
	Worst         float64              `json:"worst"`
	Reservoir     []conservativeSample `json:"reservoir"`
	DayPositive   map[string]float64   `json:"day_positive"`
	Blocks        []blockAggregate     `json:"blocks"`
}

func exportCheckpointCandidateStats(accumulators []*firingAccumulator) []checkpointCandidateStats {
	out := make([]checkpointCandidateStats, len(accumulators))
	for i, acc := range accumulators {
		if acc == nil {
			continue
		}
		days := make([]string, 0, len(acc.days))
		for d := range acc.days {
			days = append(days, d)
		}
		sort.Strings(days)
		symbols := make([]string, 0, len(acc.symbols))
		for s := range acc.symbols {
			symbols = append(symbols, s)
		}
		sort.Strings(symbols)
		out[i] = checkpointCandidateStats{
			Count:         acc.count,
			Days:          days,
			Symbols:       symbols,
			GrossSum:      acc.grossSum,
			FeeSum:        acc.feeSum,
			ConsSum:       acc.consSum,
			DelaySum:      acc.delaySum,
			StressSum:     acc.stressSum,
			ConsPositive:  acc.consPositive,
			CapacityFails: acc.capacityFails,
			PositiveSum:   acc.positiveSum,
			MaxPositive:   acc.maxPositive,
			Worst:         acc.worst,
			Reservoir:     append([]conservativeSample(nil), acc.reservoir...),
			DayPositive:   cloneFloatMap(acc.dayPositive),
			Blocks:        append([]blockAggregate(nil), acc.blocks...),
		}
	}
	return out
}

func restoreAccumulatorsFromCheckpoint(stats []checkpointCandidateStats) []*firingAccumulator {
	out := make([]*firingAccumulator, len(stats))
	for i, s := range stats {
		acc := newFiringAccumulator()
		acc.count = s.Count
		acc.grossSum = s.GrossSum
		acc.feeSum = s.FeeSum
		acc.consSum = s.ConsSum
		acc.delaySum = s.DelaySum
		acc.stressSum = s.StressSum
		acc.consPositive = s.ConsPositive
		acc.capacityFails = s.CapacityFails
		acc.positiveSum = s.PositiveSum
		acc.maxPositive = s.MaxPositive
		acc.worst = s.Worst
		acc.reservoir = append([]conservativeSample(nil), s.Reservoir...)
		acc.dayPositive = cloneFloatMap(s.DayPositive)
		acc.blocks = append([]blockAggregate(nil), s.Blocks...)
		for _, d := range s.Days {
			acc.days[d] = true
		}
		for _, sym := range s.Symbols {
			acc.symbols[sym] = true
		}
		out[i] = acc
	}
	return out
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
