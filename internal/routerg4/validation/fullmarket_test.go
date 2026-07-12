package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

func TestLoadMapBundleRequiresFullMarketScope(t *testing.T) {
	dir := writeScopedMapBundle(t, true, protocol.FullMarketScope, false, 2, 2)
	bundle, err := LoadMapBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.FullMarket || bundle.MapScope != protocol.FullMarketScope {
		t.Fatalf("scope not preserved: %+v", bundle)
	}
}

func TestLoadMapBundleRejectsTruncatedSmoke(t *testing.T) {
	dir := writeScopedMapBundle(t, false, protocol.TruncatedSmokeScope, true, 2, 1)
	if _, err := LoadMapBundle(dir); err == nil ||
		!strings.Contains(err.Error(), protocol.TruncatedSmokeScope) {
		t.Fatalf("expected truncated smoke rejection, got %v", err)
	}
}

func TestLoadMapBundleRejectsMissingScopeFields(t *testing.T) {
	dir := writeScopedMapBundle(t, true, protocol.FullMarketScope, false, 1, 1)
	path := filepath.Join(dir, "map_manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "full_market")
	delete(manifest, "map_scope")
	sealRawManifest(t, path, manifest)
	if _, err := LoadMapBundle(dir); err == nil ||
		!strings.Contains(err.Error(), "missing formal") {
		t.Fatalf("expected missing scope rejection, got %v", err)
	}
}

func TestRunConfigUsesCurrentProtocolHash(t *testing.T) {
	cfg := DefaultRunConfig(
		t.TempDir(), t.TempDir(), t.TempDir(), []string{t.TempDir()},
		1, 1, "candidate-manifest",
	)
	if cfg.ProtocolHash != protocol.RouterProtocolHash {
		t.Fatalf("config protocol hash=%s want current=%s", cfg.ProtocolHash, protocol.RouterProtocolHash)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.ProtocolHash = "stale-protocol"
	if err := cfg.Validate(); err == nil {
		t.Fatal("stale protocol hash must be rejected")
	}
}

func writeScopedMapBundle(
	t *testing.T,
	fullMarket bool,
	scope string,
	truncated bool,
	available, selected int,
) string {
	t.Helper()
	dir := t.TempDir()
	partitionName := "map_validation_okx_1h.json"
	partitionPath := filepath.Join(dir, partitionName)
	if err := os.WriteFile(partitionPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	partitionHash, err := profitmap.HashFile(partitionPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version":          "lingxi.router-g4-profit-map.manifest.v1",
		"generation":              protocol.Generation,
		"protocol_hash":           protocol.RouterProtocolHash,
		"history_data_hash":       protocol.HistoryDataHash,
		"profile_id":              profitmap.ProfileOKX1H,
		"catalog_hash":            protocol.HistoryDataHash,
		"available_primary_files": available,
		"selected_primary_files":  selected,
		"selection_truncated":     truncated,
		"full_market":             fullMarket,
		"map_scope":               scope,
		"artifact_hashes": map[string]string{
			partitionName: partitionHash,
		},
	}
	sealRawManifest(t, filepath.Join(dir, "map_manifest.json"), manifest)
	return dir
}

func sealRawManifest(t *testing.T, path string, manifest map[string]any) {
	t.Helper()
	manifest["manifest_hash"] = ""
	hash, _, err := datatruth.HashJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest["manifest_hash"] = hash
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
