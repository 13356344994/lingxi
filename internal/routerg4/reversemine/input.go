package reversemine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

// LoadMapBundle reads and verifies one profit-map output directory.
func LoadMapBundle(dir string) (MapBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return MapBundle{}, err
	}
	manifestPath := filepath.Join(abs, "map_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return MapBundle{}, fmt.Errorf("reversemine: read map manifest %q: %w", dir, err)
	}
	var manifest profitmap.MapManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return MapBundle{}, err
	}
	var rawManifest map[string]any
	if err := json.Unmarshal(raw, &rawManifest); err != nil {
		return MapBundle{}, err
	}
	fullMarket, fullMarketOK := rawManifest["full_market"].(bool)
	scope, scopeOK := rawManifest["map_scope"].(string)
	if !fullMarketOK || !scopeOK {
		return MapBundle{}, errors.New("reversemine: map manifest missing formal full_market/map_scope")
	}
	want, err := hashRawMapManifest(rawManifest)
	if err != nil {
		return MapBundle{}, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return MapBundle{}, errors.New("reversemine: map manifest hash mismatch")
	}
	if manifest.Generation != protocol.Generation || !protocol.ProfitMapProtocolCompatible(manifest.ProtocolHash) {
		return MapBundle{}, errors.New("reversemine: map manifest protocol mismatch")
	}
	if manifest.HistoryDataHash != protocol.HistoryDataHash {
		return MapBundle{}, errors.New("reversemine: map manifest history data hash mismatch")
	}
	if !fullMarket || scope != protocol.FullMarketScope ||
		!protocol.FormalPathAllowed(scope) || manifest.SelectionTruncated {
		return MapBundle{}, fmt.Errorf(
			"reversemine: formal candidate input requires full_market=true scope=%s",
			protocol.FullMarketScope,
		)
	}
	discoveryName := fmt.Sprintf("map_%s_%s.json", profitmap.PartitionDiscovery, manifest.ProfileID)
	discoveryHash, ok := manifest.ArtifactHashes[discoveryName]
	if !ok || discoveryHash == "" {
		return MapBundle{}, fmt.Errorf("reversemine: discovery map artifact missing for %s", manifest.ProfileID)
	}
	gotDiscovery, err := profitmap.HashFile(filepath.Join(abs, discoveryName))
	if err != nil {
		return MapBundle{}, err
	}
	if gotDiscovery != discoveryHash {
		return MapBundle{}, fmt.Errorf("reversemine: discovery map hash mismatch for %s", manifest.ProfileID)
	}
	discoveryPath := filepath.Join(abs, discoveryName)
	raw, err = os.ReadFile(discoveryPath)
	if err != nil {
		return MapBundle{}, err
	}
	var discovery profitmap.PartitionMap
	if err := json.Unmarshal(raw, &discovery); err != nil {
		return MapBundle{}, err
	}
	if discovery.Partition != profitmap.PartitionDiscovery || discovery.ProfileID != manifest.ProfileID {
		return MapBundle{}, errors.New("reversemine: discovery partition map identity mismatch")
	}
	return MapBundle{
		Dir: abs, Manifest: manifest, Discovery: discovery,
		FullMarket: fullMarket, Scope: scope,
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
func VerifyCatalogBinding(catalog historydata.HistoryCatalog, historyRoot string, bundles []MapBundle, maxFiles, maxLines int) error {
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("reversemine: catalog data hash mismatch")
	}
	seenProfiles := map[string]bool{}
	for _, bundle := range bundles {
		if bundle.Manifest.CatalogHash != catalog.DataHash {
			return fmt.Errorf("reversemine: map %s catalog hash mismatch", bundle.Manifest.ProfileID)
		}
		if seenProfiles[bundle.Manifest.ProfileID] {
			return fmt.Errorf("reversemine: duplicate profile map %q", bundle.Manifest.ProfileID)
		}
		seenProfiles[bundle.Manifest.ProfileID] = true
		selection, err := profitmap.SelectPrimaryFiles(
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
			return errors.New("reversemine: canonical primary files hash mismatch")
		}
		for _, file := range selection.Files {
			if err := profitmap.VerifyCatalogFile(catalog, historyRoot, file.Path); err != nil {
				return fmt.Errorf("reversemine: canonical file %s: %w", file.Path, err)
			}
		}
		btcRel := bundle.Manifest.ProfileID + "/BTC-USDT-SWAP.jsonl"
		if bundle.Manifest.ProfileID == profitmap.ProfileOKX1H {
			btcRel = "okx_1h/BTC-USDT-SWAP.jsonl"
		} else if bundle.Manifest.ProfileID == profitmap.ProfileOKX5M {
			btcRel = "okx_5m/BTC-USDT-SWAP.jsonl"
		}
		if err := profitmap.VerifyCatalogFile(catalog, historyRoot, btcRel); err != nil {
			return err
		}
		if _, err := profitmap.LoadBars(profitmap.CatalogFilePath(historyRoot, btcRel), maxLines); err != nil {
			return fmt.Errorf("reversemine: load BTC bars: %w", err)
		}
	}
	return nil
}

// CollectCells flattens all discovery cells from verified map bundles.
func CollectCells(bundles []MapBundle) []CellWorkItem {
	var items []CellWorkItem
	for _, bundle := range bundles {
		for _, cell := range bundle.Discovery.Cells {
			items = append(items, CellWorkItem{
				ProfileID:    bundle.Manifest.ProfileID,
				ManifestHash: bundle.Manifest.ManifestHash,
				Key:          cell.Key,
				Stats:        cell.Stats,
				Winners:      append([]profitmap.WinnerEntry(nil), cell.Winners...),
				PriorityTier: cellPriorityTier(cell.Stats),
			})
		}
	}
	sortCells(items)
	return items
}

func cellPriorityTier(stats profitmap.CellStats) int {
	if stats.ConservativeNetBPSMean > 0 {
		return 0
	}
	if stats.FeeNetBPSMean > 0 {
		return 1
	}
	if stats.GrossBPSMean > 0 {
		return 2
	}
	return 3
}

func sortCells(items []CellWorkItem) {
	// Fixed order within tier: profile→horizon→direction→capital→sky→liquidity→symbolclass.
	sort.Slice(items, func(i, j int) bool { return cellLess(items[i], items[j]) })
}

func cellLess(a, b CellWorkItem) bool {
	if a.PriorityTier != b.PriorityTier {
		return a.PriorityTier < b.PriorityTier
	}
	if a.ProfileID != b.ProfileID {
		return a.ProfileID < b.ProfileID
	}
	if a.Key.Horizon != b.Key.Horizon {
		return a.Key.Horizon < b.Key.Horizon
	}
	if a.Key.Direction != b.Key.Direction {
		return a.Key.Direction < b.Key.Direction
	}
	if a.Key.CapitalTier != b.Key.CapitalTier {
		return a.Key.CapitalTier < b.Key.CapitalTier
	}
	if a.Key.SkyCoarse != b.Key.SkyCoarse {
		return a.Key.SkyCoarse < b.Key.SkyCoarse
	}
	if a.Key.Liquidity != b.Key.Liquidity {
		return a.Key.Liquidity < b.Key.Liquidity
	}
	return a.Key.SymbolClass < b.Key.SymbolClass
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.OutputDir == "" {
		return errors.New("reversemine: history_root and output_dir are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("reversemine: at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("reversemine: max_files and max_lines must be positive")
	}
	if c.CandidateLimit <= 0 || c.CandidateLimit > protocol.MaxTotalCandidates {
		return fmt.Errorf("reversemine: candidate_limit must be in 1..%d", protocol.MaxTotalCandidates)
	}
	if c.ProgressEvery <= 0 {
		return errors.New("reversemine: progress_every must be positive")
	}
	return nil
}

// RoundRobinDimensions returns the frozen dimension order for fair budget allocation.
func RoundRobinDimensions() []string {
	return []string{
		"profile_id", "horizon", "direction", "capital_tier",
		"sky_coarse_market", "liquidity_bucket", "symbol_class",
	}
}

// IsForbiddenFeatureKey rejects outcome/post-decision/current-bar feature inputs.
func IsForbiddenFeatureKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return true
	}
	for _, forbidden := range protocol.ForbiddenFeatureInputs {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	if strings.Contains(lower, "current_bar") || strings.Contains(lower, "current_open") {
		return true
	}
	return false
}
