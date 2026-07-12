package playbook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

// ResidualMapManifestSchema is the expected manifest schema from residualmap output.
// Playbook reads this format without importing residualmap/ to avoid concurrent edit conflicts.
const ResidualMapManifestSchema = "lingxi.routerg10.residual-map.manifest.v1"

// ResidualMapDiscoverySchema is the expected discovery partition artifact schema.
const ResidualMapDiscoverySchema = "lingxi.routerg10.residual-map.discovery.v1"

// NoCandidatesReason documents a sealed zero-playbook run with no residual winners to mine.
const NoCandidatesReason = "NO_CANDIDATES"

// ResidualMapManifest binds one profile's personal residual map output.
type ResidualMapManifest struct {
	SchemaVersion      string            `json:"schema_version"`
	Generation         string            `json:"generation"`
	ProtocolHash       string            `json:"protocol_hash"`
	HistoryDataHash    string            `json:"history_data_hash"`
	ProfileID          string            `json:"profile_id"`
	CatalogHash        string            `json:"catalog_hash"`
	ArtifactHashes     map[string]string `json:"artifact_hashes"`
	ThresholdsHash     string            `json:"thresholds_hash"`
	CandidateStatus    string            `json:"candidate_status"`
	ResidualTierLabels []string          `json:"residual_tier_labels"`
	ResidualFailClosedReason string      `json:"residual_fail_closed_reason,omitempty"`
	ManifestHash       string            `json:"manifest_hash"`
}

// ResidualCellKey identifies one personal residual aggregation bucket.
type ResidualCellKey struct {
	EventFamily string `json:"event_family"`
	Horizon     string `json:"horizon"`
	DelayBars   int    `json:"delay_bars"`
}

// ResidualWinnerEntry is one bounded personal residual winner for reverse playbook mining.
type ResidualWinnerEntry struct {
	EventID                   string    `json:"event_id"`
	Symbol                    string    `json:"symbol"`
	DecisionTime              time.Time `json:"decision_time"`
	EventFamily               string    `json:"event_family"`
	Horizon                   string    `json:"horizon"`
	DelayBars                 int       `json:"delay_bars"`
	UserTierConservativeBPS   float64   `json:"user_tier_conservative_net_bps"`
	EligibleCapitalTiers      []string  `json:"eligible_capital_tiers"`
	MaxCapacityUSDT           float64   `json:"max_capacity_usdt"`
	ShockCluster              string    `json:"shock_cluster"`
	PersonalResidualReason    string    `json:"personal_residual_reason"`
	LargeTierFailReason       string    `json:"large_tier_fail_reason"`
	MapManifestHash           string    `json:"map_manifest_hash,omitempty"`
}

// ResidualMapCell is one discovery cell with personal residual winners.
type ResidualMapCell struct {
	Key     ResidualCellKey       `json:"key"`
	Winners []ResidualWinnerEntry `json:"winners"`
}

// PartitionResidualMap is one profile discovery partition map.
type PartitionResidualMap struct {
	SchemaVersion string                       `json:"schema_version"`
	ProfileID     string                       `json:"profile_id"`
	Partition     string                       `json:"partition"`
	Cells         map[string]ResidualMapCell   `json:"cells"`
}

// ResidualMapBundle is one verified residual-map output directory.
type ResidualMapBundle struct {
	Dir        string
	Manifest   ResidualMapManifest
	Discovery  PartitionResidualMap
	Thresholds eventmap.FrozenThresholds
}

// ResidualWinnerGroup aggregates winners sharing one reverse playbook bucket.
type ResidualWinnerGroup struct {
	Key          string
	ProfileID    string
	ManifestHash string
	CellKey      ResidualCellKey
	ShockCluster string
	Winners      []ResidualWinnerEntry
	PriorityTier int
}

// LoadResidualMapBundle reads and verifies one G10 residual-map output directory.
func LoadResidualMapBundle(dir string) (ResidualMapBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	manifestPath := filepath.Join(abs, "residual_map_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return ResidualMapBundle{}, fmt.Errorf("read residual map manifest %q: %w", dir, err)
	}
	var manifest ResidualMapManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ResidualMapBundle{}, err
	}
	var rawManifest map[string]any
	if err := json.Unmarshal(raw, &rawManifest); err != nil {
		return ResidualMapBundle{}, err
	}
	want, err := hashRawResidualManifest(rawManifest)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return ResidualMapBundle{}, errors.New("residual map manifest hash mismatch")
	}
	if manifest.Generation != protocol.Generation || manifest.ProtocolHash != protocol.RouterProtocolHash {
		return ResidualMapBundle{}, errors.New("residual map manifest protocol mismatch")
	}
	if manifest.HistoryDataHash != protocol.HistoryDataHash {
		return ResidualMapBundle{}, errors.New("residual map manifest history data hash mismatch")
	}
	if manifest.SchemaVersion != ResidualMapManifestSchema {
		return ResidualMapBundle{}, errors.New("residual map manifest schema mismatch")
	}
	if manifest.CandidateStatus != "" && manifest.CandidateStatus != protocol.PersonalWildCandidateStatus {
		return ResidualMapBundle{}, fmt.Errorf("residual map candidate_status must be %s", protocol.PersonalWildCandidateStatus)
	}

	discoveryName := fmt.Sprintf("residual_map_discovery_%s.json", manifest.ProfileID)
	discoveryHash, ok := manifest.ArtifactHashes[discoveryName]
	if !ok || discoveryHash == "" {
		return ResidualMapBundle{}, fmt.Errorf("discovery artifact missing for %s", manifest.ProfileID)
	}
	discoveryPath := filepath.Join(abs, discoveryName)
	gotDiscovery, err := eventmap.HashFile(discoveryPath)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	if gotDiscovery != discoveryHash {
		return ResidualMapBundle{}, fmt.Errorf("discovery artifact hash mismatch for %s", manifest.ProfileID)
	}
	raw, err = os.ReadFile(discoveryPath)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	var discovery PartitionResidualMap
	if err := json.Unmarshal(raw, &discovery); err != nil {
		return ResidualMapBundle{}, err
	}
	if discovery.SchemaVersion != ResidualMapDiscoverySchema {
		return ResidualMapBundle{}, errors.New("residual discovery schema mismatch")
	}
	if discovery.ProfileID != manifest.ProfileID {
		return ResidualMapBundle{}, errors.New("residual discovery profile mismatch")
	}

	thresholdsName := "residual_map_thresholds.json"
	thresholdsHash, ok := manifest.ArtifactHashes[thresholdsName]
	if !ok || thresholdsHash == "" {
		return ResidualMapBundle{}, errors.New("residual thresholds artifact missing")
	}
	thresholdsPath := filepath.Join(abs, thresholdsName)
	gotThresholds, err := eventmap.HashFile(thresholdsPath)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	if gotThresholds != thresholdsHash {
		return ResidualMapBundle{}, errors.New("residual thresholds hash mismatch")
	}
	raw, err = os.ReadFile(thresholdsPath)
	if err != nil {
		return ResidualMapBundle{}, err
	}
	var thresholds eventmap.FrozenThresholds
	if err := json.Unmarshal(raw, &thresholds); err != nil {
		return ResidualMapBundle{}, err
	}
	if thresholds.ProfileID != manifest.ProfileID {
		return ResidualMapBundle{}, errors.New("residual thresholds profile mismatch")
	}

	return ResidualMapBundle{
		Dir: abs, Manifest: manifest, Discovery: discovery, Thresholds: thresholds,
	}, nil
}

// collectResidualFailClosedReason returns one sealed fail-closed reason shared by all bundles.
func collectResidualFailClosedReason(bundles []ResidualMapBundle) (string, error) {
	var reason string
	for _, bundle := range bundles {
		got := bundle.Manifest.ResidualFailClosedReason
		if got == "" {
			continue
		}
		if reason == "" {
			reason = got
			continue
		}
		if reason != got {
			return "", errors.New("residual fail_closed_reason mismatch across profiles")
		}
	}
	return reason, nil
}

// resolveNoCandidatesBinding seals zero-playbook reasons from verified residual manifests.
func resolveNoCandidatesBinding(
	bundles []ResidualMapBundle,
	winnerSeedCount int,
	playbookCount int,
) (noCandidatesReason string, residualFailClosedReason string, err error) {
	residualFailClosedReason, err = collectResidualFailClosedReason(bundles)
	if err != nil {
		return "", "", err
	}
	if residualFailClosedReason != "" && winnerSeedCount > 0 {
		return "", "", errors.New("residual fail_closed_reason set with residual winners present")
	}
	if playbookCount == 0 && winnerSeedCount == 0 && residualFailClosedReason != "" {
		return NoCandidatesReason, residualFailClosedReason, nil
	}
	if playbookCount > 0 && residualFailClosedReason != "" {
		return "", "", errors.New("residual fail_closed_reason set with playbooks present")
	}
	return "", "", nil
}

func hashRawResidualManifest(payload map[string]any) (string, error) {
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	copyPayload["manifest_hash"] = ""
	hash, _, err := datatruth.HashJSON(copyPayload)
	return hash, err
}
