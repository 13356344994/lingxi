package reverselearn

import (
	"time"

	"lingxi/internal/routerg5/actionspace"
)

// SyntheticFixedDirectionExamples builds rows that always train fixed-direction policies.
func SyntheticFixedDirectionExamples() []TrainingExample {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	features := syntheticFeatureRegime(0, 0)
	var out []TrainingExample
	for i := 0; i < 90; i++ {
		decision := base.Add(time.Duration(i) * time.Hour)
		for _, spec := range []struct {
			verb    string
			dir     string
			horizon string
			reward  float64
		}{
			{actionspace.VerbOpenLong, actionspace.DirectionLong, "1h", 50},
			{actionspace.VerbOpenLong, actionspace.DirectionLong, "4h", 45},
			{actionspace.VerbOpenShort, actionspace.DirectionShort, "1h", -40},
			{actionspace.VerbOpenShort, actionspace.DirectionShort, "4h", -45},
			{actionspace.VerbFlat, actionspace.DirectionFlat, "1h", -10},
		} {
			size := 0.25
			if spec.verb == actionspace.VerbFlat {
				size = 0
			}
			out = append(out, TrainingExample{
				Symbol: "BTC-USDT-SWAP", DecisionTime: decision, StateLabel: actionspace.SideFlat,
				StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: actionspace.BaseEquityUSDT},
				Action: actionspace.Action{
					Verb: spec.verb, Direction: spec.dir, TargetHorizon: spec.horizon, SizeFraction: size, DelayBars: 0,
				},
				Label: "l", RewardBPS: spec.reward, Features: features,
			})
		}
	}
	return out
}

// SyntheticDiverseExamples builds context-dependent training rows for tests.
func SyntheticDiverseExamples() []TrainingExample {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []TrainingExample
	for i := 0; i < 90; i++ {
		decision := base.Add(time.Duration(i) * time.Hour)
		regime := i % 3
		features := syntheticFeatureRegime(regime, float64(i))
		for _, spec := range []struct {
			verb     string
			dir      string
			horizon  string
			reward   float64
		}{
			{actionspace.VerbOpenLong, actionspace.DirectionLong, horizonReward(regime, true), directionReward(regime, actionspace.DirectionLong)},
			{actionspace.VerbOpenShort, actionspace.DirectionShort, horizonReward(regime, true), directionReward(regime, actionspace.DirectionShort)},
			{actionspace.VerbOpenLong, actionspace.DirectionLong, horizonReward(regime, false), directionReward(regime, actionspace.DirectionLong) - 3},
			{actionspace.VerbOpenShort, actionspace.DirectionShort, horizonReward(regime, false), directionReward(regime, actionspace.DirectionShort) - 3},
			{actionspace.VerbFlat, actionspace.DirectionFlat, "1h", directionReward(regime, actionspace.DirectionFlat)},
		} {
			size := 0.25
			if spec.verb == actionspace.VerbFlat {
				size = 0
			}
			out = append(out, TrainingExample{
				Symbol: "BTC-USDT-SWAP", DecisionTime: decision, StateLabel: actionspace.SideFlat,
				StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: actionspace.BaseEquityUSDT},
				Action: actionspace.Action{
					Verb: spec.verb, Direction: spec.dir, TargetHorizon: spec.horizon, SizeFraction: size, DelayBars: 0,
				},
				Label: "l", RewardBPS: spec.reward, Features: features,
			})
		}
		longState := actionspace.StandardOracleStates(100)[1]
		shortState := actionspace.StandardOracleStates(100)[2]
		for _, spec := range []struct {
			state  actionspace.PositionState
			verb   string
			reward float64
		}{
			{longState, actionspace.VerbHold, positionedReward(regime, actionspace.SideLong, actionspace.VerbHold)},
			{longState, actionspace.VerbTrim, positionedReward(regime, actionspace.SideLong, actionspace.VerbTrim)},
			{longState, actionspace.VerbClose, positionedReward(regime, actionspace.SideLong, actionspace.VerbClose)},
			{shortState, actionspace.VerbHold, positionedReward(regime, actionspace.SideShort, actionspace.VerbHold)},
			{shortState, actionspace.VerbTrim, positionedReward(regime, actionspace.SideShort, actionspace.VerbTrim)},
			{shortState, actionspace.VerbClose, positionedReward(regime, actionspace.SideShort, actionspace.VerbClose)},
		} {
			out = append(out, TrainingExample{
				Symbol: "BTC-USDT-SWAP", DecisionTime: decision,
				StateLabel: actionspace.StateLabel(spec.state), StartState: spec.state,
				Action: actionspace.Action{
					Verb: spec.verb, Direction: spec.state.Side, TargetHorizon: "1h",
					SizeFraction: map[string]float64{actionspace.VerbTrim: 0.25, actionspace.VerbHold: 0, actionspace.VerbClose: 0}[spec.verb],
					DelayBars: 0,
				},
				Label: "l", RewardBPS: spec.reward, Features: features,
			})
		}
	}
	return out
}

func positionedReward(regime int, side, verb string) float64 {
	switch regime {
	case 0:
		if side == actionspace.SideLong {
			switch verb {
			case actionspace.VerbHold:
				return 8
			case actionspace.VerbTrim:
				return 4
			case actionspace.VerbClose:
				return -2
			}
		}
		if side == actionspace.SideShort {
			switch verb {
			case actionspace.VerbClose:
				return 10
			case actionspace.VerbTrim:
				return 6
			case actionspace.VerbHold:
				return -6
			}
		}
	case 1:
		if side == actionspace.SideShort {
			switch verb {
			case actionspace.VerbHold:
				return 8
			case actionspace.VerbTrim:
				return 4
			case actionspace.VerbClose:
				return -2
			}
		}
		if side == actionspace.SideLong {
			switch verb {
			case actionspace.VerbClose:
				return 10
			case actionspace.VerbTrim:
				return 6
			case actionspace.VerbHold:
				return -6
			}
		}
	default:
		switch verb {
		case actionspace.VerbClose, actionspace.VerbTrim:
			return 5
		case actionspace.VerbHold:
			return 0
		}
	}
	return 0
}

func horizonReward(regime int, primary bool) string {
	if regime == 0 && primary {
		return "1h"
	}
	if regime == 1 && !primary {
		return "4h"
	}
	if primary {
		return "4h"
	}
	return "1h"
}

func directionReward(regime int, direction string) float64 {
	switch regime {
	case 0:
		switch direction {
		case actionspace.DirectionLong:
			return 20
		case actionspace.DirectionShort:
			return -20
		default:
			return -5
		}
	case 1:
		switch direction {
		case actionspace.DirectionShort:
			return 20
		case actionspace.DirectionLong:
			return -20
		default:
			return -5
		}
	default:
		switch direction {
		case actionspace.DirectionFlat:
			return 15
		default:
			return -10
		}
	}
}

func syntheticFeatureRegime(regime int, offset float64) map[string]float64 {
	features := map[string]float64{
		"btc_vol": 0.2, "btc_trend": 0, "btc_drawdown": 0.2, "btc_funding_state": 0,
		"sky_confidence": 0.8, "mask_btc_funding_missing": 0, "symbol_rv": 0.2, "symbol_beta": 1.0,
		"btc_relative_return": 0, "liquidity_percentile": 0.5, "quote_volume_rank": 0.5,
		"return_rank": 0.5, "vol_rank": 0.5, "liquidity_rank": 0.5, "beta_rank": 0.5,
		"btc_relative_strength_rank": 0.5, "liquidity_score": 0.5, "vol_cost_penalty": 0.1,
		"capacity_headroom": 0.8, "cost_estimate": 0.05,
	}
	switch regime {
	case 0:
		features["btc_return"] = 2.0 + offset*0.01
		features["symbol_return"] = 2.5 + offset*0.01
		features["btc_trend"] = 1.5
		features["return_rank"] = 0.9
	case 1:
		features["btc_return"] = -2.0 - offset*0.01
		features["symbol_return"] = -2.5 - offset*0.01
		features["btc_trend"] = -1.5
		features["return_rank"] = 0.1
	default:
		features["btc_return"] = 0
		features["symbol_return"] = 0
		features["btc_trend"] = 0
		features["return_rank"] = 0.5
	}
	return features
}
