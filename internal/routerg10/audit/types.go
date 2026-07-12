package audit

import (
	"time"

	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/episodic"
)

// BrainTraceEntry is one append-only decision trace row.
type BrainTraceEntry struct {
	SchemaVersion       string                          `json:"schema_version"`
	Generation          string                          `json:"generation"`
	ProtocolHash        string                          `json:"protocol_hash"`
	BarIndex            int                             `json:"bar_index"`
	DecisionTime        time.Time                       `json:"decision_time"`
	StateHash           string                          `json:"state_hash"`
	Symbol              string                          `json:"symbol"`
	EventFamily         string                          `json:"event_family,omitempty"`
	EventPhase          float64                         `json:"event_phase,omitempty"`
	ThesisHealth        string                          `json:"thesis_health,omitempty"`
	FingerprintVersion  string                          `json:"fingerprint_version"`
	MemoryHits          []episodic.TraceMemoryHit         `json:"memory_hits"`
	CounterfactualTable counterfactual.CounterfactualTable `json:"counterfactual_table"`
	RejectReasons       []string                        `json:"reject_reasons,omitempty"`
	FinalAction         string                          `json:"final_action"`
	PositionSize        float64                         `json:"position_size"`
	Deadline            int                             `json:"deadline"`
	StopErrorState      string                          `json:"stop_error_state"`
	SolvencyState       string                          `json:"solvency_state"`
	TraceHash           string                          `json:"trace_hash"`
}

// TraceLog is an append-only in-memory brain trace ledger.
type TraceLog struct {
	Entries []BrainTraceEntry `json:"entries"`
	Seq     int               `json:"seq"`
}

// DegeneracyReport summarizes brain action distribution health.
type DegeneracyReport struct {
	SchemaVersion        string             `json:"schema_version"`
	Generation           string             `json:"generation"`
	ProtocolHash         string             `json:"protocol_hash"`
	TotalBars            int                `json:"total_bars"`
	ActionCounts         map[string]int     `json:"action_counts"`
	DominantAction       string             `json:"dominant_action,omitempty"`
	DominantFraction     float64            `json:"dominant_fraction"`
	DirectionalFraction  float64            `json:"directional_fraction"`
	BarsSinceLastExit    int                `json:"bars_since_last_exit"`
	Degenerate           bool               `json:"degenerate"`
	DegeneracyReasons    []string           `json:"degeneracy_reasons,omitempty"`
	Status               string             `json:"status"`
	PathMatrixCovered    map[string]bool    `json:"path_matrix_covered"`
	PathMatrixMissing    []string           `json:"path_matrix_missing,omitempty"`
	ReportHash           string             `json:"report_hash"`
}

// ReplayResult compares two trace replays for hash equality.
type ReplayResult struct {
	SchemaVersion   string `json:"schema_version"`
	Match           bool   `json:"match"`
	EntryCount      int    `json:"entry_count"`
	FirstMismatchAt int    `json:"first_mismatch_at,omitempty"`
	ExpectedHash    string `json:"expected_hash,omitempty"`
	ActualHash      string `json:"actual_hash,omitempty"`
}
