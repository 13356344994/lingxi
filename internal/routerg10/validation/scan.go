package validation

import (
	"context"
	"fmt"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

// ScanConfig bounds one symbol validation stream for one Event Playbook.
type ScanConfig struct {
	Context        context.Context
	Spec           eventmap.ProfileSpec
	ProfileID      string
	Boundaries     eventmap.Boundaries
	Thresholds     eventmap.FrozenThresholds
	BTCBars        []eventmap.Bar
	ShockClusterAt func(time.Time) string
	Funding        []profitmap.FundingPoint
	MaxLines       int
	Fingerprint    fingerprint.PersonalExecutionFingerprint
}

// ScanPlaybookSymbol evaluates validation-partition triggers for one frozen playbook.
func ScanPlaybookSymbol(
	bars []eventmap.Bar,
	symbol string,
	pb playbook.FrozenEventPlaybook,
	cfg ScanConfig,
	tracker *eventmap.WindowTracker,
) ([]ReplayEvent, error) {
	if len(bars) < 3 || tracker == nil {
		return nil, nil
	}
	horizon, ok := horizonSpec(cfg.Spec, pb.Horizon)
	if !ok {
		return nil, fmt.Errorf("validation: unknown horizon %q", pb.Horizon)
	}
	windowBars := rollingWindowBars(cfg.Spec)
	primaryBack := primaryReturnBars(cfg.Spec)
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	inst := LookupInstrumentConstraints(cfg.Fingerprint, symbol)
	equity := float64(protocol.ValidationInitialEquities[0])
	if actual := ActualEquityUSDT(cfg.Fingerprint); actual > 0 {
		equity = actual
	}
	var events []ReplayEvent
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
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
		g6Features := eventvalidation.FeaturesFromBar(bars, i, cfg.Spec, cfg.BTCBars, windowBars, primaryBack, decision, cfg.Spec.BarDuration)
		features := FeatureSnapshot{
			ReturnBPS: g6Features.ReturnBPS, PriorReturnBPS: g6Features.PriorReturnBPS,
			RMSBPS: g6Features.RMSBPS, AvgQuoteVolume: g6Features.AvgQuoteVolume,
			VolumeSpike: g6Features.VolumeSpike, BTCRelativeBPS: g6Features.BTCRelativeBPS,
			SessionBoundary: g6Features.SessionBoundary,
		}
		if !MatchPlaybookFamily(pb, families, features) {
			continue
		}
		if !tracker.AllowCard(symbol, pb.EventFamily, i) {
			continue
		}
		cluster := "none"
		if cfg.ShockClusterAt != nil {
			cluster = cfg.ShockClusterAt(decision)
		}
		decisionBar := bars[i]
		delay := pb.DelayBars
		if delay < 0 {
			delay = 0
		}
		costBase := buildCostBase(bars, i, cfg.Spec, horizon, cfg.ProfileID, delay, cfg.Funding)
		barFeatures := toBarFeatures(symbol, cfg.ProfileID, features, decisionBar.Close)
		_ = barFeatures

	longGross, longCons, longStress, longCap, longOK := ComputePersonalOutcome(
			bars, symbol, i, horizon, "long", int(equity), costBase, cfg.Funding, delay, cfg.Fingerprint,
		)
		shortGross, shortCons, shortStress, shortCap, shortOK := ComputePersonalOutcome(
			bars, symbol, i, horizon, "short", int(equity), costBase, cfg.Funding, delay, cfg.Fingerprint,
		)
		_, flatCons, flatStress, _, flatOK := ComputePersonalOutcome(
			bars, symbol, i, horizon, "flat", int(equity), costBase, cfg.Funding, delay, cfg.Fingerprint,
		)
		_ = longGross
		_ = shortGross
		_ = flatCons
		_ = flatStress
		if !longOK && !shortOK && !flatOK {
			continue
		}

		direction := InferDirection(pb, features)
		conservative := longCons
		stress := longStress
		capFail := longCap
		switch direction {
		case "short":
			conservative = shortCons
			stress = shortStress
			capFail = shortCap
		case "flat":
			if shortCons > longCons {
				conservative = shortCons
				stress = shortStress
			} else {
				conservative = longCons
				stress = longStress
			}
		}

		tracker.MarkCard(symbol, pb.EventFamily, i, eventmap.CooldownBarsForEvent(cfg.Spec, pb.Horizon, pb.DelayBars))
		day := decision.UTC().Format("2006-01-02")
		cliffUSDT := capacityCliffUSDT(bars, symbol, i, horizon, direction, costBase, cfg.Funding, delay, cfg.Fingerprint)
		exitIndex := i + 1 + delay + horizon.BarCount
		eventID := eventmap.BuildEventID(symbol, decision, pb.EventFamily)
		minNotional, lotNotional := inst.MinNotionalUSDT, inst.LotNotionalUSDT
		events = append(events, ReplayEvent{
			EventID: eventID, Symbol: symbol, Direction: direction, EventFamily: pb.EventFamily,
			DecisionTime: decision, DecisionBarIndex: i, Day: day, ShockCluster: cluster,
			DeadlineBarIndex: exitIndex, CompleteAtBar: exitIndex, HorizonBarCount: horizon.BarCount,
			PlaybookDelayBars: delay, ConservativeBPS: conservative, StressBPS: stress,
			CapacityFail: capFail, CapacityCliffUSDT: cliffUSDT, ReferenceNotional: equity,
			NoTrade: false, BrainAction: "",
			MinNotionalUSDT: minNotional, LotNotionalUSDT: lotNotional,
			ReturnBPS: features.ReturnBPS, PriorReturnBPS: features.PriorReturnBPS,
			DirectionDeferred: true,
			LongConservativeBPS: longCons, ShortConservativeBPS: shortCons,
			LongStressBPS: longStress, ShortStressBPS: shortStress,
		})
	}
	return events, nil
}

func capacityCliffUSDT(
	bars []eventmap.Bar,
	symbol string,
	decisionIndex int,
	horizon profitmap.HorizonSpec,
	direction string,
	base residualmap.CostBase,
	funding []profitmap.FundingPoint,
	delayBars int,
	fp fingerprint.PersonalExecutionFingerprint,
) float64 {
	if direction == "flat" {
		return 0
	}
	for _, cliff := range []float64{10000, 100000} {
		_, conservative, _, capFail, ok := ComputePersonalOutcome(
			bars, symbol, decisionIndex, horizon, direction, int(cliff), base, funding, delayBars, fp,
		)
		if !ok || capFail || conservative < 0 {
			return cliff
		}
	}
	return 100000
}

func horizonSpec(spec eventmap.ProfileSpec, label string) (profitmap.HorizonSpec, bool) {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h, true
		}
	}
	return profitmap.HorizonSpec{}, false
}

func rollingWindowBars(spec eventmap.ProfileSpec) int {
	return profitmap.RollingWindowBars(spec)
}

func primaryReturnBars(spec eventmap.ProfileSpec) int {
	if spec.BarDuration <= time.Hour {
		return 1
	}
	return 1
}

func decisionTimeAt(bars []eventmap.Bar, idx int, barDuration time.Duration) time.Time {
	return profitmap.DecisionTime(bars, idx, barDuration)
}
