// Package examboundary validates G5 public exam commitments and gauntlet consumption
// state without reading hidden manifests, time ranges, clusters, or fixture paths.
package examboundary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
	"lingxi/internal/routerg5/protocol"
)

var (
	ErrExamUnavailable     = errors.New("EXAM_UNAVAILABLE")
	ErrSchemaLeak          = errors.New("public commitment schema leak")
	ErrHiddenManifestRead  = errors.New("hidden manifest paths are forbidden")
	ErrInvalidCommitment   = errors.New("invalid public commitment")
	ErrScheduleDrift       = errors.New("commitment schedule drift")
	ErrDuplicateCommitment = errors.New("duplicate commitment")
)

// PublicCommitment is the researcher-visible exam ticket projection.
type PublicCommitment struct {
	ID            string `json:"id"`
	Queue         int    `json:"queue"`
	Kind          string `json:"kind"`
	Family        string `json:"family"`
	AlphaSpend    int    `json:"alpha_spend"`
	DataHash      string `json:"data_hash"`
	ChallengeHash string `json:"challenge_hash"`
	ProtocolHash  string `json:"protocol_hash"`
	Status        string `json:"status"`
}

// Document is the public commitment JSON envelope researchers may import.
type Document struct {
	SchemaVersion string               `json:"schema_version"`
	Generation    string               `json:"generation"`
	ProtocolHash  string               `json:"protocol_hash"`
	Status        string               `json:"status"`
	Commitments   []PublicCommitment   `json:"commitments"`
}

// VerificationReport is the researcher-visible outcome of commitment verification.
type VerificationReport struct {
	Generation   string `json:"generation"`
	ProtocolHash string `json:"protocol_hash"`
	Status       string `json:"status"`
	ExamActions  int    `json:"exam_actions"`
	Available    bool   `json:"available"`
	PaperCount   int    `json:"paper_count"`
	Reason       string `json:"reason,omitempty"`
}

// LoadDocument reads and decodes a public commitment JSON file.
func LoadDocument(path string) (Document, error) {
	if err := rejectHiddenManifestPath(path); err != nil {
		return Document{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Document{}, err
	}
	return DecodeDocument(raw)
}

// DecodeDocument parses public commitment bytes and rejects schema leaks.
func DecodeDocument(raw []byte) (Document, error) {
	if err := AssertNoSchemaLeak(raw); err != nil {
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("%w: decode: %v", ErrInvalidCommitment, err)
	}
	return doc, nil
}

// AssertNoSchemaLeak rejects JSON that contains forbidden hidden fields anywhere.
func AssertNoSchemaLeak(raw []byte) error {
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("%w: malformed json", ErrSchemaLeak)
	}
	return walkValueNoLeak(probe, "")
}

func walkValueNoLeak(node any, prefix string) error {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			if slices.Contains(protocol.ForbiddenPublicCommitmentFields, key) {
				return fmt.Errorf("%w: field %q", ErrSchemaLeak, full)
			}
			if err := walkValueNoLeak(value, full); err != nil {
				return err
			}
		}
	case []any:
		for i, value := range typed {
			full := fmt.Sprintf("%s[%d]", prefix, i)
			if err := walkValueNoLeak(value, full); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectHiddenManifestPath(path string) error {
	lower := strings.ToLower(strings.TrimSpace(path))
	switch {
	case strings.Contains(lower, "hidden-manifest"),
		strings.Contains(lower, "hidden_manifest"),
		strings.Contains(lower, "/hidden/"),
		strings.HasSuffix(lower, ".0600"):
		return fmt.Errorf("%w: %q", ErrHiddenManifestRead, path)
	default:
		return nil
	}
}

// ValidateSchedule checks order, duplicates, golden-last, and template alignment.
func ValidateSchedule(commitments []PublicCommitment) error {
	if len(commitments) == 0 {
		return ErrExamUnavailable
	}
	if len(commitments) != protocol.ExamRequiredPaperCount {
		return fmt.Errorf("%w: want %d papers, got %d", ErrScheduleDrift, protocol.ExamRequiredPaperCount, len(commitments))
	}
	seenID := map[string]bool{}
	seenQueue := map[int]bool{}
	seenChallenge := map[string]bool{}
	seenData := map[string]bool{}
	golden := 0
	for i, c := range commitments {
		if err := validateOne(c); err != nil {
			return err
		}
		slot := protocol.ExamScheduleTemplate[i]
		if c.Queue != slot.Queue || c.Kind != slot.Kind || c.Family != slot.Family || c.AlphaSpend != slot.AlphaSpend {
			return fmt.Errorf("%w: queue %d template mismatch", ErrScheduleDrift, c.Queue)
		}
		if seenID[c.ID] {
			return fmt.Errorf("%w: duplicate id %q", ErrDuplicateCommitment, c.ID)
		}
		if seenQueue[c.Queue] {
			return fmt.Errorf("%w: duplicate queue %d", ErrDuplicateCommitment, c.Queue)
		}
		if seenChallenge[c.ChallengeHash] {
			return fmt.Errorf("%w: duplicate challenge hash", ErrDuplicateCommitment)
		}
		if seenData[c.DataHash] {
			return fmt.Errorf("%w: duplicate data hash", ErrDuplicateCommitment)
		}
		seenID[c.ID] = true
		seenQueue[c.Queue] = true
		seenChallenge[c.ChallengeHash] = true
		seenData[c.DataHash] = true
		if c.Kind == string(gauntlet.KindGolden) {
			golden++
			if i != len(commitments)-1 {
				return fmt.Errorf("%w: golden must be final", ErrScheduleDrift)
			}
		}
	}
	if golden != protocol.ExamGoldenPaperCount {
		return fmt.Errorf("%w: want %d golden paper", ErrScheduleDrift, protocol.ExamGoldenPaperCount)
	}
	return nil
}

func validateOne(c PublicCommitment) error {
	if strings.TrimSpace(c.ID) == "" ||
		c.Queue <= 0 ||
		strings.TrimSpace(c.Family) == "" ||
		c.AlphaSpend <= 0 ||
		strings.TrimSpace(c.ChallengeHash) == "" ||
		strings.TrimSpace(c.DataHash) == "" ||
		strings.TrimSpace(c.ProtocolHash) == "" {
		return fmt.Errorf("%w: incomplete ticket %q", ErrInvalidCommitment, c.ID)
	}
	if c.ProtocolHash != protocol.RouterProtocolHash {
		return fmt.Errorf("%w: protocol hash mismatch on %q", ErrInvalidCommitment, c.ID)
	}
	if c.Kind != string(gauntlet.KindStandard) && c.Kind != string(gauntlet.KindGolden) {
		return fmt.Errorf("%w: invalid kind on %q", ErrInvalidCommitment, c.ID)
	}
	if !slices.Contains(protocol.AllowedCommitmentStatuses, c.Status) {
		return fmt.Errorf("%w: invalid status %q on %q", ErrInvalidCommitment, c.Status, c.ID)
	}
	return nil
}

// ValidateDocument checks envelope metadata and schedule integrity.
func ValidateDocument(doc Document) error {
	if doc.SchemaVersion != protocol.PublicCommitmentSchemaV1 {
		return fmt.Errorf("%w: schema_version=%q", ErrInvalidCommitment, doc.SchemaVersion)
	}
	if doc.Generation != protocol.Generation {
		return fmt.Errorf("%w: generation=%q", ErrInvalidCommitment, doc.Generation)
	}
	if doc.ProtocolHash != protocol.RouterProtocolHash {
		return fmt.Errorf("%w: protocol_hash mismatch", ErrInvalidCommitment)
	}
	return ValidateSchedule(doc.Commitments)
}

// VerifyGauntletState cross-checks public commitments against gauntlet consumption.
func VerifyGauntletState(doc Document, state gauntlet.State) error {
	if err := ValidateDocument(doc); err != nil {
		return err
	}
	gen, ok := generation(state, doc.Generation)
	if !ok {
		return ErrExamUnavailable
	}
	if len(gen.Tickets) != len(doc.Commitments) {
		return fmt.Errorf("%w: ticket count mismatch", ErrScheduleDrift)
	}
	for i, ticket := range gen.Tickets {
		pub := doc.Commitments[i]
		if ticket.ID != pub.ID ||
			ticket.Queue != pub.Queue ||
			string(ticket.Kind) != pub.Kind ||
			ticket.Family != pub.Family ||
			ticket.AlphaSpend != pub.AlphaSpend ||
			ticket.ChallengeHash != pub.ChallengeHash ||
			ticket.DataHash != pub.DataHash ||
			ticket.ProtocolHash != pub.ProtocolHash {
			return fmt.Errorf("%w: gauntlet ticket drift at queue %d", ErrScheduleDrift, pub.Queue)
		}
		if i < len(gen.Exams) {
			want := examStatusString(gen.Exams[i].Status)
			if pub.Status != want {
				return fmt.Errorf("%w: status drift queue %d public=%q gauntlet=%q", ErrScheduleDrift, pub.Queue, pub.Status, want)
			}
		}
	}
	return nil
}

func examStatusString(status exam.Status) string {
	switch status {
	case exam.StatusSealed:
		return "sealed"
	case exam.StatusRunning:
		return "running"
	case exam.StatusConsumedPassed:
		return "consumed_passed"
	case exam.StatusConsumedFailed:
		return "consumed_failed"
	case exam.StatusConsumedCrashed:
		return "consumed_crashed"
	default:
		return string(status)
	}
}

func generation(state gauntlet.State, id string) (gauntlet.Generation, bool) {
	for _, g := range state.Generations {
		if g.ID == id {
			return g, true
		}
	}
	return gauntlet.Generation{}, false
}

// Verify resolves availability from optional public commitments and gauntlet state.
func Verify(docPath, statePath string) (VerificationReport, error) {
	report := VerificationReport{
		Generation:   protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash,
		ExamActions:  protocol.ExamActions,
	}
	if strings.TrimSpace(docPath) == "" {
		report.Status = protocol.ExamUnavailableStatus
		report.Reason = "no public commitment document"
		return report, nil
	}
	doc, err := LoadDocument(docPath)
	if err != nil {
		if errors.Is(err, ErrExamUnavailable) || os.IsNotExist(err) {
			report.Status = protocol.ExamUnavailableStatus
			report.Reason = err.Error()
			return report, nil
		}
		return report, err
	}
	if len(doc.Commitments) == 0 {
		report.Status = protocol.ExamUnavailableStatus
		report.Reason = "empty commitments"
		return report, nil
	}
	if err := ValidateDocument(doc); err != nil {
		if errors.Is(err, ErrExamUnavailable) {
			report.Status = protocol.ExamUnavailableStatus
			report.Reason = err.Error()
			return report, nil
		}
		return report, err
	}
	if strings.TrimSpace(statePath) != "" {
		if err := rejectHiddenManifestPath(statePath); err != nil {
			return report, err
		}
		state, err := loadGauntletState(statePath)
		if err != nil && !os.IsNotExist(err) {
			return report, err
		}
		if err == nil {
			if err := VerifyGauntletState(doc, state); err != nil {
				if errors.Is(err, ErrExamUnavailable) {
					report.Status = protocol.ExamUnavailableStatus
					report.Reason = err.Error()
					return report, nil
				}
				return report, err
			}
		}
	}
	report.Status = "AVAILABLE"
	report.Available = true
	report.PaperCount = len(doc.Commitments)
	return report, nil
}

func loadGauntletState(path string) (gauntlet.State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return gauntlet.State{}, err
	}
	var state gauntlet.State
	return state, json.Unmarshal(raw, &state)
}

// ToGauntletTickets converts validated public commitments to gauntlet tickets.
func ToGauntletTickets(commitments []PublicCommitment) []gauntlet.Ticket {
	out := make([]gauntlet.Ticket, len(commitments))
	for i, c := range commitments {
		out[i] = gauntlet.Ticket{
			ID:            c.ID,
			Queue:         c.Queue,
			Kind:          gauntlet.Kind(c.Kind),
			Family:        c.Family,
			AlphaSpend:    c.AlphaSpend,
			ChallengeHash: c.ChallengeHash,
			ProtocolHash:  c.ProtocolHash,
			DataHash:      c.DataHash,
		}
	}
	return out
}

// AssertConsumptionOrder checks that consumed papers respect fixed queue order.
func AssertConsumptionOrder(state gauntlet.State, generationID string) error {
	gen, ok := generation(state, generationID)
	if !ok {
		return ErrExamUnavailable
	}
	lastConsumed := -1
	for i, e := range gen.Exams {
		if e.Consumed() {
			if i > 0 && !gen.Exams[i-1].Consumed() {
				return fmt.Errorf("%w: paper queue %d consumed before earlier paper", ErrScheduleDrift, gen.Tickets[i].Queue)
			}
			lastConsumed = i
		}
	}
	for i := 0; i < lastConsumed; i++ {
		if !gen.Exams[i].Consumed() {
			return fmt.Errorf("%w: gap before queue %d", ErrScheduleDrift, gen.Tickets[lastConsumed].Queue)
		}
	}
	return nil
}
