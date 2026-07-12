package fingerprint

import (
	"math"
	"sort"
)

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}

func finalizeQuantile(values []float64) QuantileMetric {
	if len(values) == 0 {
		return QuantileMetric{Status: StatusUnknown}
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	p50 := percentile(cp, 0.50)
	p90 := percentile(cp, 0.90)
	if math.IsNaN(p50) {
		p50 = 0
	}
	if math.IsNaN(p90) {
		p90 = 0
	}
	return QuantileMetric{
		P50:    p50,
		P90:    p90,
		N:      len(values),
		Status: StatusAvailable,
	}
}

func finalizeRate(hits, total int) RateMetric {
	if total == 0 {
		return RateMetric{Status: StatusUnknown}
	}
	return RateMetric{
		Rate:   float64(hits) / float64(total),
		N:      total,
		Status: StatusAvailable,
	}
}
