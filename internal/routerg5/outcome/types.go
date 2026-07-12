package outcome

import (
	"time"

	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const (
	PartitionDiscovery  = "discovery"
	PartitionValidation = "validation"
	PartitionWalkFwd    = "walk_forward"

	ScanCheckpointEverySymbols = 25
	QuantileReservoirPerCell   = 32
	WinnerReservoirPerCell     = 32

	FullMarketScope     = "FULL_MARKET"
	TruncatedSmokeScope = "TRUNCATED_SMOKE"
)

// DiscoveryTrainingLabelReservoirCap returns the frozen profile-level label cap.
func DiscoveryTrainingLabelReservoirCap() int {
	return g5protocol.DiscoveryTrainingLabelReservoir
}

// RewardRecord is one evaluated oracle label. It must never enter feature inputs.
type RewardRecord struct {
	Symbol                     string
	Partition                  string
	DecisionTime               time.Time
	StartState                 actionspace.PositionState
	Action                     actionspace.Action
	SoftRegime                 string
	StateLabel                 string
	GrossEquityDeltaBPS        float64
	ConservativeEquityDeltaBPS float64
	StressEquityDeltaBPS       float64
	NetEquityDeltaBPS          float64
	MarginUSDT                 float64
	TurnoverUSDT               float64
	CapacityFail               bool
	Label                      string
}

// CellKey identifies one reward-cube aggregation bucket.
type CellKey struct {
	StateLabel   string  `json:"state_label"`
	Verb         string  `json:"verb"`
	Direction    string  `json:"direction"`
	Horizon      string  `json:"horizon"`
	SizeFraction float64 `json:"size_fraction"`
	DelayBars    int     `json:"delay_bars"`
	SoftRegime   string  `json:"soft_regime"`
	Liquidity    string  `json:"liquidity_bucket"`
	SymbolClass  string  `json:"symbol_class"`
}

// ScanSymbolConfig bounds one symbol oracle stream.
type ScanSymbolConfig struct {
	Spec             profitmap.ProfileSpec
	Boundaries       profitmap.Boundaries
	SkyCutpoints     sky.Cutpoints
	SkySeries        sky.Series
	SymbolClass      profitmap.SymbolClassCutpoints
	SymbolClassLabel string
	Funding          []profitmap.FundingPoint
	ProgressEvery    int
	OnProgress       func(lines int)
}
