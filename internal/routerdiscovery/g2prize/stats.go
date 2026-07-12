package g2prize

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sort"

	"lingxi/internal/routerg2/protocol"
)

// pairedPermutationPValue is the permutation/placebo test for a matched-pair
// design: under the null hypothesis that firing the combination's rule
// carries no true differential effect over its matched controls, each
// pair's sign is exchangeable. It draws random sign flips of the observed
// per-pair differences and reports the one-sided fraction of placebo draws
// whose mean is at least as extreme as the observed mean. A real edge that
// only exists because of winner-only cherry-picking collapses under this
// test; a genuine, direction-consistent effect across pairs does not.
func pairedPermutationPValue(diffs []float64, draws int, rng *rand.Rand) float64 {
	if len(diffs) == 0 {
		return 1
	}
	observed := mean(diffs)
	if len(diffs) <= protocol.BackwardExactPermutationMaxPairs {
		total := 1 << len(diffs)
		count := 0
		for mask := 0; mask < total; mask++ {
			sum := 0.0
			for index, diff := range diffs {
				if mask&(1<<index) == 0 {
					sum -= diff
				} else {
					sum += diff
				}
			}
			if sum/float64(len(diffs)) >= observed {
				count++
			}
		}
		return float64(count) / float64(total)
	}
	count := 0
	for i := 0; i < draws; i++ {
		sum := 0.0
		for _, d := range diffs {
			if rng.Intn(2) == 0 {
				sum -= d
			} else {
				sum += d
			}
		}
		if sum/float64(len(diffs)) >= observed {
			count++
		}
	}
	return float64(count+1) / float64(draws+1)
}

// clusterBlockBootstrap resamples whole clusters (not individual pairs) with
// replacement until it has enough pairs to match the original sample size,
// then records the mean per draw. Resampling by cluster rather than by pair
// is what keeps the resulting interval honest about correlated, non-
// independent pairs sharing the same underlying shock/regime.
func clusterBlockBootstrap(pairs []pairSample, draws int, seed int64) []float64 {
	if len(pairs) == 0 {
		return nil
	}
	blocks := map[string][]float64{}
	var keys []string
	for _, p := range pairs {
		if _, ok := blocks[p.Cluster]; !ok {
			keys = append(keys, p.Cluster)
		}
		blocks[p.Cluster] = append(blocks[p.Cluster], p.Diff)
	}
	if len(keys) == 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed))
	out := make([]float64, 0, draws)
	for i := 0; i < draws; i++ {
		vals := make([]float64, 0, len(pairs))
		for len(vals) < len(pairs) {
			block := blocks[keys[rng.Intn(len(keys))]]
			vals = append(vals, block...)
		}
		if len(vals) > len(pairs) {
			vals = vals[:len(pairs)]
		}
		out = append(out, mean(vals))
	}
	return out
}

func percentileCI(vals []float64) ConfidenceInterval {
	if len(vals) == 0 {
		return ConfidenceInterval{}
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	return ConfidenceInterval{Low: percentileSorted(sorted, 0.025), High: percentileSorted(sorted, 0.975)}
}

func percentileSorted(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	pos := p * float64(len(vals)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return vals[lo]
	}
	frac := pos - float64(lo)
	return vals[lo]*(1-frac) + vals[hi]*frac
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// comboSeedOffset derives a deterministic, combination-specific offset so
// different combinations draw from independent (but reproducible) random
// streams instead of silently reusing the exact same sequence.
func comboSeedOffset(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
