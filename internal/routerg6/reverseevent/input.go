package reverseevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

// LoadMapBundle reads and verifies one G6 event-map output directory.
func LoadMapBundle(dir string) (MapBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return MapBundle{}, err
	}
	manifestPath := filepath.Join(abs, "event_map_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return MapBundle{}, fmt.Errorf("read map manifest %q: %w", dir, err)
	}
	var manifest eventmap.MapManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return MapBundle{}, err
	}
	var rawManifest map[string]any
	if err := json.Unmarshal(raw, &rawManifest); err != nil {
		return MapBundle{}, err
	}
	want, err := hashRawMapManifest(rawManifest)
	if err != nil {
		return MapBundle{}, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return MapBundle{}, errors.New("map manifest hash mismatch")
	}
	if manifest.Generation != protocol.Generation || manifest.ProtocolHash != protocol.RouterProtocolHash {
		return MapBundle{}, errors.New("map manifest protocol mismatch")
	}
	if manifest.HistoryDataHash != protocol.HistoryDataHash {
		return MapBundle{}, errors.New("map manifest history data hash mismatch")
	}

	discoveryName := fmt.Sprintf("event_map_%s_%s.json", eventmap.PartitionDiscovery, manifest.ProfileID)
	discoveryHash, ok := manifest.ArtifactHashes[discoveryName]
	if !ok || discoveryHash == "" {
		return MapBundle{}, fmt.Errorf("discovery map artifact missing for %s", manifest.ProfileID)
	}
	discoveryPath := filepath.Join(abs, discoveryName)
	gotDiscovery, err := eventmap.HashFile(discoveryPath)
	if err != nil {
		return MapBundle{}, err
	}
	if gotDiscovery != discoveryHash {
		return MapBundle{}, fmt.Errorf("discovery map hash mismatch for %s", manifest.ProfileID)
	}
	raw, err = os.ReadFile(discoveryPath)
	if err != nil {
		return MapBundle{}, err
	}
	var discovery eventmap.PartitionMap
	if err := json.Unmarshal(raw, &discovery); err != nil {
		return MapBundle{}, err
	}
	if discovery.Partition != eventmap.PartitionDiscovery || discovery.ProfileID != manifest.ProfileID {
		return MapBundle{}, errors.New("discovery partition map identity mismatch")
	}

	thresholdsName := "event_map_thresholds.json"
	thresholdsHash, ok := manifest.ArtifactHashes[thresholdsName]
	if !ok || thresholdsHash == "" {
		return MapBundle{}, errors.New("thresholds artifact missing")
	}
	thresholdsPath := filepath.Join(abs, thresholdsName)
	gotThresholds, err := eventmap.HashFile(thresholdsPath)
	if err != nil {
		return MapBundle{}, err
	}
	if gotThresholds != thresholdsHash {
		return MapBundle{}, errors.New("thresholds hash mismatch")
	}
	raw, err = os.ReadFile(thresholdsPath)
	if err != nil {
		return MapBundle{}, err
	}
	var thresholds eventmap.FrozenThresholds
	if err := json.Unmarshal(raw, &thresholds); err != nil {
		return MapBundle{}, err
	}
	if thresholds.ProfileID != manifest.ProfileID {
		return MapBundle{}, errors.New("thresholds profile mismatch")
	}

	return MapBundle{
		Dir: abs, Manifest: manifest, Discovery: discovery, Thresholds: thresholds,
	}, nil
}

func hashRawMapManifest(payload map[string]any) (string, error) {
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	copyPayload["manifest_hash"] = ""
	hash, _, err := datatruth.HashJSON(copyPayload)
	return hash, err
}

// VerifyCatalogBinding checks catalog hash and canonical primary files from map manifests.
func VerifyCatalogBinding(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	bundles []MapBundle,
	maxFiles, maxLines int,
	allowTruncatedSmoke bool,
) error {
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("catalog data hash mismatch")
	}
	seenProfiles := map[string]bool{}
	for _, bundle := range bundles {
		if bundle.Manifest.CatalogHash != catalog.DataHash {
			return fmt.Errorf("map %s catalog hash mismatch", bundle.Manifest.ProfileID)
		}
		if seenProfiles[bundle.Manifest.ProfileID] {
			return fmt.Errorf("duplicate profile map %q", bundle.Manifest.ProfileID)
		}
		seenProfiles[bundle.Manifest.ProfileID] = true
		if !allowTruncatedSmoke {
			if !bundle.Manifest.FullMarket || bundle.Manifest.SelectionTruncated {
				return fmt.Errorf("formal reverse input requires full_market non-truncated map for %s", bundle.Manifest.ProfileID)
			}
		}
		selection, err := eventmap.SelectPrimaryFiles(
			catalog, bundle.Manifest.ProfileID, maxFiles, bundle.Manifest.CatalogSymbolCount,
		)
		if err != nil {
			return err
		}
		filesHash, _, err := datatruth.HashJSON(selection.Files)
		if err != nil {
			return err
		}
		if filesHash != bundle.Manifest.SelectedFilesHash {
			return errors.New("canonical primary files hash mismatch")
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

// CollectWinnerGroups flattens discovery winners into reverse-derivation buckets.
func CollectWinnerGroups(bundles []MapBundle) []WinnerGroup {
	byKey := map[string]*WinnerGroup{}
	for _, bundle := range bundles {
		for _, cell := range bundle.Discovery.Cells {
			if len(cell.Winners) == 0 {
				continue
			}
			for _, winner := range cell.Winners {
				if winner.ConservativeBPS <= 0 {
					continue
				}
				cluster := normalizeShockCluster(winner.ShockCluster)
				key := groupKey(bundle.Manifest.ProfileID, cell.Key, cluster)
				group, ok := byKey[key]
				if !ok {
					group = &WinnerGroup{
						Key:          key,
						ProfileID:    bundle.Manifest.ProfileID,
						ManifestHash: bundle.Manifest.ManifestHash,
						CellKey:      cell.Key,
						ShockCluster: cluster,
						PriorityTier: winnerPriorityTier(winner.ConservativeBPS),
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
				tier := winnerPriorityTier(winner.ConservativeBPS)
				if tier < group.PriorityTier {
					group.PriorityTier = tier
				}
			}
		}
	}
	groups := make([]WinnerGroup, 0, len(byKey))
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

func groupKey(profileID string, key eventmap.CellKey, shockCluster string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s",
		profileID, key.EventFamily, key.Horizon, key.Direction, key.DelayBars, key.CapitalTier, shockCluster)
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

func winnerLess(a, b eventmap.WinnerEntry) bool {
	if a.ConservativeBPS != b.ConservativeBPS {
		return a.ConservativeBPS > b.ConservativeBPS
	}
	if !a.DecisionTime.Equal(b.DecisionTime) {
		return a.DecisionTime.Before(b.DecisionTime)
	}
	if a.Symbol != b.Symbol {
		return a.Symbol < b.Symbol
	}
	return a.EventID < b.EventID
}

func sortGroups(groups []WinnerGroup) {
	sort.Slice(groups, func(i, j int) bool { return groupLess(groups[i], groups[j]) })
}

func groupLess(a, b WinnerGroup) bool {
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
	if a.CellKey.Direction != b.CellKey.Direction {
		return a.CellKey.Direction < b.CellKey.Direction
	}
	if a.CellKey.DelayBars != b.CellKey.DelayBars {
		return a.CellKey.DelayBars < b.CellKey.DelayBars
	}
	if a.CellKey.CapitalTier != b.CellKey.CapitalTier {
		return a.CellKey.CapitalTier < b.CellKey.CapitalTier
	}
	return a.ShockCluster < b.ShockCluster
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
	if len(c.MapDirs) == 0 {
		return errors.New("at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.CardLimit <= 0 || c.CardLimit > MaxCardCandidates {
		return fmt.Errorf("card_limit must be in 1..%d", MaxCardCandidates)
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
	}
	return nil
}

// RoundRobinDimensions returns the frozen dimension order for fair budget allocation.
func RoundRobinDimensions() []string {
	return []string{"event_family", "direction", "horizon", "profile_id", "shock_cluster"}
}

// ProfilePrimaryFamily returns the primary OHLCV family path for one profile.
func ProfilePrimaryFamily(profileID string) (string, error) {
	spec, err := profitmap.ProfileSpecFor(profileID)
	if err != nil {
		return "", err
	}
	return spec.PrimaryFamily, nil
}
