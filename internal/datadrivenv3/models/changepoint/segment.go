package changepoint

import (
	"errors"
	"math"

	"lingxi/internal/datadrivenv3/modelapi"
)

type dayBlock struct {
	Day         int64
	Count       int
	Sum         [modelapi.FeatureCount]float64
	Present     [modelapi.FeatureCount]int
	MissingRate [modelapi.FeatureCount]float64
	Mean        [modelapi.FeatureCount]float64
	Vector      []float64 // compact missing-aware signature for change cost
}

func buildDayBlocks(examples []modelapi.TrainingExample) ([]dayBlock, error) {
	if len(examples) == 0 {
		return nil, errors.New("no examples for day blocks")
	}
	byDay := map[int64]*dayBlock{}
	order := make([]int64, 0)
	for _, ex := range examples {
		day := ex.Market.DecisionMS / MSPerDay
		blk, ok := byDay[day]
		if !ok {
			blk = &dayBlock{Day: day}
			byDay[day] = blk
			order = append(order, day)
		}
		blk.Count++
		for i := 0; i < modelapi.FeatureCount; i++ {
			bit := uint64(1) << uint(i)
			if ex.Market.MissingBits&bit != 0 {
				continue
			}
			v := ex.Market.Features[i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			blk.Sum[i] += v
			blk.Present[i]++
		}
	}
	blocks := make([]dayBlock, 0, len(order))
	for _, day := range order {
		blk := *byDay[day]
		for i := 0; i < modelapi.FeatureCount; i++ {
			if blk.Present[i] > 0 {
				blk.Mean[i] = blk.Sum[i] / float64(blk.Present[i])
				blk.MissingRate[i] = 1 - float64(blk.Present[i])/float64(blk.Count)
			} else {
				blk.Mean[i] = 0
				blk.MissingRate[i] = 1
			}
		}
		blk.Vector = signature(blk)
		blocks = append(blocks, blk)
	}
	return blocks, nil
}

// signature uses a fixed subset of distributional features + missing rates.
// Indices are stable; utc cyclical features are included as relative state, not dates.
func signature(blk dayBlock) []float64 {
	// Focus on return/vol/liquidity/funding ranks — distribution change drivers.
	idx := []int{0, 1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 14, 15, 16, 17}
	out := make([]float64, 0, len(idx)*2)
	for _, i := range idx {
		if blk.Present[i] == 0 {
			out = append(out, 0, 1) // value placeholder, fully missing
			continue
		}
		out = append(out, blk.Mean[i], blk.MissingRate[i])
	}
	return out
}

func blockCentroid(blocks []dayBlock) ([modelapi.FeatureCount]float64, uint64) {
	var sum [modelapi.FeatureCount]float64
	var n [modelapi.FeatureCount]int
	for _, blk := range blocks {
		for i := 0; i < modelapi.FeatureCount; i++ {
			if blk.Present[i] == 0 {
				continue
			}
			sum[i] += blk.Mean[i]
			n[i]++
		}
	}
	var centroid [modelapi.FeatureCount]float64
	var present uint64
	for i := 0; i < modelapi.FeatureCount; i++ {
		if n[i] == 0 {
			continue
		}
		centroid[i] = sum[i] / float64(n[i])
		present |= uint64(1) << uint(i)
	}
	return centroid, present
}

// segmentBlocks finds exactly regimeCount contiguous segments via exact DP,
// each of length >= minDays, minimizing SSE + (k-1)*penalty.
func segmentBlocks(blocks []dayBlock, regimeCount, minDays int, penalty float64) ([][2]int, float64, error) {
	n := len(blocks)
	if regimeCount < 2 || minDays < 1 || n < regimeCount*minDays {
		return nil, 0, errors.New("segmentation constraints unsatisfiable")
	}
	segCost := make([][]float64, n)
	for i := 0; i < n; i++ {
		segCost[i] = make([]float64, n+1)
		for j := i + 1; j <= n; j++ {
			if j-i < minDays {
				segCost[i][j] = math.Inf(1)
				continue
			}
			segCost[i][j] = segmentSSE(blocks[i:j])
		}
	}
	const inf = 1e100
	dp := make([][]float64, regimeCount+1)
	prev := make([][]int, regimeCount+1)
	for k := 0; k <= regimeCount; k++ {
		dp[k] = make([]float64, n+1)
		prev[k] = make([]int, n+1)
		for i := 0; i <= n; i++ {
			dp[k][i] = inf
			prev[k][i] = -1
		}
	}
	dp[0][0] = 0
	for k := 1; k <= regimeCount; k++ {
		for end := k * minDays; end <= n; end++ {
			best := inf
			bestStart := -1
			for start := (k - 1) * minDays; start <= end-minDays; start++ {
				c := dp[k-1][start] + segCost[start][end]
				if k > 1 {
					c += penalty
				}
				if c < best-1e-15 || (math.Abs(c-best) <= 1e-15 && start < bestStart) {
					best = c
					bestStart = start
				}
			}
			dp[k][end] = best
			prev[k][end] = bestStart
		}
	}
	if dp[regimeCount][n] >= inf/2 {
		return nil, 0, errors.New("no feasible segmentation")
	}
	bounds := make([][2]int, regimeCount)
	end := n
	for k := regimeCount; k >= 1; k-- {
		start := prev[k][end]
		if start < 0 {
			return nil, 0, errors.New("segmentation backtrack failed")
		}
		bounds[k-1] = [2]int{start, end}
		end = start
	}
	return bounds, dp[regimeCount][n], nil
}

func segmentSSE(blocks []dayBlock) float64 {
	if len(blocks) == 0 {
		return 0
	}
	dim := len(blocks[0].Vector)
	mean := make([]float64, dim)
	weight := make([]float64, dim)
	for _, blk := range blocks {
		for d := 0; d < dim; d++ {
			// Even indices are values; odd are missing rates. Always defined.
			mean[d] += blk.Vector[d]
			weight[d]++
		}
	}
	for d := 0; d < dim; d++ {
		if weight[d] > 0 {
			mean[d] /= weight[d]
		}
	}
	sse := 0.0
	for _, blk := range blocks {
		for d := 0; d < dim; d++ {
			diff := blk.Vector[d] - mean[d]
			sse += diff * diff
		}
	}
	return sse
}
