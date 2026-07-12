package exclusionmap

import (
	"math"

	"lingxi/internal/routerg10/residualmap"
	g4protocol "lingxi/internal/routerg4/protocol"
)

// ComputeCrowdingStress evaluates the frozen 1/3/5 micro-competitor ladder.
func ComputeCrowdingStress(ctx MetricContext, baselineGross float64) CrowdingStress {
	tier30, ok := ctx.Tiers["30"]
	if !ok || tier30.CapitalUSDT <= 0 {
		return CrowdingStress{}
	}
	baseline := tier30.ConservativeBPS
	out := CrowdingStress{}
	for _, competitors := range CrowdingStressLevels {
		stressed := stressConservativeBPS(ctx, baselineGross, tier30.CapitalUSDT, competitors)
		collapse := baseline - stressed
		point := CrowdingStressPoint{
			Competitors:          competitors,
			BaselineConservative: baseline,
			StressedConservative: stressed,
			EdgeCollapseBPS:      collapse,
			QueueCrowdingDetected: collapse >= crowdingCollapseThresholdBPS,
		}
		out.Levels = append(out.Levels, point)
	}
	if len(out.Levels) > 0 {
		last := out.Levels[len(out.Levels)-1]
		out.Passed = last.QueueCrowdingDetected
	}
	return out
}

func stressConservativeBPS(ctx MetricContext, gross, capital float64, competitors int) float64 {
	if capital <= 0 {
		return 0
	}
	microCapital := 30.0 * float64(competitors)
	extraImpact := g4protocol.CapacityImpactScaleBPS * microCapital / math.Max(ctx.Base.RollingQuoteVolumeUSDT, 1)
	if extraImpact > g4protocol.CapacityImpactCapBPS {
		extraImpact = g4protocol.CapacityImpactCapBPS
	}
	queuePenalty := queueCrowdingImpactBPS * float64(competitors) / 5.0
	tier := residualmap.TierCostBreakdown{}
	for _, t := range ctx.Tiers {
		if t.TierLabel == "30" {
			tier = t
			break
		}
	}
	feeBPS := tier.FeeBPS
	slipBPS := tier.SlipBPS
	impactBPS := tier.ImpactBPS + extraImpact + queuePenalty
	fundingBPS := tier.FundingBPS
	return gross - feeBPS - slipBPS - impactBPS - fundingBPS - ctx.PostPublicationDecayBPS
}
