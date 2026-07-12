package residualmap

import (
	"errors"
	"fmt"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// BuildLedger seals append-only evaluation attempts.
func BuildLedger(fingerprintHash string, mapManifestHashes map[string]string, attempts []ExperimentAttempt) (ExperimentLedger, error) {
	ledger := ExperimentLedger{
		SchemaVersion:     SchemaVersionLedger,
		Generation:        Generation,
		ProtocolHash:      g10protocol.RouterProtocolHash,
		FingerprintHash:   fingerprintHash,
		MapManifestHashes: copyStringMap(mapManifestHashes),
		Attempts:          append([]ExperimentAttempt(nil), attempts...),
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
		return fmt.Errorf("residualmap: ledger schema %q", ledger.SchemaVersion)
	}
	want, err := hashLedger(ledger)
	if err != nil {
		return err
	}
	if ledger.LedgerHash == "" || ledger.LedgerHash != want {
		return errors.New("residualmap: ledger hash mismatch (tamper/drift)")
	}
	return nil
}

// NewAttempt builds one ledger row for an evaluation.
func NewAttempt(ordinal int, item EventWorkItem, result EvaluationResult) ExperimentAttempt {
	status := LedgerStatusRejected
	reason := ""
	if len(result.RejectReasons) == 0 && result.Accepted {
		status = LedgerStatusAccepted
	} else if len(result.RejectReasons) > 0 {
		reason = result.RejectReasons[0]
	}
	direction := result.Direction
	if direction == "" {
		direction = item.Event.Direction
	}
	horizon := result.Horizon
	if horizon == "" {
		horizon = item.Event.Horizon
	}
	delay := result.DelayBars
	if delay == 0 && item.Event.DelayBars > 0 {
		delay = item.Event.DelayBars
	}
	return ExperimentAttempt{
		AttemptOrdinal: ordinal,
		EventID:        item.Event.EventID,
		ProfileID:      item.ProfileID,
		EventFamily:    item.Event.EventFamily,
		Horizon:        horizon,
		Direction:      direction,
		DelayBars:      delay,
		Status:         status,
		StatusReason:   reason,
		RejectReasons:  append([]string(nil), result.RejectReasons...),
		EvaluatedAt:    time.Now().UTC(),
	}
}

// BuildOpportunity converts one accepted evaluation into a frozen residual row.
func BuildOpportunity(item EventWorkItem, result EvaluationResult, fingerprintHash string) (ResidualOpportunity, error) {
	direction := result.Direction
	if direction == "" {
		direction = item.Event.Direction
	}
	horizon := result.Horizon
	if horizon == "" {
		horizon = item.Event.Horizon
	}
	delay := result.DelayBars
	if delay == 0 {
		delay = item.Event.DelayBars
	}
	opp := ResidualOpportunity{
		SchemaVersion:        SchemaVersion,
		Generation:           Generation,
		ProtocolHash:         g10protocol.RouterProtocolHash,
		EventID:              item.Event.EventID,
		Symbol:               item.Event.Symbol,
		ProfileID:            item.ProfileID,
		EventFamily:          item.Event.EventFamily,
		Horizon:              horizon,
		Direction:            direction,
		DelayBars:            delay,
		DecisionTime:         item.Event.DecisionTime,
		ShockCluster:         item.ShockCluster,
		Status:               CandidateStatus,
		TierCosts:            result.TierCosts,
		EligibleCapitalTiers: append([]string(nil), result.EligibleCapitalTiers...),
		MaxCapacityUSDT:      result.MaxCapacityUSDT,
		CapacityCliffUSDT:    result.CapacityCliffUSDT,
		CapacityCliffReason:  result.CapacityCliffReason,
		LargeTierFailReasons: append([]string(nil), result.LargeTierFailReasons...),
		FingerprintHash:      fingerprintHash,
		MapManifestHash:      item.ManifestHash,
	}
	hash, err := hashOpportunity(opp)
	if err != nil {
		return ResidualOpportunity{}, err
	}
	opp.ResidualHash = hash
	return opp, nil
}

func hashOpportunity(opp ResidualOpportunity) (string, error) {
	payload := opp
	payload.ResidualHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateOpportunity checks one opportunity self-hash.
func ValidateOpportunity(opp ResidualOpportunity) error {
	want, err := hashOpportunity(opp)
	if err != nil {
		return err
	}
	if opp.ResidualHash == "" || opp.ResidualHash != want {
		return errors.New("residualmap: residual hash mismatch (tamper/drift)")
	}
	return nil
}
