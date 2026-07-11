package datatruth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type QualityMark string

const (
	QualityOK                 QualityMark = "ok"
	QualityClockInvalid       QualityMark = "clock_invalid"
	QualityLate               QualityMark = "late"
	QualityDuplicate          QualityMark = "duplicate"
	QualityOutOfOrder         QualityMark = "out_of_order"
	QualityGap                QualityMark = "gap"
	QualityCorrection         QualityMark = "correction"
	QualityPayloadHashMissing QualityMark = "payload_hash_missing"
)

type EventEnvelope struct {
	EventID     string          `json:"event_id"`
	Source      string          `json:"source"`
	Stream      string          `json:"stream"`
	Instrument  string          `json:"instrument,omitempty"`
	Sequence    int64           `json:"sequence"`
	EventTime   time.Time       `json:"event_time"`
	ReceiveTime time.Time       `json:"receive_time"`
	ProcessTime time.Time       `json:"process_time"`
	PayloadHash string          `json:"payload_hash"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	RevisionOf  string          `json:"revision_of,omitempty"`
	DecisionID  string          `json:"decision_id,omitempty"`
	Quality     []QualityMark   `json:"quality"`
}

type StreamState struct {
	LastSequence  int64             `json:"last_sequence"`
	SeenSequences map[int64]string  `json:"seen_sequences"`
	SeenHashes    map[string]string `json:"seen_hashes"`
}

type ObserveOptions struct {
	LateAfter time.Duration
}

type EventAssessment struct {
	Event        EventEnvelope `json:"event"`
	Accepted     bool          `json:"accepted"`
	Duplicate    bool          `json:"duplicate"`
	OutOfOrder   bool          `json:"out_of_order"`
	Gap          bool          `json:"gap"`
	GapFrom      int64         `json:"gap_from,omitempty"`
	GapTo        int64         `json:"gap_to,omitempty"`
	ClockInvalid bool          `json:"clock_invalid"`
	Correction   bool          `json:"correction"`
	MissingHash  bool          `json:"missing_hash"`
	Quality      []QualityMark `json:"quality"`
}

func NewEnvelope(source, stream, instrument string, sequence int64, eventTime, receiveTime, processTime time.Time, payload any) (EventEnvelope, error) {
	hash, raw, err := HashJSON(payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	env := EventEnvelope{
		Source:      strings.TrimSpace(source),
		Stream:      strings.TrimSpace(stream),
		Instrument:  strings.TrimSpace(instrument),
		Sequence:    sequence,
		EventTime:   eventTime.UTC(),
		ReceiveTime: receiveTime.UTC(),
		ProcessTime: processTime.UTC(),
		PayloadHash: hash,
		Payload:     raw,
	}
	env.EventID = EventID(env)
	return env, nil
}

func EventID(env EventEnvelope) string {
	parts := []string{env.Source, env.Stream, env.Instrument, fmt.Sprint(env.Sequence), env.PayloadHash}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func Observe(prev StreamState, env EventEnvelope, opts ObserveOptions) (StreamState, EventAssessment) {
	next := cloneState(prev)
	if next.SeenSequences == nil {
		next.SeenSequences = map[int64]string{}
	}
	if next.SeenHashes == nil {
		next.SeenHashes = map[string]string{}
	}

	assessment := EventAssessment{Event: env, Accepted: true}
	add := func(mark QualityMark) {
		assessment.Quality = append(assessment.Quality, mark)
	}

	if env.EventTime.IsZero() || env.ReceiveTime.IsZero() || env.ProcessTime.IsZero() || env.ProcessTime.Before(env.ReceiveTime) {
		assessment.ClockInvalid = true
		add(QualityClockInvalid)
	}
	if opts.LateAfter > 0 && !env.EventTime.IsZero() && env.ReceiveTime.Sub(env.EventTime) > opts.LateAfter {
		add(QualityLate)
	}
	if strings.TrimSpace(env.PayloadHash) == "" {
		assessment.MissingHash = true
		add(QualityPayloadHashMissing)
	}
	if strings.TrimSpace(env.RevisionOf) != "" {
		assessment.Correction = true
		add(QualityCorrection)
	}

	if oldHash, ok := next.SeenSequences[env.Sequence]; ok && oldHash == env.PayloadHash {
		assessment.Duplicate = true
		assessment.Accepted = false
		add(QualityDuplicate)
	}
	if oldID, ok := next.SeenHashes[env.PayloadHash]; ok && oldID != "" {
		assessment.Duplicate = true
		assessment.Accepted = false
		add(QualityDuplicate)
	}
	if env.Sequence < prev.LastSequence {
		assessment.OutOfOrder = true
		add(QualityOutOfOrder)
	}
	if env.Sequence > prev.LastSequence+1 && prev.LastSequence != 0 {
		assessment.Gap = true
		assessment.GapFrom = prev.LastSequence + 1
		assessment.GapTo = env.Sequence - 1
		add(QualityGap)
	}
	if len(assessment.Quality) == 0 {
		add(QualityOK)
	}
	env.Quality = append([]QualityMark(nil), assessment.Quality...)
	assessment.Event = env

	if assessment.Accepted {
		next.SeenSequences[env.Sequence] = env.PayloadHash
		if env.PayloadHash != "" {
			next.SeenHashes[env.PayloadHash] = env.EventID
		}
		if env.Sequence > next.LastSequence {
			next.LastSequence = env.Sequence
		}
	}
	return next, assessment
}

func HashJSON(v any) (string, json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", nil, err
	}
	canonical, err := json.Marshal(normalizeJSON(decoded))
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), canonical, nil
}

func HashStrings(parts ...string) string {
	clean := append([]string(nil), parts...)
	sort.Strings(clean)
	sum := sha256.Sum256([]byte(strings.Join(clean, "\x00")))
	return hex.EncodeToString(sum[:])
}

func ValidateDecisionID(decisionID string) error {
	if strings.TrimSpace(decisionID) == "" {
		return errors.New("decision_id is required")
	}
	for _, r := range decisionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ':' || r == '.' {
			continue
		}
		return fmt.Errorf("decision_id contains invalid character %q", r)
	}
	return nil
}

func cloneState(st StreamState) StreamState {
	out := StreamState{LastSequence: st.LastSequence}
	if st.SeenSequences != nil {
		out.SeenSequences = make(map[int64]string, len(st.SeenSequences))
		for k, v := range st.SeenSequences {
			out.SeenSequences[k] = v
		}
	}
	if st.SeenHashes != nil {
		out.SeenHashes = make(map[string]string, len(st.SeenHashes))
		for k, v := range st.SeenHashes {
			out.SeenHashes[k] = v
		}
	}
	return out
}

func normalizeJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = normalizeJSON(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalizeJSON(x[i])
		}
		return out
	default:
		return x
	}
}
