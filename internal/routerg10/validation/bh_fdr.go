package validation

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// benjaminiHochbergFDRWithFamilyDenominator applies BH-FDR to tested hypotheses
// while using a pre-registered conservative family denominator m that may include
// orphan ledger attempts counted as multiple-testing budget without fake p=1.
func benjaminiHochbergFDRWithFamilyDenominator(testedPValues []float64, familyDenominator int, q float64) ([]bool, error) {
	if q <= 0 || q > 1 {
		return nil, errors.New("fdr q must be in (0,1]")
	}
	n := len(testedPValues)
	if n == 0 {
		return nil, nil
	}
	if familyDenominator < n {
		return nil, fmt.Errorf("family denominator %d smaller than tested count %d", familyDenominator, n)
	}
	type indexed struct {
		idx int
		p   float64
	}
	order := make([]indexed, n)
	for i, p := range testedPValues {
		if math.IsNaN(p) || p < 0 || p > 1 {
			return nil, fmt.Errorf("invalid p-value at index %d: %v", i, p)
		}
		order[i] = indexed{idx: i, p: p}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].p == order[j].p {
			return order[i].idx < order[j].idx
		}
		return order[i].p < order[j].p
	})
	pass := make([]bool, n)
	maxK := -1
	for k := n; k >= 1; k-- {
		threshold := float64(k) / float64(familyDenominator) * q
		if order[k-1].p <= threshold {
			maxK = k
			break
		}
	}
	if maxK < 0 {
		return pass, nil
	}
	for i := 0; i < maxK; i++ {
		pass[order[i].idx] = true
	}
	return pass, nil
}
