package profitmap

import (
	"math"
	"time"

	"lingxi/internal/routerg4/protocol"
)

// CostInputs holds decision-bar execution proxies.
type CostInputs struct {
	CapitalUSDT            float64
	RollingQuoteVolumeUSDT float64
	BarHigh                float64
	BarLow                 float64
	BarMid                 float64
	FundingBPS             float64
}

// ComputeReturns applies the frozen G4 cost model for one opportunity.
func ComputeReturns(grossBPS float64, in CostInputs) (feeNet, conservativeNet float64, capacityFail bool) {
	feeNet = grossBPS - protocol.RoundtripTakerFeeBPS

	capacityImpact := protocol.CapacityImpactScaleBPS * in.CapitalUSDT / math.Max(in.RollingQuoteVolumeUSDT, 1)
	if capacityImpact > protocol.CapacityImpactCapBPS {
		capacityImpact = protocol.CapacityImpactCapBPS
	}
	capacityFail = capacityImpact >= protocol.CapacityImpactCapBPS

	mid := math.Max(in.BarMid, 1e-12)
	barRange := (in.BarHigh - in.BarLow) / mid * 10000 * protocol.BarRangeToxicityScale
	if barRange > protocol.BarRangeToxicityCapBPS {
		barRange = protocol.BarRangeToxicityCapBPS
	}

	conservativeNet = grossBPS -
		protocol.RoundtripTakerFeeBPS -
		protocol.BaseSpreadSlippageFloorBPS -
		capacityImpact -
		barRange -
		in.FundingBPS
	return feeNet, conservativeNet, capacityFail
}

// StressConservativeNet applies the protocol cost stress multiplier to non-fee costs.
func StressConservativeNet(grossBPS, feeNet, conservativeNet float64) float64 {
	nonFeeCosts := grossBPS - conservativeNet - protocol.RoundtripTakerFeeBPS
	if nonFeeCosts < 0 {
		nonFeeCosts = 0
	}
	stressed := grossBPS - protocol.RoundtripTakerFeeBPS - nonFeeCosts*protocol.CostStressMultiplier
	return stressed
}

// SignedGrossBPS returns directional gross return in bps.
func SignedGrossBPS(entry, exit float64, direction string) float64 {
	if entry <= 0 || exit <= 0 {
		return 0
	}
	raw := (exit/entry - 1) * 10000
	if direction == string(DirectionShort) {
		raw = -raw
	}
	return raw
}

// FundingBPSOverHold sums abs settled funding rates over [entry, exit) in bps.
func FundingBPSOverHold(rates []FundingPoint, entry, exit time.Time) float64 {
	var sum float64
	for _, p := range rates {
		if p.Time.Before(entry) {
			continue
		}
		if !p.Time.Before(exit) {
			break
		}
		sum += math.Abs(p.Rate)
	}
	return sum * 10000
}

// FundingPoint is one settled funding observation.
type FundingPoint struct {
	Time time.Time
	Rate float64
}
