package exitproof

import (
	"testing"
	"time"
)

func TestExitDepthBlocksCertification(t *testing.T) {
	p, err := BuildProof(Input{
		TriggerID: "no_depth", DecisionTime: time.Now().UTC(),
		ExitDepthUSDT: 0, CanExitBeforeMaintenance: true, PartialCloseFeasible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.CertificationOK {
		t.Fatal("zero exit depth must not certify")
	}
	if err := ValidateProof(p); err != nil {
		t.Fatal(err)
	}
}

func TestExitDepthCertifies(t *testing.T) {
	p, err := BuildProof(Input{
		TriggerID: "ok", DecisionTime: time.Now().UTC(),
		ExitDepthUSDT: 5000, CanExitBeforeMaintenance: true,
		ExtremeExitSlippageBPS: 15, PartialCloseFeasible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.CertificationOK || !p.ExitDepthSufficient {
		t.Fatalf("proof: %+v", p)
	}
}
