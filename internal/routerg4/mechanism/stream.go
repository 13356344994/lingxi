package mechanism

import (
	"context"
	"fmt"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

// WFScanConfig bounds one symbol strict walk-forward stream.
type WFScanConfig struct {
	Context          context.Context
	Spec             profitmap.ProfileSpec
	Boundaries       profitmap.Boundaries
	SkyCutpoints     sky.Cutpoints
	SkySeries        sky.Series
	SymbolClass      profitmap.SymbolClassCutpoints
	SymbolClassLabel string
	Funding          []profitmap.FundingPoint
	MaxLines         int
	ProgressEvery    int
	OnProgress       func(lines int)
}

// StreamWFSymbol evaluates walk-forward-partition decision points only.
func StreamWFSymbol(
	bars []profitmap.Bar,
	symbol string,
	cfg WFScanConfig,
	emit func(validationEventContext) error,
) error {
	if len(bars) < 2 {
		return nil
	}
	windowBars := profitmap.RollingWindowBars(cfg.Spec)
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	lines := 0
	for i := 0; i < len(bars)-1; i++ {
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
		decision := profitmap.DecisionTime(bars, i, cfg.Spec.BarDuration)
		if cfg.Boundaries.PartitionAt(decision) != profitmap.PartitionWalkFwd {
			continue
		}
		entryIndex := i + 1
		if entryIndex >= len(bars) {
			continue
		}
		if !bars[entryIndex].OpenTime.Equal(decision) {
			continue
		}
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
		decisionBar := bars[i]
		rollingVol := profitmap.RollingQuoteVolumeUSDT(bars, i, windowBars)
		costIn := profitmap.CostInputs{
			RollingQuoteVolumeUSDT: rollingVol,
			BarHigh:                decisionBar.High,
			BarLow:                 decisionBar.Low,
			BarMid:                 decisionBar.Mid(),
		}
		for _, horizon := range cfg.Spec.Horizons {
			for _, direction := range []mechanism.Direction{profitmap.DirectionLong, profitmap.DirectionShort} {
				for _, tier := range protocol.CapitalTiersUSDT {
					normal, ok := computeWFOutcome(bars, i, horizon, direction, tier, costIn, cfg.Funding, 0)
					if !ok {
						continue
					}
					delayed, ok := computeWFOutcome(bars, i, horizon, direction, tier, costIn, cfg.Funding, 1)
					if !ok {
						continue
					}
					if err := emit(validationEventContext{
						Symbol:       symbol,
						DecisionTime: decision,
						CellKey: profitmap.CellKey{
							SkyCoarse:   skyLabel,
							Liquidity:   liq,
							SymbolClass: symbolClass,
							Horizon:     horizon.Label,
							Direction:   string(direction),
							CapitalTier: tier,
						},
						GrossBPS:              normal.GrossBPS,
						FeeNetBPS:             normal.FeeNetBPS,
						ConservativeBPS:       normal.ConservativeBPS,
						DelayConservativeBPS:  delayed.ConservativeBPS,
						StressConservativeBPS: normal.StressConservativeBPS,
						CapacityFail:          normal.CapacityFail,
					}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

type wfOutcomeMetrics struct {
	GrossBPS              float64
	FeeNetBPS             float64
	ConservativeBPS       float64
	StressConservativeBPS float64
	CapacityFail          bool
}

type validationEventContext struct {
	Symbol                string
	DecisionTime          time.Time
	CellKey               profitmap.CellKey
	GrossBPS              float64
	FeeNetBPS             float64
	ConservativeBPS       float64
	DelayConservativeBPS  float64
	StressConservativeBPS float64
	CapacityFail          bool
}

func computeWFOutcome(
	bars []profitmap.Bar,
	decisionIndex int,
	horizon profitmap.HorizonSpec,
	direction mechanism.Direction,
	tier int,
	costIn profitmap.CostInputs,
	funding []profitmap.FundingPoint,
	entryDelayBars int,
) (wfOutcomeMetrics, bool) {
	entryIndex := decisionIndex + 1 + entryDelayBars
	exitIndex := entryIndex + horizon.BarCount
	if entryIndex >= len(bars) || exitIndex >= len(bars) {
		return wfOutcomeMetrics{}, false
	}
	entryTime := bars[entryIndex].OpenTime
	exitTime := bars[exitIndex].OpenTime
	entryPrice := bars[entryIndex].Open
	exitPrice := bars[exitIndex].Open
	if entryPrice <= 0 || exitPrice <= 0 {
		return wfOutcomeMetrics{}, false
	}
	wantExit := entryTime.Add(horizon.Duration)
	if !exitTime.Equal(wantExit) {
		return wfOutcomeMetrics{}, false
	}
	costIn.CapitalUSDT = float64(tier)
	costIn.FundingBPS = profitmap.FundingBPSOverHold(funding, entryTime, exitTime)
	gross := profitmap.SignedGrossBPS(entryPrice, exitPrice, string(direction))
	feeNet, conservative, capFail := profitmap.ComputeReturns(gross, costIn)
	stress := profitmap.StressConservativeNet(gross, feeNet, conservative)
	return wfOutcomeMetrics{
		GrossBPS:              gross,
		FeeNetBPS:             feeNet,
		ConservativeBPS:       conservative,
		StressConservativeBPS: stress,
		CapacityFail:          capFail,
	}, true
}

// RejectWFPartitionLeak returns an error if decision is outside walk-forward.
func RejectWFPartitionLeak(boundaries profitmap.Boundaries, decision time.Time) error {
	if boundaries.PartitionAt(decision) != profitmap.PartitionWalkFwd {
		return fmt.Errorf("mechanism: partition leak outside walk_forward at %s", decision.UTC().Format(time.RFC3339))
	}
	return nil
}
