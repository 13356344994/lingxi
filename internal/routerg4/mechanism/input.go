package mechanism

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/validation"
)

// LoadValidationBundle reads and verifies sealed validation outputs.
func LoadValidationBundle(dir string) (ValidationBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ValidationBundle{}, err
	}
	read := func(name string, dst any) error {
		raw, err := os.ReadFile(filepath.Join(abs, name))
		if err != nil {
			return fmt.Errorf("mechanism: read %s: %w", name, err)
		}
		return json.Unmarshal(raw, dst)
	}
	var report validation.ValidationReport
	if err := read("g4_validation_report.json", &report); err != nil {
		return ValidationBundle{}, err
	}
	if err := validation.ValidateReport(report); err != nil {
		return ValidationBundle{}, fmt.Errorf("mechanism: validation report: %w", err)
	}
	var survivors validation.ValidationSurvivors
	if err := read("validation_survivors.json", &survivors); err != nil {
		return ValidationBundle{}, err
	}
	if err := validation.ValidateSurvivors(survivors); err != nil {
		return ValidationBundle{}, fmt.Errorf("mechanism: validation survivors: %w", err)
	}
	var receipt validation.ValidationReceipt
	if err := read("validation_receipt.json", &receipt); err != nil {
		return ValidationBundle{}, err
	}
	if err := validation.ValidateReceipt(receipt); err != nil {
		return ValidationBundle{}, fmt.Errorf("mechanism: validation receipt: %w", err)
	}
	if survivors.ReportHash != report.ReportHash {
		return ValidationBundle{}, errors.New("mechanism: survivors/report hash binding mismatch")
	}
	if receipt.ReportHash != report.ReportHash || receipt.SurvivorsHash != survivors.SurvivorsHash {
		return ValidationBundle{}, errors.New("mechanism: validation receipt binding mismatch")
	}
	if report.Generation != protocol.Generation || report.ProtocolHash != protocol.RouterProtocolHash {
		return ValidationBundle{}, errors.New("mechanism: validation report protocol mismatch")
	}
	return ValidationBundle{Dir: abs, Report: report, Survivors: survivors, Receipt: receipt}, nil
}

// VerifyInputBinding checks validation, catalog, maps, and candidate cross-links.
func VerifyInputBinding(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	bundle InputBundle,
	maxFiles, maxLines int,
) error {
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("mechanism: catalog data hash mismatch")
	}
	vr := bundle.Validation.Report
	vrec := bundle.Validation.Receipt
	if vr.CandidateManifestHash != bundle.Candidates.Manifest.ManifestHash {
		return errors.New("mechanism: validation/candidate manifest hash mismatch")
	}
	if vrec.CandidateManifestHash != bundle.Candidates.Manifest.ManifestHash {
		return errors.New("mechanism: validation receipt/candidate manifest mismatch")
	}
	if vrec.CandidatesHash != bundle.Candidates.Manifest.CandidatesHash {
		return errors.New("mechanism: validation receipt candidates hash mismatch")
	}
	if vrec.LedgerHash != bundle.Candidates.Manifest.LedgerHash {
		return errors.New("mechanism: validation receipt ledger hash mismatch")
	}
	if vrec.CatalogHash != catalog.DataHash {
		return errors.New("mechanism: validation receipt catalog hash mismatch")
	}
	if !bundle.Candidates.Manifest.FreezeBeforeValidation {
		return errors.New("mechanism: candidates must be frozen before validation")
	}
	if err := RejectTruncatedMaps(bundle.Maps); err != nil {
		return err
	}
	passByHash := map[string]bool{}
	for _, result := range vr.Results {
		if result.Status == validation.StatusValidationPass {
			passByHash[result.CanonicalHash] = true
		}
	}
	for _, survivor := range bundle.Validation.Survivors.Survivors {
		if !passByHash[survivor.CanonicalHash] {
			return fmt.Errorf("mechanism: survivor %s is not VALIDATION_PASS", survivor.CanonicalHash)
		}
	}
	return validation.VerifyInputBinding(catalog, historyRoot, bundle.Maps, bundle.Candidates, maxFiles, maxLines)
}

// RejectTruncatedMaps prevents TRUNCATED_SMOKE inputs from entering mechanism/WF.
func RejectTruncatedMaps(maps []validation.MapBundle) error {
	for _, mapBundle := range maps {
		if mapBundle.Manifest.SelectionTruncated {
			return fmt.Errorf(
				"mechanism: map %s is %s and cannot enter formal mechanism path",
				mapBundle.Manifest.ProfileID, protocol.TruncatedSmokeScope,
			)
		}
	}
	return nil
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.ValidationOutputDir == "" || c.CandidateOutputDir == "" || c.OutputDir == "" {
		return errors.New("mechanism: history_root, validation_output_dir, candidate_output_dir, and output_dir are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("mechanism: at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("mechanism: max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("mechanism: progress_every must be positive")
	}
	if c.ValidationReportHash == "" {
		return errors.New("mechanism: validation_report_hash is required")
	}
	if c.CandidateManifestHash == "" {
		return errors.New("mechanism: candidate_manifest_hash is required")
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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

// DefaultRunConfig builds config with protocol bindings.
func DefaultRunConfig(
	historyRoot, validationDir, candidateDir, outputDir string,
	mapDirs []string,
	maxFiles, maxLines int,
	validationReportHash, candidateManifestHash string,
) RunConfig {
	return RunConfig{
		SchemaVersion:         SchemaVersionConfig,
		HistoryRoot:           historyRoot,
		ValidationOutputDir:   validationDir,
		CandidateOutputDir:    candidateDir,
		OutputDir:             outputDir,
		MapDirs:               append([]string(nil), mapDirs...),
		MaxFiles:              maxFiles,
		MaxLines:              maxLines,
		ProgressEvery:         protocol.ScanProgressEveryLines,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		ValidationReportHash:  validationReportHash,
		CandidateManifestHash: candidateManifestHash,
	}
}
