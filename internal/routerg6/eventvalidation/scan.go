package eventvalidation

import (
	"context"
	"fmt"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// ScanConfig bounds one symbol validation stream for one EventCard.
type ScanConfig struct {
	Context         context.Context
	Spec            eventmap.ProfileSpec
	Boundaries      eventmap.Boundaries
	Thresholds      eventmap.FrozenThresholds
	BTCBars         []eventmap.Bar
	ShockClusterAt  func(time.Time) string
	Funding         []profitmap.FundingPoint
	MaxLines        int
	ProgressEvery   int
	OnProgress      func(lines int)
}

// ScanCardSymbol evaluates validation-partition triggers for one frozen EventCard.
func ScanCardSymbol(
	bars []eventmap.Bar,
	symbol string,
	card reverseevent.FrozenEventCard,
	cfg ScanConfig,
	tracker *eventmap.WindowTracker,
	acc *cardAccumulator,
) error {
	if len(bars) < 3 || tracker == nil || acc == nil {
		return nil
	}
	horizon, ok := horizonSpec(cfg.Spec, card.Horizon)
	if !ok {
		return fmt.Errorf("eventvalidation: unknown horizon %q", card.Horizon)
	}
	windowBars := rollingWindowBars(cfg.Spec)
	primaryBack := primaryReturnBars(cfg.Spec)
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	lines := 0
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		lines++
		if cfg.ProgressEvery > 0 && cfg.OnProgress != nil && lines%cfg.ProgressEvery == 0 {
			cfg.OnProgress(lines)
		}
		if profitmap.RejectCurrentBarOpen(bars, i, cfg.Spec.BarDuration) {
			continue
		}
		decision := decisionTimeAt(bars, i, cfg.Spec.BarDuration)
		if cfg.Boundaries.PartitionAt(decision) != eventmap.PartitionValidation {
			continue
		}
		entryIndex := i + 1
		if entryIndex >= len(bars) || !bars[entryIndex].OpenTime.Equal(decision) {
			continue
		}
		families := eventmap.DetectFamilies(symbol, bars, i, cfg.Spec, cfg.Boundaries, cfg.Thresholds, cfg.BTCBars, windowBars)
		features := FeaturesFromBar(bars, i, cfg.Spec, cfg.BTCBars, windowBars, primaryBack, decision, cfg.Spec.BarDuration)
		if !MatchCardFamily(card, families, features) {
			continue
		}
		if !tracker.AllowCard(symbol, card.EventFamily, i) {
			continue
		}
		cluster := "none"
		if cfg.ShockClusterAt != nil {
			cluster = cfg.ShockClusterAt(decision)
		}
		decisionBar := bars[i]
		rollingVol := profitmap.RollingQuoteVolumeUSDT(bars, i, windowBars)
		costIn := profitmap.CostInputs{
			RollingQuoteVolumeUSDT: rollingVol,
			BarHigh:                decisionBar.High,
			BarLow:                 decisionBar.Low,
			BarMid:                 decisionBar.Mid(),
		}
		tracker.MarkCard(symbol, card.EventFamily, i, eventmap.CooldownBarsForEvent(cfg.Spec, card.Horizon, card.DelayBars))
		day := decision.UTC().Format("2006-01-02")

		for _, delay := range protocol.ValidationDelayBars {
			for _, tier := range protocol.ValidationInitialEquities {
				gross, feeNet, conservative, stress, capFail, ok := computeOutcome(
					bars, i, horizon, card.Direction, int(tier), costIn, cfg.Funding, delay,
				)
				if !ok {
					continue
				}
				acc.add(FiringSample{
					Symbol:                symbol,
					DecisionTime:          decision,
					Day:                   day,
					ShockCluster:          cluster,
					DelayBars:             delay,
					CapitalTier:           int(tier),
					OutcomeClass:          classifyOutcome(conservative),
					GrossBPS:              gross,
					FeeNetBPS:             feeNet,
					ConservativeBPS:       conservative,
					StressConservativeBPS: stress,
					CapacityFail:          capFail,
				})
			}
		}
	}
	return nil
}
