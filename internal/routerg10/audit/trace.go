package audit

import (
	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/episodic"
)

// NewTraceLog creates an empty append-only trace log.
func NewTraceLog() *TraceLog {
	return &TraceLog{Entries: []BrainTraceEntry{}}
}

// Append seals one trace entry with state_hash and trace_hash.
func (l *TraceLog) Append(entry BrainTraceEntry) (BrainTraceEntry, error) {
	if entry.SchemaVersion == "" {
		entry.SchemaVersion = SchemaVersion
	}
	stateHash, err := HashState(entry)
	if err != nil {
		return BrainTraceEntry{}, err
	}
	entry.StateHash = stateHash
	traceHash, err := HashTrace(entry)
	if err != nil {
		return BrainTraceEntry{}, err
	}
	entry.TraceHash = traceHash
	l.Entries = append(l.Entries, entry)
	l.Seq++
	return entry, nil
}

// HashState hashes decision-time state without trace_hash.
func HashState(entry BrainTraceEntry) (string, error) {
	payload := struct {
		BarIndex           int                     `json:"bar_index"`
		DecisionTime       interface{}             `json:"decision_time"`
		Symbol             string                  `json:"symbol"`
		EventFamily        string                  `json:"event_family"`
		EventPhase         float64                 `json:"event_phase"`
		ThesisHealth       string                  `json:"thesis_health"`
		FingerprintVersion string                  `json:"fingerprint_version"`
		MemoryHits         []episodic.TraceMemoryHit `json:"memory_hits"`
		CounterfactualHash string                  `json:"counterfactual_table_hash"`
		RejectReasons      []string                `json:"reject_reasons"`
		FinalAction        string                  `json:"final_action"`
		PositionSize       float64                 `json:"position_size"`
		Deadline           int                     `json:"deadline"`
		StopErrorState     string                  `json:"stop_error_state"`
		SolvencyState      string                  `json:"solvency_state"`
	}{
		BarIndex:           entry.BarIndex,
		DecisionTime:       entry.DecisionTime.UTC(),
		Symbol:             entry.Symbol,
		EventFamily:        entry.EventFamily,
		EventPhase:         entry.EventPhase,
		ThesisHealth:       entry.ThesisHealth,
		FingerprintVersion: entry.FingerprintVersion,
		MemoryHits:         entry.MemoryHits,
		CounterfactualHash: entry.CounterfactualTable.TableHash,
		RejectReasons:      entry.RejectReasons,
		FinalAction:        entry.FinalAction,
		PositionSize:       entry.PositionSize,
		Deadline:           entry.Deadline,
		StopErrorState:     entry.StopErrorState,
		SolvencyState:      entry.SolvencyState,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashTrace returns canonical trace hash with trace_hash cleared.
func HashTrace(entry BrainTraceEntry) (string, error) {
	payload := entry
	payload.TraceHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashLog returns canonical hash for full append-only trace log.
func HashLog(log TraceLog) (string, error) {
	hash, _, err := datatruth.HashJSON(log.Entries)
	return hash, err
}

// ValidateEntry checks required fields are present.
func ValidateEntry(entry BrainTraceEntry) error {
	if entry.StateHash == "" || entry.TraceHash == "" {
		return errMissingHash("state_hash/trace_hash")
	}
	if entry.FingerprintVersion == "" {
		return errMissingHash("fingerprint_version")
	}
	if entry.FinalAction == "" {
		return errMissingHash("final_action")
	}
	if entry.StopErrorState == "" || entry.SolvencyState == "" {
		return errMissingHash("stop_error_state/solvency_state")
	}
	if entry.CounterfactualTable.TableHash == "" {
		return errMissingHash("counterfactual_table")
	}
	return nil
}

type missingFieldError string

func (e missingFieldError) Error() string { return "audit: missing " + string(e) }

func errMissingHash(field string) error { return missingFieldError(field) }
