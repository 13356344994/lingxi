package executionpolicy

import (
	"fmt"
	"strings"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// ClassifyMakerQuadrant labels one trigger for maker selection-bias analysis.
// Maker touch without fill is never counted as filled.
func ClassifyMakerQuadrant(tr TriggerRecord) string {
	if tr.MakerFilled {
		if tr.NetPnLUSDT > 0 {
			return QuadrantFilledProfitable
		}
		return QuadrantFilledUnprofitable
	}
	if tr.SubsequentlyProfitable {
		return QuadrantUnfilledSubsequentlyProfitable
	}
	return QuadrantUnfilledNotProfitable
}

// MakerTouchIsNotFill reports touch without fill — not a fill for bias stats.
func MakerTouchIsNotFill(tr TriggerRecord) bool {
	return tr.MakerTouched && !tr.MakerFilled
}

// MissedProfitablePnL returns PnL credited to maker path when unfilled.
// Protocol requires zero — subsequent profit is MISSED_PROFITABLE_MOVE only.
func MissedProfitablePnL(tr TriggerRecord) float64 {
	if tr.MakerFilled {
		return tr.NetPnLUSDT
	}
	return 0
}

// FreezeBoundedMaker evaluates maker path using decision-visible costs only.
func FreezeBoundedMaker(tr TriggerRecord) ActionEvaluation {
	ev := ActionEvaluation{Action: ActionBoundedMaker}
	notional := ReferenceNotional(tr)
	if tr.MakerTouched && !tr.MakerFilled {
		ev.PFill = 0
		setExpectedNet(&ev, 0, 0)
		ev.Allowed = true
		return ev
	}
	pFill := 0.0
	if tr.MakerFilled {
		pFill = 1
	}
	costBPS := tr.SpreadBPS + tr.FeeBPS
	expectedBPS := tr.RemainingEdgeBPS - costBPS
	if tr.MakerFilled {
		populateExpectedNetFromUSDT(&ev, tr.NetPnLUSDT, notional)
	} else if tr.SubsequentlyProfitable {
		setExpectedNet(&ev, 0, 0)
	} else {
		populateExpectedNetFromBPS(&ev, expectedBPS, notional)
	}
	ev.PFill = pFill
	ev.Allowed = true
	return ev
}

// FreezeConditionalMarketableLimit evaluates conditional taker path.
// LONG: MaxPriceCap is highest acceptable buy price. SHORT: MaxPriceCap is lowest acceptable sell floor.
func FreezeConditionalMarketableLimit(tr TriggerRecord) ActionEvaluation {
	ev := ActionEvaluation{Action: ActionConditionalMarketableLimit}
	if tr.MaxPriceCap > 0 {
		if tr.Side == "short" {
			if tr.Price < tr.MaxPriceCap {
				ev.BlockReason = "below_min_price_floor"
				return ev
			}
		} else if tr.Price > tr.MaxPriceCap {
			ev.BlockReason = "above_max_price_cap"
			return ev
		}
	}
	slip := tr.SpreadBPS
	if slip > tr.MaxSlippageBPS && tr.MaxSlippageBPS > 0 {
		ev.BlockReason = "above_max_slippage"
		return ev
	}
	remaining := tr.RemainingEdgeBPS - tr.SpreadBPS - tr.FeeBPS - tr.LatencyMS*0.01
	if remaining < tr.MinRemainingEdgeBPS {
		ev.BlockReason = "below_min_remaining_edge"
		return ev
	}
	if tr.TimeoutMS <= 0 {
		ev.BlockReason = "timeout_cancel_required"
		return ev
	}
	populateExpectedNetFromBPS(&ev, remaining, ReferenceNotional(tr))
	ev.Allowed = true
	return ev
}

// FreezeSkip evaluates explicit skip path.
func FreezeSkip() ActionEvaluation {
	ev := ActionEvaluation{Action: ActionSkip, Allowed: true}
	setExpectedNet(&ev, 0, 0)
	return ev
}

// SelectFrozenAction picks action from frozen expectations only — never from realized outcome.
func SelectFrozenAction(actions []ActionEvaluation, gate MakerFillSelectionGate) string {
	best := ActionSkip
	bestEV := 0.0
	for _, a := range actions {
		if !a.Allowed {
			continue
		}
		if a.Action == ActionBoundedMaker && gate.ForbidMaker {
			continue
		}
		if a.ExpectedNetUSDT > bestEV {
			bestEV = a.ExpectedNetUSDT
			best = a.Action
		}
	}
	return best
}

// ValidateActionsCompared ensures all three protocol actions are present.
func ValidateActionsCompared(actions []ActionEvaluation) error {
	want := g10protocolv2.ExecutionActionsCompared
	seen := map[string]bool{}
	for _, a := range actions {
		seen[a.Action] = true
	}
	for _, act := range want {
		if !seen[act] {
			return fmt.Errorf("executionpolicy: missing frozen action %s", act)
		}
	}
	return nil
}

// BuildMakerStats aggregates maker metrics across all triggers (full denominator).
func BuildMakerStats(triggers []TriggerRecord) MakerStats {
	stats := MakerStats{Quadrants: MakerQuadrantCounts{TotalTriggers: len(triggers)}}
	var filledNet []float64
	var fastNet, slowNet []float64
	for _, tr := range triggers {
		q := ClassifyMakerQuadrant(tr)
		switch q {
		case QuadrantFilledProfitable:
			stats.Quadrants.FilledProfitable++
		case QuadrantFilledUnprofitable:
			stats.Quadrants.FilledUnprofitable++
		case QuadrantUnfilledSubsequentlyProfitable:
			stats.Quadrants.UnfilledSubsequentlyProfitable++
		default:
			stats.Quadrants.UnfilledNotProfitable++
		}
		if tr.MakerFilled {
			filledNet = append(filledNet, tr.NetPnLUSDT)
			if tr.FillWaitMS > 0 && tr.FillWaitMS <= FastFillThresholdMS {
				fastNet = append(fastNet, tr.NetPnLUSDT)
			} else if tr.FillWaitMS > FastFillThresholdMS {
				slowNet = append(slowNet, tr.NetPnLUSDT)
			}
		}
		if tr.PartialFill {
			stats.PartialFillCount++
		}
		if tr.ThroughPrice {
			stats.ThroughPriceCount++
		}
	}
	filled := stats.Quadrants.FilledProfitable + stats.Quadrants.FilledUnprofitable
	if len(triggers) > 0 {
		stats.PFill = float64(filled) / float64(len(triggers))
	}
	stats.ExpectedNetGivenFilled = mean(filledNet)
	stats.FastFillExpectedNet = mean(fastNet)
	stats.SlowFillExpectedNet = mean(slowNet)
	return stats
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// EvaluateToxicFill returns toxic fill status if revalidation/exit required.
func EvaluateToxicFill(tr TriggerRecord) string {
	if !tr.MakerFilled && !tr.ToxicFastFullFill && !tr.ToxicThroughPrice {
		return ""
	}
	if tr.ToxicFastFullFill || tr.ToxicThroughPrice || tr.ToxicBookSyncBad {
		if !tr.ThesisStillValid {
			return ToxicFillExitStatus
		}
		return ToxicFillRevalidateStatus
	}
	return ""
}

// FreezePolicy builds one trigger's frozen execution policy.
func FreezePolicy(tr TriggerRecord, cohort []TriggerRecord) (FrozenPolicy, error) {
	if strings.TrimSpace(tr.TriggerID) == "" {
		return FrozenPolicy{}, fmt.Errorf("executionpolicy: trigger_id required")
	}
	if tr.DecisionTime.IsZero() {
		return FrozenPolicy{}, fmt.Errorf("executionpolicy: decision_time required")
	}

	maker := FreezeBoundedMaker(tr)
	cml := FreezeConditionalMarketableLimit(tr)
	skip := FreezeSkip()
	actions := []ActionEvaluation{maker, cml, skip}

	stats := BuildMakerStats(cohort)
	gate := EvaluateMakerFillSelectionGate(stats)
	selected := SelectFrozenAction(actions, gate)

	fp := FrozenPolicy{
		SchemaVersion:            SchemaVersion,
		TriggerID:                tr.TriggerID,
		DecisionTime:             tr.DecisionTime.UTC(),
		FreezeBeforeOutcomeKnown: true,
		ActionsCompared:          actions,
		MakerStats:               stats,
		MakerQuadrant:            ClassifyMakerQuadrant(tr),
		MakerGate:                gate,
		NoLongLivedMaker:         true,
		NoChaseAfterMiss:         true,
		ToxicFillStatus:          EvaluateToxicFill(tr),
		SelectedAction:           selected,
		Side:                     tr.Side,
		Price:                    tr.Price,
		MaxPriceCap:              tr.MaxPriceCap,
		MaxSlippageBPS:           tr.MaxSlippageBPS,
		MinRemainingEdgeBPS:      tr.MinRemainingEdgeBPS,
		TimeoutMS:                tr.TimeoutMS,
		SpreadBPS:                tr.SpreadBPS,
		FeeBPS:                   tr.FeeBPS,
		LatencyMS:                tr.LatencyMS,
	}
	hash, err := CanonicalPolicyHash(fp)
	if err != nil {
		return FrozenPolicy{}, err
	}
	fp.ExecutionPolicyHash = hash
	return fp, nil
}
