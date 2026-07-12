package actionspace

import (
	"fmt"
	"sort"

	g5protocol "lingxi/internal/routerg5/protocol"
)

// EnumerateActions returns all legal oracle actions for one starting state.
func EnumerateActions(symbol string, state PositionState, horizons []string) ([]Action, error) {
	if state.Side != SideFlat && state.Side != SideLong && state.Side != SideShort {
		return nil, fmt.Errorf("actionspace: invalid starting side %q", state.Side)
	}
	if len(horizons) == 0 {
		return nil, fmt.Errorf("actionspace: horizons required")
	}
	var verbs []string
	switch state.Side {
	case SideFlat:
		verbs = []string{VerbFlat, VerbOpenLong, VerbOpenShort}
	case SideLong, SideShort:
		verbs = []string{VerbHold, VerbIncrease, VerbTrim, VerbClose, VerbReverse}
	}
	var out []Action
	for _, verb := range verbs {
		sizeGrid := sizeGridForVerb(verb)
		for _, size := range sizeGrid {
			for _, delay := range g5protocol.DelayBars {
				for _, horizon := range horizons {
					action := Action{
						Symbol:        symbol,
						Verb:          verb,
						Direction:     directionFor(verb, state.Side),
						TargetHorizon: horizon,
						SizeFraction:  size,
						DelayBars:     delay,
					}
					if err := ValidateTransition(state, action); err != nil {
						continue
					}
					out = append(out, action)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Verb != b.Verb {
			return a.Verb < b.Verb
		}
		if a.Direction != b.Direction {
			return a.Direction < b.Direction
		}
		if a.TargetHorizon != b.TargetHorizon {
			return a.TargetHorizon < b.TargetHorizon
		}
		if a.SizeFraction != b.SizeFraction {
			return a.SizeFraction < b.SizeFraction
		}
		return a.DelayBars < b.DelayBars
	})
	return out, nil
}

func sizeGridForVerb(verb string) []float64 {
	switch verb {
	case VerbFlat, VerbHold, VerbClose:
		return []float64{0.0}
	case VerbOpenLong, VerbOpenShort, VerbIncrease, VerbTrim, VerbReverse:
		var sizes []float64
		for _, s := range g5protocol.OracleSizeFractions {
			if s > 0 {
				sizes = append(sizes, s)
			}
		}
		return sizes
	default:
		return nil
	}
}

func directionFor(verb, side string) string {
	switch verb {
	case VerbFlat, VerbClose:
		return DirectionFlat
	case VerbOpenLong, VerbIncrease, VerbHold, VerbTrim:
		if side == SideShort && (verb == VerbHold || verb == VerbIncrease || verb == VerbTrim) {
			return DirectionShort
		}
		if side == SideShort && verb == VerbOpenLong {
			return DirectionLong
		}
		return DirectionLong
	case VerbOpenShort:
		return DirectionShort
	case VerbReverse:
		if side == SideLong {
			return DirectionShort
		}
		return DirectionLong
	default:
		return DirectionFlat
	}
}

// ActionSpaceHash returns a stable hash input for enumerated action space knobs.
func ActionSpaceHash(horizons []string) (string, error) {
	payload := struct {
		Verbs      []string  `json:"verbs"`
		DelayBars  []int     `json:"delay_bars"`
		Sizes      []float64 `json:"oracle_size_fractions"`
		Horizons   []string  `json:"horizons"`
		MaxLev     int       `json:"max_account_leverage"`
		StdSize    float64   `json:"standard_position_size_fraction"`
		BaseEquity float64   `json:"base_equity_usdt"`
	}{
		Verbs:      append([]string(nil), g5protocol.ActionVerbs...),
		DelayBars:  append([]int(nil), g5protocol.DelayBars...),
		Sizes:      append([]float64(nil), g5protocol.OracleSizeFractions...),
		Horizons:   append([]string(nil), horizons...),
		MaxLev:     g5protocol.MaxAccountLeverage,
		StdSize:    StandardPositionSizeFraction,
		BaseEquity: BaseEquityUSDT,
	}
	return hashPayload(payload)
}
