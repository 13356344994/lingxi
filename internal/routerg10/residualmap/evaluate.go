package residualmap

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

// LoadFingerprint reads and verifies one personal execution fingerprint artifact.
func LoadFingerprint(path string) (FingerprintEnvelope, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FingerprintEnvelope{}, fmt.Errorf("read fingerprint %q: %w", path, err)
	}
	var fp fingerprint.PersonalExecutionFingerprint
	if err := json.Unmarshal(raw, &fp); err != nil {
		return FingerprintEnvelope{}, err
	}
	if err := fingerprint.ValidateFingerprintSchema(fp); err != nil {
		return FingerprintEnvelope{}, err
	}
	return FingerprintEnvelope{Fingerprint: fp, Path: path}, nil
}

// LoadMapBundles reads verified G6 event-map output directories.
func LoadMapBundles(dirs []string) ([]reverseevent.MapBundle, map[string]string, map[string]string, error) {
	bundles := make([]reverseevent.MapBundle, 0, len(dirs))
	manifestHashes := map[string]string{}
	artifactHashes := map[string]string{}
	for _, dir := range dirs {
		bundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return nil, nil, nil, err
		}
		bundles = append(bundles, bundle)
		manifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
		for name, hash := range bundle.Manifest.ArtifactHashes {
			artifactHashes[bundle.Manifest.ProfileID+"/"+name] = hash
		}
	}
	return bundles, manifestHashes, artifactHashes, nil
}

// EvaluateFromGross supports pure unit tests without bar replay.
func EvaluateFromGross(item EventWorkItem, gross float64, base CostBase, adj PersonalAdjustments, actualEquity float64) EvaluationResult {
	tiers := ComputeTierCosts(gross, base, adj, actualEquity)
	result := ClassifyResidual(tiers, item, adj)
	result.EventID = item.Event.EventID
	if item.Event.Direction != "" {
		result.Direction = item.Event.Direction
	}
	if item.Event.Horizon != "" {
		result.Horizon = item.Event.Horizon
	}
	result.DelayBars = item.Event.DelayBars
	return result
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

// HashResidualMap returns tamper-evident hash for one map artifact.
func HashResidualMap(m PersonalResidualMap) (string, error) {
	payload := m
	payload.MapHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func findDecisionBar(bars []eventmap.Bar, decision time.Time, barDuration time.Duration) int {
	want := decision.UTC()
	for i := range bars {
		closeTime := bars[i].OpenTime.Add(barDuration)
		if closeTime.Equal(want) {
			return i
		}
	}
	return -1
}

func horizonHoldHours(spec eventmap.ProfileSpec, horizon string) float64 {
	for _, h := range spec.Horizons {
		if h.Label == horizon {
			return h.Duration.Hours()
		}
	}
	return spec.BarDuration.Hours()
}
