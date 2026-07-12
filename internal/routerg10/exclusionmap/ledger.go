package exclusionmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// BuildLedger seals append-only exclusion evaluation attempts.
func BuildLedger(
	constitutionHash string,
	mapManifestHashes map[string]string,
	announcementTapeHash, announcementManifestHash string,
	attempts []ExperimentAttempt,
) (ExperimentLedger, error) {
	ledger := ExperimentLedger{
		SchemaVersion:            SchemaVersionLedger,
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		ConstitutionHash:         constitutionHash,
		MapManifestHashes:        copyStringMap(mapManifestHashes),
		AnnouncementTapeHash:     announcementTapeHash,
		AnnouncementManifestHash: announcementManifestHash,
		Attempts:                 append([]ExperimentAttempt(nil), attempts...),
	}
	hash, err := hashLedger(ledger)
	if err != nil {
		return ExperimentLedger{}, err
	}
	ledger.LedgerHash = hash
	return ledger, nil
}

func hashLedger(ledger ExperimentLedger) (string, error) {
	payload := ledger
	payload.LedgerHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateLedger checks ledger self-hash.
func ValidateLedger(ledger ExperimentLedger) error {
	if ledger.SchemaVersion != SchemaVersionLedger {
		return fmt.Errorf("exclusionmap: ledger schema %q", ledger.SchemaVersion)
	}
	want, err := hashLedger(ledger)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("exclusionmap: ledger hash mismatch (tamper/drift)")
	}
	return nil
}

// NewAttempt builds one ledger row for an evaluation.
func NewAttempt(ordinal int, item EventWorkItem, result EvaluationResult, proof ExclusionProof) ExperimentAttempt {
	status := LedgerStatusRejected
	reason := ""
	if result.Accepted && len(result.RejectReasons) == 0 {
		status = LedgerStatusAccepted
		reason = result.AcceptReason
	} else if len(result.RejectReasons) > 0 {
		reason = result.RejectReasons[0]
	}
	return ExperimentAttempt{
		AttemptOrdinal:     ordinal,
		EventID:            item.Event.EventID,
		ProfileID:          item.ProfileID,
		EventFamily:        item.Event.EventFamily,
		Horizon:            result.Horizon,
		Direction:          result.Direction,
		DelayBars:          result.DelayBars,
		Status:             status,
		StatusReason:       reason,
		AcceptReason:       result.AcceptReason,
		RejectReasons:      append([]string(nil), result.RejectReasons...),
		ExclusionProofHash: proof.ExclusionProofHash,
		EvaluatedAt:        time.Now().UTC(),
	}
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func unmarshalJSON(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
