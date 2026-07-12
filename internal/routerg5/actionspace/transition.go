package actionspace

import (
	"fmt"
	"math"
)

// ValidateTransition reports whether an action is legal from a position state.
func ValidateTransition(state PositionState, action Action) error {
	if err := ValidateAction(action); err != nil {
		return err
	}
	switch state.Side {
	case SideFlat:
		switch action.Verb {
		case VerbFlat, VerbOpenLong, VerbOpenShort:
			return nil
		default:
			return fmt.Errorf("actionspace: verb %q invalid from flat", action.Verb)
		}
	case SideLong:
		switch action.Verb {
		case VerbHold, VerbIncrease, VerbTrim, VerbClose, VerbReverse:
			return nil
		default:
			return fmt.Errorf("actionspace: verb %q invalid from long", action.Verb)
		}
	case SideShort:
		switch action.Verb {
		case VerbHold, VerbIncrease, VerbTrim, VerbClose, VerbReverse:
			return nil
		default:
			return fmt.Errorf("actionspace: verb %q invalid from short", action.Verb)
		}
	default:
		return fmt.Errorf("actionspace: unknown side %q", state.Side)
	}
}

// ApplyTransition returns the post-entry position state and turnover notional.
func ApplyTransition(state PositionState, action Action, entryPrice float64) (PositionState, float64, error) {
	if err := ValidateTransition(state, action); err != nil {
		return PositionState{}, 0, err
	}
	if entryPrice <= 0 && action.Verb != VerbFlat && action.Verb != VerbHold {
		return PositionState{}, 0, fmt.Errorf("actionspace: entry price must be positive")
	}
	equity := state.EquityUSDT
	if equity <= 0 {
		equity = BaseEquityUSDT
	}
	out := PositionState{EquityUSDT: equity}
	var turnover float64
	priorNotional := NotionalUSDT(state)

	switch action.Verb {
	case VerbFlat:
		out.Side = SideFlat
		out.SizeFraction = 0
		out.BasisPrice = 0
		out.FixedNotionalUSDT = 0
	case VerbHold:
		out = state
	case VerbOpenLong:
		if action.SizeFraction <= 0 {
			return PositionState{}, 0, fmt.Errorf("actionspace: open_long requires positive size")
		}
		out.Side = SideLong
		out.SizeFraction = action.SizeFraction
		out.BasisPrice = entryPrice
		out.FixedNotionalUSDT = DeployNotionalUSDT(equity, action.SizeFraction)
		turnover = out.FixedNotionalUSDT
	case VerbOpenShort:
		if action.SizeFraction <= 0 {
			return PositionState{}, 0, fmt.Errorf("actionspace: open_short requires positive size")
		}
		out.Side = SideShort
		out.SizeFraction = action.SizeFraction
		out.BasisPrice = entryPrice
		out.FixedNotionalUSDT = DeployNotionalUSDT(equity, action.SizeFraction)
		turnover = out.FixedNotionalUSDT
	case VerbIncrease:
		if action.SizeFraction <= 0 {
			return PositionState{}, 0, fmt.Errorf("actionspace: increase requires positive size")
		}
		newSize := state.SizeFraction + action.SizeFraction
		if newSize > 1.0+1e-9 {
			return PositionState{}, 0, fmt.Errorf("actionspace: size fraction exceeds 1.0")
		}
		addedNotional := DeployNotionalUSDT(equity, action.SizeFraction)
		out.Side = state.Side
		out.SizeFraction = newSize
		out.BasisPrice = weightedBasis(state, entryPrice, action.SizeFraction)
		out.FixedNotionalUSDT = priorNotional + addedNotional
		turnover = addedNotional
	case VerbTrim:
		if action.SizeFraction <= 0 {
			return PositionState{}, 0, fmt.Errorf("actionspace: trim requires positive size")
		}
		if action.SizeFraction > state.SizeFraction+1e-9 {
			return PositionState{}, 0, fmt.Errorf("actionspace: trim exceeds position size")
		}
		newSize := state.SizeFraction - action.SizeFraction
		trimmed := action.SizeFraction
		trimRatio := trimmed / state.SizeFraction
		trimmedNotional := priorNotional * trimRatio
		if newSize <= 1e-9 {
			out.Side = SideFlat
			out.SizeFraction = 0
			out.BasisPrice = 0
			out.FixedNotionalUSDT = 0
		} else {
			out.Side = state.Side
			out.SizeFraction = newSize
			out.BasisPrice = state.BasisPrice
			out.FixedNotionalUSDT = priorNotional - trimmedNotional
		}
		turnover = trimmedNotional
	case VerbClose:
		turnover = priorNotional
		out.Side = SideFlat
		out.SizeFraction = 0
		out.BasisPrice = 0
		out.FixedNotionalUSDT = 0
	case VerbReverse:
		if action.SizeFraction <= 0 {
			return PositionState{}, 0, fmt.Errorf("actionspace: reverse requires positive size")
		}
		newNotional := DeployNotionalUSDT(equity, action.SizeFraction)
		turnover = priorNotional + newNotional
		if state.Side == SideLong {
			out.Side = SideShort
		} else {
			out.Side = SideLong
		}
		out.SizeFraction = action.SizeFraction
		out.BasisPrice = entryPrice
		out.FixedNotionalUSDT = newNotional
	default:
		return PositionState{}, 0, fmt.Errorf("actionspace: unknown verb %q", action.Verb)
	}
	return out, turnover, nil
}

func weightedBasis(state PositionState, entryPrice float64, addedFraction float64) float64 {
	if state.SizeFraction <= 0 || state.BasisPrice <= 0 {
		return entryPrice
	}
	total := state.SizeFraction + addedFraction
	if total <= 0 {
		return entryPrice
	}
	return (state.BasisPrice*state.SizeFraction + entryPrice*addedFraction) / total
}

func maxLeverage() int {
	return 5
}

// MarkEquity marks account equity at a price without applying execution costs.
func MarkEquity(state PositionState, price float64) float64 {
	equity := state.EquityUSDT
	if equity <= 0 {
		equity = BaseEquityUSDT
	}
	if state.Side == SideFlat || state.SizeFraction <= 0 || state.BasisPrice <= 0 || price <= 0 {
		return equity
	}
	notional := NotionalUSDT(state)
	var grossBPS float64
	switch state.Side {
	case SideLong:
		grossBPS = (price/state.BasisPrice - 1) * 10000
	case SideShort:
		grossBPS = (1 - price/state.BasisPrice) * 10000
	default:
		return equity
	}
	return equity + notional*grossBPS/10000
}

// ExecutionCostUSDT applies the G4 fixed-cost proxy to entry turnover.
func ExecutionCostUSDT(turnover float64, capacityImpactBPS, barRangeBPS float64) float64 {
	if turnover <= 0 {
		return 0
	}
	fixedBPS := 10.0 + 2.0 + capacityImpactBPS + barRangeBPS
	return turnover * fixedBPS / 10000
}

// CapacityImpactBPS mirrors the frozen G4 capacity formula for one notional.
func CapacityImpactBPS(notional, rollingQuoteVolume float64) float64 {
	impact := 10000.0 * notional / math.Max(rollingQuoteVolume, 1)
	if impact > 50 {
		return 50
	}
	return impact
}

// BarRangeBPS mirrors the frozen G4 bar-range toxicity proxy.
func BarRangeBPS(high, low, mid float64) float64 {
	m := math.Max(mid, 1e-12)
	rng := (high - low) / m * 10000 * 0.5
	if rng > 100 {
		return 100
	}
	return rng
}
