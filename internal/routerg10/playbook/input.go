package playbook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

// VerifyCatalogBinding checks catalog hash and canonical primary files from residual map manifests.
func VerifyCatalogBinding(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	bundles []ResidualMapBundle,
	maxFiles, maxLines int,
	allowTruncatedSmoke bool,
) error {
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("catalog data hash mismatch")
	}
	seenProfiles := map[string]bool{}
	for _, bundle := range bundles {
		if bundle.Manifest.CatalogHash != catalog.DataHash {
			return fmt.Errorf("residual map %s catalog hash mismatch", bundle.Manifest.ProfileID)
		}
		if seenProfiles[bundle.Manifest.ProfileID] {
			return fmt.Errorf("duplicate profile residual map %q", bundle.Manifest.ProfileID)
		}
		seenProfiles[bundle.Manifest.ProfileID] = true
		_ = allowTruncatedSmoke
		selection, err := eventmap.SelectPrimaryFiles(
			catalog, bundle.Manifest.ProfileID, maxFiles, 0,
		)
		if err != nil {
			return err
		}
		for _, file := range selection.Files {
			if err := eventmap.VerifyCatalogFile(catalog, historyRoot, file.Path); err != nil {
				return fmt.Errorf("canonical file %s: %w", file.Path, err)
			}
		}
		btcRel := bundle.Manifest.ProfileID + "/BTC-USDT-SWAP.jsonl"
		if bundle.Manifest.ProfileID == eventmap.ProfileOKX1H {
			btcRel = "okx_1h/BTC-USDT-SWAP.jsonl"
		} else if bundle.Manifest.ProfileID == eventmap.ProfileOKX5M {
			btcRel = "okx_5m/BTC-USDT-SWAP.jsonl"
		}
		if err := eventmap.VerifyCatalogFile(catalog, historyRoot, btcRel); err != nil {
			return err
		}
		if _, err := eventmap.LoadBars(eventmap.CatalogFilePath(historyRoot, btcRel), maxLines); err != nil {
			return fmt.Errorf("load BTC bars: %w", err)
		}
	}
	return nil
}

// CollectResidualGroups flattens personal residual winners into reverse-derivation buckets.
func CollectResidualGroups(bundles []ResidualMapBundle) []ResidualWinnerGroup {
	byKey := map[string]*ResidualWinnerGroup{}
	for _, bundle := range bundles {
		for _, cell := range bundle.Discovery.Cells {
			if len(cell.Winners) == 0 {
				continue
			}
			for _, winner := range cell.Winners {
				if winner.UserTierConservativeBPS < protocol.PersonalConservativeExpectedNetMin {
					continue
				}
				cluster := normalizeShockCluster(winner.ShockCluster)
				key := groupKey(bundle.Manifest.ProfileID, cell.Key, cluster)
				group, ok := byKey[key]
				if !ok {
					group = &ResidualWinnerGroup{
						Key:          key,
						ProfileID:    bundle.Manifest.ProfileID,
						ManifestHash: bundle.Manifest.ManifestHash,
						CellKey:      cell.Key,
						ShockCluster: cluster,
						PriorityTier: winnerPriorityTier(winner.UserTierConservativeBPS),
					}
					byKey[key] = group
				}
				w := winner
				if w.MapManifestHash == "" {
					w.MapManifestHash = bundle.Manifest.ManifestHash
				}
				if w.ShockCluster == "" {
					w.ShockCluster = cluster
				}
				group.Winners = append(group.Winners, w)
				tier := winnerPriorityTier(winner.UserTierConservativeBPS)
				if tier < group.PriorityTier {
					group.PriorityTier = tier
				}
			}
		}
	}
	groups := make([]ResidualWinnerGroup, 0, len(byKey))
	for _, group := range byKey {
		sort.Slice(group.Winners, func(i, j int) bool {
			return winnerLess(group.Winners[i], group.Winners[j])
		})
		groups = append(groups, *group)
	}
	sortGroups(groups)
	return groups
}

func normalizeShockCluster(cluster string) string {
	if cluster == "" {
		return "none"
	}
	return cluster
}

func groupKey(profileID string, key ResidualCellKey, shockCluster string) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s",
		profileID, key.EventFamily, key.Horizon, key.DelayBars, shockCluster)
}

func winnerPriorityTier(conservativeBPS float64) int {
	switch {
	case conservativeBPS >= 20:
		return 0
	case conservativeBPS >= 10:
		return 1
	case conservativeBPS >= 5:
		return 2
	default:
		return 3
	}
}

func winnerLess(a, b ResidualWinnerEntry) bool {
	if a.UserTierConservativeBPS != b.UserTierConservativeBPS {
		return a.UserTierConservativeBPS > b.UserTierConservativeBPS
	}
	if !a.DecisionTime.Equal(b.DecisionTime) {
		return a.DecisionTime.Before(b.DecisionTime)
	}
	if a.Symbol != b.Symbol {
		return a.Symbol < b.Symbol
	}
	return a.EventID < b.EventID
}

func sortGroups(groups []ResidualWinnerGroup) {
	sort.Slice(groups, func(i, j int) bool { return groupLess(groups[i], groups[j]) })
}

func groupLess(a, b ResidualWinnerGroup) bool {
	if a.PriorityTier != b.PriorityTier {
		return a.PriorityTier < b.PriorityTier
	}
	if a.ProfileID != b.ProfileID {
		return a.ProfileID < b.ProfileID
	}
	if a.CellKey.EventFamily != b.CellKey.EventFamily {
		return a.CellKey.EventFamily < b.CellKey.EventFamily
	}
	if a.CellKey.Horizon != b.CellKey.Horizon {
		return a.CellKey.Horizon < b.CellKey.Horizon
	}
	if a.CellKey.DelayBars != b.CellKey.DelayBars {
		return a.CellKey.DelayBars < b.CellKey.DelayBars
	}
	return a.ShockCluster < b.ShockCluster
}

// NonOverlapViolations reports symbol/family/date collisions inside one playbook group.
func NonOverlapViolations(group ResidualWinnerGroup, cooldownBars int, barDuration time.Duration) []string {
	if cooldownBars < 1 {
		cooldownBars = 1
	}
	if barDuration <= 0 {
		barDuration = time.Hour
	}
	winners := append([]ResidualWinnerEntry(nil), group.Winners...)
	sort.Slice(winners, func(i, j int) bool {
		if winners[i].Symbol != winners[j].Symbol {
			return winners[i].Symbol < winners[j].Symbol
		}
		return winners[i].DecisionTime.Before(winners[j].DecisionTime)
	})
	type slot struct {
		end time.Time
	}
	last := map[string]slot{}
	var violations []string
	cooldown := time.Duration(cooldownBars) * barDuration
	for _, winner := range winners {
		key := winner.Symbol + "|" + winner.EventFamily
		if prev, ok := last[key]; ok && winner.DecisionTime.Before(prev.end) {
			violations = append(violations, winner.EventID)
		}
		last[key] = slot{end: winner.DecisionTime.Add(cooldown)}
	}
	return violations
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.OutputDir == "" {
		return errors.New("history_root and output_dir are required")
	}
	if len(c.ResidualMapDirs) == 0 {
		return errors.New("at least one residual map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.PlaybookLimit <= 0 || c.PlaybookLimit > MaxPlaybookCandidates {
		return fmt.Errorf("playbook_limit must be in 1..%d", MaxPlaybookCandidates)
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
	}
	return nil
}

// RoundRobinDimensions returns the frozen dimension order for fair budget allocation.
func RoundRobinDimensions() []string {
	return []string{"event_family", "horizon", "profile_id", "shock_cluster"}
}

// ProfilePrimaryFamily returns the primary OHLCV family path for one profile.
func ProfilePrimaryFamily(profileID string) (string, error) {
	spec, err := profitmap.ProfileSpecFor(profileID)
	if err != nil {
		return "", err
	}
	return spec.PrimaryFamily, nil
}

// WriteResidualMapBundle writes a synthetic residual map bundle for tests.
func WriteResidualMapBundle(dir string, bundle ResidualMapBundle) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	discoveryName := fmt.Sprintf("residual_map_discovery_%s.json", bundle.Manifest.ProfileID)
	discoveryPath := filepath.Join(dir, discoveryName)
	thresholdsPath := filepath.Join(dir, "residual_map_thresholds.json")
	manifestPath := filepath.Join(dir, "residual_map_manifest.json")

	if err := writeJSONFile(discoveryPath, bundle.Discovery); err != nil {
		return err
	}
	if err := writeJSONFile(thresholdsPath, bundle.Thresholds); err != nil {
		return err
	}
	gotDiscovery, err := eventmap.HashFile(discoveryPath)
	if err != nil {
		return err
	}
	gotThresholds, err := eventmap.HashFile(thresholdsPath)
	if err != nil {
		return err
	}
	manifest := bundle.Manifest
	manifest.ArtifactHashes = map[string]string{
		discoveryName:                  gotDiscovery,
		"residual_map_thresholds.json": gotThresholds,
	}
	hash, err := hashResidualManifest(manifest)
	if err != nil {
		return err
	}
	manifest.ManifestHash = hash
	return writeJSONFile(manifestPath, manifest)
}

func hashResidualManifest(m ResidualMapManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func writeJSONFile(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}
