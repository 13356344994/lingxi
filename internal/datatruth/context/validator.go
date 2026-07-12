package contextdata

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

type ValidateConfig struct {
	DayDir         string
	Date           string
	MaxOIStaleness time.Duration
	Now            func() time.Time
	OnProgress     func(Progress)
}

func ValidateDay(cfg ValidateConfig) (DailyVerdict, error) {
	if cfg.DayDir == "" {
		return DailyVerdict{}, fmt.Errorf("contextdata: day-dir is required")
	}
	dayStart, err := time.Parse("2006-01-02", cfg.Date)
	if err != nil {
		return DailyVerdict{}, fmt.Errorf("contextdata: invalid UTC date %q: %w", cfg.Date, err)
	}
	dayStart = dayStart.UTC()
	dayEnd := dayStart.Add(24 * time.Hour)
	if cfg.MaxOIStaleness <= 0 {
		cfg.MaxOIStaleness = 15 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	result := DailyVerdict{
		SchemaVersion: SchemaVersion,
		Date:          cfg.Date,
		Verdict:       VerdictPass,
		Manifests:     map[Kind]PartitionManifest{},
		CreatedAt:     cfg.Now().UTC(),
	}
	for _, kind := range RequiredKinds {
		path := filepath.Join(cfg.DayDir, string(kind)+".manifest.json")
		manifest, err := loadManifest(path)
		if err != nil {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, missingReason(kind))
			continue
		}
		result.Manifests[kind] = manifest
		if manifest.Date != cfg.Date || manifest.Kind != kind || !manifest.Completed || manifest.Verdict != VerdictPass {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, manifest.Reasons...)
			result.Reasons = append(result.Reasons, ReasonQualityInvalid)
		}
		if manifest.OutputFile == "" {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, missingReason(kind))
			continue
		}
		hash, err := hashFileProgress(manifest.OutputFile, func(bytesHashed int64) {
			if cfg.OnProgress != nil {
				cfg.OnProgress(Progress{Stage: "validate_hash", Kind: kind, BytesHashed: bytesHashed})
			}
		})
		if err != nil || hash != manifest.OutputHash {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, ReasonSourceIdentity)
		}
	}

	validatePITCoverage(&result, cfg.DayDir, dayStart)
	if oi, ok := result.Manifests[KindOI]; ok {
		if oi.Clocks.MaxReceiveTime.IsZero() || oi.Clocks.MaxReceiveTime.Before(dayEnd.Add(-cfg.MaxOIStaleness)) {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, ReasonOIStale)
		}
	}
	for _, kind := range []Kind{KindMids, KindDepth} {
		if manifest, ok := result.Manifests[kind]; ok {
			if manifest.Clocks.MinReceiveTime.After(dayStart.Add(15*time.Minute)) ||
				manifest.Clocks.MaxReceiveTime.Before(dayEnd.Add(-15*time.Minute)) {
				result.Verdict = VerdictFail
				result.Reasons = append(result.Reasons, ReasonIncompleteDay)
			}
		}
	}
	result.Normalize()
	return result, nil
}

func WriteDailyVerdict(path string, verdict DailyVerdict) error {
	verdict.Normalize()
	return saveJSONAtomic(path, verdict)
}

func validatePITCoverage(result *DailyVerdict, dayDir string, dayStart time.Time) {
	var instruments datatruth.InstrumentCatalog
	instrumentsValid := false
	if manifest, ok := result.Manifests[KindInstrument]; ok && manifest.OutputFile != "" {
		if err := readJSON(manifest.OutputFile, &instruments); err != nil || instruments.Validate() != nil || len(instruments.Versions) == 0 {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, ReasonPITMissing)
		} else {
			covered := false
			for _, version := range instruments.Versions {
				if !version.KnownFrom.After(dayStart) {
					covered = true
					break
				}
			}
			if !covered {
				result.Verdict = VerdictFail
				result.Reasons = append(result.Reasons, ReasonPITMissing)
			} else {
				instrumentsValid = true
			}
		}
	}
	if manifest, ok := result.Manifests[KindUniverse]; ok && manifest.OutputFile != "" {
		var catalog datatruth.UniverseCatalog
		if err := readJSON(manifest.OutputFile, &catalog); err != nil || catalog.Validate() != nil || len(catalog.Versions) == 0 {
			result.Verdict = VerdictFail
			result.Reasons = append(result.Reasons, ReasonUniverseMissing)
		} else {
			covered := false
			for _, version := range catalog.Versions {
				if !version.KnownFrom.After(dayStart) {
					covered = true
					break
				}
			}
			if !covered {
				result.Verdict = VerdictFail
				result.Reasons = append(result.Reasons, ReasonUniverseMissing)
			} else if instrumentsValid {
				ids := map[string]bool{}
				for _, version := range catalog.Versions {
					ids[version.UniverseID] = true
				}
				for id := range ids {
					universe, ok := catalog.UniverseAt(id, dayStart, dayStart)
					if !ok {
						continue
					}
					if err := datatruth.ValidateUniverseMembers(universe, instruments, dayStart, dayStart); err != nil {
						result.Verdict = VerdictFail
						result.Reasons = append(result.Reasons, ReasonPITMissing)
					}
				}
			}
		}
	}
	_ = dayDir
}

func missingReason(kind Kind) string {
	switch kind {
	case KindInstrument:
		return ReasonPITMissing
	case KindUniverse:
		return ReasonUniverseMissing
	case KindMids:
		return ReasonMidsMissing
	case KindOI:
		return ReasonOIMissing
	case KindXVenue:
		return ReasonXVenueMissing
	case KindDepth:
		return ReasonDepthMissing
	default:
		return ReasonSchemaInvalid
	}
}

func ManifestExists(dayDir string, kind Kind) bool {
	_, err := os.Stat(filepath.Join(dayDir, string(kind)+".manifest.json"))
	return err == nil
}
