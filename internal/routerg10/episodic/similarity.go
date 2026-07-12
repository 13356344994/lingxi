package episodic

import (
	"math"
	"strings"
)

type featureWeights struct {
	returnBPS       float64
	priorReturnBPS  float64
	btcRelativeBPS  float64
	barRangeBPS     float64
	eventPhase      float64
	rollingVolume   float64
	feeDeltaBPS     float64
	slipDeltaBPS    float64
	latencyP90MS    float64
	eventFamilyMatch float64
}

var defaultWeights = featureWeights{
	returnBPS:         1.0,
	priorReturnBPS:  0.8,
	btcRelativeBPS:  0.6,
	barRangeBPS:     0.5,
	eventPhase:      1.2,
	rollingVolume:   0.4,
	feeDeltaBPS:     0.7,
	slipDeltaBPS:    0.7,
	latencyP90MS:    0.0005,
	eventFamilyMatch: 2.0,
}

// SimilarityScore returns higher-is-better similarity in [0,1].
func SimilarityScore(q QueryVector, ep FrozenEpisode) float64 {
	w := defaultWeights
	var dist float64
	dist += w.returnBPS * sqNorm(q.State.ReturnBPS, ep.State.ReturnBPS, 200)
	dist += w.priorReturnBPS * sqNorm(q.State.PriorReturnBPS, ep.State.PriorReturnBPS, 200)
	dist += w.btcRelativeBPS * sqNorm(q.State.BTCRelativeBPS, ep.State.BTCRelativeBPS, 150)
	dist += w.barRangeBPS * sqNorm(q.State.BarRangeBPS, ep.State.BarRangeBPS, 300)
	dist += w.eventPhase * sqNorm(q.EventPhase, ep.EventPhase, 1)
	dist += w.rollingVolume * sqNorm(q.State.RollingVolumeUSDT, ep.State.RollingVolumeUSDT, 1e7)
	dist += w.feeDeltaBPS * sqNorm(q.PersonalExec.FeeDeltaBPS, ep.PersonalExec.FeeDeltaBPS, 20)
	dist += w.slipDeltaBPS * sqNorm(q.PersonalExec.SlipDeltaBPS, ep.PersonalExec.SlipDeltaBPS, 20)
	dist += w.latencyP90MS * sqNorm(q.PersonalExec.LatencyP90MS, ep.PersonalExec.LatencyP90MS, 2000)
	if q.EventFamily != "" && ep.EventFamily != "" {
		if q.EventFamily == ep.EventFamily {
			dist += 0
		} else {
			dist += w.eventFamilyMatch
		}
	}
	if q.Symbol != "" && ep.Symbol != "" && !sameSymbol(q.Symbol, ep.Symbol) {
		dist += 0.5
	}
	return math.Exp(-dist)
}

func sqNorm(a, b, scale float64) float64 {
	if scale <= 0 {
		scale = 1
	}
	d := (a - b) / scale
	return d * d
}

func sameSymbol(a, b string) bool {
	na := normalizeSymbol(a)
	nb := normalizeSymbol(b)
	return na == nb
}

func normalizeSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.TrimSuffix(s, "-SWAP")
}
