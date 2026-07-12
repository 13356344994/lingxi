package outcome

import (
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
)

// StreamSymbol evaluates one symbol and emits reward records immediately.
func StreamSymbol(bars []profitmap.Bar, symbol string, cfg ScanSymbolConfig, emit func(RewardRecord, string, string) error) error {
	if len(bars) < 2 {
		return nil
	}
	horizonLabels := make([]string, len(cfg.Spec.Horizons))
	horizonByLabel := map[string]profitmap.HorizonSpec{}
	for i, h := range cfg.Spec.Horizons {
		horizonLabels[i] = h.Label
		horizonByLabel[h.Label] = h
	}
	lines := 0
	for i := 0; i < len(bars)-1; i++ {
		lines++
		if cfg.ProgressEvery > 0 && cfg.OnProgress != nil && lines%cfg.ProgressEvery == 0 {
			cfg.OnProgress(lines)
		}
		if profitmap.RejectCurrentBarOpen(bars, i, cfg.Spec.BarDuration) {
			continue
		}
		decision := profitmap.DecisionTime(bars, i, cfg.Spec.BarDuration)
		partition := cfg.Boundaries.PartitionAt(decision)
		if partition == "" {
			continue
		}
		windowBars := profitmap.RollingWindowBars(cfg.Spec)
		avgQuote := profitmap.AvgQuoteVolumeUSDT(bars, i, windowBars)
		liq := profitmap.LiquidityBucket(avgQuote)
		skyTime := decision
		if cfg.Spec.ID == profitmap.ProfileOKX5M {
			skyTime = profitmap.SkyDecisionTimeFor5m(decision)
		}
		skyLabel := profitmap.SkyAtDecision(cfg.SkyCutpoints, cfg.SkySeries, skyTime)
		symbolClass := cfg.SymbolClassLabel
		if symbolClass == "" {
			symbolClass = cfg.SymbolClass.Classify(avgQuote)
		}
		basis := bars[i].Close
		for _, startState := range actionspace.StandardOracleStates(basis) {
			actions, err := actionspace.EnumerateActions(symbol, startState, horizonLabels)
			if err != nil {
				return err
			}
			for _, action := range actions {
				horizon, ok := horizonByLabel[action.TargetHorizon]
				if !ok {
					continue
				}
				rec, ok := EvaluateReward(bars, i, startState, action, horizon, cfg.Spec, cfg.Funding)
				if !ok {
					continue
				}
				rec.Symbol = symbol
				rec.Partition = partition
				rec.DecisionTime = decision
				rec.SoftRegime = skyLabel
				rec.Action.Symbol = symbol
				if err := emit(rec, liq, symbolClass); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// CellKeyFromRecord builds the cube cell key for one reward record.
func CellKeyFromRecord(rec RewardRecord, liq, symbolClass string) CellKey {
	return CellKey{
		StateLabel:   rec.StateLabel,
		Verb:         rec.Action.Verb,
		Direction:    rec.Action.Direction,
		Horizon:      rec.Action.TargetHorizon,
		SizeFraction: rec.Action.SizeFraction,
		DelayBars:    rec.Action.DelayBars,
		SoftRegime:   rec.SoftRegime,
		Liquidity:    liq,
		SymbolClass:  symbolClass,
	}
}
