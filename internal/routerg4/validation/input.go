package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

// LoadCandidateBundle reads and verifies sealed discovery outputs.
func LoadCandidateBundle(dir string) (CandidateBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return CandidateBundle{}, err
	}
	manifestPath := filepath.Join(abs, "candidate_manifest.json")
	manifest, err := reversemine.LoadCandidateManifest(manifestPath)
	if err != nil {
		return CandidateBundle{}, fmt.Errorf("validation: candidate manifest: %w", err)
	}
	if err := reversemine.VerifyOutputArtifacts(abs, manifest); err != nil {
		return CandidateBundle{}, fmt.Errorf("validation: candidate artifacts: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(abs, "candidates.json"))
	if err != nil {
		return CandidateBundle{}, err
	}
	var payload struct {
		Candidates []reversemine.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CandidateBundle{}, err
	}
	if len(payload.Candidates) != manifest.ActualCandidateCount {
		return CandidateBundle{}, errors.New("validation: candidate count mismatch with manifest (frozen set tampered)")
	}
	gotHash, err := reversemine.HashCandidates(payload.Candidates)
	if err != nil {
		return CandidateBundle{}, err
	}
	if gotHash != manifest.CandidatesHash {
		return CandidateBundle{}, errors.New("validation: candidates hash mismatch")
	}
	for _, c := range payload.Candidates {
		if err := reversemine.ValidateCandidate(c); err != nil {
			return CandidateBundle{}, err
		}
	}
	raw, err = os.ReadFile(filepath.Join(abs, "g4_candidate_ledger.json"))
	if err != nil {
		return CandidateBundle{}, err
	}
	var ledger reversemine.CandidateLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return CandidateBundle{}, err
	}
	if err := reversemine.ValidateLedger(ledger); err != nil {
		return CandidateBundle{}, err
	}
	return CandidateBundle{
		Dir:        abs,
		Manifest:   manifest,
		Candidates: payload.Candidates,
		Ledger:     ledger,
	}, nil
}

// LoadMapBundle reads and verifies one profit-map output directory.
func LoadMapBundle(dir string) (MapBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return MapBundle{}, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "map_manifest.json"))
	if err != nil {
		return MapBundle{}, fmt.Errorf("validation: read map manifest %q: %w", dir, err)
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
	mapScope, mapScopeOK := rawManifest["map_scope"].(string)
	if !fullMarketOK || !mapScopeOK {
		return MapBundle{}, errors.New("validation: map manifest missing formal full_market/map_scope")
	}
	want, err := hashRawMapManifest(rawManifest)
	if err != nil {
		return MapBundle{}, err
	}
	if manifest.ManifestHash == "" || manifest.ManifestHash != want {
		return MapBundle{}, errors.New("validation: map manifest hash mismatch")
	}
	if manifest.Generation != protocol.Generation || !protocol.ProfitMapProtocolCompatible(manifest.ProtocolHash) {
		return MapBundle{}, errors.New("validation: map manifest protocol mismatch")
	}
	if manifest.HistoryDataHash != protocol.HistoryDataHash {
		return MapBundle{}, errors.New("validation: map manifest history data hash mismatch")
	}
	if err := verifyFormalMapScope(manifest, fullMarket, mapScope); err != nil {
		return MapBundle{}, err
	}
	if err := verifyValidationPartitionArtifact(abs, manifest); err != nil {
		return MapBundle{}, err
	}
	return MapBundle{
		Dir: abs, Manifest: manifest, FullMarket: fullMarket, MapScope: mapScope,
	}, nil
}

func verifyValidationPartitionArtifact(dir string, manifest profitmap.MapManifest) error {
	name := fmt.Sprintf("map_%s_%s.json", profitmap.PartitionValidation, manifest.ProfileID)
	want, ok := manifest.ArtifactHashes[name]
	if !ok || want == "" {
		return fmt.Errorf("validation: validation partition artifact missing for %s", manifest.ProfileID)
	}
	got, err := profitmap.HashFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("validation: validation partition hash mismatch for %s", manifest.ProfileID)
	}
	return nil
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

func verifyFormalMapScope(manifest profitmap.MapManifest, fullMarket bool, mapScope string) error {
	if !fullMarket ||
		mapScope != protocol.FullMarketScope ||
		!protocol.FormalPathAllowed(mapScope) ||
		manifest.SelectionTruncated ||
		manifest.SelectedPrimaryFiles != manifest.AvailablePrimaryFiles {
		return fmt.Errorf(
			"validation: formal validation requires full_market=true scope=%s; %s is forbidden",
			protocol.FullMarketScope, protocol.TruncatedSmokeScope,
		)
	}
	return nil
}

// VerifyInputBinding checks catalog, maps, and candidate manifest cross-links.
func VerifyInputBinding(
	catalog historydata.HistoryCatalog,
	historyRoot string,
	bundles []MapBundle,
	candidates CandidateBundle,
	maxFiles, maxLines int,
) error {
	if catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("validation: catalog data hash mismatch")
	}
	if candidates.Manifest.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("validation: candidate manifest history hash mismatch")
	}
	if !candidates.Manifest.FreezeBeforeValidation {
		return errors.New("validation: candidates must be frozen before validation")
	}
	seenProfiles := map[string]bool{}
	for _, bundle := range bundles {
		if err := verifyFormalMapScope(
			bundle.Manifest, bundle.FullMarket, bundle.MapScope,
		); err != nil {
			return err
		}
		if bundle.Manifest.CatalogHash != catalog.DataHash {
			return fmt.Errorf("validation: map %s catalog hash mismatch", bundle.Manifest.ProfileID)
		}
		wantManifest := candidates.Manifest.MapManifestHashes[bundle.Manifest.ProfileID]
		if wantManifest == "" || wantManifest != bundle.Manifest.ManifestHash {
			return fmt.Errorf("validation: map manifest hash mismatch for profile %s", bundle.Manifest.ProfileID)
		}
		if seenProfiles[bundle.Manifest.ProfileID] {
			return fmt.Errorf("validation: duplicate profile map %q", bundle.Manifest.ProfileID)
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
			return errors.New("validation: canonical primary files hash mismatch")
		}
		for _, file := range selection.Files {
			if err := profitmap.VerifyCatalogFile(catalog, historyRoot, file.Path); err != nil {
				return fmt.Errorf("validation: canonical file %s: %w", file.Path, err)
			}
		}
	}
	for profileID := range candidates.Manifest.MapManifestHashes {
		if !seenProfiles[profileID] {
			return fmt.Errorf("validation: missing map bundle for profile %q", profileID)
		}
	}
	return nil
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.CandidateOutputDir == "" || c.OutputDir == "" {
		return errors.New("validation: history_root, candidate_output_dir, and output_dir are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("validation: at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("validation: max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("validation: progress_every must be positive")
	}
	if c.CandidateManifestHash == "" {
		return errors.New("validation: candidate_manifest_hash is required")
	}
	if c.Generation != protocol.Generation ||
		c.ProtocolHash != protocol.RouterProtocolHash ||
		c.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("validation: run config protocol binding mismatch")
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
