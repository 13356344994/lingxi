// Package actionspace defines the frozen G5 dynamic action space and position
// state machine used by the outcome oracle.
package actionspace

import (
	"fmt"
	"math"

	g5protocol "lingxi/internal/routerg5/protocol"
)

const (
	SideFlat  = "flat"
	SideLong  = "long"
	SideShort = "short"

	DirectionFlat  = "flat"
	DirectionLong  = "long"
	DirectionShort = "short"

	VerbFlat      = "flat"
	VerbOpenLong  = "open_long"
	VerbOpenShort = "open_short"
	VerbHold      = "hold"
	VerbIncrease  = "increase"
	VerbTrim      = "trim"
	VerbClose     = "close"
	VerbReverse   = "reverse"

	StandardPositionSizeFraction = 0.5
	BaseEquityUSDT               = 1000.0
)

// Action is one frozen oracle action candidate at a decision point.
type Action struct {
	Symbol        string  `json:"symbol"`
	Verb          string  `json:"verb"`
	Direction     string  `json:"direction"`
	TargetHorizon string  `json:"target_horizon"`
	SizeFraction  float64 `json:"size_fraction"`
	DelayBars     int     `json:"delay_bars"`
}

// PositionState is the account position snapshot before applying an action.
type PositionState struct {
	Side              string  `json:"side"`
	SizeFraction      float64 `json:"size_fraction"`
	BasisPrice        float64 `json:"basis_price"`
	EquityUSDT        float64 `json:"equity_usdt"`
	FixedNotionalUSDT float64 `json:"fixed_notional_usdt"`
}

// StandardOracleStates returns the three frozen starting states for oracle enumeration.
func StandardOracleStates(basisPrice float64) []PositionState {
	base := BaseEquityUSDT
	longNotional := DeployNotionalUSDT(base, StandardPositionSizeFraction)
	return []PositionState{
		{Side: SideFlat, SizeFraction: 0, BasisPrice: 0, EquityUSDT: base},
		{Side: SideLong, SizeFraction: StandardPositionSizeFraction, BasisPrice: basisPrice, EquityUSDT: base, FixedNotionalUSDT: longNotional},
		{Side: SideShort, SizeFraction: StandardPositionSizeFraction, BasisPrice: basisPrice, EquityUSDT: base, FixedNotionalUSDT: longNotional},
	}
}

// StateLabel returns a stable aggregation label for one position state.
func StateLabel(state PositionState) string {
	switch state.Side {
	case SideFlat:
		return SideFlat
	case SideLong, SideShort:
		return state.Side
	default:
		return "invalid"
	}
}

// DeployNotionalUSDT returns notional for a new deployment at entry equity.
func DeployNotionalUSDT(equity, sizeFraction float64) float64 {
	if sizeFraction <= 0 {
		return 0
	}
	if equity <= 0 {
		equity = BaseEquityUSDT
	}
	return equity * sizeFraction * float64(g5protocol.MaxAccountLeverage)
}

// NotionalUSDT returns frozen deployed notional; never rescales with mark equity.
func NotionalUSDT(state PositionState) float64 {
	if state.Side == SideFlat || state.SizeFraction <= 0 {
		return 0
	}
	if state.FixedNotionalUSDT > 0 {
		return state.FixedNotionalUSDT
	}
	return DeployNotionalUSDT(state.EquityUSDT, state.SizeFraction)
}

// MaxAdditionalSize returns remaining capacity before the 1.0 size-fraction cap.
func MaxAdditionalSize(state PositionState) float64 {
	if state.Side == SideFlat {
		return 1.0
	}
	return math.Max(0, 1.0-state.SizeFraction)
}

// DirectionForVerb returns the frozen direction label for one verb and starting side.
func DirectionForVerb(verb, side string) string {
	return directionFor(verb, side)
}

// IncreasesOrMaintainsRisk reports verbs that add or keep exposure after a breach gate trips.
func IncreasesOrMaintainsRisk(verb string) bool {
	switch verb {
	case VerbOpenLong, VerbOpenShort, VerbIncrease, VerbReverse, VerbHold:
		return true
	default:
		return false
	}
}

// ValidateAction rejects malformed oracle actions.
func ValidateAction(action Action) error {
	if action.DelayBars < 0 {
		return fmt.Errorf("actionspace: delay must be non-negative")
	}
	if action.SizeFraction < 0 || action.SizeFraction > 1.0+1e-9 {
		return fmt.Errorf("actionspace: size fraction out of range")
	}
	if action.TargetHorizon == "" && action.Verb != VerbFlat {
		return fmt.Errorf("actionspace: target horizon required")
	}
	return nil
}
