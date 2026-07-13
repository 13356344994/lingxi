package walkforward

import (
	"testing"

	g10validation "lingxi/internal/routerg10/validation"
)

func TestSkippedReportRetainsValidationAndExperimentBindings(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion:        SchemaVersionConfig,
		ValidationReportHash: "validation-hash",
		SurvivorsHash:        "survivors-hash",
	}
	validation := g10validation.ValidationReport{
		ReportHash:     "validation-hash",
		PlaybookHash:   "playbook-hash",
		ExperimentHash: "experiment-hash",
	}
	survivors := g10validation.ValidationSurvivors{
		SurvivorsHash: "survivors-hash",
	}
	wf, _, _, _, err := BuildSkippedReport(cfg, ReasonPlaybookUnavailable, validation, survivors)
	if err != nil {
		t.Fatal(err)
	}
	if wf.PlaybookHash != validation.PlaybookHash || wf.ExperimentHash != validation.ExperimentHash {
		t.Fatalf("skipped report lost playbook binding: %+v", wf)
	}
	if wf.ValidationReportHash != validation.ReportHash || wf.SurvivorsHash != survivors.SurvivorsHash {
		t.Fatalf("skipped report lost validation binding: %+v", wf)
	}
	if wf.ConfigHash == "" {
		t.Fatal("skipped report missing config hash")
	}
}
