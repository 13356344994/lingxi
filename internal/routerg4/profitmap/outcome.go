package profitmap

import (
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/protocol"
)

// ScanSymbolConfig bounds one symbol scan.
type ScanSymbolConfig struct {
	Spec             ProfileSpec
	Boundaries       Boundaries
	SkyCutpoints     sky.Cutpoints
	SkySeries        sky.Series
	SymbolClass      SymbolClassCutpoints
	SymbolClassLabel string
	Funding          []FundingPoint
	MaxLines         int
	ProgressEvery    int
	OnProgress       func(lines int)
}

// StreamSymbol evaluates one symbol and emits each opportunity immediately.
func StreamSymbol(bars []Bar, symbol string, cfg ScanSymbolConfig, emit func(Opportunity) error) error {
	if len(bars) < 2 {
		return nil
	}
	windowBars := RollingWindowBars(cfg.Spec)
	lines := 0
	for i := 0; i < len(bars)-1; i++ {
		lines++
		if cfg.ProgressEvery > 0 && cfg.OnProgress != nil && lines%cfg.ProgressEvery == 0 {
			cfg.OnProgress(lines)
		}
		if RejectCurrentBarOpen(bars, i, cfg.Spec.BarDuration) {
			continue
		}
		decision := DecisionTime(bars, i, cfg.Spec.BarDuration)
		partition := cfg.Boundaries.PartitionAt(decision)
		if partition == "" {
			continue
		}
		entryIndex := i + 1
		if entryIndex >= len(bars) {
			continue
		}
		entryTime := bars[entryIndex].OpenTime
		entryPrice := bars[entryIndex].Open
		if entryPrice <= 0 {
			continue
		}
		if !bars[entryIndex].OpenTime.Equal(decision) {
			continue
		}
		avgQuote := AvgQuoteVolumeUSDT(bars, i, windowBars)
		liq := LiquidityBucket(avgQuote)
		skyTime := decision
		if cfg.Spec.ID == ProfileOKX5M {
			skyTime = SkyDecisionTimeFor5m(decision)
		}
		skyLabel := SkyAtDecision(cfg.SkyCutpoints, cfg.SkySeries, skyTime)
		symbolClass := cfg.SymbolClassLabel
		if symbolClass == "" {
			symbolClass = cfg.SymbolClass.Classify(avgQuote)
		}
		decisionBar := bars[i]
		rollingVol := RollingQuoteVolumeUSDT(bars, i, windowBars)

		for _, horizon := range cfg.Spec.Horizons {
			exitIndex := entryIndex + horizon.BarCount
			if exitIndex >= len(bars) {
				continue
			}
			exitTime := bars[exitIndex].OpenTime
			exitPrice := bars[exitIndex].Open
			if exitPrice <= 0 {
				continue
			}
			// Exit must be horizon-end bar open.
			wantExit := entryTime.Add(horizon.Duration)
			if !exitTime.Equal(wantExit) {
				continue
			}
			fundingBPS := FundingBPSOverHold(cfg.Funding, entryTime, exitTime)
			costIn := CostInputs{
				RollingQuoteVolumeUSDT: rollingVol,
				BarHigh:                decisionBar.High,
				BarLow:                 decisionBar.Low,
				BarMid:                 decisionBar.Mid(),
				FundingBPS:             fundingBPS,
			}
			for _, direction := range []mechanism.Direction{DirectionLong, DirectionShort} {
				gross := SignedGrossBPS(entryPrice, exitPrice, string(direction))
				for _, tier := range protocol.CapitalTiersUSDT {
					costIn.CapitalUSDT = float64(tier)
					feeNet, conservative, capFail := ComputeReturns(gross, costIn)
					wipeout := gross > protocol.NeutralBandBPS && conservative <= 0
					if err := emit(Opportunity{
						Symbol:          symbol,
						Partition:       partition,
						DecisionTime:    decision,
						SkyCoarse:       skyLabel,
						LiquidityBucket: liq,
						SymbolClass:     symbolClass,
						Horizon:         horizon.Label,
						Direction:       direction,
						CapitalTier:     tier,
						GrossBPS:        gross,
						FeeNetBPS:       feeNet,
						ConservativeBPS: conservative,
						CostWipeout:     wipeout,
						CapacityFail:    capFail,
					}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// ScanSymbol is a small-fixture convenience wrapper. Production uses
// StreamSymbol and never materializes all opportunities.
func ScanSymbol(bars []Bar, symbol string, cfg ScanSymbolConfig) []Opportunity {
	var out []Opportunity
	_ = StreamSymbol(bars, symbol, cfg, func(opp Opportunity) error {
		out = append(out, opp)
		return nil
	})
	return out
}
