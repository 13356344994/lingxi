package changepoint

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"lingxi/internal/datadrivenv3/modelapi"
)

// discriminativeThresholds builds relative feature-space clauses that separate
// the segment from the complement. Absolute dates are never included.
func discriminativeThresholds(blocks []dayBlock, start, end int, centroid [modelapi.FeatureCount]float64, present uint64) []FeatureThreshold {
	type scored struct {
		idx     int
		delta   float64
		meanIn  float64
		meanOut float64
	}
	var scores []scored
	for i := 0; i < modelapi.FeatureCount; i++ {
		bit := uint64(1) << uint(i)
		if present&bit == 0 {
			continue
		}
		var sumIn, sumOut float64
		var nIn, nOut int
		for bi, blk := range blocks {
			if blk.Present[i] == 0 {
				continue
			}
			if bi >= start && bi < end {
				sumIn += blk.Mean[i]
				nIn++
			} else {
				sumOut += blk.Mean[i]
				nOut++
			}
		}
		if nIn == 0 {
			continue
		}
		meanIn := sumIn / float64(nIn)
		meanOut := 0.0
		if nOut > 0 {
			meanOut = sumOut / float64(nOut)
		}
		delta := math.Abs(meanIn - meanOut)
		scores = append(scores, scored{idx: i, delta: delta, meanIn: meanIn, meanOut: meanOut})
	}
	// Sort by delta desc, then index asc.
	for i := 0; i < len(scores); i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].delta > scores[i].delta+1e-15 || (math.Abs(scores[j].delta-scores[i].delta) <= 1e-15 && scores[j].idx < scores[i].idx) {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}
	const maxClauses = 3
	out := make([]FeatureThreshold, 0, maxClauses+1)
	limit := maxClauses
	if len(scores) < limit {
		limit = len(scores)
	}
	for i := 0; i < limit; i++ {
		s := scores[i]
		mid := (s.meanIn + s.meanOut) / 2
		op := ">="
		if s.meanIn < s.meanOut {
			op = "<"
		}
		out = append(out, FeatureThreshold{
			FeatureIndex: s.idx,
			FeatureName:  modelapi.FeatureOrder[s.idx],
			Op:           op,
			Value:        mid,
		})
	}
	// Missing-aware: require the top present feature to be observed at predict time.
	if len(out) > 0 {
		out = append([]FeatureThreshold{{
			FeatureIndex: out[0].FeatureIndex,
			FeatureName:  out[0].FeatureName,
			Op:           "present",
		}}, out...)
	}
	_ = centroid
	return out
}

func formatPredicate(r RegimeDescriptor) string {
	parts := make([]string, 0, len(r.Thresholds)+3)
	parts = append(parts, fmt.Sprintf("regime_id=%s", r.RegimeID))
	if r.Rare {
		parts = append(parts, "rare_state=true")
	} else {
		parts = append(parts, "rare_state=false")
	}
	for _, th := range r.Thresholds {
		switch th.Op {
		case "present":
			parts = append(parts, fmt.Sprintf("present(%s)", th.FeatureName))
		case "missing":
			parts = append(parts, fmt.Sprintf("missing(%s)", th.FeatureName))
		default:
			parts = append(parts, fmt.Sprintf("%s%s%s", th.FeatureName, th.Op, formatFloat(th.Value)))
		}
	}
	parts = append(parts, "reason="+sanitizeReason(r.Reason))
	return strings.Join(parts, " && ")
}

func sanitizeReason(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ";", ",")
	return s
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 12, 64)
}

func evaluatePredicate(pred string, market modelapi.MarketState) (bool, error) {
	clauses := strings.Split(pred, " && ")
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if strings.HasPrefix(clause, "regime_id=") || strings.HasPrefix(clause, "rare_state=") || strings.HasPrefix(clause, "reason=") {
			continue
		}
		if strings.HasPrefix(clause, "present(") && strings.HasSuffix(clause, ")") {
			name := clause[len("present(") : len(clause)-1]
			idx, ok := featureIndex(name)
			if !ok {
				return false, fmt.Errorf("unknown feature %q", name)
			}
			if market.MissingBits&(uint64(1)<<uint(idx)) != 0 {
				return false, nil
			}
			continue
		}
		if strings.HasPrefix(clause, "missing(") && strings.HasSuffix(clause, ")") {
			name := clause[len("missing(") : len(clause)-1]
			idx, ok := featureIndex(name)
			if !ok {
				return false, fmt.Errorf("unknown feature %q", name)
			}
			if market.MissingBits&(uint64(1)<<uint(idx)) == 0 {
				return false, nil
			}
			continue
		}
		name, op, value, ok := parseCompare(clause)
		if !ok {
			return false, fmt.Errorf("unparseable predicate clause %q", clause)
		}
		idx, ok := featureIndex(name)
		if !ok {
			return false, fmt.Errorf("unknown feature %q", name)
		}
		if market.MissingBits&(uint64(1)<<uint(idx)) != 0 {
			return false, nil
		}
		v := market.Features[idx]
		switch op {
		case ">=":
			if !(v >= value) {
				return false, nil
			}
		case "<":
			if !(v < value) {
				return false, nil
			}
		case ">":
			if !(v > value) {
				return false, nil
			}
		case "<=":
			if !(v <= value) {
				return false, nil
			}
		default:
			return false, fmt.Errorf("unsupported op %q", op)
		}
	}
	return true, nil
}

func parseCompare(clause string) (name, op string, value float64, ok bool) {
	for _, candidate := range []string{">=", "<=", ">", "<"} {
		if i := strings.Index(clause, candidate); i > 0 {
			name = clause[:i]
			op = candidate
			var err error
			value, err = strconv.ParseFloat(clause[i+len(candidate):], 64)
			if err != nil {
				return "", "", 0, false
			}
			return name, op, value, true
		}
	}
	return "", "", 0, false
}

func featureIndex(name string) (int, bool) {
	for i, n := range modelapi.FeatureOrder {
		if n == name {
			return i, true
		}
	}
	return -1, false
}

func regimeDistance(r RegimeDescriptor, market modelapi.MarketState) float64 {
	d := 0.0
	used := 0
	for f := 0; f < modelapi.FeatureCount; f++ {
		bit := uint64(1) << uint(f)
		if r.PresentMask&bit == 0 {
			continue
		}
		if market.MissingBits&bit != 0 {
			continue
		}
		diff := market.Features[f] - r.Centroid[f]
		d += diff * diff
		used++
	}
	if used == 0 {
		return math.Inf(1)
	}
	return d / float64(used)
}

// nearestRegime picks the closest relative-state regime by missing-aware centroid distance.
func nearestRegime(regimes []RegimeDescriptor, market modelapi.MarketState) int {
	best := -1
	bestDist := math.Inf(1)
	for i, r := range regimes {
		d := regimeDistance(r, market)
		if math.IsInf(d, 1) {
			continue
		}
		if d < bestDist-1e-15 || (math.Abs(d-bestDist) <= 1e-15 && (best < 0 || i < best)) {
			bestDist = d
			best = i
		}
	}
	return best
}
