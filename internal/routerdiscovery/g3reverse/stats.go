package g3reverse

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
)

// ConfidenceInterval is a two-sided 95% interval from cluster block bootstrap.
type ConfidenceInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type pairSample struct {
	TreatedID string
	ControlID string
	Cluster   string
	Diff      float64
}

// pairedPermutationPValue tests matched-pair sign exchangeability under the null.
func pairedPermutationPValue(diffs []float64, draws int, rng *rand.Rand, exactMax int) float64 {
	if len(diffs) == 0 {
		return 1
	}
	observed := meanFloats(diffs)
	if len(diffs) <= exactMax {
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
		out = append(out, meanFloats(vals))
	}
	return out
}

func percentileCI(vals []float64) ConfidenceInterval {
	if len(vals) == 0 {
		return ConfidenceInterval{}
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	return ConfidenceInterval{
		Low:  percentileSorted(sorted, 0.025),
		High: percentileSorted(sorted, 0.975),
	}
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

func meanFloats(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func distinctClusters(pairs []pairSample) int {
	set := map[string]bool{}
	for _, p := range pairs {
		if p.Cluster != "" {
			set[p.Cluster] = true
		}
	}
	return len(set)
}

func ruleSeedOffset(hash string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(hash))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func aggregateClusterMeans(samples map[string][]float64) map[string]float64 {
	out := map[string]float64{}
	for cluster, values := range samples {
		if len(values) == 0 {
			continue
		}
		out[cluster] = meanFloats(values)
	}
	return out
}

func worstLeaveOneClusterOut(clusters map[string]float64) float64 {
	if len(clusters) < 2 {
		return math.Inf(-1)
	}
	worst := math.Inf(1)
	for omitted := range clusters {
		var sum float64
		count := 0
		for cluster, value := range clusters {
			if cluster != omitted {
				sum += value
				count++
			}
		}
		value := sum / float64(count)
		if value < worst {
			worst = value
		}
	}
	return worst
}

func binomialUpperTail(n, k int) float64 {
	var probability float64
	for i := k; i <= n; i++ {
		probability += float64(binomialCoefficient(n, i)) / math.Pow(2, float64(n))
	}
	return probability
}

func binomialCoefficient(n, k int) int64 {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	var result int64 = 1
	for i := 1; i <= k; i++ {
		result = result * int64(n-k+i) / int64(i)
	}
	return result
}

func positiveClusterCount(clusters map[string]float64) int {
	count := 0
	for _, value := range clusters {
		if value > 0 {
			count++
		}
	}
	return count
}
