package eventmap

import (
	"lingxi/internal/routerg4/profitmap"
)

// StreamSymbol detects feature-only events and emits expanded outcomes immediately.
func StreamSymbol(
	bars []Bar,
	symbol string,
	cfg ScanSymbolConfig,
	tracker *WindowTracker,
	emit func(EventOutcome) error,
) (int, error) {
	if len(bars) < 3 || tracker == nil {
		return 0, nil
	}
	windowBars := cfg.WindowBars
	if windowBars <= 0 {
		windowBars = RollingWindowBars(cfg.Spec)
	}
	primaryBack := primaryReturnBars(cfg.Spec)
	events := 0
	lines := 0
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		lines++
		if cfg.ProgressEvery > 0 && cfg.OnProgress != nil && lines%cfg.ProgressEvery == 0 {
			cfg.OnProgress(lines)
		}
		families := DetectFamilies(symbol, bars, i, cfg.Spec, cfg.Boundaries, cfg.Thresholds, cfg.BTCBars, windowBars)
		if len(families) == 0 {
			continue
		}
		decision := profitmap.DecisionTime(bars, i, cfg.Spec.BarDuration)
		partition := cfg.Boundaries.PartitionAt(decision)
		ret, priorRet, _, avgQuote, btcRel := featureContext(bars, i, cfg.Spec, cfg.BTCBars, windowBars, primaryBack)
		liq := LiquidityBucket(avgQuote)
		cluster := "none"
		if cfg.ShockClusterAt != nil {
			cluster = cfg.ShockClusterAt(decision)
		}
		for _, family := range families {
			event := DetectedEvent{
				ID:              BuildEventID(symbol, decision, family),
				Symbol:          symbol,
				Family:          family,
				Partition:       partition,
				DecisionTime:    decision,
				DecisionBarIdx:  i,
				ShockCluster:    cluster,
				SessionBucket:   sessionBucket(decision),
				LiquidityBucket: liq,
				ReturnBPS:       ret,
				PriorReturnBPS:  priorRet,
				BTCRelativeBPS:  btcRel,
				OptionalMissing: append([]string(nil), cfg.OptionalMissing...),
			}
			emitted := false
			for _, out := range ExpandOutcomes(event, bars, cfg.Spec, cfg.Funding) {
				cooldown := CooldownBarsForEvent(cfg.Spec, out.Horizon, out.DelayBars)
				if !tracker.AllowOutcome(symbol, family, out.Horizon, out.DelayBars, i) {
					continue
				}
				tracker.MarkOutcome(symbol, family, out.Horizon, out.DelayBars, i, cooldown)
				emitted = true
				if err := emit(out); err != nil {
					return events, err
				}
			}
			if emitted {
				events++
			}
		}
	}
	return events, nil
}
