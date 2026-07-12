package personalbrain

import (
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/executionpolicy"
)

// BrainTraceEntry is one append-only G10P brain trace row (v2 schema).
type BrainTraceEntry struct {
	SchemaVersion       string                 `json:"schema_version"`
	Generation          string                 `json:"generation"`
	ProtocolHash        string                 `json:"protocol_hash"`
	BarIndex            int                    `json:"bar_index"`
	DecisionTime        time.Time              `json:"decision_time"`
	StateHash           string                 `json:"state_hash"`
	Symbol              string                 `json:"symbol"`
	EventFamily         string                 `json:"event_family,omitempty"`
	EventPhase          float64                `json:"event_phase,omitempty"`
	ThesisHealth        string                 `json:"thesis_health,omitempty"`
	ConstitutionHash    string                 `json:"constitution_hash"`
	ExclusionProofHash  string                 `json:"exclusion_proof_hash,omitempty"`
	AnnouncementVisibility AnnouncementVisibility `json:"announcement_visibility"`
	EntryProofHash      string                 `json:"entry_proof_hash,omitempty"`
	ExecutionPolicyHash string                 `json:"execution_policy_hash,omitempty"`
	MakerSelectionBias  string                 `json:"maker_selection_bias,omitempty"`
	EnterGate           string                 `json:"enter_gate"`
	ExecutionActions    []executionpolicy.ActionEvaluation `json:"execution_actions_compared,omitempty"`
	FillOrMissReason    string                 `json:"fill_or_miss_reason,omitempty"`
	ToxicFillReason     string                 `json:"toxic_fill_reason,omitempty"`
	ExitProofHash       string                 `json:"exit_proof_hash,omitempty"`
	CrowdingStress      string                 `json:"crowding_stress,omitempty"`
	TerminalRiskUsage   TerminalRiskUsage      `json:"terminal_risk_usage"`
	RejectReasons       []string               `json:"reject_reasons,omitempty"`
	FinalAction         string                 `json:"final_action"`
	PositionSize        float64                `json:"position_size"`
	Deadline            int                    `json:"deadline"`
	StopErrorState      string                 `json:"stop_error_state"`
	SolvencyState       string                 `json:"solvency_state"`
	TradeAllowed        bool                   `json:"trade_allowed"`
	TraceHash           string                 `json:"trace_hash"`
}

// TraceLog is an append-only in-memory G10P brain trace ledger.
type TraceLog struct {
	Entries []BrainTraceEntry `json:"entries"`
	Seq     int               `json:"seq"`
}

// NewTraceLog creates an empty append-only trace log.
func NewTraceLog() *TraceLog {
	return &TraceLog{Entries: []BrainTraceEntry{}}
}

// Append seals one trace entry with state_hash and trace_hash.
func (l *TraceLog) Append(entry BrainTraceEntry) (BrainTraceEntry, error) {
	if entry.SchemaVersion == "" {
		entry.SchemaVersion = TraceSchemaVersion
	}
	if entry.Generation == "" {
		entry.Generation = Generation
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
		SchemaVersion          string                               `json:"schema_version"`
		Generation             string                               `json:"generation"`
		ProtocolHash           string                               `json:"protocol_hash"`
		BarIndex               int                                  `json:"bar_index"`
		DecisionTime           interface{}                          `json:"decision_time"`
		Symbol                 string                               `json:"symbol"`
		EventFamily            string                               `json:"event_family"`
		EventPhase             float64                              `json:"event_phase"`
		ThesisHealth           string                               `json:"thesis_health"`
		ConstitutionHash       string                               `json:"constitution_hash"`
		ExclusionProofHash     string                               `json:"exclusion_proof_hash"`
		AnnouncementVisibility AnnouncementVisibility               `json:"announcement_visibility"`
		EntryProofHash         string                               `json:"entry_proof_hash"`
		ExecutionPolicyHash    string                               `json:"execution_policy_hash"`
		ExecutionActions       []executionpolicy.ActionEvaluation   `json:"execution_actions_compared"`
		MakerSelectionBias     string                               `json:"maker_selection_bias"`
		EnterGate              string                               `json:"enter_gate"`
		FillOrMissReason       string                               `json:"fill_or_miss_reason"`
		ToxicFillReason        string                               `json:"toxic_fill_reason"`
		ExitProofHash          string                               `json:"exit_proof_hash"`
		CrowdingStress         string                               `json:"crowding_stress"`
		TerminalRiskUsage      TerminalRiskUsage                    `json:"terminal_risk_usage"`
		RejectReasons          []string                             `json:"reject_reasons"`
		FinalAction            string                               `json:"final_action"`
		PositionSize           float64                              `json:"position_size"`
		Deadline               int                                  `json:"deadline"`
		StopErrorState         string                               `json:"stop_error_state"`
		SolvencyState          string                               `json:"solvency_state"`
		TradeAllowed           bool                                 `json:"trade_allowed"`
	}{
		SchemaVersion:          entry.SchemaVersion,
		Generation:             entry.Generation,
		ProtocolHash:           entry.ProtocolHash,
		BarIndex:               entry.BarIndex,
		DecisionTime:           entry.DecisionTime.UTC(),
		Symbol:                 entry.Symbol,
		EventFamily:            entry.EventFamily,
		EventPhase:             entry.EventPhase,
		ThesisHealth:           entry.ThesisHealth,
		ConstitutionHash:       entry.ConstitutionHash,
		ExclusionProofHash:     entry.ExclusionProofHash,
		AnnouncementVisibility: entry.AnnouncementVisibility,
		EntryProofHash:         entry.EntryProofHash,
		ExecutionPolicyHash:    entry.ExecutionPolicyHash,
		ExecutionActions:       entry.ExecutionActions,
		MakerSelectionBias:     entry.MakerSelectionBias,
		EnterGate:              entry.EnterGate,
		FillOrMissReason:       entry.FillOrMissReason,
		ToxicFillReason:        entry.ToxicFillReason,
		ExitProofHash:          entry.ExitProofHash,
		CrowdingStress:         entry.CrowdingStress,
		TerminalRiskUsage:      entry.TerminalRiskUsage,
		RejectReasons:          entry.RejectReasons,
		FinalAction:            entry.FinalAction,
		PositionSize:           entry.PositionSize,
		Deadline:               entry.Deadline,
		StopErrorState:         entry.StopErrorState,
		SolvencyState:          entry.SolvencyState,
		TradeAllowed:           entry.TradeAllowed,
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

// ValidateEntry checks v2 required fields are present.
func ValidateEntry(entry BrainTraceEntry) error {
	if entry.StateHash == "" || entry.TraceHash == "" {
		return errMissingField("state_hash/trace_hash")
	}
	if entry.ConstitutionHash == "" {
		return errMissingField("constitution_hash")
	}
	if entry.EnterGate == "" {
		return errMissingField("enter_gate")
	}
	if entry.FinalAction == "" {
		return errMissingField("final_action")
	}
	if entry.StopErrorState == "" || entry.SolvencyState == "" {
		return errMissingField("stop_error_state/solvency_state")
	}
	for _, field := range TraceRequiredFields {
		switch field {
		case "state_hash":
			if entry.StateHash == "" {
				return errMissingField(field)
			}
		case "constitution_hash":
			if entry.ConstitutionHash == "" {
				return errMissingField(field)
			}
		case "exclusion_proof_hash":
			// optional when no event
		case "announcement_visibility":
			// always present as struct
		case "entry_proof_hash":
		case "execution_policy_hash":
		case "maker_selection_bias":
		case "fill_or_miss_reason":
		case "toxic_fill_reason":
		case "exit_proof_hash":
		case "crowding_stress":
		case "terminal_risk_usage":
			if !entry.TerminalRiskUsage.CeilingIsNotTarget {
				return errMissingField("terminal_risk_usage.ceiling_is_not_target")
			}
		case "final_action":
			if entry.FinalAction == "" {
				return errMissingField(field)
			}
		case "trace_hash":
			if entry.TraceHash == "" {
				return errMissingField(field)
			}
		}
	}
	return nil
}

type missingFieldError string

func (e missingFieldError) Error() string { return "personalbrain trace: missing " + string(e) }

func errMissingField(field string) error { return missingFieldError(field) }

// VerifyReplay compares two trace logs for deterministic hash equality.
func VerifyReplay(a, b TraceLog) ReplayResult {
	res := ReplayResult{
		SchemaVersion: SchemaVersionReplay,
		EntryCount:    len(a.Entries),
		Match:         true,
	}
	if len(a.Entries) != len(b.Entries) {
		res.Match = false
		return res
	}
	h1, _ := HashLog(a)
	h2, _ := HashLog(b)
	res.ExpectedHash = h1
	res.ActualHash = h2
	if h1 != h2 {
		res.Match = false
		for i := range a.Entries {
			if a.Entries[i].TraceHash != b.Entries[i].TraceHash {
				res.FirstMismatchAt = i
				break
			}
		}
	}
	return res
}
