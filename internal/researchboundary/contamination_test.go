package researchboundary

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanUsageAndConservativeLedger(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "_study.py")
	if err := os.WriteFile(script, []byte(`open("/records/history/okx_1h/BTC.jsonl")`), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := ScanUsage(ScanConfig{
		Roots:    []string{filepath.Join(dir, "*.py")},
		Families: []string{"okx_1h", "okx_5m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Family != "okx_1h" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
	ledger, err := BuildLedger([]string{"okx_1h", "okx_5m"}, evidence, nil, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Families) != 2 {
		t.Fatalf("families=%+v", ledger.Families)
	}
	for _, family := range ledger.Families {
		if family.Status != StatusResearchOnly {
			t.Fatalf("unproven family must default to RESEARCH_ONLY: %+v", family)
		}
	}
}

func TestCommitmentRequiresCompletePreResearchProof(t *testing.T) {
	_, err := BuildLedger([]string{"okx_1h"}, nil, []SealedCommitment{{
		ID: "exam-1", DataHash: "data",
	}}, time.Now())
	if err == nil {
		t.Fatal("incomplete commitment must be rejected")
	}
	now := time.Now().UTC()
	ledger, err := BuildLedger([]string{"okx_1h"}, nil, []SealedCommitment{{
		ID: "exam-1", DataHash: "data", ChallengeHash: "challenge",
		ProtocolHash: "protocol", CommittedAt: now,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Commitments) != 1 || ledger.Families[0].Status != StatusResearchOnly {
		t.Fatalf("sealed commitment must not retroactively make a whole family sealed: %+v", ledger)
	}
}
