package mechanism

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
	"lingxi/internal/routerg4/validation"
)

func TestZeroSurvivorsOutputs(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot:   t.TempDir(), ValidationOutputDir: t.TempDir(),
		CandidateOutputDir: t.TempDir(), OutputDir: t.TempDir(),
		MapDirs: []string{t.TempDir()}, MaxFiles: 1, MaxLines: 100, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
		ValidationReportHash: "vr", CandidateManifestHash: "cm",
	}
	report := validation.ValidationReport{
		SchemaVersion: validation.SchemaVersionReport,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		CandidateManifestHash: "cm", ExamActions: 0, CreatedAt: time.Now().UTC(),
	}
	hash, err := validation.BuildReport(cfgAsValidation(cfg), validation.CandidateBundle{
		Manifest: reversemine.CandidateManifest{ManifestHash: "cm", CandidatesHash: "c", LedgerHash: "l", CatalogHash: "cat"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	report = hash
	survivors, err := validation.BuildSurvivors(report)
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := BuildAttributionDecisions(survivors.Survivors, nil)
	if err != nil {
		t.Fatal(err)
	}
	mechReport, err := BuildMechanismReport(cfg, report, survivors, decisions)
	if err != nil {
		t.Fatal(err)
	}
	if mechReport.SurvivorCount != 0 {
		t.Fatalf("count=%d", mechReport.SurvivorCount)
	}
	wfReport, err := BuildStrictWFReport(mechReport, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := BuildFinalLeaves(mechReport, wfReport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves.Leaves) != 0 {
		t.Fatal("expected zero leaves")
	}
}

func cfgAsValidation(cfg RunConfig) validation.RunConfig {
	return validation.RunConfig{
		SchemaVersion: cfg.SchemaVersion, HistoryRoot: cfg.HistoryRoot,
		CandidateOutputDir: cfg.CandidateOutputDir, OutputDir: cfg.OutputDir,
		MapDirs: cfg.MapDirs, MaxFiles: cfg.MaxFiles, MaxLines: cfg.MaxLines,
		ProgressEvery: cfg.ProgressEvery, Generation: cfg.Generation,
		ProtocolHash: cfg.ProtocolHash, HistoryDataHash: cfg.HistoryDataHash,
		CandidateManifestHash: cfg.CandidateManifestHash,
	}
}

func TestValidationFailCannotEnterMechanismBinding(t *testing.T) {
	cell := profitmap.CellKey{
		SkyCoarse: "ret=low/rv=mid", Liquidity: "liq_6", SymbolClass: "major",
		Horizon: "1h", Direction: "long", CapitalTier: 30,
	}
	candidate, err := buildTestCandidate(1, cell, []reversemine.Condition{
		{FeatureKey: "symbol_return_1h", Op: reversemine.OpGTE, Threshold: 0, Quantile: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle := InputBundle{
		Validation: ValidationBundle{
			Report: validation.ValidationReport{
				CandidateManifestHash: "cm",
				Results: []validation.CandidateResult{{
					CanonicalHash: candidate.CanonicalHash, Status: validation.StatusValidationFail,
				}},
			},
			Survivors: validation.ValidationSurvivors{
				Survivors: []validation.SurvivorEntry{{
					CanonicalHash: candidate.CanonicalHash, Status: validation.SurvivorStatus,
				}},
			},
			Receipt: validation.ValidationReceipt{
				CatalogHash: protocol.HistoryDataHash,
				CandidateManifestHash: "cm",
				CandidatesHash: "c",
				LedgerHash: "l",
			},
		},
		Candidates: validation.CandidateBundle{
			Manifest: reversemine.CandidateManifest{
				ManifestHash: "cm", CandidatesHash: "c", LedgerHash: "l",
				FreezeBeforeValidation: true, CatalogHash: protocol.HistoryDataHash,
				HistoryDataHash: protocol.HistoryDataHash,
			},
		},
	}
	err = VerifyInputBinding(historydata.HistoryCatalog{DataHash: protocol.HistoryDataHash}, t.TempDir(), bundle, 1, 100)
	if err == nil || !strings.Contains(err.Error(), "VALIDATION_PASS") {
		t.Fatalf("expected validation pass guard, got %v", err)
	}
}

func TestReceiptTamperRejected(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: t.TempDir(), ValidationOutputDir: t.TempDir(),
		CandidateOutputDir: t.TempDir(), OutputDir: t.TempDir(),
		MapDirs: []string{t.TempDir()}, MaxFiles: 1, MaxLines: 100, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
		ValidationReportHash: "vr", CandidateManifestHash: "cm",
	}
	report, _ := BuildMechanismReport(cfg, validation.ValidationReport{
		SchemaVersion: validation.SchemaVersionReport, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, ReportHash: "vr", CandidateManifestHash: "cm",
		ExamActions: 0, CreatedAt: time.Now().UTC(),
	}, validation.ValidationSurvivors{
		SchemaVersion: validation.SchemaVersionSurvivors, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, ReportHash: "vr", ExamActions: 0,
	}, nil)
	wfReport, _ := BuildStrictWFReport(report, nil)
	leaves, _ := BuildFinalLeaves(report, wfReport, nil)
	stage := BuildStageSummary(ValidationBundle{
		Report: validation.ValidationReport{ReportHash: "vr"},
		Survivors: validation.ValidationSurvivors{SurvivorsHash: "vs"},
		Receipt: validation.ValidationReceipt{ReceiptHash: "vrec", CatalogHash: "cat"},
	}, report, wfReport, leaves)
	receipt, err := BuildReceipt(cfg, InputBundle{
		Validation: ValidationBundle{
			Report: validation.ValidationReport{ReportHash: "vr"},
			Survivors: validation.ValidationSurvivors{SurvivorsHash: "vs"},
			Receipt: validation.ValidationReceipt{
				ReceiptHash: "vrec", CatalogHash: "cat",
				MapManifestHashes: map[string]string{}, MapArtifactHashes: map[string]string{},
			},
		},
		Candidates: validation.CandidateBundle{
			Manifest: reversemine.CandidateManifest{
				ManifestHash: "cm", CandidatesHash: "c", LedgerHash: "l",
			},
		},
	}, report, wfReport, leaves, stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	receipt.MechanismReportHash = "tampered"
	if err := ValidateReceipt(receipt); err == nil {
		t.Fatal("tampered receipt must fail")
	}
}

func TestCheckpointTamperRejected(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: dir, ValidationOutputDir: dir, CandidateOutputDir: dir, OutputDir: dir,
		MapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
		ValidationReportHash: "vr", CandidateManifestHash: "cm",
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		CatalogHash: "abc", ValidationReportHash: "vr", ValidationSurvivorsHash: "vs",
		ValidationReceiptHash: "vrec", CandidateManifestHash: "cm",
		CandidatesHash: "c", LedgerHash: "l", OutputDir: dir,
		MapManifestHashes: map[string]string{"okx_1h": "hash"},
		WFStatsByHash: map[string]checkpointWFStats{},
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

func TestFinalLeavesJSONBindings(t *testing.T) {
	report := MechanismReport{ReportHash: "mr", ValidationReportHash: "vr", ExamActions: 0}
	wf := StrictWFReport{ReportHash: "wr", ExamActions: 0}
	leaves, err := BuildFinalLeaves(report, wf, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(leaves)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"leaves_hash"`) {
		t.Fatal("missing leaves_hash")
	}
}

func buildTestCandidate(ordinal int, cell profitmap.CellKey, conditions []reversemine.Condition) (reversemine.Candidate, error) {
	c := reversemine.Candidate{
		Ordinal: ordinal, ProfileID: profitmap.ProfileOKX1H, CellKey: cell,
		Conditions: conditions, Status: reversemine.CandidateStatus,
		SourceWinnerCount: 1, SourceWinnerIDsHash: "h", MapManifestHash: "m",
		WinnerSeedMode: reversemine.WinnerSeedMode, SearchSpaceID: reversemine.SearchSpaceID,
		DerivationAlgorithm: reversemine.DerivationAlgorithmVer,
	}
	hash, err := reversemine.CanonicalCandidateHash(c)
	if err != nil {
		return reversemine.Candidate{}, err
	}
	c.CanonicalHash = hash
	return c, nil
}
