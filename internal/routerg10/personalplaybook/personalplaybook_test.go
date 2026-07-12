package personalplaybook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/reversetapes"
)

func TestProtocolNoDrift(t *testing.T) {
	if DiscoveryMode != g10protocolv2.DiscoveryMode {
		t.Fatalf("discovery mode drift: %s", DiscoveryMode)
	}
	if len(PlaybookHashChain) != 7 {
		t.Fatalf("hash chain length = %d", len(PlaybookHashChain))
	}
	if StatusNotPersonalEnough != g10protocolv2.NotPersonalEnoughStatus {
		t.Fatal("not personal enough status drift")
	}
}

func TestCertifiedTapeDerivesPlaybook(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seeds := CollectCertifiedSeeds(fix.CertifiedTapes)
	playbooks, discovery, ledger, err := Derive(seeds, fix.DiscoveryTapes, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) < 2 {
		t.Fatalf("expected certified playbooks, got %d", len(playbooks))
	}
	certified := 0
	for _, pb := range playbooks {
		if pb.Status == StatusPersonalWildCandidate {
			certified++
			if err := ValidatePlaybook(pb); err != nil {
				t.Fatalf("playbook invalid: %v", err)
			}
			if pb.ProofBinding.ConstitutionHash == "" || pb.ProofBinding.ExclusionProofHash == "" {
				t.Fatal("proof binding incomplete")
			}
			if pb.ExecutionNarrative.MakerSelectionConclusion == "" {
				t.Fatal("missing maker selection conclusion")
			}
			if pb.RiskNarrative.EventTerminalBudgetFraction != 0.40 {
				t.Fatalf("event budget = %v", pb.RiskNarrative.EventTerminalBudgetFraction)
			}
			if pb.RiskNarrative.AccountTerminalBudgetFraction != 0.70 {
				t.Fatalf("account budget = %v", pb.RiskNarrative.AccountTerminalBudgetFraction)
			}
		}
	}
	if certified == 0 {
		t.Fatal("expected at least one personal_wild_candidate")
	}
	if len(discovery) != 1 {
		t.Fatalf("discovery playbooks = %d want 1", len(discovery))
	}
	if err := ValidateDiscoveryPlaybook(discovery[0]); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
}

func TestNotPersonalEnoughDetected(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var notPersonal PersonalWildPlaybook
	for _, tape := range fix.CertifiedTapes {
		if tape.EventID == "evt-c" {
			pb, err := BuildPlaybook(tape, 1)
			if err != nil {
				t.Fatal(err)
			}
			notPersonal = pb
		}
	}
	if notPersonal.Status != StatusNotPersonalEnough {
		t.Fatalf("status=%s", notPersonal.Status)
	}
	if len(notPersonal.NotPersonalReasons) == 0 {
		t.Fatal("expected not_personal reasons")
	}
}

func TestDiscoveryOnlyNotCertifiable(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pb, err := BuildDiscoveryPlaybook(fix.DiscoveryTapes[0])
	if err != nil {
		t.Fatal(err)
	}
	if pb.Certified {
		t.Fatal("discovery must not certify")
	}
	if pb.Status != StatusDiscoveryOnly {
		t.Fatalf("status=%s", pb.Status)
	}
}

func TestPublicTriggerNoExclusionFields(t *testing.T) {
	spec := PublicTriggerSpec{
		Conditions: []PublicTriggerCondition{{
			Field: "institution_exclude_reason", Op: "eq", Value: 1,
		}},
	}
	if err := validatePublicTrigger(spec); err == nil {
		t.Fatal("exclusion field in trigger must fail")
	}
}

func TestPlaybookHashChainBinding(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pb, err := BuildPlaybook(fix.CertifiedTapes[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range PlaybookHashChain[:len(PlaybookHashChain)-1] {
		switch field {
		case "ConstitutionHash":
			if pb.ProofBinding.ConstitutionHash == "" {
				t.Fatal("missing constitution hash")
			}
		case "ExclusionProofHash":
			if pb.ProofBinding.ExclusionProofHash == "" {
				t.Fatal("missing exclusion hash")
			}
		case "EntryProofHash":
			if pb.ProofBinding.EntryProofHash == "" {
				t.Fatal("missing entry hash")
			}
		case "ExecutionPolicyHash":
			if pb.ProofBinding.ExecutionPolicyHash == "" {
				t.Fatal("missing execution hash")
			}
		case "ExitProofHash":
			if pb.ProofBinding.ExitProofHash == "" {
				t.Fatal("missing exit hash")
			}
		case "AnnouncementTapeHash":
			if pb.ProofBinding.AnnouncementTapeHash == "" {
				t.Fatal("missing announcement hash")
			}
		}
	}
}

func TestLedgerTamperDetection(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, ledger, err := Derive(CollectCertifiedSeeds(fix.CertifiedTapes), nil, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := ledger
	bad.Attempts[0].Status = "evil"
	if err := ValidateLedger(bad); err == nil {
		t.Fatal("tampered ledger must fail")
	}
}

func TestPlaybookHashTamperDetection(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pb, err := BuildPlaybook(fix.CertifiedTapes[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	pb.PlaybookHash = "deadbeef"
	if err := ValidatePlaybook(pb); err == nil {
		t.Fatal("tampered playbook hash must fail")
	}
}

func TestCheckpointTamperDetection(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		ReverseTapesDir: dir,
		OutputDir:       filepath.Join(dir, "out"),
		PlaybookLimit:   8,
		Generation:      Generation,
		ProtocolHash:    g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		Generation: Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash, ConfigHash: cfgHash,
		OutputDir: cfg.OutputDir, Done: false,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail")
	}
}

func TestDedupSkipsDuplicateTape(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dup := fix.CertifiedTapes[0]
	seeds := []TapeSeed{{Tape: dup}, {Tape: dup}}
	playbooks, _, ledger, err := Derive(seeds, nil, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(playbooks) != 1 {
		t.Fatalf("dedup playbooks = %d want 1", len(playbooks))
	}
	found := false
	for _, attempt := range ledger.Attempts {
		if attempt.Status == LedgerStatusDeduped {
			found = true
		}
	}
	if !found {
		t.Fatal("ledger must record dedup skip")
	}
}

func TestRunnerEndToEnd(t *testing.T) {
	root := t.TempDir()
	fix, err := WriteFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "playbooks")
	cfg := RunConfig{
		SchemaVersion:   SchemaVersionConfig,
		ReverseTapesDir: fix.ReverseTapesDir,
		OutputDir:       outDir,
		PlaybookLimit:   8,
		Generation:      Generation,
		ProtocolHash:    g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config:         cfg,
		CheckpointPath: filepath.Join(root, "cp.json"),
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ManifestHash == "" {
		t.Fatal("manifest hash missing")
	}
	if err := VerifyOutputArtifacts(outDir, result.Manifest); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInputBundleRejectsBadTape(t *testing.T) {
	root := t.TempDir()
	fix, err := WriteFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := fix.CertifiedTapes[0]
	bad.Status = reversetapes.TapeStatusDiscoveryOnly
	if err := writeTapesJSONL(filepath.Join(fix.ReverseTapesDir, reversetapes.OutputTapesFile), []reversetapes.ExecutableProfitTape{bad}); err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		SchemaVersion:   SchemaVersionConfig,
		ReverseTapesDir: fix.ReverseTapesDir,
		OutputDir:       filepath.Join(root, "out"),
		PlaybookLimit:   4,
		ProtocolHash:    g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	if _, err := LoadInputBundle(cfg); err == nil {
		t.Fatal("non-certified tape in certified file must fail")
	}
}

func TestAllowedActionsIncludeSkip(t *testing.T) {
	allowed := allowedActionsFor(g10executionpolicy.ActionBoundedMaker)
	hasSkip := false
	for _, a := range allowed {
		if a == g10executionpolicy.ActionSkip {
			hasSkip = true
		}
	}
	if !hasSkip {
		t.Fatal("allowed actions must include SKIP")
	}
}

func TestPlaybookLimit64(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, ReverseTapesDir: "/r", OutputDir: "/o",
		PlaybookLimit: 65, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("playbook_limit above 64 must fail")
	}
}
