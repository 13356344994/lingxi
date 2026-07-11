package exam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type Status string

const (
	StatusDraft           Status = "draft"
	StatusSealed          Status = "sealed"
	StatusRunning         Status = "running"
	StatusConsumedPassed  Status = "consumed_passed"
	StatusConsumedFailed  Status = "consumed_failed"
	StatusConsumedCrashed Status = "consumed_crashed"
)

type Exam struct {
	ExamID         string    `json:"exam_id"`
	Generation     string    `json:"generation"`
	Order          int       `json:"order"`
	Status         Status    `json:"status"`
	ChallengeHash  string    `json:"challenge_hash"`
	ProtocolHash   string    `json:"protocol_hash"`
	DataHash       string    `json:"data_hash"`
	FeatureHash    string    `json:"feature_hash,omitempty"`
	ExperimentHash string    `json:"experiment_hash,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	SealedAt       time.Time `json:"sealed_at,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	ConsumedAt     time.Time `json:"consumed_at,omitempty"`
	AttemptID      string    `json:"attempt_id,omitempty"`
	Verdict        *Verdict  `json:"verdict,omitempty"`
	Receipt        *Receipt  `json:"receipt,omitempty"`
}

type Verdict struct {
	Passed       bool               `json:"passed"`
	Reason       string             `json:"reason"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
	DecisionID   string             `json:"decision_id,omitempty"`
	ReturnedAt   time.Time          `json:"returned_at"`
	ConsumedNote string             `json:"consumed_note,omitempty"`
}

type Receipt struct {
	Payload          ReceiptPayload `json:"payload"`
	KeyID            string         `json:"key_id,omitempty"`
	Algorithm        string         `json:"algorithm,omitempty"`
	Signature        string         `json:"signature,omitempty"`
	Signed           bool           `json:"signed"`
	SecurityBoundary string         `json:"security_boundary"`
}

type ReceiptPayload struct {
	ExamID         string    `json:"exam_id"`
	Generation     string    `json:"generation"`
	AttemptID      string    `json:"attempt_id"`
	Status         Status    `json:"status"`
	ChallengeHash  string    `json:"challenge_hash"`
	ProtocolHash   string    `json:"protocol_hash"`
	DataHash       string    `json:"data_hash"`
	FeatureHash    string    `json:"feature_hash,omitempty"`
	ExperimentHash string    `json:"experiment_hash,omitempty"`
	VerdictHash    string    `json:"verdict_hash"`
	ConsumedAt     time.Time `json:"consumed_at"`
}

type ReceiptSigner interface {
	SignReceipt(ctx context.Context, payload ReceiptPayload) (Receipt, error)
}

type UnsignedBoundarySigner struct {
	Boundary string
}

func (s UnsignedBoundarySigner) SignReceipt(_ context.Context, payload ReceiptPayload) (Receipt, error) {
	boundary := strings.TrimSpace(s.Boundary)
	if boundary == "" {
		boundary = "UNSIGNED_BOUNDARY: no real key management service is configured; this receipt is an interface artifact only and must not be treated as cryptographic proof"
	}
	return Receipt{Payload: payload, Signed: false, SecurityBoundary: boundary}, nil
}

func NewDraft(examID, generation string, order int, challengeHash, protocolHash, dataHash string, createdAt time.Time) Exam {
	return Exam{
		ExamID:        strings.TrimSpace(examID),
		Generation:    strings.TrimSpace(generation),
		Order:         order,
		Status:        StatusDraft,
		ChallengeHash: strings.TrimSpace(challengeHash),
		ProtocolHash:  strings.TrimSpace(protocolHash),
		DataHash:      strings.TrimSpace(dataHash),
		CreatedAt:     createdAt.UTC(),
	}
}

func Seal(e Exam, now time.Time) (Exam, error) {
	if e.Status == "" {
		e.Status = StatusDraft
	}
	if e.Status != StatusDraft {
		return Exam{}, fmt.Errorf("cannot seal exam in status %s", e.Status)
	}
	if err := e.validateForSeal(); err != nil {
		return Exam{}, err
	}
	e.Status = StatusSealed
	e.SealedAt = now.UTC()
	return e, nil
}

func Start(e Exam, now time.Time) (Exam, error) {
	if e.Status != StatusSealed {
		return Exam{}, fmt.Errorf("cannot start exam in status %s", e.Status)
	}
	e.Status = StatusRunning
	e.StartedAt = now.UTC()
	e.AttemptID = attemptID(e, now.UTC())
	return e, nil
}

func Consume(ctx context.Context, e Exam, verdict Verdict, now time.Time, signer ReceiptSigner) (Exam, error) {
	status := StatusConsumedFailed
	if verdict.Passed {
		status = StatusConsumedPassed
	}
	return consumeWithStatus(ctx, e, verdict, status, now, signer)
}

func Crash(ctx context.Context, e Exam, reason string, now time.Time, signer ReceiptSigner) (Exam, error) {
	return consumeWithStatus(ctx, e, Verdict{
		Passed:       false,
		Reason:       strings.TrimSpace(reason),
		ReturnedAt:   now.UTC(),
		ConsumedNote: "crash_consumes_exam_and_cannot_be_replayed",
	}, StatusConsumedCrashed, now, signer)
}

func consumeWithStatus(ctx context.Context, e Exam, verdict Verdict, status Status, now time.Time, signer ReceiptSigner) (Exam, error) {
	if e.Status != StatusRunning {
		return Exam{}, fmt.Errorf("cannot consume exam in status %s", e.Status)
	}
	if signer == nil {
		return Exam{}, errors.New("receipt signer is required; use UnsignedBoundarySigner only when explicitly accepting the non-cryptographic boundary")
	}
	if verdict.ReturnedAt.IsZero() {
		verdict.ReturnedAt = now.UTC()
	} else {
		verdict.ReturnedAt = verdict.ReturnedAt.UTC()
	}
	if verdict.DecisionID != "" {
		if err := datatruth.ValidateDecisionID(verdict.DecisionID); err != nil {
			return Exam{}, err
		}
	}
	e.Status = status
	e.ConsumedAt = now.UTC()
	e.Verdict = &verdict
	payload, err := receiptPayload(e, verdict)
	if err != nil {
		return Exam{}, err
	}
	receipt, err := signer.SignReceipt(ctx, payload)
	if err != nil {
		return Exam{}, err
	}
	e.Receipt = &receipt
	return e, nil
}

func NextSealed(exams []Exam) (Exam, bool) {
	candidates := make([]Exam, 0, len(exams))
	for _, e := range exams {
		if e.Status == StatusSealed {
			candidates = append(candidates, e)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Order == candidates[j].Order {
			if candidates[i].SealedAt.Equal(candidates[j].SealedAt) {
				return candidates[i].ExamID < candidates[j].ExamID
			}
			return candidates[i].SealedAt.Before(candidates[j].SealedAt)
		}
		return candidates[i].Order < candidates[j].Order
	})
	if len(candidates) == 0 {
		return Exam{}, false
	}
	return candidates[0], true
}

func (e Exam) Consumed() bool {
	return e.Status == StatusConsumedPassed || e.Status == StatusConsumedFailed || e.Status == StatusConsumedCrashed
}

func (e Exam) validateForSeal() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"exam_id", e.ExamID},
		{"generation", e.Generation},
		{"challenge_hash", e.ChallengeHash},
		{"protocol_hash", e.ProtocolHash},
		{"data_hash", e.DataHash},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if e.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func receiptPayload(e Exam, verdict Verdict) (ReceiptPayload, error) {
	if e.AttemptID == "" {
		return ReceiptPayload{}, errors.New("attempt_id is required")
	}
	vh, _, err := datatruth.HashJSON(verdict)
	if err != nil {
		return ReceiptPayload{}, err
	}
	return ReceiptPayload{
		ExamID:         e.ExamID,
		Generation:     e.Generation,
		AttemptID:      e.AttemptID,
		Status:         e.Status,
		ChallengeHash:  e.ChallengeHash,
		ProtocolHash:   e.ProtocolHash,
		DataHash:       e.DataHash,
		FeatureHash:    e.FeatureHash,
		ExperimentHash: e.ExperimentHash,
		VerdictHash:    vh,
		ConsumedAt:     e.ConsumedAt,
	}, nil
}

func attemptID(e Exam, startedAt time.Time) string {
	raw, _ := json.Marshal([]string{e.ExamID, e.Generation, e.ChallengeHash, startedAt.Format(time.RFC3339Nano)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
