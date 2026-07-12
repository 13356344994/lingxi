package validation

import (
	"math"

	"lingxi/internal/routerg10/brain"
	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/fingerprint"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/residualmap"
)

// BrainPickResult is the frozen local counterfactual decision for one trigger.
type BrainPickResult struct {
	Action          string
	Direction       string
	ConservativeBPS float64
	StressBPS       float64
	NoTrade         bool
}

// PickBrainDirection compares long/short/flat counterfactual branches via Brain-local scoring.
func PickBrainDirection(
	pb playbook.FrozenEventPlaybook,
	feat FeatureSnapshot,
	barFeatures brain.BarFeatures,
	fp fingerprint.PersonalExecutionFingerprint,
	costBase residualmap.CostBase,
	inst InstrumentConstraints,
	equity float64,
	longConservative, shortConservative, longStress, shortStress float64,
	solvent bool,
) BrainPickResult {
	matched := brain.MatchPlaybooks([]playbook.FrozenEventPlaybook{pb}, barFeatures, 0)
	var me brain.MatchedEvent
	if len(matched) > 0 {
		me = matched[0]
	} else {
		me = brain.MatchedEvent{
			Playbook:    pb,
			EventFamily: pb.EventFamily,
			GrossLongBPS:  feat.ReturnBPS,
			GrossShortBPS: -feat.ReturnBPS,
		}
	}
	me.GrossLongBPS = feat.ReturnBPS
	me.GrossShortBPS = -feat.ReturnBPS

	minNotional := inst.MinNotionalUSDT
	lotStep := 0.0
	contractValue := inst.ContractValue
	if inst.Unknown {
		minNotional = 0
		lotStep = 0
		contractValue = 0
	} else if inst.LotNotionalUSDT > 0 && inst.ContractValue > 0 {
		lotStep = inst.LotNotionalUSDT / inst.ContractValue
	}

	ctx := counterfactual.DecisionContext{
		Symbol:       barFeatures.Symbol,
		ProfileID:    barFeatures.ProfileID,
		EquityUSDT:   equity,
		Fingerprint:  fp,
		CostBase:     costBase,
		PersonalAdj:  LookupPersonalAdjustments(fp, barFeatures.Symbol, "long"),
		MinNotionalUSDT: minNotional,
		LotStep:         lotStep,
		ContractValue:   contractValue,
		Solvent:         solvent,
		Event: counterfactual.EventContext{
			HasEvent:      true,
			EventFamily:   pb.EventFamily,
			PlaybookHash:  pb.PlaybookHash,
			Horizon:       pb.Horizon,
			GrossLongBPS:  me.GrossLongBPS,
			GrossShortBPS: me.GrossShortBPS,
			DelayBars:     pb.DelayBars,
		},
		CompletionBars: me.CompletionBars,
		CapacityUSDT:   me.CapacityUSDT,
	}
	decision, err := counterfactual.Decide(ctx, g10protocol.RouterProtocolHash)
	if err != nil {
		return BrainPickResult{Action: "NO_TRADE", Direction: "flat", NoTrade: true}
	}

	result := BrainPickResult{
		Action:    decision.FinalAction,
		Direction: directionFromAction(decision.FinalAction),
		NoTrade:   decision.FinalAction == "NO_TRADE" || decision.FinalAction == "HOLD",
	}
	switch result.Direction {
	case "long":
		result.ConservativeBPS = longConservative
		result.StressBPS = longStress
	case "short":
		result.ConservativeBPS = shortConservative
		result.StressBPS = shortStress
	default:
		result.ConservativeBPS = math.Max(longConservative, shortConservative)
		if result.ConservativeBPS < 0 {
			result.ConservativeBPS = 0
		}
		result.StressBPS = math.Max(longStress, shortStress)
	}
	return result
}

func directionFromAction(action string) string {
	switch action {
	case "LONG", "ADD":
		return "long"
	case "SHORT", "REVERSE":
		return "short"
	default:
		return "flat"
	}
}

func toBarFeatures(symbol, profileID string, feat FeatureSnapshot, price float64) brain.BarFeatures {
	return brain.BarFeatures{
		Symbol:            symbol,
		ProfileID:         profileID,
		ReturnBPS:         feat.ReturnBPS,
		PriorReturnBPS:    feat.PriorReturnBPS,
		BTCRelativeBPS:    feat.BTCRelativeBPS,
		BarRangeBPS:       feat.RMSBPS,
		RollingVolumeUSDT: feat.AvgQuoteVolume,
		Price:             price,
	}
}
