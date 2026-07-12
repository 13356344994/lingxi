package walkforward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"lingxi/internal/datatruth"
	g10validation "lingxi/internal/routerg10/validation"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// VerifyValidationInputs loads and verifies validation report/survivors from validation dir.
func VerifyValidationInputs(validationDir, playbookManifestHash string) (g10validation.ValidationReport, g10validation.ValidationSurvivors, error) {
	report, err := loadValidationReport(validationDir)
	if err != nil {
		return g10validation.ValidationReport{}, g10validation.ValidationSurvivors{}, err
	}
	if report.PlaybookManifestHash != playbookManifestHash {
		return g10validation.ValidationReport{}, g10validation.ValidationSurvivors{}, errors.New("walkforward: validation manifest hash mismatch")
	}
	survivors, err := loadValidationSurvivors(validationDir)
	if err != nil {
		return g10validation.ValidationReport{}, g10validation.ValidationSurvivors{}, err
	}
	if survivors.ReportHash != report.ReportHash {
		return g10validation.ValidationReport{}, g10validation.ValidationSurvivors{}, errors.New("walkforward: survivors/report hash mismatch")
	}
	if survivors.PlaybookManifestHash != playbookManifestHash {
		return g10validation.ValidationReport{}, g10validation.ValidationSurvivors{}, errors.New("walkforward: survivors manifest hash mismatch")
	}
	return report, survivors, nil
}

func loadValidationReport(dir string) (g10validation.ValidationReport, error) {
	raw, err := os.ReadFile(fmt.Sprintf("%s/g10_validation_report.json", dir))
	if err != nil {
		return g10validation.ValidationReport{}, err
	}
	var report g10validation.ValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return g10validation.ValidationReport{}, err
	}
	if err := g10validation.ValidateReport(report); err != nil {
		return g10validation.ValidationReport{}, err
	}
	return report, nil
}

func loadValidationSurvivors(dir string) (g10validation.ValidationSurvivors, error) {
	raw, err := os.ReadFile(fmt.Sprintf("%s/g10_validation_survivors.json", dir))
	if err != nil {
		return g10validation.ValidationSurvivors{}, err
	}
	var survivors g10validation.ValidationSurvivors
	if err := json.Unmarshal(raw, &survivors); err != nil {
		return g10validation.ValidationSurvivors{}, err
	}
	if err := g10validation.ValidateSurvivors(survivors); err != nil {
		return g10validation.ValidationSurvivors{}, err
	}
	return survivors, nil
}

func hashValidationReport(r g10validation.ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidationPassed checks whether validation stage produced at least hash-verified report.
func ValidationPassed(report g10validation.ValidationReport) bool {
	return report.ReportHash != "" && report.SchemaVersion == g10validation.SchemaVersionReport &&
		report.ProtocolHash == g10protocol.RouterProtocolHash
}
