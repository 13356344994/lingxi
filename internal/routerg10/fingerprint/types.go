package fingerprint

import "time"

// SourcePaths points at optional local private execution logs. Empty path means
// the stream is skipped and marked UNKNOWN in the fingerprint envelope.
type SourcePaths struct {
	Settlements      string `json:"settlements"`
	MakerAttempts    string `json:"maker_attempts"`
	OpenDecisions    string `json:"open_decisions"`
	ExecutableShadow string `json:"executable_shadow"`
	EquitySnapshots  string `json:"equity_snapshots"`
	InstrumentMeta   string `json:"instrument_meta"`
}

// RunConfig binds CLI knobs for bounded streaming aggregation.
type RunConfig struct {
	SchemaVersion string      `json:"schema_version"`
	Generation    string      `json:"generation"`
	RecordRoot    string      `json:"record_root"`
	OutputDir     string      `json:"output_dir"`
	Sources       SourcePaths `json:"sources"`
	MaxLines      int         `json:"max_lines"`
	LowCPU        bool        `json:"low_cpu"`
}

// SourceStatus reports per-input availability without leaking file contents.
type SourceStatus struct {
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"`
	Lines    int    `json:"lines,omitempty"`
	Decoded  int    `json:"decoded,omitempty"`
	Skipped  int    `json:"skipped,omitempty"`
	Malformed int   `json:"malformed,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
}

const (
	StatusAvailable = "AVAILABLE"
	StatusUnknown   = "UNKNOWN"
	StatusTruncated = "TRUNCATED"
)

// QuantileMetric is a bounded aggregate with explicit UNKNOWN when empty.
type QuantileMetric struct {
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	N      int     `json:"n"`
	Status string  `json:"status"`
}

// RateMetric is a bounded ratio aggregate.
type RateMetric struct {
	Rate   float64 `json:"rate"`
	N      int     `json:"n"`
	Status string  `json:"status"`
}

// InstrumentConstraints carries tick/lot/ctVal/ctType and dynamic min-notional for a symbol.
// Static min_notional is retained for audit only; eligibility uses DynamicMinNotionalUSDT.
type InstrumentConstraints struct {
	TickSize               string  `json:"tick_size,omitempty"`
	LotSize                string  `json:"lot_size,omitempty"`
	MinSize                string  `json:"min_sz,omitempty"`
	ContractValue          string  `json:"contract_value,omitempty"`
	ContractType           string  `json:"ct_type,omitempty"`
	MinNotional            string  `json:"min_notional,omitempty"`
	DynamicMinNotionalUSDT float64 `json:"dynamic_min_notional_usdt,omitempty"`
	Status                 string  `json:"status"`
}

// CellKey is the aggregation partition.
type CellKey struct {
	Venue          string `json:"venue"`
	Symbol         string `json:"symbol"`
	HourUTC        string `json:"hour_utc"`
	Direction      string `json:"direction"`
	OrderType      string `json:"order_type"`
	NotionalBucket string `json:"notional_bucket"`
}

// ExecutionCell is one venue/symbol/hour/direction/order-type/notional bucket.
type ExecutionCell struct {
	CellKey
	FeeBPS         QuantileMetric        `json:"fee_bps"`
	EntrySlipBPS   QuantileMetric        `json:"entry_slip_bps"`
	ExitSlipBPS    QuantileMetric        `json:"exit_slip_bps"`
	MakerFillRate  RateMetric            `json:"maker_fill_rate"`
	MakerWaitMS    QuantileMetric        `json:"maker_wait_ms"`
	LatencyMS      QuantileMetric        `json:"latency_ms"`
	CancelRiskRate RateMetric            `json:"cancel_risk_rate"`
	FundingRate    QuantileMetric        `json:"funding_rate"`
	Instrument     InstrumentConstraints `json:"instrument"`
	SampleCount    int                   `json:"sample_count"`
}

// EquitySummary is aggregate cash context only (no per-snapshot rows).
type EquitySummary struct {
	MedianCashUSDT float64 `json:"median_cash_usdt"`
	P90CashUSDT    float64 `json:"p90_cash_usdt"`
	Venues         int     `json:"venues"`
	Status         string  `json:"status"`
}

// PersonalExecutionFingerprint is the redacted local execution portrait.
type PersonalExecutionFingerprint struct {
	SchemaVersion string                  `json:"schema_version"`
	Generation    string                  `json:"generation"`
	FingerprintHash string                `json:"fingerprint_hash"`
	BuiltAt       time.Time               `json:"built_at"`
	ConfigHash    string                  `json:"config_hash"`
	SourceStatus  map[string]SourceStatus `json:"source_status"`
	Equity        EquitySummary           `json:"equity"`
	Cells         []ExecutionCell         `json:"cells"`
}
