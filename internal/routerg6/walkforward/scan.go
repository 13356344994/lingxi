package walkforward

import (
	"context"
	"fmt"
	"math"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// WFEvent is one walk_forward partition trigger prepared for account replay.
type WFEvent struct {
	EventID          string
	Symbol           string
	Direction        string
	EventFamily      string
	DecisionTime     time.Time
	DecisionBarIndex int
	Day              string
	ShockCluster     string
	DeadlineBarIndex int
	CompleteAtBar    int
	HorizonBarCount  int
	CardDelayBars    int
	ConservativeBPS  float64
	StressBPS        float64
	CostEstimateBPS  float64
	CapacityFail     bool
	ReferenceNotional float64
	MarginTimeSec    int64
}

// ScanConfig bounds one symbol walk_forward stream for one EventCard.
type ScanConfig struct {
	Context        context.Context
	Spec           eventmap.ProfileSpec
	Boundaries     eventmap.Boundaries
	Thresholds     eventmap.FrozenThresholds
	BTCBars        []eventmap.Bar
	ShockClusterAt func(time.Time) string
	Funding        []profitmap.FundingPoint
	MaxLines       int
}

// ScanCardSymbolWalkForward evaluates walk_forward partition triggers for one frozen EventCard.
func ScanCardSymbolWalkForward(
	bars []eventmap.Bar,
	symbol string,
	card reverseevent.FrozenEventCard,
	cfg ScanConfig,
	tracker *eventmap.WindowTracker,
) ([]WFEvent, error) {
	if len(bars) < 3 || tracker == nil {
		return nil, nil
	}
	horizon, ok := horizonFor(cfg.Spec, card.Horizon)
	if !ok {
		return nil, fmt.Errorf("walkforward: unknown horizon %q", card.Horizon)
	}
	windowBars := profitmap.RollingWindowBars(cfg.Spec)
	primaryBack := primaryReturnBars(cfg.Spec)
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var events []WFEvent
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if profitmap.RejectCurrentBarOpen(bars, i, cfg.Spec.BarDuration) {
			continue
		}
		decision := profitmap.DecisionTime(bars, i, cfg.Spec.BarDuration)
		if cfg.Boundaries.PartitionAt(decision) != eventmap.PartitionWalkFwd {
			continue
		}
		entryIndex := i + 1
		if entryIndex >= len(bars) || !bars[entryIndex].OpenTime.Equal(decision) {
			continue
		}
		families := eventmap.DetectFamilies(symbol, bars, i, cfg.Spec, cfg.Boundaries, cfg.Thresholds, cfg.BTCBars, windowBars)
		features := eventvalidation.FeaturesFromBar(bars, i, cfg.Spec, cfg.BTCBars, windowBars, primaryBack, decision, cfg.Spec.BarDuration)
		if !eventvalidation.MatchCardFamily(card, families, features) {
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
		cooldown := eventmap.CooldownBarsForEvent(cfg.Spec, card.Horizon, card.DelayBars)
		tracker.MarkCard(symbol, card.EventFamily, i, cooldown)
		day := decision.UTC().Format("2006-01-02")

		delay := card.DelayBars
		if delay < 0 {
			delay = 0
		}
		tier := int(protocol.CapitalTiersUSDT[0])
		if len(card.CapitalTiers) > 0 {
			tier = card.CapitalTiers[0]
		}
		_, _, conservative, stress, capFail, ok := computeOutcome(
			bars, i, horizon, card.Direction, tier, costIn, cfg.Funding, delay,
		)
		if !ok {
			continue
		}
		exitIndex := i + 1 + delay + horizon.BarCount
		eventID := eventmap.BuildEventID(symbol, decision, card.EventFamily)
		events = append(events, WFEvent{
			EventID:           eventID,
			Symbol:            symbol,
			Direction:         card.Direction,
			EventFamily:       card.EventFamily,
			DecisionTime:      decision,
			DecisionBarIndex:  i,
			Day:               day,
			ShockCluster:      cluster,
			DeadlineBarIndex:  exitIndex,
			CompleteAtBar:     exitIndex,
			HorizonBarCount:   horizon.BarCount,
			CardDelayBars:     delay,
			ConservativeBPS:   conservative,
			StressBPS:         stress,
			CostEstimateBPS:   math.Max(0, conservative-stress),
			CapacityFail:      capFail,
			ReferenceNotional: float64(tier),
			MarginTimeSec:     int64(horizon.Duration / time.Second),
		})
	}
	return events, nil
}

func primaryReturnBars(spec eventmap.ProfileSpec) int {
	if spec.ID == eventmap.ProfileOKX5M {
		return 12
	}
	return 1
}

func computeOutcome(
	bars []eventmap.Bar,
	decisionIndex int,
	horizon profitmap.HorizonSpec,
	direction string,
	tier int,
	costIn profitmap.CostInputs,
	funding []profitmap.FundingPoint,
	delayBars int,
) (gross, feeNet, conservative, stress float64, capacityFail bool, ok bool) {
	entryIndex := decisionIndex + 1 + delayBars
	exitIndex := entryIndex + horizon.BarCount
	if entryIndex >= len(bars) || exitIndex >= len(bars) {
		return 0, 0, 0, 0, false, false
	}
	entryTime := bars[entryIndex].OpenTime
	exitTime := bars[exitIndex].OpenTime
	entryPrice := bars[entryIndex].Open
	exitPrice := bars[exitIndex].Open
	if entryPrice <= 0 || exitPrice <= 0 {
		return 0, 0, 0, 0, false, false
	}
	wantExit := entryTime.Add(horizon.Duration)
	if !exitTime.Equal(wantExit) {
		return 0, 0, 0, 0, false, false
	}
	costIn.CapitalUSDT = float64(tier)
	costIn.FundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
	gross = profitmap.SignedGrossBPS(entryPrice, exitPrice, direction)
	feeNet, conservative, capacityFail = profitmap.ComputeReturns(gross, costIn)
	stress = profitmap.StressConservativeNet(gross, feeNet, conservative)
	if math.IsNaN(stress) || math.IsInf(stress, 0) {
		return 0, 0, 0, 0, false, false
	}
	return gross, feeNet, conservative, stress, capacityFail, true
}
