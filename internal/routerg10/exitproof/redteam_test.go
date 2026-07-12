package exitproof

import (
	"testing"
	"time"
)

func TestBuildProofIgnoresSelfDeclaredBool(t *testing.T) {
	decision := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	p, err := BuildProof(Input{
		TriggerID: "bool_lie", DecisionTime: decision,
		ExitDepthUSDT: 5000, CanExitBeforeMaintenance: true,
		PartialCloseFeasible: true, PositionUSDT: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.CertificationOK {
		t.Fatal("must not certify from self-declared bool when replayed depth insufficient")
	}
	if p.ReplaySource != ReplaySourceHistorical {
		t.Fatalf("replay_source=%q", p.ReplaySource)
	}
}

func TestReplayMaintenanceDeadline(t *testing.T) {
	decision := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	p, err := BuildProofFromReplay(ReplayInput{
		TriggerID: "maint", DecisionTime: decision,
		ExitDepthUSDT: 5000, PositionUSDT: 1000,
		MaintenanceAt: decision.Add(30 * time.Minute),
		ExitWindowEnd: decision.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.CanExitBeforeDelistOrMaint {
		t.Fatal("maintenance before exit window end must block exit")
	}
}

func TestReplayCertifiesWithDepthAndWindow(t *testing.T) {
	decision := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	p, err := BuildProofFromReplay(ReplayInput{
		TriggerID: "ok", DecisionTime: decision,
		ExitDepthUSDT: 5000, PositionUSDT: 1000,
		ExitWindowEnd: decision.Add(time.Hour),
		ExtremeExitSlippageBPS: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.CertificationOK {
		t.Fatalf("proof: %+v", p)
	}
}
