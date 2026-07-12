package eventvalidation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

// LoadCardBundle reads and verifies sealed reverse-events outputs.
func LoadCardBundle(dir string) (CardBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return CardBundle{}, err
	}
	manifest, err := reverseevent.LoadManifest(filepath.Join(abs, "event_card_manifest.json"))
	if err != nil {
		return CardBundle{}, fmt.Errorf("card manifest: %w", err)
	}
	if err := reverseevent.VerifyOutputArtifacts(abs, manifest); err != nil {
		return CardBundle{}, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "g6_event_cards.json"))
	if err != nil {
		return CardBundle{}, err
	}
	var payload struct {
		Cards []reverseevent.FrozenEventCard `json:"cards"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return CardBundle{}, err
	}
	raw, err = os.ReadFile(filepath.Join(abs, "g6_event_card_ledger.json"))
	if err != nil {
		return CardBundle{}, err
	}
	var ledger reverseevent.ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return CardBundle{}, err
	}
	if err := reverseevent.ValidateLedger(ledger); err != nil {
		return CardBundle{}, err
	}
	return CardBundle{
		Dir: abs, Manifest: manifest, Cards: payload.Cards, Ledger: ledger,
	}, nil
}

// VerifyBindings checks card/map/catalog/profile consistency.
func VerifyBindings(
	cfg RunConfig,
	catalog historydata.HistoryCatalog,
	bundle CardBundle,
	mapManifestHashes map[string]string,
) error {
	if cfg.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("eventvalidation: config protocol mismatch")
	}
	if cfg.HistoryDataHash != protocol.HistoryDataHash || catalog.DataHash != protocol.HistoryDataHash {
		return errors.New("eventvalidation: history data hash mismatch")
	}
	if bundle.Manifest.ManifestHash != cfg.CardManifestHash {
		return errors.New("eventvalidation: card manifest hash mismatch")
	}
	if bundle.Manifest.CatalogHash != catalog.DataHash {
		return errors.New("eventvalidation: catalog hash mismatch")
	}
	if bundle.Manifest.EventCardHash == "" {
		return errors.New("eventvalidation: manifest event_card_hash missing")
	}
	gotCards, err := reverseevent.HashEventCards(bundle.Cards)
	if err != nil {
		return err
	}
	if gotCards != bundle.Manifest.EventCardHash {
		return errors.New("eventvalidation: event cards hash mismatch")
	}
	if bundle.Manifest.ExperimentHash != bundle.Ledger.LedgerHash {
		return errors.New("eventvalidation: experiment hash mismatch")
	}
	for profile, want := range bundle.Manifest.MapManifestHashes {
		if got := mapManifestHashes[profile]; got != want {
			return fmt.Errorf("eventvalidation: map manifest hash mismatch for %s", profile)
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
	if c.HistoryRoot == "" || c.CardOutputDir == "" || c.OutputDir == "" {
		return errors.New("history_root, card_output_dir, and output_dir are required")
	}
	if c.CardManifestHash == "" {
		return errors.New("card_manifest_hash is required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("at least one map output directory is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
	}
	return nil
}
