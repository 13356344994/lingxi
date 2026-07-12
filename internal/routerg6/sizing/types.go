// Package sizing implements G6 event stress-budget notional sizing with downward-only
// min-notional/lot floors. It clips by per-event stress (≤0.5% equity), account stress
// (≤2% equity), and effective leverage (≤5×).
package sizing

// Input holds one event sizing request at decision time.
type Input struct {
	EquityUSDT               float64
	ConservativeEdgeUSDT     float64
	StressLossUSDT           float64
	ReferenceNotionalUSDT    float64
	MarginTimeSec            int64
	MinNotionalUSDT          float64
	LotNotionalUSDT          float64
	UsedAccountStressUSDT    float64
	UsedNotionalUSDT         float64
	MaxEventStressFraction   float64
	MaxAccountStressFraction float64
	MaxLeverage              float64
}

// Result is the sized notional outcome for one event.
type Result struct {
	NotionalUSDT      float64
	StressBudgetUSDT  float64
	Skipped           bool
	SkipReason        string
	RankingScore      float64
}
