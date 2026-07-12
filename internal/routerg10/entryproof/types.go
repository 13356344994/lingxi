package entryproof

import "time"

// SourcePaths points at optional local execution evidence streams.
type SourcePaths struct {
	Triggers       string `json:"triggers"`
	Settlements    string `json:"settlements"`
	MakerAttempts  string `json:"maker_attempts"`
	ExecutableShadow string `json:"executable_shadow"`
	InstrumentMeta string `json:"instrument_meta"`
}

// RunConfig binds CLI knobs for bounded entry-proof generation.
type RunConfig struct {
	SchemaVersion string      `json:"schema_version"`
	Generation    string      `json:"generation"`
	RecordRoot    string      `json:"record_root"`
	OutputDir     string      `json:"output_dir"`
	Sources       SourcePaths `json:"sources"`
	MaxLines      int         `json:"max_lines"`
	LowCPU        bool        `json:"low_cpu"`
}

// TriggerRecord is one decision-time trigger with pre-outcome visible info.
type TriggerRecord struct {
	TriggerID           string    `json:"trigger_id"`
	DecisionTime        time.Time `json:"decision_time"`
	Symbol              string    `json:"symbol"`
	Venue               string    `json:"venue"`
	Side                string    `json:"side"`
	Price               float64   `json:"price"`
	OrderType           string    `json:"order_type"`
	DecisionVisibleInfo string    `json:"decision_visible_info"`

	MinSz string `json:"min_sz"`
	LotSz string `json:"lot_sz"`
	CtVal string `json:"ct_val"`
	CtType string `json:"ct_type"`

	ExecutableDepthUSDT float64 `json:"executable_depth_usdt"`
	ParticipationRate   float64 `json:"participation_rate"`
	LatencyMS           float64 `json:"latency_ms"`
	PartialFill         bool    `json:"partial_fill"`
	QueueAheadUSDT      float64 `json:"queue_ahead_usdt"`
	MaxWaitMS           int64   `json:"max_wait_ms"`
	CancelRule          string  `json:"cancel_rule"`
	NoChaseRule         string  `json:"no_chase_rule"`

	EvidenceGrade string `json:"evidence_grade,omitempty"`

	ArtifactBinding ArtifactBinding `json:"artifact_binding,omitempty"`
	ShadowSourceID  string          `json:"shadow_source_id,omitempty"`

	RealFill      bool `json:"real_fill,omitempty"`
	MakerTouched  bool `json:"maker_touched,omitempty"`
	MakerFilled   bool `json:"maker_filled,omitempty"`
	HasSettlement bool `json:"has_settlement,omitempty"`
	HasShadow     bool `json:"has_shadow,omitempty"`
}

// EntryProof freezes one candidate's entry eligibility at decision time.
type EntryProof struct {
	SchemaVersion string    `json:"schema_version"`
	TriggerID     string    `json:"trigger_id"`
	DecisionTime  time.Time `json:"decision_time"`
	Venue         string    `json:"venue"`

	DecisionVisibleInfo string  `json:"decision_visible_info"`
	OrderType           string  `json:"order_type"`
	Price               float64 `json:"price"`
	MinSz               string  `json:"min_sz"`
	LotSz               string  `json:"lot_sz"`
	CtVal               string  `json:"ct_val"`
	CtType              string  `json:"ct_type"`
	DynamicMinNotional  float64 `json:"dynamic_min_notional"`

	LatencyMS           float64 `json:"latency"`
	ExecutableDepth     float64 `json:"executable_depth"`
	ParticipationRate   float64 `json:"participation_rate"`
	PartialFill         bool    `json:"partial_fill"`
	QueueEstimate       float64 `json:"queue_estimate"`
	MaxWaitMS           int64   `json:"max_wait"`
	CancelRule          string  `json:"cancel_rule"`
	NoChaseRule         string  `json:"no_chase_rule"`
	EvidenceGrade       string  `json:"evidence_grade"`

	EvidenceArtifactHash string `json:"evidence_artifact_hash,omitempty"`
	EvidenceArtifactKind string `json:"evidence_artifact_kind,omitempty"`
	ShadowSourceID       string `json:"shadow_source_id,omitempty"`

	EntryStatus     string `json:"entry_status,omitempty"`
	EntryProofHash  string `json:"entry_proof_hash"`
	CertificationOK bool   `json:"certification_ok"`
	BlockReason     string `json:"block_reason,omitempty"`
}

// Manifest seals entry-proof run outputs.
type Manifest struct {
	SchemaVersion   string            `json:"schema_version"`
	Generation      string            `json:"generation"`
	ConfigHash      string            `json:"config_hash"`
	ProofCount      int               `json:"proof_count"`
	CertifiedCount  int               `json:"certified_count"`
	DiscoveryCount  int               `json:"discovery_count"`
	BlockedCount    int               `json:"blocked_count"`
	ProofsHash      string            `json:"proofs_hash"`
	SourceStatus    map[string]string `json:"source_status"`
	CreatedAt       time.Time         `json:"created_at"`
	ManifestHash    string            `json:"manifest_hash"`
}

// RunResult holds sealed entry-proof outputs.
type RunResult struct {
	Proofs   []EntryProof
	Manifest Manifest
	OutputPath string
}
