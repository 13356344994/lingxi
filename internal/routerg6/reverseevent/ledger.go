package reverseevent

import (
	"errors"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/protocol"
)

// SealLedger stamps ledger hash over append-only attempts.
func SealLedger(ledger ExperimentLedger) (ExperimentLedger, error) {
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return ExperimentLedger{}, errors.New("ledger generation/protocol mismatch")
	}
	if ledger.DiscoveryMode != protocol.DiscoveryMode {
		return ExperimentLedger{}, errors.New("ledger discovery mode mismatch")
	}
	hash, err := HashLedgerAttempts(ledger.Attempts)
	if err != nil {
		return ExperimentLedger{}, err
	}
	ledger.LedgerHash = hash
	return ledger, nil
}

// HashLedgerAttempts hashes ordered ledger attempts.
func HashLedgerAttempts(attempts []ExperimentAttempt) (string, error) {
	hash, _, err := datatruth.HashJSON(attempts)
	return hash, err
}

// ValidateLedger checks append-only invariants and hash integrity.
func ValidateLedger(ledger ExperimentLedger) error {
	if ledger.SchemaVersion != SchemaVersionLedger {
		return errors.New("ledger schema mismatch")
	}
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("ledger generation/protocol mismatch")
	}
	if ledger.LedgerStage != LedgerStageDiscovery {
		return errors.New("ledger stage mismatch")
	}
	if ledger.DiscoveryMode != protocol.DiscoveryMode {
		return errors.New("ledger discovery mode mismatch")
	}
	for i, attempt := range ledger.Attempts {
		if attempt.AttemptOrdinal != i+1 {
			return errors.New("attempt ordinals must be contiguous starting at 1")
		}
		if attempt.LedgerStage != LedgerStageDiscovery {
			return errors.New("attempt ledger stage mismatch")
		}
		switch attempt.Status {
		case LedgerStatusDuplicate, LedgerStatusInvalid, LedgerStatusUnexplained, LedgerStatusNonOverlap:
		default:
			return errors.New("unknown ledger status")
		}
	}
	want, err := HashLedgerAttempts(ledger.Attempts)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("ledger hash mismatch")
	}
	return nil
}

func ledgerStatusCounts(attempts []ExperimentAttempt) map[string]int {
	counts := map[string]int{
		LedgerStatusDuplicate:   0,
		LedgerStatusInvalid:     0,
		LedgerStatusUnexplained: 0,
		LedgerStatusNonOverlap:  0,
	}
	for _, attempt := range attempts {
		counts[attempt.Status]++
	}
	return counts
}
