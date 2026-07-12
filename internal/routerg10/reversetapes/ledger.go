package reversetapes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// BuildLedger seals append-only reverse-tape attempts.
func BuildLedger(
	constitutionHash string,
	exclusionMapHash, exclusionManifestHash string,
	entryManifestHash, executionManifestHash string,
	announcementTapeHash, announcementManifestHash string,
	attempts []ExperimentAttempt,
) (ExperimentLedger, error) {
	ledger := ExperimentLedger{
		SchemaVersion:            SchemaVersionLedger,
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		ConstitutionHash:         constitutionHash,
		ExclusionMapHash:         exclusionMapHash,
		ExclusionManifestHash:    exclusionManifestHash,
		EntryProofManifestHash:   entryManifestHash,
		ExecutionManifestHash:    executionManifestHash,
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
		return fmt.Errorf("reversetapes: ledger schema %q", ledger.SchemaVersion)
	}
	want, err := hashLedger(ledger)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("reversetapes: ledger hash mismatch")
	}
	return nil
}

// NewAttempt builds one ledger row.
func NewAttempt(ordinal int, proof exclusionmap.ExclusionProof, result EvaluationResult) ExperimentAttempt {
	status := LedgerStatusRejected
	reason := ""
	if result.Accepted {
		if result.Status == TapeStatusDiscoveryOnly {
			status = LedgerStatusDiscovery
		} else {
			status = LedgerStatusAccepted
		}
		reason = result.Status
	} else if len(result.RejectReasons) > 0 {
		reason = result.RejectReasons[0]
	}
	return ExperimentAttempt{
		AttemptOrdinal: ordinal,
		EventID:        proof.EventID,
		ProfileID:      proof.ProfileID,
		EventFamily:    proof.EventFamily,
		Horizon:        proof.Horizon,
		Direction:      proof.Direction,
		DelayBars:      proof.DelayBars,
		Status:         status,
		StatusReason:   reason,
		RejectReasons:  append([]string(nil), result.RejectReasons...),
		TapeHash:       result.Tape.TapeHash,
		EvaluatedAt:    time.Now().UTC(),
	}
}

// WriteLedger atomically writes reverse_tapes_ledger.json.
func WriteLedger(outputDir string, ledger ExperimentLedger) error {
	return writeJSONAtomic(filepath.Join(outputDir, LedgerFileName), ledger)
}

func writeJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
