package validation

import (
	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/brain"
	"lingxi/internal/routerg10/fingerprint"
)

// DegeneracySummary records formal-history Brain trace degeneration checks.
type DegeneracySummary struct {
	Status            string `json:"status"`
	Degenerate        bool   `json:"degenerate"`
	DominantAction    string `json:"dominant_action,omitempty"`
	DominantFraction  float64 `json:"dominant_fraction,omitempty"`
	BarsSinceLastExit int    `json:"bars_since_last_exit,omitempty"`
	Reasons           []string `json:"reasons,omitempty"`
}

// CheckBrainDegeneracy replays validation triggers through Brain and audits trace health.
func CheckBrainDegeneracy(
	events []ReplayEvent,
	fp fingerprint.PersonalExecutionFingerprint,
	profileID string,
) (DegeneracySummary, error) {
	if len(events) == 0 {
		return DegeneracySummary{Status: "EMPTY", Degenerate: true, Reasons: []string{"no_events"}}, nil
	}
	inst := LookupInstrumentConstraints(fp, events[0].Symbol)
	cfg := brain.Config{
		InitialEquityUSDT: 30,
		Fingerprint:       fp,
		PersonalAdj:       LookupPersonalAdjustments(fp, events[0].Symbol, "long"),
		ProtocolHash:      "degeneracy-check",
	}
	if !inst.Unknown {
		cfg.MinNotionalUSDT = inst.MinNotionalUSDT
		if inst.ContractValue > 0 && inst.LotNotionalUSDT > 0 {
			cfg.LotStep = inst.LotNotionalUSDT / inst.ContractValue
			cfg.ContractValue = inst.ContractValue
		}
	}
	b := brain.NewBrain(cfg)
	pathTags := map[string]bool{}
	for _, ev := range events {
		if ev.NoTrade {
			continue
		}
		feat := FeatureSnapshot{ReturnBPS: ev.ReturnBPS, PriorReturnBPS: ev.PriorReturnBPS}
		barFeat := toBarFeatures(ev.Symbol, profileID, feat, 100)
		bar := brain.BarInput{
			BarIndex:     ev.DecisionBarIndex,
			DecisionTime: ev.DecisionTime,
			Features:     barFeat,
			Prices:       map[string]float64{ev.Symbol: 100},
		}
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
	report, err := audit.DetectDegeneracy(*b.Trace, pathTags)
	if err != nil {
		return DegeneracySummary{}, err
	}
	return DegeneracySummary{
		Status:            report.Status,
		Degenerate:        report.Degenerate,
		DominantAction:    report.DominantAction,
		DominantFraction:  report.DominantFraction,
		BarsSinceLastExit: report.BarsSinceLastExit,
		Reasons:           report.DegeneracyReasons,
	}, nil
}
