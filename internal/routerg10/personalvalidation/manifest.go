package personalvalidation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalplaybook"
)

// BuildReport constructs g10_personal_validation_report.json payload.
func BuildReport(
	cfg RunConfig,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
	results []PlaybookResult,
	historyScope map[string]HistoryScopeSummary,
	formalValidation bool,
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
		SchemaVersion:        SchemaVersionReport,
		Generation:           Generation,
		ProtocolHash:         ProtocolHash,
		PlaybookManifestHash: bundle.Manifest.ManifestHash,
		PlaybookHash:         bundle.Manifest.PlaybookHash,
		ExperimentHash:       bundle.Manifest.ExperimentHash,
		ConstitutionHash:     cfg.ConstitutionHash,
		MapManifestHashes:    cloneStringMap(mapManifestHashes),
		HistoryScope:         historyScope,
		FormalValidation:     formalValidation,
		CatalogHash:          cfg.HistoryDataHash,
		ConfigHash:           cfgHash,
		PlaybookCount:        len(results),
		PassCount:            pass,
		FailCount:            fail,
		InconclusiveCount:    inconclusive,
		Results:              results,
		BrainDegeneracyClear: brainDegeneracyClear,
		ExamActions:          g10protocolv2.ExamActions,
		TradeAllowed:         false,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashReport(report)
	if err != nil {
		return ValidationReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func hashReport(r ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildSurvivors constructs g10_personal_validation_survivors.json from pass results.
func BuildSurvivors(report ValidationReport) (ValidationSurvivors, error) {
	entries := make([]SurvivorEntry, 0)
	for _, r := range report.Results {
		if r.Status != StatusValidationPass {
			continue
		}
		conservative, stress := frozenSizingFromResult(r)
		entries = append(entries, SurvivorEntry{
			Ordinal: r.Ordinal, PlaybookHash: r.PlaybookHash, ProfileID: r.ProfileID,
			EventFamily: r.EventFamily, PersonalTapeFamily: r.PersonalTapeFamily, Horizon: r.Horizon,
			Status: SurvivorStatus, Layers: r.Layers,
			EligibleCapitalTiers: r.EligibleCapitalTiers, MaxCapacityUSDT: r.MaxCapacityUSDT,
			FrozenConservativeMeanBPS: conservative, FrozenStressConservativeMeanBPS: stress,
			TierResults: r.TierResults, TradeAllowed: false, ExamEligible: false,
			ExamActions: g10protocolv2.ExamActions,
		})
	}
	survivors := ValidationSurvivors{
		SchemaVersion:        SchemaVersionSurvivors,
		Generation:           Generation,
		ProtocolHash:         ProtocolHash,
		PlaybookManifestHash: report.PlaybookManifestHash,
		ReportHash:           report.ReportHash,
		Survivors:            entries,
		ExamActions:          g10protocolv2.ExamActions,
		TradeAllowed:         false,
	}
	hash, err := hashSurvivors(survivors)
	if err != nil {
		return ValidationSurvivors{}, err
	}
	survivors.SurvivorsHash = hash
	return survivors, nil
}

func frozenSizingFromResult(r PlaybookResult) (conservative, stress float64) {
	for _, tr := range r.TierResults {
		if tr.Status != StatusValidationPass {
			continue
		}
		if tr.FrozenConservativeMeanBPS != 0 {
			return tr.FrozenConservativeMeanBPS, tr.StressConservativeMeanBPS
		}
	}
	return 0, 0
}

func hashSurvivors(s ValidationSurvivors) (string, error) {
	payload := s
	payload.SurvivorsHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildReceipt constructs personal_validation_receipt.json.
func BuildReceipt(
	cfg RunConfig,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
	canonicalDataHashes map[string]string,
	report ValidationReport,
	survivors ValidationSurvivors,
) (ValidationReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		SchemaVersion:            SchemaVersionReceipt,
		Generation:               Generation,
		ProtocolHash:             ProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		PlaybookManifestHash:     bundle.Manifest.ManifestHash,
		PlaybookHash:             bundle.Manifest.PlaybookHash,
		ExperimentHash:           bundle.Manifest.ExperimentHash,
		ConstitutionHash:         cfg.ConstitutionHash,
		ReverseTapesManifestHash: cfg.ReverseTapesManifestHash,
		AnnouncementManifestHash: cfg.AnnouncementManifestHash,
		MapManifestHashes:        cloneStringMap(mapManifestHashes),
		CatalogHash:              cfg.HistoryDataHash,
		ConfigHash:               cfgHash,
		ReportHash:               report.ReportHash,
		SurvivorsHash:            survivors.SurvivorsHash,
		CanonicalDataHashes:      cloneStringMap(canonicalDataHashes),
		ExamActions:              g10protocolv2.ExamActions,
		TradeAllowed:             false,
		CreatedAt:                time.Now().UTC(),
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

// WriteOutputs atomically writes personal validation artifacts.
func WriteOutputs(outputDir string, report ValidationReport, survivors ValidationSurvivors, receipt ValidationReceipt, layers LayerSummary) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputReportFile), report); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputSurvivorsFile), survivors); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputReceiptFile), receipt); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(outputDir, OutputLayerFile), layers)
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
	if r.TradeAllowed || r.ExamActions != 0 {
		return errors.New("exam/live must be false")
	}
	if r.FormalValidation && r.PassCount > 0 {
		for _, scope := range r.HistoryScope {
			if scope.Truncated || !scope.FullMarket {
				return errors.New("formal report cannot pass with truncated scope")
			}
		}
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
	if s.TradeAllowed || s.ExamActions != 0 {
		return errors.New("exam/live must be false")
	}
	return nil
}

// VerifyValidationInputs loads and verifies validation outputs for walk-forward consumption.
func VerifyValidationInputs(validationDir, playbookManifestHash string) (ValidationReport, ValidationSurvivors, error) {
	raw, err := os.ReadFile(filepath.Join(validationDir, OutputReportFile))
	if err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	var report ValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	if err := ValidateReport(report); err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	if report.PlaybookManifestHash != playbookManifestHash {
		return ValidationReport{}, ValidationSurvivors{}, errors.New("validation manifest hash mismatch")
	}
	raw, err = os.ReadFile(filepath.Join(validationDir, OutputSurvivorsFile))
	if err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	var survivors ValidationSurvivors
	if err := json.Unmarshal(raw, &survivors); err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	if err := ValidateSurvivors(survivors); err != nil {
		return ValidationReport{}, ValidationSurvivors{}, err
	}
	if survivors.ReportHash != report.ReportHash {
		return ValidationReport{}, ValidationSurvivors{}, errors.New("validation report/survivors hash mismatch")
	}
	return report, survivors, nil
}

// LoadPlaybookByHash finds one playbook from bundle by hash.
func LoadPlaybookByHash(bundle PlaybookBundle, hash string) (personalplaybook.PersonalWildPlaybook, bool) {
	for _, pb := range bundle.Playbooks {
		if pb.PlaybookHash == hash {
			return pb, true
		}
	}
	return personalplaybook.PersonalWildPlaybook{}, false
}
