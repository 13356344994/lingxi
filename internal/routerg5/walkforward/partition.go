package walkforward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/portfoliosim"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// LoadWalkForwardFeatures reads walk_forward-partition feature shards only.
func LoadWalkForwardFeatures(dir string) (map[string][]features.DecisionContext, features.ContextManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "feature_context_manifest.json"))
	if err != nil {
		return nil, features.ContextManifest{}, err
	}
	var manifest features.ContextManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, features.ContextManifest{}, err
	}
	if manifest.ProtocolHash != g5protocol.RouterProtocolHash {
		return nil, features.ContextManifest{}, errors.New("walkforward: feature protocol hash mismatch")
	}
	if manifest.ExamActions != g5protocol.ExamActions {
		return nil, features.ContextManifest{}, errors.New("walkforward: feature exam_actions must be 0")
	}
	byTimeSymbol := map[string][]features.DecisionContext{}
	for _, shard := range manifest.Shards {
		rows, err := features.ReadContextShard(filepath.Join(dir, shard.FileName))
		if err != nil {
			return nil, features.ContextManifest{}, err
		}
		for _, row := range rows {
			if row.Partition != outcome.PartitionWalkFwd {
				continue
			}
			if err := features.RejectForbiddenContext(row.Features); err != nil {
				return nil, features.ContextManifest{}, err
			}
			key := row.DecisionTime.UTC().Format(time.RFC3339Nano)
			byTimeSymbol[key] = append(byTimeSymbol[key], row)
		}
	}
	return byTimeSymbol, manifest, nil
}

// RejectNonWalkForward ensures WF replay never reads validation/discovery rows.
func RejectNonWalkForward(rows []features.DecisionContext) error {
	for _, row := range rows {
		if row.Partition != outcome.PartitionWalkFwd {
			return fmt.Errorf("walkforward: partition leak %q at %s", row.Partition, row.DecisionTime.UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}

// RejectValidationPartition rejects validation rows in WF replay.
func RejectValidationPartition(rows []features.DecisionContext) error {
	for _, row := range rows {
		if row.Partition == outcome.PartitionValidation {
			return fmt.Errorf("walkforward: validation partition forbidden")
		}
	}
	return nil
}

// PurgeEmbargoDuration returns max(lookback, action_horizon) for one profile.
func PurgeEmbargoDuration(profile features.Profile) time.Duration {
	lookback := profile.MaxFeatureLookback
	horizon := profile.BarDuration
	if len(profile.Horizons) > 0 {
		horizon = profile.Horizons[len(profile.Horizons)-1].Duration
	}
	if lookback > horizon {
		return lookback
	}
	return horizon
}

// BuildFolds splits sorted WF timestamps into foldCount continuous chronological folds
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
			RowCount:   len(filtered),
		})
	}
	return folds, nil
}

// VerifyValidationReport loads and verifies a signed VALIDATION_PASS report.
func VerifyValidationReport(dir string, policyHash, featureHash, experimentHash string) (portfoliosim.ValidationReport, error) {
	path := filepath.Join(dir, "g5_validation_report.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return portfoliosim.ValidationReport{}, err
	}
	var report portfoliosim.ValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return portfoliosim.ValidationReport{}, err
	}
	want, err := hashValidationReport(report)
	if err != nil {
		return portfoliosim.ValidationReport{}, err
	}
	if report.ReportHash == "" || report.ReportHash != want {
		return portfoliosim.ValidationReport{}, errors.New("walkforward: validation report hash mismatch")
	}
	if report.ProtocolHash != g5protocol.RouterProtocolHash {
		return portfoliosim.ValidationReport{}, errors.New("walkforward: validation protocol hash mismatch")
	}
	if report.PolicyHash != policyHash || report.FeatureHash != featureHash || report.ExperimentHash != experimentHash {
		return portfoliosim.ValidationReport{}, errors.New("walkforward: validation policy hash chain mismatch")
	}
	if report.Status != portfoliosim.StatusValidationPass {
		return report, fmt.Errorf("walkforward: validation status %q", report.Status)
	}
	return report, nil
}

func hashValidationReport(r portfoliosim.ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = SchemaVersionConfig
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}
