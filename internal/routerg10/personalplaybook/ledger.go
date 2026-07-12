package personalplaybook

import (
	"errors"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// SealLedger stamps ledger hash over append-only attempts.
func SealLedger(ledger ExperimentLedger) (ExperimentLedger, error) {
	if ledger.Generation != Generation || ledger.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return ExperimentLedger{}, errors.New("ledger generation/protocol mismatch")
	}
	if ledger.DiscoveryMode != DiscoveryMode {
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
	if ledger.Generation != Generation || ledger.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("ledger generation/protocol mismatch")
	}
	if ledger.LedgerStage != LedgerStagePersonalWild {
		return errors.New("ledger stage mismatch")
	}
	if ledger.DiscoveryMode != DiscoveryMode {
		return errors.New("ledger discovery mode mismatch")
	}
	for i, attempt := range ledger.Attempts {
		if attempt.AttemptOrdinal != i+1 {
			return errors.New("attempt ordinals must be contiguous starting at 1")
		}
		if attempt.LedgerStage != LedgerStagePersonalWild {
			return errors.New("attempt ledger stage mismatch")
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
	counts := map[string]int{}
	for _, attempt := range attempts {
		counts[attempt.Status]++
	}
	return counts
}
