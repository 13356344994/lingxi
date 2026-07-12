package walkforward

import (
	"math"

	g6protocol "lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/eventvalidation"
)

// FrozenSizingInput carries validation-survivor frozen edge/stress for WF queue sizing.
type FrozenSizingInput struct {
	ConservativeMeanBPS       float64
	StressConservativeMeanBPS float64
	ReferenceNotional         float64
}

// FrozenSizingFromSurvivor extracts hash-bound sizing metrics for one capital tier.
func FrozenSizingFromSurvivor(entry eventvalidation.SurvivorEntry, capitalTier int) FrozenSizingInput {
	ref := float64(capitalTier)
	if ref <= 0 {
		ref = g6protocol.CapitalTiersUSDT[0]
	}
	out := FrozenSizingInput{
		ConservativeMeanBPS:       entry.FrozenConservativeMeanBPS,
		StressConservativeMeanBPS: entry.FrozenStressConservativeMeanBPS,
		ReferenceNotional:         ref,
	}
	if out.ConservativeMeanBPS <= 0 {
		for _, tr := range entry.TierResults {
			if tr.CapitalTier == capitalTier {
				out.ConservativeMeanBPS = tr.Firing.ConservativeMeanBPS
				out.StressConservativeMeanBPS = tr.Firing.StressConservativeMeanBPS
				break
			}
		}
	}
	if out.ConservativeMeanBPS <= 0 {
		out.ConservativeMeanBPS = g6protocol.WFSizingEdgeFloorBPS
	}
	if out.StressConservativeMeanBPS <= 0 {
		out.StressConservativeMeanBPS = math.Max(
			out.ConservativeMeanBPS*0.5,
			ref*g6protocol.MaxEventStressLossFraction*10000/ref,
		)
	}
	return out
}
