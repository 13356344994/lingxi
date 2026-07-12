package counterfactual

import (
	"math"

	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

func scoreAction(ctx DecisionContext, action string) ActionMetric {
	m := ActionMetric{
		Action:                 action,
		DelayBars:              ctx.Event.DelayBars,
		ExpectedCompletionBars: ctx.CompletionBars,
		CapacityUSDT:           ctx.CapacityUSDT,
		SizeScale:              1.0,
	}
	switch action {
	case "NO_TRADE":
		m.PersonalConservativeNetBPS = 0
		m.StressLossBPS = 0
		m.MarginOccupationUSDT = 0
		m.MinLotFeasible = true
		m.Allowed = true
	case "LONG":
		m = scoreOpen(ctx, m, ctx.Event.GrossLongBPS, "long")
	case "SHORT":
		m = scoreOpen(ctx, m, ctx.Event.GrossShortBPS, "short")
	case "HOLD":
		m = scoreHold(ctx, m)
	case "ADD":
		m = scoreAdd(ctx, m)
	case "TRIM":
		m = scoreTrim(ctx, m)
	case "CLOSE":
		m = scoreClose(ctx, m)
	case "REVERSE":
		m = scoreReverse(ctx, m)
	default:
		m.RejectReasons = append(m.RejectReasons, "UNKNOWN_ACTION")
	}
	m.RankScore = rankScore(m)
	return m
}

func scoreOpen(ctx DecisionContext, m ActionMetric, grossBPS float64, direction string) ActionMetric {
	adj := resolvePersonalAdj(ctx, direction)
	tiers := residualmap.ComputeTierCosts(grossBPS, ctx.CostBase, adj, ctx.EquityUSDT)
	tier := tiers[g10protocol.ActualEquityTierLabel]
	if tier.CapitalUSDT == 0 {
		tier = tiers["30"]
	}
	m.PersonalConservativeNetBPS = tier.ConservativeBPS
	delayBars := ctx.Event.DelayBars
	if delayBars <= 0 {
		delayBars = ctx.CostBase.DelayBars
	}
	m.StressLossBPS = AdverseStressBPS(tier, delayBars)
	m.MarginOccupationUSDT = marginOccupation(tier.CapitalUSDT, ctx.CompletionBars, ctx.ProfileID)
	m.MinLotFeasible, m.RejectReasons = minLotFeasible(ctx, tier.CapitalUSDT, m.RejectReasons)
	m.CapacityUSDT = math.Min(ctx.CapacityUSDT, tier.CapitalUSDT)
	m.Allowed = true
	return m
}

func scoreHold(ctx DecisionContext, m ActionMetric) ActionMetric {
	if !ctx.Position.HasPosition {
		m.PersonalConservativeNetBPS = 0
		m.Allowed = ctx.Event.HasEvent
		if !ctx.Event.HasEvent {
			m.RejectReasons = append(m.RejectReasons, RejectNoMatchingEvent)
		}
		return m
	}
	m.PersonalConservativeNetBPS = 0
	m.MarginOccupationUSDT = ctx.Position.NotionalUSDT
	m.MinLotFeasible = true
	m.Allowed = true
	return m
}

func scoreAdd(ctx DecisionContext, m ActionMetric) ActionMetric {
	if !ctx.Position.HasPosition {
		m.RejectReasons = append(m.RejectReasons, "NO_POSITION_TO_ADD")
		return m
	}
	open := scoreOpen(ctx, m, directionalGross(ctx), ctx.Position.Direction)
	open.Action = "ADD"
	open.MarginOccupationUSDT += ctx.Position.NotionalUSDT
	open.Allowed = true
	return open
}

func scoreTrim(ctx DecisionContext, m ActionMetric) ActionMetric {
	if !ctx.Position.HasPosition {
		m.RejectReasons = append(m.RejectReasons, "NO_POSITION_TO_TRIM")
		return m
	}
	m.PersonalConservativeNetBPS = 0
	m.StressLossBPS = ctx.StressBudgetBPS * 0.5
	m.MarginOccupationUSDT = ctx.Position.NotionalUSDT * 0.5
	m.MinLotFeasible = true
	m.Allowed = true
	return m
}

func scoreClose(ctx DecisionContext, m ActionMetric) ActionMetric {
	if !ctx.Position.HasPosition {
		m.PersonalConservativeNetBPS = 0
		m.MinLotFeasible = true
		m.Allowed = false
		m.RejectReasons = append(m.RejectReasons, "NO_POSITION_TO_CLOSE")
		return m
	}
	m.PersonalConservativeNetBPS = 0
	m.StressLossBPS = 0
	m.MarginOccupationUSDT = 0
	m.MinLotFeasible = true
	m.Allowed = true
	return m
}

func scoreReverse(ctx DecisionContext, m ActionMetric) ActionMetric {
	if !ctx.Position.HasPosition {
		m.RejectReasons = append(m.RejectReasons, "NO_POSITION_TO_REVERSE")
		return m
	}
	var gross float64
	dir := "long"
	if ctx.Position.Direction == "long" {
		gross = ctx.Event.GrossShortBPS
		dir = "short"
	} else {
		gross = ctx.Event.GrossLongBPS
		dir = "long"
	}
	open := scoreOpen(ctx, m, gross, dir)
	open.Action = "REVERSE"
	open.Allowed = true
	return open
}

func directionalGross(ctx DecisionContext) float64 {
	if ctx.Position.Direction == "short" {
		return ctx.Event.GrossShortBPS
	}
	return ctx.Event.GrossLongBPS
}

func marginOccupation(capital float64, completionBars int, profileID string) float64 {
	barHours := 1.0
	if profileID == "okx_5m" {
		barHours = 5.0 / 60.0
	}
	holdHours := float64(completionBars) * barHours
	if holdHours <= 0 {
		holdHours = barHours
	}
	return capital * holdHours / 24.0
}

func minLotFeasible(ctx DecisionContext, notional float64, reasons []string) (bool, []string) {
	if ctx.MinNotionalUSDT <= 0 && ctx.LotStep <= 0 {
		return true, reasons
	}
	if ctx.MinNotionalUSDT > 0 && notional < ctx.MinNotionalUSDT {
		return false, append(reasons, RejectBelowMinNotional, RejectMinLotInfeasible)
	}
	if ctx.LotStep > 0 && ctx.ContractValue > 0 {
		lots := notional / ctx.ContractValue
		steps := math.Floor(lots/ctx.LotStep + 1e-9)
		if steps < 1 {
			return false, append(reasons, RejectMinLotInfeasible)
		}
	}
	return true, reasons
}

func rankScore(m ActionMetric) float64 {
	stress := math.Max(m.StressLossBPS, 1e-6)
	margin := math.Max(m.MarginOccupationUSDT, 1e-6)
	return m.PersonalConservativeNetBPS / stress / margin
}
