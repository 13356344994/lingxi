package brain

import (
	"math"

	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/fingerprint"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

func pickPrimaryEvent(b *Brain, in BarInput, events []MatchedEvent) MatchedEvent {
	if len(events) == 0 {
		return MatchedEvent{}
	}
	if len(events) == 1 {
		return events[0]
	}
	costBase := in.CostBase
	if costBase.ProfileID == "" {
		costBase = residualmap.CostBase{
			ProfileID:              in.Features.ProfileID,
			RollingQuoteVolumeUSDT: in.Features.RollingVolumeUSDT,
			BarHigh:                in.Features.Price * 1.001,
			BarLow:                 in.Features.Price * 0.999,
			BarMid:                 in.Features.Price,
			DelayBars:              in.ScenarioDelay,
		}
	}
	equity := b.Account.Core.EquityUSDT
	if equity <= 0 {
		equity = b.Config.InitialEquityUSDT
	}
	best := events[0]
	bestScore := eventPersonalRank(b.Config.Fingerprint, in, events[0], costBase, equity)
	for _, ev := range events[1:] {
		score := eventPersonalRank(b.Config.Fingerprint, in, ev, costBase, equity)
		if score > bestScore {
			best = ev
			bestScore = score
		}
	}
	return best
}

func eventPersonalRank(
	fp fingerprint.PersonalExecutionFingerprint,
	in BarInput,
	ev MatchedEvent,
	costBase residualmap.CostBase,
	equity float64,
) float64 {
	delay := in.ScenarioDelay
	if ev.Playbook.DelayBars > 0 {
		delay = ev.Playbook.DelayBars
	}
	costBase.DelayBars = delay
	longScore := directionRankScore(fp, in.Features.Symbol, "long", ev.GrossLongBPS, costBase, equity, delay, ev.CompletionBars)
	shortScore := directionRankScore(fp, in.Features.Symbol, "short", ev.GrossShortBPS, costBase, equity, delay, ev.CompletionBars)
	return math.Max(longScore, shortScore)
}

func directionRankScore(
	fp fingerprint.PersonalExecutionFingerprint,
	symbol, direction string,
	grossBPS float64,
	costBase residualmap.CostBase,
	equity float64,
	delayBars, completionBars int,
) float64 {
	adj := residualmap.LookupPersonalAdjustments(fp, symbol, direction)
	if adj.CriticalUnknown {
		return 0
	}
	tiers := residualmap.ComputeTierCosts(grossBPS, costBase, adj, equity)
	tier := tiers[g10protocol.ActualEquityTierLabel]
	if tier.CapitalUSDT == 0 {
		tier = tiers["30"]
	}
	if tier.ConservativeBPS < g10protocol.PersonalConservativeExpectedNetMin {
		return 0
	}
	stress := counterfactual.AdverseStressBPS(tier, delayBars)
	margin := marginOccupationRank(tier.CapitalUSDT, completionBars, costBase.ProfileID)
	return tier.ConservativeBPS / stress / margin
}

func marginOccupationRank(capital float64, completionBars int, profileID string) float64 {
	barHours := 1.0
	if profileID == "okx_5m" {
		barHours = 5.0 / 60.0
	}
	holdHours := float64(completionBars) * barHours
	if holdHours <= 0 {
		holdHours = barHours
	}
	return math.Max(capital*holdHours/24.0, 1e-6)
}
