package walkforward

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// PurgeEmbargoDuration returns max(profile lookback, event deadline) for one card.
func PurgeEmbargoDuration(spec eventmap.ProfileSpec, card reverseevent.FrozenEventCard) time.Duration {
	lookback := spec.MaxFeatureLookback
	deadline := spec.BarDuration
	if horizon, ok := horizonFor(spec, card.Horizon); ok {
		deadline = horizon.Duration
	}
	if lookback > deadline {
		return lookback
	}
	return deadline
}

func horizonFor(spec eventmap.ProfileSpec, label string) (profitmap.HorizonSpec, bool) {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h, true
		}
	}
	return profitmap.HorizonSpec{}, false
}

// BuildFolds splits sorted WF event timestamps into foldCount chronological folds
// with purge/embargo gaps enforced between folds.
func BuildFolds(timestamps []string, foldCount int, embargo time.Duration) ([]Fold, error) {
	if foldCount < 1 {
		return nil, errors.New("walkforward: fold count required")
	}
	if len(timestamps) < foldCount {
		return nil, fmt.Errorf("walkforward: insufficient WF timestamps %d for %d folds", len(timestamps), foldCount)
	}
	sort.Strings(timestamps)
	chunk := len(timestamps) / foldCount
	if chunk < 1 {
		return nil, errors.New("walkforward: fold chunk too small")
	}
	folds := make([]Fold, 0, foldCount)
	var prevEnd time.Time
	for i := 0; i < foldCount; i++ {
		start := i * chunk
		end := start + chunk
		if i == foldCount-1 {
			end = len(timestamps)
		}
		slice := timestamps[start:end]
		filtered := make([]string, 0, len(slice))
		for _, ts := range slice {
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, err
			}
			if i > 0 && embargo > 0 && !t.After(prevEnd.Add(embargo)) {
				continue
			}
			filtered = append(filtered, ts)
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("walkforward: fold %d empty after purge/embargo", i)
		}
		startTime, err := time.Parse(time.RFC3339Nano, filtered[0])
		if err != nil {
			return nil, err
		}
		endTime, err := time.Parse(time.RFC3339Nano, filtered[len(filtered)-1])
		if err != nil {
			return nil, err
		}
		prevEnd = endTime
		folds = append(folds, Fold{
			Index:      i,
			Timestamps: append([]string(nil), filtered...),
			StartTime:  startTime,
			EndTime:    endTime,
			EventCount: len(filtered),
		})
	}
	return folds, nil
}

// VerifyValidationInputs loads and verifies validation survivors for WF eligibility.
func VerifyValidationInputs(dir, cardManifestHash string) (eventvalidation.ValidationReport, eventvalidation.ValidationSurvivors, error) {
	report, err := loadValidationReport(dir)
	if err != nil {
		return eventvalidation.ValidationReport{}, eventvalidation.ValidationSurvivors{}, err
	}
	if report.ProtocolHash != protocol.RouterProtocolHash {
		return report, eventvalidation.ValidationSurvivors{}, errors.New("walkforward: validation protocol hash mismatch")
	}
	if report.CardManifestHash != cardManifestHash {
		return report, eventvalidation.ValidationSurvivors{}, errors.New("walkforward: validation card manifest hash mismatch")
	}
	survivors, err := loadValidationSurvivors(dir)
	if err != nil {
		return report, eventvalidation.ValidationSurvivors{}, err
	}
	if report.PassCount == 0 && len(survivors.Survivors) == 0 {
		return report, survivors, fmt.Errorf("walkforward: no validation survivors")
	}
	if survivors.ProtocolHash != protocol.RouterProtocolHash {
		return report, survivors, errors.New("walkforward: survivors protocol hash mismatch")
	}
	if survivors.CardManifestHash != cardManifestHash {
		return report, survivors, errors.New("walkforward: survivors card manifest hash mismatch")
	}
	if survivors.ReportHash != report.ReportHash {
		return report, survivors, errors.New("walkforward: survivors report hash mismatch")
	}
	if err := eventvalidation.ValidateSurvivors(survivors); err != nil {
		return report, survivors, err
	}
	if report.PassCount == 0 {
		return report, survivors, fmt.Errorf("walkforward: validation pass count zero")
	}
	if len(survivors.Survivors) == 0 {
		return report, survivors, fmt.Errorf("walkforward: no validation survivors")
	}
	if len(survivors.Survivors) != report.PassCount {
		return report, survivors, fmt.Errorf("walkforward: survivor count %d != pass count %d", len(survivors.Survivors), report.PassCount)
	}
	passCards := map[string]bool{}
	for _, r := range report.Results {
		if r.Status == eventvalidation.StatusValidationPass {
			passCards[r.EventCardHash] = true
		}
	}
	for _, entry := range survivors.Survivors {
		if !passCards[entry.EventCardHash] {
			return report, survivors, fmt.Errorf("walkforward: survivor %s not VALIDATION_PASS", entry.EventCardHash)
		}
		if entry.TradeAllowed || entry.ExamEligible {
			return report, survivors, fmt.Errorf("walkforward: survivor %s must not allow trade/exam", entry.EventCardHash)
		}
	}
	return report, survivors, nil
}

func loadValidationReport(dir string) (eventvalidation.ValidationReport, error) {
	raw, err := readJSONFile(dir, "g6_validation_report.json")
	if err != nil {
		return eventvalidation.ValidationReport{}, err
	}
	var report eventvalidation.ValidationReport
	if err := decodeJSON(raw, &report); err != nil {
		return eventvalidation.ValidationReport{}, fmt.Errorf("walkforward: validation report decode: %w", err)
	}
	want, err := hashValidationReport(report)
	if err != nil {
		return eventvalidation.ValidationReport{}, err
	}
	if report.ReportHash == "" || report.ReportHash != want {
		return eventvalidation.ValidationReport{}, errors.New("walkforward: validation report hash mismatch")
	}
	return report, nil
}

func loadValidationSurvivors(dir string) (eventvalidation.ValidationSurvivors, error) {
	raw, err := readJSONFile(dir, "g6_validation_survivors.json")
	if err != nil {
		return eventvalidation.ValidationSurvivors{}, err
	}
	var survivors eventvalidation.ValidationSurvivors
	if err := decodeJSON(raw, &survivors); err != nil {
		return eventvalidation.ValidationSurvivors{}, fmt.Errorf("walkforward: survivors decode: %w", err)
	}
	want, err := hashSurvivors(survivors)
	if err != nil {
		return eventvalidation.ValidationSurvivors{}, err
	}
	if survivors.SurvivorsHash == "" || survivors.SurvivorsHash != want {
		return eventvalidation.ValidationSurvivors{}, errors.New("walkforward: survivors hash mismatch")
	}
	return survivors, nil
}

func hashValidationReport(r eventvalidation.ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func hashSurvivors(s eventvalidation.ValidationSurvivors) (string, error) {
	payload := s
	payload.SurvivorsHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// RejectNonWalkForwardPartition rejects rows outside walk_forward partition.
func RejectNonWalkForwardPartition(partition string) error {
	if partition != eventmap.PartitionWalkFwd {
		return fmt.Errorf("walkforward: partition leak %q", partition)
	}
	return nil
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = SchemaVersionConfig
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.CardOutputDir == "" || c.ValidationDir == "" || c.OutputDir == "" {
		return errors.New("walkforward: history_root, card_output_dir, validation_dir, and output_dir are required")
	}
	if c.CardManifestHash == "" {
		return errors.New("walkforward: card_manifest_hash is required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("walkforward: at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("walkforward: max_files and max_lines must be positive")
	}
	if c.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("walkforward: protocol hash mismatch")
	}
	if c.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("walkforward: history data hash mismatch")
	}
	return nil
}

// DefaultRunConfig returns frozen WF config.
func DefaultRunConfig(historyRoot, cardOutputDir, validationDir, outputDir string, mapDirs []string) RunConfig {
	return RunConfig{
		SchemaVersion:   SchemaVersionConfig,
		HistoryRoot:     historyRoot,
		CardOutputDir:   cardOutputDir,
		ValidationDir:   validationDir,
		OutputDir:       outputDir,
		MapDirs:         append([]string(nil), mapDirs...),
		ProgressEvery:   1000,
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
}
