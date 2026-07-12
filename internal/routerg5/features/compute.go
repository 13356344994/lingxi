package features

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
)

// MarketSnapshot holds BTC and optional funding for one profile scan.
type MarketSnapshot struct {
	BTCBars []profitmap.Bar
	Funding []profitmap.FundingPoint
}

// TimestampBatch computes DecisionContext rows for one decision timestamp.
func TimestampBatch(
	schema FeatureSchema,
	profile Profile,
	decisionTime time.Time,
	partition string,
	market MarketSnapshot,
	inputs []PITUniverseInput,
	capitalUSDT float64,
) ([]DecisionContext, error) {
	visible := VisibleSymbols(inputs, profile, decisionTime)
	if len(visible) == 0 {
		return nil, nil
	}
	visibleSet := map[string]bool{}
	for _, sym := range visible {
		visibleSet[sym] = true
	}
	btc := ComputeBTCFeatures(market.BTCBars, market.Funding, profile, decisionTime)
	locals := map[string]SymbolLocal{}
	inputBySymbol := map[string]PITUniverseInput{}
	for _, in := range inputs {
		inputBySymbol[in.Symbol] = in
	}
	for _, sym := range visible {
		in := inputBySymbol[sym]
		loc, ok := ComputeSymbolLocal(sym, in.Bars, market.BTCBars, profile, decisionTime, capitalUSDT)
		if !ok {
			continue
		}
		locals[sym] = loc
	}
	rankBySymbol := ApplyCrossSectionRanks(locals)
	var out []DecisionContext
	for _, sym := range visible {
		loc, ok := locals[sym]
		if !ok {
			continue
		}
		row, err := BuildDecisionContext(schema, profile.ID, sym, decisionTime, partition, btc, loc, rankBySymbol[sym])
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// DecisionTimesFromBTC returns closed-bar decision timestamps in [start,end).
func DecisionTimesFromBTC(bars []profitmap.Bar, profile Profile, start, end time.Time) []time.Time {
	var out []time.Time
	for i := range bars {
		decision := profitmap.DecisionTime(bars, i, profile.BarDuration)
		if decision.Before(start) || !decision.Before(end) {
			continue
		}
		if profitmap.RejectCurrentBarOpen(bars, i, profile.BarDuration) {
			continue
		}
		out = append(out, decision)
	}
	return out
}

// DefaultCapitalUSDT is the reference capital for cost/capacity soft scores.
const DefaultCapitalUSDT = 1000.0
