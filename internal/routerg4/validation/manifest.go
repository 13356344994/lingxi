package validation

import (
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

// BuildReport constructs g4_validation_report.json payload.
func BuildReport(
	cfg RunConfig,
	candidateBundle CandidateBundle,
	mapManifestHashes map[string]string,
	results []CandidateResult,
) (ValidationReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReport{}, err
	}
	pass, fail, inconclusive := 0, 0, 0
	phenomenonPass := map[string]bool{}
	for _, r := range results {
		switch r.Status {
		case StatusValidationPass:
			pass++
			phenomenonPass[r.PhenomenonID] = true
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
		CandidateManifestHash:     candidateBundle.Manifest.ManifestHash,
		CandidatesHash:            candidateBundle.Manifest.CandidatesHash,
		LedgerHash:                candidateBundle.Manifest.LedgerHash,
		MapManifestHashes:         cloneStringMap(mapManifestHashes),
		CatalogHash:               candidateBundle.Manifest.CatalogHash,
		ConfigHash:                cfgHash,
		CandidateCount:            len(results),
		PassCount:                 pass,
		CandidatePassCount:        pass,
		FailCount:                 fail,
		InconclusiveCount:         inconclusive,
		UniquePhenomenonPassCount: len(phenomenonPass),
		Results:                   results,
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

func hashReport(r ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildSurvivors constructs validation_survivors.json from pass results.
func BuildSurvivors(report ValidationReport) (ValidationSurvivors, error) {
	entries := make([]SurvivorEntry, 0)
	for _, r := range report.Results {
		if r.Status != StatusValidationPass {
			continue
		}
		entries = append(entries, SurvivorEntry{
			Ordinal:       r.Ordinal,
			CanonicalHash: r.CanonicalHash,
			PhenomenonID:  r.PhenomenonID,
			ProfileID:     r.ProfileID,
			CellKey:       r.CellKey,
			Status:        SurvivorStatus,
			Conditions:    r.Candidate.Conditions,
			Firing:        r.Firing,
		})
	}
	survivors := ValidationSurvivors{
		SchemaVersion:         SchemaVersionSurvivors,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		CandidateManifestHash: report.CandidateManifestHash,
		ReportHash:            report.ReportHash,
		Survivors:             entries,
		ExamActions:           protocol.ExamActions,
	}
	hash, err := hashSurvivors(survivors)
	if err != nil {
		return ValidationSurvivors{}, err
	}
	survivors.SurvivorsHash = hash
	return survivors, nil
}

func hashSurvivors(s ValidationSurvivors) (string, error) {
	payload := s
	payload.SurvivorsHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildReceipt constructs validation_receipt.json binding all provenance.
func BuildReceipt(
	cfg RunConfig,
	candidateBundle CandidateBundle,
	mapManifestHashes, mapArtifactHashes, canonicalDataHashes map[string]string,
	report ValidationReport,
	survivors ValidationSurvivors,
) (ValidationReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		SchemaVersion:         SchemaVersionReceipt,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		CandidateManifestHash: candidateBundle.Manifest.ManifestHash,
		CandidatesHash:        candidateBundle.Manifest.CandidatesHash,
		LedgerHash:            candidateBundle.Manifest.LedgerHash,
		MapManifestHashes:     cloneStringMap(mapManifestHashes),
		MapArtifactHashes:     cloneStringMap(mapArtifactHashes),
		CatalogHash:           candidateBundle.Manifest.CatalogHash,
		ConfigHash:            cfgHash,
		ReportHash:            report.ReportHash,
		SurvivorsHash:         survivors.SurvivorsHash,
		CanonicalDataHashes:   cloneStringMap(canonicalDataHashes),
		ExamActions:           protocol.ExamActions,
		CreatedAt:             time.Now().UTC(),
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
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_validation_report.json"), report); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "validation_survivors.json"), survivors); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "validation_receipt.json"), receipt); err != nil {
		return err
	}
	return nil
}

// ValidateReport checks report self-hash integrity.
func ValidateReport(r ValidationReport) error {
	want, err := hashReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errReportHashMismatch
	}
	return nil
}

// ValidateSurvivors checks survivor self-hash and report binding.
func ValidateSurvivors(s ValidationSurvivors) error {
	want, err := hashSurvivors(s)
	if err != nil {
		return err
	}
	if s.SurvivorsHash == "" || s.SurvivorsHash != want {
		return errSurvivorsHashMismatch
	}
	if s.ExamActions != protocol.ExamActions {
		return errExamActions
	}
	return nil
}

// ValidateReceipt checks receipt self-hash integrity.
func ValidateReceipt(r ValidationReceipt) error {
	want, err := hashReceipt(r)
	if err != nil {
		return err
	}
	if r.ReceiptHash == "" || r.ReceiptHash != want {
		return errReceiptHashMismatch
	}
	if r.ExamActions != protocol.ExamActions {
		return errExamActions
	}
	return nil
}

var (
	errReportHashMismatch    = datatruthError("validation: report hash mismatch")
	errSurvivorsHashMismatch = datatruthError("validation: survivors hash mismatch")
	errReceiptHashMismatch   = datatruthError("validation: receipt hash mismatch")
	errExamActions           = datatruthError("validation: exam_actions must be zero")
)

type datatruthError string

func (e datatruthError) Error() string { return string(e) }

// VerifyCandidateFrozen rejects candidate set changes after discovery freeze.
func VerifyCandidateFrozen(manifest reversemine.CandidateManifest, candidates []reversemine.Candidate) error {
	if len(candidates) != manifest.ActualCandidateCount {
		return errFrozenCandidateCount
	}
	got, err := reversemine.HashCandidates(candidates)
	if err != nil {
		return err
	}
	if got != manifest.CandidatesHash {
		return errFrozenCandidateHash
	}
	return nil
}

var (
	errFrozenCandidateCount = datatruthError("validation: frozen candidate count mismatch")
	errFrozenCandidateHash  = datatruthError("validation: frozen candidates hash mismatch")
)
