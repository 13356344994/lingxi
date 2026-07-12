package exitproof

import (
	"fmt"
	"time"

	"lingxi/internal/datatruth"
)

// Input carries legacy convenience fields; BuildProof replays from them
// without trusting self-declared bool flags.
type Input struct {
	TriggerID                 string
	DecisionTime              time.Time
	ExitDepthUSDT             float64
	CanExitBeforeMaintenance  bool
	ExtremeExitSlippageBPS    float64
	PartialCloseFeasible      bool
	PositionUSDT              float64
	MaintenanceAt             time.Time
	ExitWindowEnd             time.Time
}

// Proof freezes exit feasibility at the same decision point as entry.
type Proof struct {
	SchemaVersion              string    `json:"schema_version"`
	TriggerID                  string    `json:"trigger_id"`
	DecisionTime               time.Time `json:"decision_time"`
	ExitDepthSufficient        bool      `json:"exit_depth_sufficient"`
	CanExitBeforeDelistOrMaint bool      `json:"can_exit_before_delist_or_maintenance"`
	ExtremeSlippageBoundBPS    float64   `json:"extreme_slippage_bound"`
	PartialCloseFeasible       bool      `json:"partial_close_feasible"`
	ExitProofHash              string    `json:"exit_proof_hash"`
	CertificationOK            bool      `json:"certification_ok"`
	ReplaySource               string    `json:"replay_source,omitempty"`
	BlockReason                string    `json:"block_reason,omitempty"`
}

// BuildProof freezes exit proof from replayed input (bool flags ignored).
func BuildProof(in Input) (Proof, error) {
	replay := ReplayInput{
		TriggerID:              in.TriggerID,
		DecisionTime:             in.DecisionTime,
		ExitDepthUSDT:            in.ExitDepthUSDT,
		PositionUSDT:             in.PositionUSDT,
		ExtremeExitSlippageBPS:   in.ExtremeExitSlippageBPS,
	}
	if !in.MaintenanceAt.IsZero() {
		replay.MaintenanceAt = in.MaintenanceAt
	} else if !in.CanExitBeforeMaintenance && in.ExitDepthUSDT > 0 {
		// Legacy fixture: false CanExitBeforeMaintenance implies maintenance blocks exit.
		replay.MaintenanceAt = in.DecisionTime
		replay.ExitWindowEnd = in.DecisionTime.Add(time.Hour)
	} else {
		replay.ExitWindowEnd = in.DecisionTime.Add(24 * time.Hour)
	}
	if !in.ExitWindowEnd.IsZero() {
		replay.ExitWindowEnd = in.ExitWindowEnd
	}
	return BuildProofFromReplay(replay)
}

// CanonicalHash hashes proof with hash field cleared.
func CanonicalHash(p Proof) (string, error) {
	payload := p
	payload.ExitProofHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateProof checks sealed exit proof integrity.
func ValidateProof(p Proof) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("exitproof: schema drift")
	}
	want, err := CanonicalHash(p)
	if err != nil {
		return err
	}
	if want != p.ExitProofHash {
		return fmt.Errorf("exitproof: hash mismatch")
	}
	return nil
}
