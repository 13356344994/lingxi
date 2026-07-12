package profitmap

import (
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg4/protocol"
)

const (
	ProfileOKX1H = "okx_1h"
	ProfileOKX5M = "okx_5m"

	PartitionDiscovery  = "discovery"
	PartitionValidation = "validation"
	PartitionWalkFwd    = "walk_forward"

	SymbolClassMajor    = "major"
	SymbolClassMid      = "mid"
	SymbolClassLongTail = "long_tail"

	MaxWinnersPerCell = protocol.MapWinnerReservoirPerCell
)

// Direction aliases for map keys.
const (
	DirectionLong  = mechanism.DirectionLong
	DirectionShort = mechanism.DirectionShort
)

// ProfileSpec binds one data profile to bar timing and outcome horizons.
type ProfileSpec struct {
	ID                    string
	PrimaryFamily         string
	BarDuration           time.Duration
	MaxFeatureLookback    time.Duration
	MaxOutcomeHorizon     time.Duration
	Horizons              []HorizonSpec
	RollingQuoteVolumeWin time.Duration
}

// HorizonSpec is one holding horizon in bar counts.
type HorizonSpec struct {
	Label    string
	BarCount int
	Duration time.Duration
}

// Boundaries are chronologically ordered partition edges for one profile.
type Boundaries struct {
	ProfileID       string        `json:"profile_id"`
	Start           time.Time     `json:"start"`
	DiscoveryEnd    time.Time     `json:"discovery_end"`
	ValidationStart time.Time     `json:"validation_start"`
	ValidationEnd   time.Time     `json:"validation_end"`
	WFStart         time.Time     `json:"wf_start"`
	End             time.Time     `json:"end"`
	PurgeEmbargo    time.Duration `json:"purge_embargo"`
}

// CellKey identifies one profit-map aggregation bucket.
type CellKey struct {
	SkyCoarse   string `json:"sky_coarse_market"`
	Liquidity   string `json:"liquidity_bucket"`
	SymbolClass string `json:"symbol_class"`
	Horizon     string `json:"horizon"`
	Direction   string `json:"direction"`
	CapitalTier int    `json:"capital_tier"`
}

// Opportunity is one evaluated decision point (not persisted in bulk).
type Opportunity struct {
	Symbol          string
	Partition       string
	DecisionTime    time.Time
	SkyCoarse       string
	LiquidityBucket string
	SymbolClass     string
	Horizon         string
	Direction       mechanism.Direction
	CapitalTier     int
	GrossBPS        float64
	FeeNetBPS       float64
	ConservativeBPS float64
	CostWipeout     bool
	CapacityFail    bool
}
