package personalvalidation

import (
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
)

// CheckBrainDegeneracy replays validation triggers through personal Brain and audits trace health.
func CheckBrainDegeneracy(
	events []TriggerEvent,
	pb personalplaybook.PersonalWildPlaybook,
) (DegeneracySummary, error) {
	if len(events) == 0 {
		return DegeneracySummary{Status: "EMPTY", Degenerate: true, Reasons: []string{"no_triggers"}}, nil
	}
	cfg := personalbrain.Config{
		InitialEquityUSDT: CapitalTiersUSDT[0],
		ConstitutionHash:  pb.ProofBinding.ConstitutionHash,
		MinNotionalUSDT:   pb.EntryNarrative.DynamicMinNotionalUSDT,
		ProtocolHash:      ProtocolHash,
		TradeAllowed:      false,
	}
	b := personalbrain.NewBrain(cfg)
	pathTags := map[string]bool{}
	for _, ev := range events {
		sim := personalbrain.ExecutionSimState{
			MakerFilled: ev.MakerFilled, MakerTouchedNotFilled: ev.MakerTouchedNotFilled,
			SubsequentlyProfitable: ev.SubsequentlyProfitable,
			ThesisStillValid: !ev.ToxicExit,
		}
		bar := personalbrain.BarInput{
			BarIndex: ev.DecisionBarIndex, DecisionTime: ev.DecisionTime,
			Features: personalbrain.BarFeatures{
				Symbol: ev.Symbol, ProfileID: ev.ProfileID,
				ReturnBPS: ev.ReturnBPS, PriorReturnBPS: ev.PriorReturnBPS, Price: 100,
			},
			Prices: map[string]float64{ev.Symbol: 100}, ExecutionSim: sim,
			CapitalTierUSDT: CapitalTiersUSDT[0],
		}
		matched := personalbrain.MatchPersonalPlaybooks([]personalplaybook.PersonalWildPlaybook{pb}, bar.Features, ev.DecisionBarIndex)
		bar.Events = matched
		out, err := b.DecideBar(bar)
		if err != nil {
			return DegeneracySummary{}, err
		}
		if out.Trace.FinalAction != "" {
			pathTags[out.Trace.FinalAction] = true
		}
	}
	if b.Trace == nil || len(b.Trace.Entries) == 0 {
		return DegeneracySummary{Status: "EMPTY", Degenerate: true, Reasons: []string{"no_trace"}}, nil
	}
	report, err := personalbrain.DetectDegeneracy(*b.Trace, pathTags)
	if err != nil {
		return DegeneracySummary{}, err
	}
	return DegeneracySummary{
		Status: report.Status, Degenerate: report.Degenerate,
		DominantAction: report.DominantAction, DominantFraction: report.DominantFraction,
		BarsSinceLastExit: report.BarsSinceLastExit, Reasons: report.DegeneracyReasons,
	}, nil
}
