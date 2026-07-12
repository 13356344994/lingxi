package reversemine

import (
	"errors"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

const (
	LedgerStatusDuplicate   = "duplicate"
	LedgerStatusInvalid     = "invalid"
	LedgerStatusRegistered  = "registered"
	LedgerStatusUnexplained = "unexplained"
)

// SealLedger stamps ledger hash over append-only entries.
func SealLedger(ledger CandidateLedger) (CandidateLedger, error) {
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return CandidateLedger{}, errors.New("reversemine: ledger generation/protocol mismatch")
	}
	if err := protocol.ValidateReverseMineBinding(ledger.SearchSpaceID, ledger.DerivationAlgorithm); err != nil {
		return CandidateLedger{}, err
	}
	hash, err := HashLedgerEntries(ledger.Entries)
	if err != nil {
		return CandidateLedger{}, err
	}
	ledger.LedgerHash = hash
	return ledger, nil
}

// HashLedgerEntries hashes ordered ledger entries.
func HashLedgerEntries(entries []LedgerEntry) (string, error) {
	hash, _, err := datatruth.HashJSON(entries)
	return hash, err
}

// ValidateLedger checks append-only invariants and hash integrity.
func ValidateLedger(ledger CandidateLedger) error {
	if ledger.SchemaVersion != SchemaVersionLedger {
		return errors.New("reversemine: ledger schema mismatch")
	}
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("reversemine: ledger generation/protocol mismatch")
	}
	if err := protocol.ValidateReverseMineBinding(ledger.SearchSpaceID, ledger.DerivationAlgorithm); err != nil {
		return err
	}
	for i, entry := range ledger.Entries {
		if entry.AttemptOrdinal != i+1 {
			return errors.New("reversemine: ledger ordinals must be contiguous starting at 1")
		}
		if entry.LedgerStage != LedgerStageDiscovery {
			return errors.New("reversemine: ledger stage mismatch")
		}
		switch entry.Status {
		case LedgerStatusDuplicate, LedgerStatusInvalid, LedgerStatusRegistered, LedgerStatusUnexplained:
		default:
			return errors.New("reversemine: unknown ledger status")
		}
	}
	want, err := HashLedgerEntries(ledger.Entries)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("reversemine: ledger hash mismatch")
	}
	return nil
}

func ledgerStatusCounts(entries []LedgerEntry) map[string]int {
	counts := map[string]int{
		LedgerStatusDuplicate:   0,
		LedgerStatusInvalid:     0,
		LedgerStatusRegistered:  0,
		LedgerStatusUnexplained: 0,
	}
	for _, entry := range entries {
		counts[entry.Status]++
	}
	return counts
}
