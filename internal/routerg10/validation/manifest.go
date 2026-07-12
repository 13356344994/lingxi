package validation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
)

// BuildReport constructs g10_validation_report.json payload.
func BuildReport(
	cfg RunConfig,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
	results []PlaybookResult,
	ledger playbook.ExperimentLedger,
) (ValidationReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReport{}, err
	}
	pass, fail, inconclusive := 0, 0, 0
	brainDegeneracyClear := true
	for _, r := range results {
		if r.BrainDegeneracy.Degenerate {
			brainDegeneracyClear = false
		}
		switch r.Status {
		case StatusValidationPass:
			pass++
		case StatusValidationFail:
			fail++
		default:
			inconclusive++
		}
	}
	report := ValidationReport{
		SchemaVersion:             SchemaVersionReport,
		Generation:                protocol.Generation,
		ProtocolHash:              protocol.RouterProtocolHash,
		PlaybookManifestHash:      bundle.Manifest.ManifestHash,
		PlaybookHash:              bundle.Manifest.PlaybookHash,
		ExperimentHash:            bundle.Manifest.ExperimentHash,
		MapManifestHashes:         cloneStringMap(mapManifestHashes),
		CatalogHash:               bundle.Manifest.CatalogHash,
		ConfigHash:                cfgHash,
		PlaybookCount:             len(results),
		PassCount:                 pass,
		FailCount:                 fail,
		InconclusiveCount:         inconclusive,
		DiscoveryScreenFamily:     protocol.ValidationDiscoveryScreenFamily,
		DiscoveryScreenFamilySize: discoveryScreenFamilySize(ledger),
		Results:                   results,
		BrainDegeneracyClear:      brainDegeneracyClear,
		ExamActions:               protocol.ExamActions,
		CreatedAt:                 time.Now().UTC(),
	}
	hash, err := hashReport(report)
	if err != nil {
		return ValidationReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func discoveryScreenFamilySize(ledger playbook.ExperimentLedger) int {
	if len(ledger.Attempts) > 0 {
		return len(ledger.Attempts)
	}
	return 0
}

func hashReport(r ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildSurvivors constructs g10_validation_survivors.json from pass results.
func BuildSurvivors(report ValidationReport) (ValidationSurvivors, error) {
	entries := make([]SurvivorEntry, 0)
	for _, r := range report.Results {
		if r.Status != StatusValidationPass {
			continue
		}
		conservative, stress := frozenSizingFromResult(r)
		entries = append(entries, SurvivorEntry{
			Ordinal: r.Ordinal, PlaybookHash: r.PlaybookHash, ProfileID: r.ProfileID,
			EventFamily: r.EventFamily, Horizon: r.Horizon, Status: SurvivorStatus,
			PayerStatus: protocol.UnexplainedPersonalEdgeStatus, TradeAllowed: false, ExamEligible: false,
			EligibleCapitalTiers: r.EligibleCapitalTiers, MaxCapacityUSDT: r.MaxCapacityUSDT,
			FrozenConservativeMeanBPS: conservative, FrozenStressConservativeMeanBPS: stress,
			TierResults: r.TierResults,
		})
	}
	survivors := ValidationSurvivors{
		SchemaVersion:        SchemaVersionSurvivors,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		PlaybookManifestHash: report.PlaybookManifestHash,
		ReportHash:           report.ReportHash,
		Survivors:            entries,
		ExamActions:          protocol.ExamActions,
	}
	hash, err := hashSurvivors(survivors)
	if err != nil {
		return ValidationSurvivors{}, err
	}
	survivors.SurvivorsHash = hash
	return survivors, nil
}

func frozenSizingFromResult(r PlaybookResult) (conservative, stress float64) {
	if r.FrozenConservativeMeanBPS != 0 || r.FrozenStressConservativeMeanBPS != 0 {
		return r.FrozenConservativeMeanBPS, r.FrozenStressConservativeMeanBPS
	}
	for _, tr := range r.TierResults {
		if tr.Status != StatusValidationPass {
			continue
		}
		if tr.FrozenConservativeMeanBPS != 0 {
			conservative = tr.FrozenConservativeMeanBPS
			stress = tr.StressConservativeMeanBPS
		}
	}
	return conservative, stress
}

func hashSurvivors(s ValidationSurvivors) (string, error) {
	payload := s
	payload.SurvivorsHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildReceipt constructs validation_receipt.json.
func BuildReceipt(
	cfg RunConfig,
	bundle PlaybookBundle,
	mapManifestHashes, mapArtifactHashes, canonicalDataHashes map[string]string,
	report ValidationReport,
	survivors ValidationSurvivors,
) (ValidationReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		SchemaVersion:             SchemaVersionReceipt,
		Generation:                protocol.Generation,
		ProtocolHash:              protocol.RouterProtocolHash,
		HistoryDataHash:           protocol.HistoryDataHash,
		PlaybookManifestHash:      bundle.Manifest.ManifestHash,
		PlaybookHash:              bundle.Manifest.PlaybookHash,
		ExperimentHash:            bundle.Manifest.ExperimentHash,
		MapManifestHashes:         cloneStringMap(mapManifestHashes),
		MapArtifactHashes:         cloneStringMap(mapArtifactHashes),
		CatalogHash:               bundle.Manifest.CatalogHash,
		ConfigHash:                cfgHash,
		ReportHash:                report.ReportHash,
		SurvivorsHash:             survivors.SurvivorsHash,
		DiscoveryScreenFamily:     report.DiscoveryScreenFamily,
		DiscoveryScreenFamilySize: report.DiscoveryScreenFamilySize,
		CanonicalDataHashes:       cloneStringMap(canonicalDataHashes),
		ExamActions:               protocol.ExamActions,
		CreatedAt:                 time.Now().UTC(),
	}
	hash, err := hashReceipt(receipt)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

func hashReceipt(r ValidationReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// WriteOutputs atomically writes validation artifacts.
func WriteOutputs(outputDir string, report ValidationReport, survivors ValidationSurvivors, receipt ValidationReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g10_validation_report.json"), report); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g10_validation_survivors.json"), survivors); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(outputDir, "validation_receipt.json"), receipt)
}

func writeJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ValidateReport checks report hash integrity.
func ValidateReport(r ValidationReport) error {
	if r.SchemaVersion != SchemaVersionReport {
		return errors.New("report schema mismatch")
	}
	want, err := hashReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errors.New("report hash mismatch")
	}
	return nil
}

// ValidateSurvivors checks survivors hash integrity.
func ValidateSurvivors(s ValidationSurvivors) error {
	if s.SchemaVersion != SchemaVersionSurvivors {
		return errors.New("survivors schema mismatch")
	}
	want, err := hashSurvivors(s)
	if err != nil {
		return err
	}
	if s.SurvivorsHash == "" || s.SurvivorsHash != want {
		return errors.New("survivors hash mismatch")
	}
	return nil
}
