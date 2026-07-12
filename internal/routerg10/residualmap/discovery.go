package residualmap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

const (
	DiscoverySchemaVersion = "lingxi.routerg10.residual-map.discovery.v1"
	ThresholdsFileName     = "residual_map_thresholds.json"
)

// ProfileBundleManifest is the playbook-compatible per-profile manifest.
type ProfileBundleManifest struct {
	SchemaVersion      string            `json:"schema_version"`
	Generation         string            `json:"generation"`
	ProtocolHash       string            `json:"protocol_hash"`
	HistoryDataHash    string            `json:"history_data_hash"`
	ProfileID          string            `json:"profile_id"`
	CatalogHash        string            `json:"catalog_hash"`
	FingerprintHash    string            `json:"fingerprint_hash"`
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

// ResidualWinnerEntry is one bounded personal residual winner.
type ResidualWinnerEntry struct {
	EventID                 string    `json:"event_id"`
	Symbol                  string    `json:"symbol"`
	DecisionTime            time.Time `json:"decision_time"`
	EventFamily             string    `json:"event_family"`
	Horizon                 string    `json:"horizon"`
	DelayBars               int       `json:"delay_bars"`
	UserTierConservativeBPS float64   `json:"user_tier_conservative_net_bps"`
	EligibleCapitalTiers    []string  `json:"eligible_capital_tiers"`
	MaxCapacityUSDT         float64   `json:"max_capacity_usdt"`
	ShockCluster            string    `json:"shock_cluster"`
	PersonalResidualReason  string    `json:"personal_residual_reason"`
	LargeTierFailReason     string    `json:"large_tier_fail_reason"`
	MapManifestHash         string    `json:"map_manifest_hash,omitempty"`
}

// ResidualMapCell is one discovery cell with personal residual winners.
type ResidualMapCell struct {
	Key     ResidualCellKey       `json:"key"`
	Winners []ResidualWinnerEntry `json:"winners"`
}

// PartitionResidualMap is one profile discovery partition map.
type PartitionResidualMap struct {
	SchemaVersion string                     `json:"schema_version"`
	ProfileID     string                     `json:"profile_id"`
	Partition     string                     `json:"partition"`
	Cells         map[string]ResidualMapCell `json:"cells"`
}

// BuildDiscoveryMap groups accepted opportunities into playbook-compatible cells.
func BuildDiscoveryMap(profileID string, opportunities []ResidualOpportunity) PartitionResidualMap {
	cells := map[string]ResidualMapCell{}
	for _, opp := range opportunities {
		if opp.ProfileID != profileID {
			continue
		}
		key := ResidualCellKey{
			EventFamily: opp.EventFamily,
			Horizon:     opp.Horizon,
			DelayBars:   opp.DelayBars,
		}
		cellID := fmt.Sprintf("%s|%s|%d", key.EventFamily, key.Horizon, key.DelayBars)
		cell := cells[cellID]
		cell.Key = key
		cell.Winners = append(cell.Winners, opportunityToWinner(opp))
		cells[cellID] = cell
	}
	return PartitionResidualMap{
		SchemaVersion: DiscoverySchemaVersion,
		ProfileID:     profileID,
		Partition:     eventmap.PartitionDiscovery,
		Cells:         cells,
	}
}

func opportunityToWinner(opp ResidualOpportunity) ResidualWinnerEntry {
	conservative := bestUserTierConservative(opp.TierCosts)
	failReason := ""
	if len(opp.LargeTierFailReasons) > 0 {
		failReason = opp.LargeTierFailReasons[0]
	}
	return ResidualWinnerEntry{
		EventID:                 opp.EventID,
		Symbol:                  opp.Symbol,
		DecisionTime:            opp.DecisionTime,
		EventFamily:             opp.EventFamily,
		Horizon:                 opp.Horizon,
		DelayBars:               opp.DelayBars,
		UserTierConservativeBPS: conservative,
		EligibleCapitalTiers:    append([]string(nil), opp.EligibleCapitalTiers...),
		MaxCapacityUSDT:           opp.MaxCapacityUSDT,
		ShockCluster:              opp.ShockCluster,
		PersonalResidualReason:    opp.CapacityCliffReason,
		LargeTierFailReason:       failReason,
		MapManifestHash:           opp.MapManifestHash,
	}
}

func bestUserTierConservative(tiers map[string]TierCostBreakdown) float64 {
	best := -1e18
	for _, label := range UserTierLabels {
		tier, ok := tiers[label]
		if !ok {
			continue
		}
		if tier.ConservativeBPS > best {
			best = tier.ConservativeBPS
		}
	}
	if best == -1e18 {
		return 0
	}
	return best
}

// BuildProfileBundleManifest seals one profile bundle for playbook consumption.
func BuildProfileBundleManifest(
	profileID string,
	catalogHash string,
	fingerprintHash string,
	artifactHashes map[string]string,
	thresholdsHash string,
	residualFailClosedReason string,
) (ProfileBundleManifest, error) {
	m := ProfileBundleManifest{
		SchemaVersion:      SchemaVersionManifest,
		Generation:         Generation,
		ProtocolHash:       g10protocol.RouterProtocolHash,
		HistoryDataHash:    g10protocol.HistoryDataHash,
		ProfileID:          profileID,
		CatalogHash:        catalogHash,
		FingerprintHash:    fingerprintHash,
		ArtifactHashes:     copyStringMap(artifactHashes),
		ThresholdsHash:     thresholdsHash,
		CandidateStatus:    CandidateStatus,
		ResidualTierLabels: append([]string(nil), TierLabels...),
		ResidualFailClosedReason: residualFailClosedReason,
	}
	hash, err := hashProfileBundleManifest(m)
	if err != nil {
		return ProfileBundleManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashProfileBundleManifest(m ProfileBundleManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// WriteProfileBundles writes per-profile discovery/manifest/thresholds for playbook input.
func WriteProfileBundles(
	outputDir string,
	catalogHash string,
	fingerprintHash string,
	opportunities []ResidualOpportunity,
	bundles []reverseevent.MapBundle,
	residualFailClosedReason string,
) (map[string]string, error) {
	byProfile := map[string][]ResidualOpportunity{}
	for _, opp := range opportunities {
		byProfile[opp.ProfileID] = append(byProfile[opp.ProfileID], opp)
	}
	allHashes := map[string]string{}
	for _, bundle := range bundles {
		profileID := bundle.Manifest.ProfileID
		profileDir := filepath.Join(outputDir, profileID)
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			return nil, err
		}
		discovery := BuildDiscoveryMap(profileID, byProfile[profileID])
		discoveryName := fmt.Sprintf("residual_map_discovery_%s.json", profileID)
		discoveryPath := filepath.Join(profileDir, discoveryName)
		if err := writeJSON(discoveryPath, discovery); err != nil {
			return nil, err
		}
		discoveryHash, err := HashFile(discoveryPath)
		if err != nil {
			return nil, err
		}
		thresholds := bundle.Thresholds
		thresholdsPath := filepath.Join(profileDir, ThresholdsFileName)
		if err := writeJSON(thresholdsPath, thresholds); err != nil {
			return nil, err
		}
		thresholdsHash, err := HashFile(thresholdsPath)
		if err != nil {
			return nil, err
		}
		artifactHashes := map[string]string{
			discoveryName:        discoveryHash,
			ThresholdsFileName: thresholdsHash,
		}
		manifest, err := BuildProfileBundleManifest(
			profileID, catalogHash, fingerprintHash, artifactHashes, thresholdsHash, residualFailClosedReason,
		)
		if err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(profileDir, ManifestFileName), manifest); err != nil {
			return nil, err
		}
		manifestHash, err := HashFile(filepath.Join(profileDir, ManifestFileName))
		if err != nil {
			return nil, err
		}
		allHashes[profileID+"/"+ManifestFileName] = manifestHash
		allHashes[profileID+"/"+discoveryName] = discoveryHash
		allHashes[profileID+"/"+ThresholdsFileName] = thresholdsHash
	}
	return allHashes, nil
}

// LoadProfileBundleManifest reads one playbook-compatible profile manifest.
func LoadProfileBundleManifest(path string) (ProfileBundleManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ProfileBundleManifest{}, err
	}
	var m ProfileBundleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return ProfileBundleManifest{}, err
	}
	want, err := hashProfileBundleManifest(m)
	if err != nil {
		return ProfileBundleManifest{}, err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return ProfileBundleManifest{}, fmt.Errorf("residualmap: profile manifest hash mismatch")
	}
	return m, nil
}
