package eventmap

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
)

// ShockIndex maps each decision time to a non-overlap shock block id.
type ShockIndex struct {
	threshold float64
	episodes  []shockEpisode
	lookup    map[int64]string
}

type shockEpisode struct {
	Start    time.Time
	End      time.Time
	Cluster  string
}

// BuildShockIndex groups adjacent BTC shock bars into shared clusters.
func BuildShockIndex(btcBars []Bar, spec ProfileSpec, th FrozenThresholds) ShockIndex {
	idx := ShockIndex{
		threshold: th.BTCShockReturnAbsBPS,
		lookup:    map[int64]string{},
	}
	if len(btcBars) < 2 {
		return idx
	}
	var current *shockEpisode
	for i := 1; i < len(btcBars); i++ {
		ret := abs(logReturnBPS(btcBars, i, 1))
		if !isFinite(ret) || ret < idx.threshold {
			current = nil
			continue
		}
		decision := profitmap.DecisionTime(btcBars, i, spec.BarDuration)
		if current == nil {
			current = &shockEpisode{
				Start:   decision,
				End:     decision,
				Cluster: decision.UTC().Format(time.RFC3339Nano),
			}
			idx.episodes = append(idx.episodes, *current)
		} else {
			ep := &idx.episodes[len(idx.episodes)-1]
			ep.End = decision
			current = ep
		}
	}
	for _, ep := range idx.episodes {
		idx.lookup[ep.Start.UnixNano()] = ep.Cluster
	}
	return idx
}

// ClusterAt returns the shock cluster for one decision time.
func (s ShockIndex) ClusterAt(decision time.Time) string {
	for _, ep := range s.episodes {
		if !decision.Before(ep.Start) && !decision.After(ep.End) {
			return ep.Cluster
		}
	}
	return "none"
}

// Lookup returns the cluster id for a decision timestamp.
func (s ShockIndex) Lookup(decision time.Time) string {
	if cluster := s.ClusterAt(decision); cluster != "" {
		return cluster
	}
	return "none"
}
