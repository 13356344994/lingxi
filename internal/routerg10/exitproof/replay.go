package exitproof

import (
	"fmt"
	"time"
)

// ReplayInput carries historically replayed exit feasibility signals.
// Bool convenience fields on Input are derived — never trusted directly.
type ReplayInput struct {
	TriggerID          string
	DecisionTime       time.Time
	ExitDepthUSDT      float64
	PositionUSDT       float64
	MaintenanceAt      time.Time // zero = no blocking maintenance/delist before exit window
	ExitWindowEnd      time.Time // last bar/time exit can complete
	ExtremeExitSlippageBPS float64
}

// ReplayExitFeasibility derives certification fields from replayed history.
func ReplayExitFeasibility(in ReplayInput) (exitDepthSufficient, canExitBeforeMaint, partialCloseFeasible bool, blockReason string) {
	if in.ExitDepthUSDT <= 0 {
		return false, false, false, "exit_depth_replay_zero"
	}
	pos := in.PositionUSDT
	if pos <= 0 {
		pos = in.ExitDepthUSDT
	}
	exitDepthSufficient = in.ExitDepthUSDT >= pos
	partialCloseFeasible = in.ExitDepthUSDT >= pos*0.25
	canExitBeforeMaint = true
	if !in.MaintenanceAt.IsZero() && !in.ExitWindowEnd.IsZero() {
		canExitBeforeMaint = in.ExitWindowEnd.Before(in.MaintenanceAt)
	}
	if !exitDepthSufficient {
		blockReason = "exit_depth_insufficient"
	} else if !canExitBeforeMaint {
		blockReason = "maintenance_before_exit"
	} else if !partialCloseFeasible {
		blockReason = "partial_close_infeasible"
	}
	return exitDepthSufficient, canExitBeforeMaint, partialCloseFeasible, blockReason
}

// BuildProofFromReplay freezes exit proof from historically replayed signals.
func BuildProofFromReplay(in ReplayInput) (Proof, error) {
	if in.TriggerID == "" {
		return Proof{}, fmt.Errorf("exitproof: trigger_id required")
	}
	if in.DecisionTime.IsZero() {
		return Proof{}, fmt.Errorf("exitproof: decision_time required")
	}
	depthOK, maintOK, partialOK, blockReason := ReplayExitFeasibility(in)
	p := Proof{
		SchemaVersion:              SchemaVersion,
		TriggerID:                  in.TriggerID,
		DecisionTime:               in.DecisionTime.UTC(),
		ExitDepthSufficient:        depthOK,
		CanExitBeforeDelistOrMaint: maintOK,
		ExtremeSlippageBoundBPS:    in.ExtremeExitSlippageBPS,
		PartialCloseFeasible:       partialOK,
		CertificationOK:            depthOK && maintOK && partialOK,
		ReplaySource:               ReplaySourceHistorical,
	}
	if !p.CertificationOK {
		if blockReason == "" {
			blockReason = "exit_not_proven"
		}
		p.BlockReason = blockReason
	}
	hash, err := CanonicalHash(p)
	if err != nil {
		return Proof{}, err
	}
	p.ExitProofHash = hash
	return p, nil
}
