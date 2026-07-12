package walkforward

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// BuildFolds splits sorted WF event timestamps into foldCount chronological folds with embargo.
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
			Index: i, Timestamps: filtered, StartTime: startTime, EndTime: endTime, EventCount: len(filtered),
		})
	}
	return folds, nil
}

func PartitionEventsByFold(events []WFEvent, folds []Fold) map[int][]WFEvent {
	out := map[int][]WFEvent{}
	foldTS := map[int]map[string]bool{}
	for _, fold := range folds {
		set := map[string]bool{}
		for _, ts := range fold.Timestamps {
			set[ts] = true
		}
		foldTS[fold.Index] = set
	}
	for _, ev := range events {
		ts := ev.DecisionTime.UTC().Format(time.RFC3339Nano)
		for idx, set := range foldTS {
			if set[ts] {
				out[idx] = append(out[idx], ev)
				break
			}
		}
	}
	return out
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = SchemaVersionConfig
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.PlaybookOutputDir == "" || c.ValidationDir == "" || c.OutputDir == "" {
		return errors.New("walkforward: history_root, playbook_output_dir, validation_dir, and output_dir are required")
	}
	if c.PlaybookManifestHash == "" {
		return errors.New("walkforward: playbook_manifest_hash is required")
	}
	if len(c.ResidualMapDirs) == 0 {
		return errors.New("walkforward: at least one residual-map-dir is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("walkforward: max_files and max_lines must be positive")
	}
	if c.ProtocolHash != g10protocol.RouterProtocolHash {
		return errors.New("walkforward: protocol hash mismatch")
	}
	if c.HistoryDataHash != g10protocol.HistoryDataHash {
		return errors.New("walkforward: history data hash mismatch")
	}
	if c.FingerprintPath == "" || c.FingerprintHash == "" {
		return errors.New("walkforward: fingerprint_path and fingerprint_hash are required")
	}
	return nil
}

// DefaultRunConfig returns frozen WF config.
func DefaultRunConfig(historyRoot, playbookDir, validationDir, outputDir string, mapDirs []string) RunConfig {
	return RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: historyRoot, PlaybookOutputDir: playbookDir,
		ValidationDir: validationDir, OutputDir: outputDir, ResidualMapDirs: append([]string(nil), mapDirs...),
		ProgressEvery: 1000, Generation: g10protocol.Generation, ProtocolHash: g10protocol.RouterProtocolHash,
		HistoryDataHash: g10protocol.HistoryDataHash,
	}
}

// RejectPartitionLeak ensures event is walk_forward only.
func RejectPartitionLeak(decision time.Time, boundaries eventmap.Boundaries) error {
	if boundaries.PartitionAt(decision) != eventmap.PartitionWalkFwd {
		return fmt.Errorf("walkforward: partition leak at %s", decision.UTC().Format(time.RFC3339Nano))
	}
	return nil
}
