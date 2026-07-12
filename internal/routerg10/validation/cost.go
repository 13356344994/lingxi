package validation

import (
	"math"
	"strconv"

	"lingxi/internal/routerg10/fingerprint"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

// ComputePersonalOutcome evaluates gross and personal conservative/stress returns for one tier.
func ComputePersonalOutcome(
	bars []eventmap.Bar,
	symbol string,
	decisionIndex int,
	horizon profitmap.HorizonSpec,
	direction string,
	tier int,
	base residualmap.CostBase,
	funding []profitmap.FundingPoint,
	delayBars int,
	fp fingerprint.PersonalExecutionFingerprint,
) (gross, conservative, stress float64, capacityFail bool, ok bool) {
	if direction == "flat" {
		return 0, 0, 0, false, true
	}
	entryIndex := decisionIndex + 1 + delayBars
	exitIndex := entryIndex + horizon.BarCount
	if entryIndex >= len(bars) || exitIndex >= len(bars) {
		return 0, 0, 0, false, false
	}
	entryTime := bars[entryIndex].OpenTime
	exitTime := bars[exitIndex].OpenTime
	entryPrice := bars[entryIndex].Open
	exitPrice := bars[exitIndex].Open
	if entryPrice <= 0 || exitPrice <= 0 {
		return 0, 0, 0, false, false
	}
	wantExit := entryTime.Add(horizon.Duration)
	if !exitTime.Equal(wantExit) {
		return 0, 0, 0, false, false
	}
	gross = profitmap.SignedGrossBPS(entryPrice, exitPrice, direction)
	adj := LookupPersonalAdjustments(fp, symbol, direction)
	actual := ActualEquityUSDT(fp)
	tiers := residualmap.ComputeTierCosts(gross, base, adj, actual)
	label := tierLabelForCapital(tier)
	tierCost, found := tiers[label]
	if !found {
		return 0, 0, 0, false, false
	}
	conservative = tierCost.ConservativeBPS
	capacityFail = tierCost.CapacityFail
	stress = adverseStressBPS(tierCost, delayBars)
	if math.IsNaN(stress) || math.IsInf(stress, 0) {
		return 0, 0, 0, false, false
	}
	return gross, conservative, stress, capacityFail, true
}

func tierLabelForCapital(tier int) string {
	switch tier {
	case int(g10protocol.MicroCapitalTiersUSDT[0]):
		return "30"
	case int(g10protocol.MicroCapitalTiersUSDT[1]):
		return "100"
	case int(g10protocol.MicroCapitalTiersUSDT[2]):
		return "1000"
	case 10000:
		return "10000"
	case 100000:
		return "100000"
	default:
		return strconv.Itoa(tier)
	}
}

func adverseStressBPS(tier residualmap.TierCostBreakdown, delayBars int) float64 {
	adverse := tier.FeeBPS + tier.SlipBPS + tier.ImpactBPS + math.Abs(tier.FundingBPS)
	if delayBars > 0 {
		adverse += float64(delayBars) * 0.5
	}
	stress := adverse * g10protocol.CostStressMultiplier
	if tier.ConservativeBPS < 0 {
		lossStress := -tier.ConservativeBPS * g10protocol.CostStressMultiplier
		if lossStress > stress {
			stress = lossStress
		}
	}
	return math.Max(stress, 1e-6)
}

func buildCostBase(
	bars []eventmap.Bar,
	decisionIndex int,
	spec eventmap.ProfileSpec,
	horizon profitmap.HorizonSpec,
	profileID string,
	delayBars int,
	funding []profitmap.FundingPoint,
) residualmap.CostBase {
	decisionBar := bars[decisionIndex]
	windowBars := rollingWindowBars(spec)
	entryIndex := decisionIndex + 1 + delayBars
	exitIndex := entryIndex + horizon.BarCount
	var entryTime, exitTime = decisionBar.OpenTime, decisionBar.OpenTime
	if entryIndex < len(bars) {
		entryTime = bars[entryIndex].OpenTime
	}
	if exitIndex < len(bars) {
		exitTime = bars[exitIndex].OpenTime
	}
	return residualmap.CostBase{
		RollingQuoteVolumeUSDT: profitmap.RollingQuoteVolumeUSDT(bars, decisionIndex, windowBars),
		BarHigh:                decisionBar.High,
		BarLow:                 decisionBar.Low,
		BarMid:                 decisionBar.Mid(),
		FundingBPS:             profitmap.FundingBPSOverHold(funding, entryTime, exitTime),
		ProfileID:              profileID,
		Horizon:                horizon.Label,
		DelayBars:              delayBars,
		HoldHours:              horizon.Duration.Hours(),
	}
}
