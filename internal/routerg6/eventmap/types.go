package eventmap

import (
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/protocol"
)

const (
	ProfileOKX1H = profitmap.ProfileOKX1H
	ProfileOKX5M = profitmap.ProfileOKX5M

	PartitionDiscovery  = profitmap.PartitionDiscovery
	PartitionValidation = profitmap.PartitionValidation
	PartitionWalkFwd    = profitmap.PartitionWalkFwd

	FamilyExtremeContinuation = "ohlcv_extreme_continuation"
	FamilyExtremeReversal     = "ohlcv_extreme_reversal"
	FamilySessionLiquidity    = "session_liquidity_recovery"
	FamilyBTCRelative         = "btc_relative_dislocation"

	DirectionLong  = mechanism.DirectionLong
	DirectionShort = mechanism.DirectionShort

	MaxWinnersPerCell = 32
)

// ProfileSpec aliases the frozen G4 profile timing used for events.
type ProfileSpec = profitmap.ProfileSpec

// Boundaries aliases G4 chronological partitions.
type Boundaries = profitmap.Boundaries

// Bar aliases G4 confirmed OHLCV bars.
type Bar = profitmap.Bar

// FrozenThresholds holds discovery-fit quantile cutpoints; never uses outcomes.
type FrozenThresholds struct {
	ProfileID              string    `json:"profile_id"`
	FrozenAt               time.Time `json:"frozen_at"`
	ExtremeReturnAbsBPS    float64   `json:"extreme_return_abs_bps"`
	ExtremeRMSBPS          float64   `json:"extreme_rms_bps"`
	LowLiquidityQuote      float64   `json:"low_liquidity_quote"`
	VolumeSpikeQuote       float64   `json:"volume_spike_quote"`
	BTCRelativeAbsBPS      float64   `json:"btc_relative_abs_bps"`
	BTCShockReturnAbsBPS   float64   `json:"btc_shock_return_abs_bps"`
	DiscoverySampleCount   int       `json:"discovery_sample_count"`
}

// DetectedEvent is one feature-only trigger before outcome expansion.
type DetectedEvent struct {
	ID              string
	Symbol          string
	Family          string
	Partition       string
	DecisionTime    time.Time
	DecisionBarIdx  int
	ShockCluster    string
	SessionBucket   string
	LiquidityBucket string
	ReturnBPS       float64
	PriorReturnBPS  float64
	BTCRelativeBPS  float64
	OptionalMissing []string
}

// EventOutcome is one evaluated long/short × horizon × delay × capital outcome.
type EventOutcome struct {
	EventID          string
	Symbol           string
	EventFamily      string
	Partition        string
	DecisionTime     time.Time
	ShockCluster     string
	SessionBucket    string
	LiquidityBucket  string
	Horizon          string
	Direction        string
	DelayBars        int
	CapitalTier      int
	GrossBPS         float64
	FeeNetBPS        float64
	ConservativeBPS  float64
	CostWipeout      bool
	CapacityFail     bool
	OptionalMissing  []string
}

// CellKey identifies one event-map aggregation bucket.
type CellKey struct {
	EventFamily string `json:"event_family"`
	Horizon     string `json:"horizon"`
	Direction   string `json:"direction"`
	DelayBars   int    `json:"delay_bars"`
	CapitalTier int    `json:"capital_tier"`
}

// WinnerEntry is one bounded discovery winner kept for reverse mining.
type WinnerEntry struct {
	EventID         string    `json:"event_id"`
	Symbol          string    `json:"symbol"`
	DecisionTime    time.Time `json:"decision_time"`
	EventFamily     string    `json:"event_family"`
	Horizon         string    `json:"horizon"`
	Direction       string    `json:"direction"`
	DelayBars       int       `json:"delay_bars"`
	CapitalTier     int       `json:"capital_tier"`
	ConservativeBPS float64   `json:"conservative_net_bps"`
	ShockCluster    string    `json:"shock_cluster"`
	MapManifestHash string    `json:"map_manifest_hash,omitempty"`
}

// RunConfig is the bounded scan configuration bound into checkpoints.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	ProfileID           string `json:"profile_id"`
	HistoryRoot         string `json:"history_root"`
	OutputDir           string `json:"output_dir"`
	MaxFiles            int    `json:"max_files"`
	MaxLines            int    `json:"max_lines"`
	CatalogSymbolLimit  int    `json:"catalog_symbol_limit"`
	ProgressEvery       int    `json:"progress_every"`
	LowCPU              bool   `json:"low_cpu"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
	AllowTruncatedSmoke bool   `json:"allow_truncated_smoke"`
}

func (c RunConfig) Validate() error {
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errf("max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errf("progress_every must be positive")
	}
	return nil
}

// ScanSymbolConfig bounds one symbol event scan.
type ScanSymbolConfig struct {
	Spec            ProfileSpec
	Boundaries      Boundaries
	Thresholds      FrozenThresholds
	BTCBars         []Bar
	ShockClusterAt  func(decision time.Time) string
	Funding         []profitmap.FundingPoint
	Optional        map[string]float64
	OptionalMissing []string
	MaxLines        int
	ProgressEvery   int
	OnProgress      func(lines int)
	WindowBars      int
}

// PrimaryEventFamilies returns frozen G6 OHLCV event families in protocol order.
func PrimaryEventFamilies() []string {
	return append([]string(nil), protocol.PrimaryOHLCVEventFamilies...)
}
