package eventvalidation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// BuildReport constructs g6_validation_report.json payload.
func BuildReport(
	cfg RunConfig,
	bundle CardBundle,
	mapManifestHashes map[string]string,
	results []CardResult,
	ledger reverseevent.ExperimentLedger,
) (ValidationReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReport{}, err
	}
	pass, fail, inconclusive := 0, 0, 0
	for _, r := range results {
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
		SchemaVersion:     SchemaVersionReport,
		Generation:        protocol.Generation,
		ProtocolHash:      protocol.RouterProtocolHash,
		CardManifestHash:  bundle.Manifest.ManifestHash,
		EventCardHash:     bundle.Manifest.EventCardHash,
		ExperimentHash:    bundle.Manifest.ExperimentHash,
		MapManifestHashes: cloneStringMap(mapManifestHashes),
		CatalogHash:       bundle.Manifest.CatalogHash,
		ConfigHash:        cfgHash,
		CardCount:         len(results),
		PassCount:         pass,
		FailCount:         fail,
		InconclusiveCount: inconclusive,
		DiscoveryScreenFamily:     protocol.ValidationDiscoveryScreenFamily,
		DiscoveryScreenFamilySize: discoveryScreenFamilySize(ledger),
		Results:           results,
		ExamActions:       protocol.ExamActions,
		CreatedAt:         time.Now().UTC(),
	}
	hash, err := hashReport(report)
	if err != nil {
		return ValidationReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func discoveryScreenFamilySize(ledger reverseevent.ExperimentLedger) int {
	if len(ledger.Attempts) > 0 {
		return len(ledger.Attempts)
	}
	return 0
}

func frozenSizingFromResult(r CardResult) (conservative, stress float64) {
	baseTier := int(protocol.CapitalTiersUSDT[0])
	for _, tr := range r.TierResults {
		if tr.CapitalTier == baseTier {
			return tr.Firing.ConservativeMeanBPS, tr.Firing.StressConservativeMeanBPS
		}
	}
	if len(r.TierResults) > 0 {
		return r.TierResults[0].Firing.ConservativeMeanBPS, r.TierResults[0].Firing.StressConservativeMeanBPS
	}
	return 0, 0
}

func hashReport(r ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildSurvivors constructs g6_validation_survivors.json from pass results.
func BuildSurvivors(report ValidationReport) (ValidationSurvivors, error) {
	entries := make([]SurvivorEntry, 0)
	for _, r := range report.Results {
		if r.Status != StatusValidationPass {
			continue
		}
		conservative, stress := frozenSizingFromResult(r)
		entries = append(entries, SurvivorEntry{
			Ordinal:                         r.Ordinal,
			EventCardHash:                   r.EventCardHash,
			ProfileID:                       r.ProfileID,
			EventFamily:                     r.EventFamily,
			Horizon:                         r.Horizon,
			Direction:                       r.Direction,
			Status:                          SurvivorStatus,
			PayerStatus:                     reverseevent.PayerStatusUnexplained,
			TradeAllowed:                    false,
			ExamEligible:                    false,
			FrozenConservativeMeanBPS:       conservative,
			FrozenStressConservativeMeanBPS: stress,
			TierResults:                     r.TierResults,
		})
	}
	survivors := ValidationSurvivors{
		SchemaVersion:    SchemaVersionSurvivors,
		Generation:       protocol.Generation,
		ProtocolHash:     protocol.RouterProtocolHash,
		CardManifestHash: report.CardManifestHash,
		ReportHash:       report.ReportHash,
		Survivors:        entries,
		ExamActions:      protocol.ExamActions,
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

// BuildReceipt constructs g6_validation_receipt.json binding all provenance.
func BuildReceipt(
	cfg RunConfig,
	bundle CardBundle,
	mapManifestHashes, mapArtifactHashes, canonicalDataHashes map[string]string,
	report ValidationReport,
	survivors ValidationSurvivors,
	ledger reverseevent.ExperimentLedger,
) (ValidationReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		SchemaVersion:       SchemaVersionReceipt,
		Generation:          protocol.Generation,
		ProtocolHash:        protocol.RouterProtocolHash,
		HistoryDataHash:     protocol.HistoryDataHash,
		CardManifestHash:    bundle.Manifest.ManifestHash,
		EventCardHash:       bundle.Manifest.EventCardHash,
		ExperimentHash:      bundle.Manifest.ExperimentHash,
		MapManifestHashes:   cloneStringMap(mapManifestHashes),
		MapArtifactHashes:   cloneStringMap(mapArtifactHashes),
		CatalogHash:         bundle.Manifest.CatalogHash,
		ConfigHash:          cfgHash,
		ReportHash:          report.ReportHash,
		SurvivorsHash:       survivors.SurvivorsHash,
		DiscoveryScreenFamily:     protocol.ValidationDiscoveryScreenFamily,
		DiscoveryScreenFamilySize: discoveryScreenFamilySize(ledger),
		CanonicalDataHashes: cloneStringMap(canonicalDataHashes),
		ExamActions:         protocol.ExamActions,
		CreatedAt:           time.Now().UTC(),
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
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_validation_report.json"), report); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_validation_survivors.json"), survivors); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_validation_receipt.json"), receipt); err != nil {
		return err
	}
	return nil
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

// ValidateReport checks report self-hash integrity.
func ValidateReport(r ValidationReport) error {
	want, err := hashReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errReportHashMismatch
	}
	if r.ExamActions != protocol.ExamActions {
		return errExamActions
	}
	return nil
}

// ValidateSurvivors checks survivor self-hash and bindings.
func ValidateSurvivors(s ValidationSurvivors) error {
	want, err := hashSurvivors(s)
	if err != nil {
		return err
	}
	if s.SurvivorsHash == "" || s.SurvivorsHash != want {
		return errSurvivorsHashMismatch
	}
	for _, entry := range s.Survivors {
		if entry.TradeAllowed || entry.ExamEligible {
			return errSurvivorTradeExam
		}
		if entry.Status != SurvivorStatus || entry.PayerStatus != reverseevent.PayerStatusUnexplained {
			return errSurvivorStatus
		}
		if entry.FrozenConservativeMeanBPS <= 0 {
			return errSurvivorFrozenSizing
		}
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

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var (
	errReportHashMismatch    = datatruthError("eventvalidation: report hash mismatch")
	errSurvivorsHashMismatch = datatruthError("eventvalidation: survivors hash mismatch")
	errReceiptHashMismatch   = datatruthError("eventvalidation: receipt hash mismatch")
	errExamActions           = datatruthError("eventvalidation: exam_actions must be zero")
	errSurvivorTradeExam     = datatruthError("eventvalidation: survivors must not allow trade or exam")
	errSurvivorStatus        = datatruthError("eventvalidation: survivors must remain UNEXPLAINED")
	errSurvivorFrozenSizing  = datatruthError("eventvalidation: survivors must carry frozen sizing edge")
)

type datatruthError string

func (e datatruthError) Error() string { return string(e) }
