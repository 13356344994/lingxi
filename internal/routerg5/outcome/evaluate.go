package outcome

import (
	"fmt"
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// EvaluateReward computes incremental account equity delta for one action path.
func EvaluateReward(
	bars []profitmap.Bar,
	decisionIndex int,
	startState actionspace.PositionState,
	action actionspace.Action,
	horizon profitmap.HorizonSpec,
	spec profitmap.ProfileSpec,
	funding []profitmap.FundingPoint,
) (RewardRecord, bool) {
	if err := actionspace.ValidateTransition(startState, action); err != nil {
		return RewardRecord{}, false
	}
	if profitmap.RejectCurrentBarOpen(bars, decisionIndex, spec.BarDuration) {
		return RewardRecord{}, false
	}
	entryIndex := decisionIndex + 1 + action.DelayBars
	exitIndex := entryIndex + horizon.BarCount
	if entryIndex >= len(bars) || exitIndex >= len(bars) {
		return RewardRecord{}, false
	}
	decision := profitmap.DecisionTime(bars, decisionIndex, spec.BarDuration)
	entryTime := bars[entryIndex].OpenTime
	exitTime := bars[exitIndex].OpenTime
	if action.DelayBars == 0 && !entryTime.Equal(decision) {
		return RewardRecord{}, false
	}
	wantExit := entryTime.Add(horizon.Duration)
	if !exitTime.Equal(wantExit) {
		return RewardRecord{}, false
	}
	decisionClose := bars[decisionIndex].Close
	entryOpen := bars[entryIndex].Open
	exitOpen := bars[exitIndex].Open
	if decisionClose <= 0 || entryOpen <= 0 || exitOpen <= 0 {
		return RewardRecord{}, false
	}

	startEquity := actionspace.MarkEquity(startState, decisionClose)
	postState, turnover, err := actionspace.ApplyTransition(startState, action, entryOpen)
	if err != nil {
		return RewardRecord{}, false
	}

	windowBars := profitmap.RollingWindowBars(spec)
	rollingVol := profitmap.RollingQuoteVolumeUSDT(bars, decisionIndex, windowBars)
	decisionBar := bars[decisionIndex]
	capBPS := actionspace.CapacityImpactBPS(turnover, rollingVol)
	rangeBPS := actionspace.BarRangeBPS(decisionBar.High, decisionBar.Low, decisionBar.Mid())
	entryCost := actionspace.ExecutionCostUSDT(turnover, capBPS, rangeBPS)

	capital, grossBPS, holdFunding := pathMetrics(
		startState, postState, action, decisionClose, entryOpen, exitOpen, entryTime, exitTime, funding,
	)
	costIn := profitmap.CostInputs{
		CapitalUSDT:            capital,
		RollingQuoteVolumeUSDT: rollingVol,
		BarHigh:                decisionBar.High,
		BarLow:                 decisionBar.Low,
		BarMid:                 decisionBar.Mid(),
		FundingBPS:             holdFunding,
	}
	feeNet, conservativeBPS, capacityFail := profitmap.ComputeReturns(grossBPS, costIn)
	stressBPS := profitmap.StressConservativeNet(grossBPS, feeNet, conservativeBPS)

	grossDeltaUSDT := capital*grossBPS/10000 - entryCost
	conservativeDeltaUSDT := capital*conservativeBPS/10000 - entryCost
	stressDeltaUSDT := capital*stressBPS/10000 - entryCost

	return RewardRecord{
		StartState:                 startState,
		Action:                     action,
		StateLabel:                 actionspace.StateLabel(startState),
		GrossEquityDeltaBPS:        usdtToEquityBPS(startEquity, grossDeltaUSDT),
		ConservativeEquityDeltaBPS: usdtToEquityBPS(startEquity, conservativeDeltaUSDT),
		StressEquityDeltaBPS:       usdtToEquityBPS(startEquity, stressDeltaUSDT),
		NetEquityDeltaBPS:          usdtToEquityBPS(startEquity, conservativeDeltaUSDT),
		MarginUSDT:                 actionspace.NotionalUSDT(postState),
		TurnoverUSDT:               turnover,
		CapacityFail:               capacityFail,
		Label:                      rewardLabel(startState, action),
	}, true
}

func pathMetrics(
	startState, postState actionspace.PositionState,
	action actionspace.Action,
	decisionClose, entryOpen, exitOpen float64,
	entryTime, exitTime time.Time,
	funding []profitmap.FundingPoint,
) (capital, grossBPS, fundingBPS float64) {
	switch action.Verb {
	case actionspace.VerbFlat:
		return 0, 0, 0
	case actionspace.VerbIncrease:
		oldNotional := actionspace.NotionalUSDT(startState)
		addedNotional := actionspace.DeployNotionalUSDT(startState.EquityUSDT, action.SizeFraction)
		if addedNotional <= 0 {
			return 0, 0, 0
		}
		oldGross := profitmap.SignedGrossBPS(decisionClose, exitOpen, startState.Side)
		newGross := profitmap.SignedGrossBPS(entryOpen, exitOpen, postState.Side)
		oldDelta := oldNotional * oldGross / 10000
		newDelta := addedNotional * newGross / 10000
		totalNotional := oldNotional + addedNotional
		if totalNotional <= 0 {
			return 0, 0, 0
		}
		grossBPS = (oldDelta + newDelta) / totalNotional * 10000
		fundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
		return totalNotional, grossBPS, fundingBPS
	case actionspace.VerbClose, actionspace.VerbTrim:
		if action.Verb == actionspace.VerbTrim {
			priorNotional := actionspace.NotionalUSDT(startState)
			if startState.SizeFraction <= 0 || action.SizeFraction <= 0 {
				return 0, 0, 0
			}
			trimRatio := action.SizeFraction / startState.SizeFraction
			capital = priorNotional * trimRatio
		} else {
			capital = actionspace.NotionalUSDT(startState)
		}
		grossBPS = profitmap.SignedGrossBPS(decisionClose, entryOpen, startState.Side)
		return capital, grossBPS, 0
	case actionspace.VerbReverse:
		capital = actionspace.NotionalUSDT(startState)
		closeBPS := profitmap.SignedGrossBPS(decisionClose, entryOpen, startState.Side)
		openBPS := profitmap.SignedGrossBPS(entryOpen, exitOpen, postState.Side)
		oldNotional := actionspace.NotionalUSDT(startState)
		newNotional := actionspace.NotionalUSDT(postState)
		if oldNotional <= 0 {
			return capital, closeBPS, 0
		}
		closeDelta := oldNotional * closeBPS / 10000
		openDelta := newNotional * openBPS / 10000
		grossBPS = (closeDelta + openDelta) / oldNotional * 10000
		fundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
		return capital, grossBPS, fundingBPS
	default:
		capital = actionspace.NotionalUSDT(postState)
		if startState.Side == actionspace.SideFlat {
			grossBPS = profitmap.SignedGrossBPS(entryOpen, exitOpen, postState.Side)
		} else {
			grossBPS = profitmap.SignedGrossBPS(decisionClose, exitOpen, postState.Side)
		}
		fundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
		return capital, grossBPS, fundingBPS
	}
}

func usdtToEquityBPS(startEquity, deltaUSDT float64) float64 {
	if startEquity <= 0 {
		return 0
	}
	return deltaUSDT / startEquity * 10000
}

func rewardLabel(state actionspace.PositionState, action actionspace.Action) string {
	return fmt.Sprintf("%s|%s|%s|h=%s|s=%.2f|d=%d",
		actionspace.StateLabel(state),
		action.Verb,
		action.Direction,
		action.TargetHorizon,
		action.SizeFraction,
		action.DelayBars,
	)
}

// LeverageCapOK rejects notionals above the frozen 5x account cap.
func LeverageCapOK(state actionspace.PositionState) bool {
	maxNotional := state.EquityUSDT * float64(g5protocol.MaxAccountLeverage)
	if maxNotional <= 0 {
		maxNotional = actionspace.BaseEquityUSDT * float64(g5protocol.MaxAccountLeverage)
	}
	return actionspace.NotionalUSDT(state) <= maxNotional+1e-6
}

// OverlapIncrementalOK ensures hold reward uses one position path, not stacked notionals.
func OverlapIncrementalOK(bars []profitmap.Bar, decisionIndex int, state actionspace.PositionState, spec profitmap.ProfileSpec) bool {
	if state.Side == actionspace.SideFlat {
		return true
	}
	horizon := spec.Horizons[0]
	action := actionspace.Action{
		Verb:          actionspace.VerbHold,
		Direction:     state.Side,
		TargetHorizon: horizon.Label,
		SizeFraction:  0,
		DelayBars:     0,
	}
	rec, ok := EvaluateReward(bars, decisionIndex, state, action, horizon, spec, nil)
	if !ok {
		return true
	}
	decisionClose := bars[decisionIndex].Close
	exitIndex := decisionIndex + 1 + horizon.BarCount
	if exitIndex >= len(bars) {
		return true
	}
	exitOpen := bars[exitIndex].Open
	wantUSDT := actionspace.MarkEquity(state, exitOpen) - actionspace.MarkEquity(state, decisionClose)
	gotUSDT := rec.GrossEquityDeltaBPS * actionspace.MarkEquity(state, decisionClose) / 10000
	return math.Abs(wantUSDT-gotUSDT) < 1.0
}
