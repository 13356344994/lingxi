package g3reverse

import (
	"errors"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

// LedgerStatus values for the append-only alpha / search ledger.
const (
	LedgerStatusConsidered = "considered"
	LedgerStatusDuplicate  = "duplicate"
	LedgerStatusTruncated  = "truncated"
	LedgerStatusRegistered = "registered"
)

// LedgerEntry records one derivation attempt with full provenance.
// Append-only: entries are never deleted or rewritten.
type LedgerEntry struct {
	Ordinal                       int                 `json:"ordinal"`
	CanonicalRuleHash             string              `json:"canonical_rule_hash"`
	DerivationAlgorithmConfigHash string              `json:"derivation_algorithm_config_hash"`
	AlphaStage                    string              `json:"alpha_stage"`
	AlphaCharge                   float64             `json:"alpha_charge"`
	Status                        string              `json:"status"`
	SkyState                      string              `json:"sky_state"`
	SorterDomain                  string              `json:"sorter_domain"`
	Horizon                       Horizon             `json:"horizon"`
	Direction                     mechanism.Direction `json:"direction"`
	RuleKind                      string              `json:"rule_kind"`
	Conditions                    []RuleCondition     `json:"conditions"`
	// SourceWinnerEventIDs is retained only for backward-compatible decoding.
	// Streaming derivation never populates it; compact hash/count provenance is
	// used instead so ledger size does not scale by attempts × winner count.
	SourceWinnerEventIDs        []string `json:"source_winner_event_ids,omitempty"`
	SourceWinnerIDsHash         string   `json:"source_winner_ids_hash,omitempty"`
	SourceWinnerCount           int      `json:"source_winner_count,omitempty"`
	AvailableFeatureSampleCount int      `json:"available_feature_sample_count"`
}

// AlphaLedger is the append-only record of every considered derivation attempt.
type AlphaLedger struct {
	Generation                    string        `json:"generation"`
	ProtocolHash                  string        `json:"protocol_hash"`
	DerivationAlgorithmConfigHash string        `json:"derivation_algorithm_config_hash"`
	Entries                       []LedgerEntry `json:"entries"`
	LedgerHash                    string        `json:"ledger_hash"`
}

// HashLedger commits the full append-only entry list.
func HashLedger(entries []LedgerEntry) (string, error) {
	hash, _, err := datatruth.HashJSON(entries)
	return hash, err
}

// SealLedger stamps LedgerHash over the ordered entries.
func SealLedger(ledger AlphaLedger) (AlphaLedger, error) {
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return AlphaLedger{}, errors.New("g3reverse: ledger generation/protocol must match frozen G3 protocol")
	}
	if ledger.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
		return AlphaLedger{}, errors.New("g3reverse: ledger derivation algorithm config hash mismatch")
	}
	hash, err := HashLedger(ledger.Entries)
	if err != nil {
		return AlphaLedger{}, err
	}
	ledger.LedgerHash = hash
	return ledger, nil
}

// Validate checks ledger integrity and append-only invariants.
func (ledger AlphaLedger) Validate() error {
	if ledger.Generation != protocol.Generation || ledger.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("g3reverse: ledger generation/protocol mismatch")
	}
	if ledger.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
		return errors.New("g3reverse: ledger derivation algorithm config hash mismatch")
	}
	for i, entry := range ledger.Entries {
		if entry.Ordinal != i+1 {
			return errors.New("g3reverse: ledger ordinals must be contiguous append order starting at 1")
		}
		if entry.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
			return errors.New("g3reverse: ledger entry derivation algorithm config hash mismatch")
		}
		switch entry.Status {
		case LedgerStatusConsidered, LedgerStatusDuplicate, LedgerStatusTruncated, LedgerStatusRegistered:
		default:
			return errors.New("g3reverse: unknown ledger status")
		}
		if entry.CanonicalRuleHash == "" && entry.Status != LedgerStatusConsidered {
			return errors.New("g3reverse: ledger entry missing canonical rule hash")
		}
	}
	want, err := HashLedger(ledger.Entries)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("g3reverse: ledger hash mismatch")
	}
	return nil
}

func ledgerStatusCounts(entries []LedgerEntry) map[string]int {
	counts := map[string]int{
		LedgerStatusConsidered: 0,
		LedgerStatusDuplicate:  0,
		LedgerStatusTruncated:  0,
		LedgerStatusRegistered: 0,
	}
	for _, entry := range entries {
		counts[entry.Status]++
	}
	return counts
}
