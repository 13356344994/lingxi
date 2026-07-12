package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
)

// LoadPlaybookBundle reads and verifies sealed playbooks outputs.
func LoadPlaybookBundle(dir string) (PlaybookBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return PlaybookBundle{}, err
	}
	manifest, err := playbook.LoadManifest(filepath.Join(abs, "event_playbook_manifest.json"))
	if err != nil {
		return PlaybookBundle{}, fmt.Errorf("playbook manifest: %w", err)
	}
	if err := playbook.VerifyOutputArtifacts(abs, manifest); err != nil {
		return PlaybookBundle{}, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "g10_event_playbooks.json"))
	if err != nil {
		return PlaybookBundle{}, err
	}
	var payload struct {
		Playbooks []playbook.FrozenEventPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PlaybookBundle{}, err
	}
	raw, err = os.ReadFile(filepath.Join(abs, "g10_event_playbook_ledger.json"))
	if err != nil {
		return PlaybookBundle{}, err
	}
	var ledger playbook.ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return PlaybookBundle{}, err
	}
	if err := playbook.ValidateLedger(ledger); err != nil {
		return PlaybookBundle{}, err
	}
	return PlaybookBundle{
		Dir: abs, Manifest: manifest, Playbooks: payload.Playbooks, Ledger: ledger,
	}, nil
}

// VerifyBindings checks playbook/map/catalog/profile consistency.
func VerifyBindings(
	cfg RunConfig,
	catalog historydata.HistoryCatalog,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
) error {
	if cfg.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("validation: config protocol mismatch")
	}
	if cfg.HistoryDataHash != protocol.HistoryDataHash || catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("validation: history data hash mismatch")
	}
	if bundle.Manifest.ManifestHash != cfg.PlaybookManifestHash {
		return errors.New("validation: playbook manifest hash mismatch")
	}
	if bundle.Manifest.CatalogHash != catalog.DataHash {
		return errors.New("validation: catalog hash mismatch")
	}
	got, err := playbook.HashPlaybooks(bundle.Playbooks)
	if err != nil {
		return err
	}
	if got != bundle.Manifest.PlaybookHash {
		return errors.New("validation: playbooks hash mismatch")
	}
	if bundle.Manifest.ExperimentHash != bundle.Ledger.LedgerHash {
		return errors.New("validation: experiment hash mismatch")
	}
	for profile, want := range bundle.Manifest.MapManifestHashes {
		if got := mapManifestHashes[profile]; got != want {
			return fmt.Errorf("validation: map manifest hash mismatch for %s", profile)
		}
	}
	return nil
}

// VerifyFormalHistoryScope rejects truncated smoke inputs for formal validation.
func VerifyFormalHistoryScope(
	catalog historydata.HistoryCatalog,
	maxFiles int,
	allowTruncatedSmoke bool,
	bundles []playbook.ResidualMapBundle,
) error {
	if allowTruncatedSmoke {
		return nil
	}
	for _, bundle := range bundles {
		if err := RequireFullCatalogSelection(catalog, bundle.Manifest.ProfileID, maxFiles, allowTruncatedSmoke); err != nil {
			return err
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
	if c.HistoryRoot == "" || c.PlaybookOutputDir == "" || c.OutputDir == "" {
		return errors.New("history_root, playbook_output_dir, and output_dir are required")
	}
	if c.PlaybookManifestHash == "" {
		return errors.New("playbook_manifest_hash is required")
	}
	if c.FingerprintPath == "" || c.FingerprintHash == "" {
		return errors.New("fingerprint_path and fingerprint_hash are required")
	}
	if len(c.ResidualMapDirs) == 0 {
		return errors.New("at least one residual-map-dir is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
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
