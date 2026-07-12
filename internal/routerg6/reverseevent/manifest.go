package reverseevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/protocol"
)

// BuildManifest seals all bindings before validation replay.
func BuildManifest(
	cfg RunConfig,
	catalogHash string,
	mapManifestHashes map[string]string,
	mapArtifactHashes map[string]string,
	cards []FrozenEventCard,
	ledger ExperimentLedger,
	coverage CoverageSummary,
	coverageHash string,
) (EventCardManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return EventCardManifest{}, err
	}
	cardsHash, err := HashEventCards(cards)
	if err != nil {
		return EventCardManifest{}, err
	}
	m := EventCardManifest{
		SchemaVersion:          SchemaVersionManifest,
		Generation:             protocol.Generation,
		ProtocolHash:           protocol.RouterProtocolHash,
		HistoryDataHash:        protocol.HistoryDataHash,
		DiscoveryMode:          protocol.DiscoveryMode,
		ConfigHash:             cfgHash,
		CatalogHash:            catalogHash,
		MapManifestHashes:      cloneStringMap(mapManifestHashes),
		MapArtifactHashes:      cloneStringMap(mapArtifactHashes),
		CardLimit:              cfg.CardLimit,
		ActualCardCount:        len(cards),
		EventCardHash:          cardsHash,
		ExperimentHash:         ledger.LedgerHash,
		CoverageHash:           coverageHash,
		Coverage:               coverage,
		FreezeBeforeValidation: protocol.FreezeBeforeValidation,
		ExamActions:            protocol.ExamActions,
		CreatedAt:              time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return EventCardManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m EventCardManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m EventCardManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return errors.New("manifest schema mismatch")
	}
	if m.Generation != protocol.Generation || m.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("manifest protocol mismatch")
	}
	if m.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("manifest history hash mismatch")
	}
	if m.DiscoveryMode != protocol.DiscoveryMode {
		return errors.New("manifest discovery mode mismatch")
	}
	if !m.FreezeBeforeValidation {
		return errors.New("freeze_before_validation must be true")
	}
	if m.CoverageHash == "" {
		return errors.New("manifest coverage hash missing")
	}
	computedCoverageHash, _, err := datatruth.HashJSON(m.Coverage)
	if err != nil || computedCoverageHash != m.CoverageHash {
		return errors.New("manifest coverage payload mismatch")
	}
	if m.ExamActions != protocol.ExamActions {
		return errors.New("exam_actions must be zero")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errors.New("manifest hash mismatch")
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

// WriteOutputs atomically writes discovery artifacts.
func WriteOutputs(outputDir string, cards []FrozenEventCard, ledger ExperimentLedger, manifest EventCardManifest, summary RunSummary) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_event_card_ledger.json"), ledger); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_event_cards.json"), struct {
		Cards       []FrozenEventCard `json:"cards"`
		ExamActions int               `json:"exam_actions"`
	}{Cards: cards, ExamActions: protocol.ExamActions}); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "event_card_manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_reverse_events_summary.json"), summary); err != nil {
		return err
	}
	return nil
}

// LoadManifest reads and verifies event_card_manifest.json.
func LoadManifest(path string) (EventCardManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return EventCardManifest{}, err
	}
	var manifest EventCardManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return EventCardManifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return EventCardManifest{}, err
	}
	return manifest, nil
}

// VerifyOutputArtifacts checks on-disk artifact hashes against manifest.
func VerifyOutputArtifacts(outputDir string, manifest EventCardManifest) error {
	ledgerPath := filepath.Join(outputDir, "g6_event_card_ledger.json")
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		return fmt.Errorf("ledger artifact: %w", err)
	}
	var ledger ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return err
	}
	if err := ValidateLedger(ledger); err != nil {
		return err
	}
	if ledger.LedgerHash != manifest.ExperimentHash {
		return errors.New("ledger artifact hash mismatch")
	}
	cardsPath := filepath.Join(outputDir, "g6_event_cards.json")
	raw, err = os.ReadFile(cardsPath)
	if err != nil {
		return err
	}
	var payload struct {
		Cards []FrozenEventCard `json:"cards"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for _, card := range payload.Cards {
		if err := ValidateEventCard(card); err != nil {
			return fmt.Errorf("loaded card binding/integrity: %w", err)
		}
	}
	got, err := HashEventCards(payload.Cards)
	if err != nil {
		return err
	}
	if got != manifest.EventCardHash {
		return errors.New("cards artifact hash mismatch")
	}
	return nil
}
