package personalplaybook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg6/eventmap"
)

// BuildManifest seals all bindings before validation replay.
func BuildManifest(
	cfg RunConfig,
	bundle InputBundle,
	playbooks []PersonalWildPlaybook,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
) (PlaybookManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return PlaybookManifest{}, err
	}
	playbooksHash, err := HashPlaybooks(playbooks)
	if err != nil {
		return PlaybookManifest{}, err
	}
	certified, discovery, notPersonal := countByStatus(playbooks)
	m := PlaybookManifest{
		SchemaVersion:            SchemaVersionManifest,
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		DiscoveryMode:            DiscoveryMode,
		ConfigHash:               cfgHash,
		ReverseTapesManifestHash: bundle.Manifest.ManifestHash,
		ReverseTapesLedgerHash:   bundle.Ledger.LedgerHash,
		ConstitutionHash:         bundle.ConstitutionHash,
		PlaybookLimit:            cfg.PlaybookLimit,
		CertifiedPlaybookCount:   certified,
		DiscoveryPlaybookCount:   discovery,
		NotPersonalCount:         notPersonal,
		PlaybookHash:             playbooksHash,
		ExperimentHash:           ledger.LedgerHash,
		FreezeBeforeValidation:   true,
		ExamActions:              g10protocolv2.ExamActions,
		ArtifactHashes:           cloneStringMap(artifactHashes),
		CreatedAt:                time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return PlaybookManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m PlaybookManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m PlaybookManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return errors.New("manifest schema mismatch")
	}
	if m.Generation != Generation || m.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("manifest protocol mismatch")
	}
	if m.DiscoveryMode != DiscoveryMode {
		return errors.New("manifest discovery mode mismatch")
	}
	if !m.FreezeBeforeValidation {
		return errors.New("freeze_before_validation must be true")
	}
	if m.ExamActions != g10protocolv2.ExamActions {
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

func countByStatus(playbooks []PersonalWildPlaybook) (certified, discovery, notPersonal int) {
	for _, pb := range playbooks {
		switch pb.Status {
		case StatusPersonalWildCandidate:
			certified++
		case StatusDiscoveryOnly:
			discovery++
		case StatusNotPersonalEnough:
			notPersonal++
		}
	}
	return certified, discovery, notPersonal
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

// WriteOutputs atomically writes personal playbook artifacts.
func WriteOutputs(
	outputDir string,
	playbooks []PersonalWildPlaybook,
	discovery []PersonalWildPlaybook,
	ledger ExperimentLedger,
	manifest PlaybookManifest,
	summary RunSummary,
) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, LedgerFileName), ledger); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputPlaybooksFile), struct {
		Playbooks   []PersonalWildPlaybook `json:"playbooks"`
		ExamActions int                    `json:"exam_actions"`
	}{Playbooks: playbooks, ExamActions: g10protocolv2.ExamActions}); err != nil {
		return err
	}
	if err := WriteDiscoveryJSONL(filepath.Join(outputDir, OutputDiscoveryFile), discovery); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, ManifestFileName), manifest); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(outputDir, SummaryFileName), summary)
}

// WriteDiscoveryJSONL writes discovery-only playbooks as JSONL.
func WriteDiscoveryJSONL(path string, playbooks []PersonalWildPlaybook) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, pb := range playbooks {
		if err := enc.Encode(pb); err != nil {
			return err
		}
	}
	return nil
}

// LoadManifest reads and verifies personal_playbook_manifest.json.
func LoadManifest(path string) (PlaybookManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PlaybookManifest{}, err
	}
	var manifest PlaybookManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return PlaybookManifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return PlaybookManifest{}, err
	}
	return manifest, nil
}

// VerifyOutputArtifacts checks on-disk artifact hashes against manifest.
func VerifyOutputArtifacts(outputDir string, manifest PlaybookManifest) error {
	ledgerPath := filepath.Join(outputDir, LedgerFileName)
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
	playbooksPath := filepath.Join(outputDir, OutputPlaybooksFile)
	raw, err = os.ReadFile(playbooksPath)
	if err != nil {
		return err
	}
	var payload struct {
		Playbooks []PersonalWildPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for _, pb := range payload.Playbooks {
		if pb.Status == StatusPersonalWildCandidate {
			if err := ValidatePlaybook(pb); err != nil {
				return fmt.Errorf("loaded playbook: %w", err)
			}
		}
	}
	got, err := HashPlaybooks(payload.Playbooks)
	if err != nil {
		return err
	}
	if got != manifest.PlaybookHash {
		return errors.New("playbooks artifact hash mismatch")
	}
	if manifest.ArtifactHashes != nil {
		for name, want := range manifest.ArtifactHashes {
			gotHash, err := eventmap.HashFile(filepath.Join(outputDir, name))
			if err != nil {
				return fmt.Errorf("artifact %s: %w", name, err)
			}
			if gotHash != want {
				return fmt.Errorf("artifact hash mismatch for %s", name)
			}
		}
	}
	return nil
}

// BuildSummary constructs run summary receipt.
func BuildSummary(
	bundle InputBundle,
	playbooks []PersonalWildPlaybook,
	discovery []PersonalWildPlaybook,
	ledger ExperimentLedger,
	manifest PlaybookManifest,
	playbookLimit int,
) RunSummary {
	certified, _, notPersonal := countByStatus(playbooks)
	noReason := ""
	if certified == 0 && len(bundle.CertifiedTapes) > 0 {
		noReason = "all_certified_tapes_not_personal_enough_or_rejected"
	} else if certified == 0 {
		noReason = "no_executable_certified_reverse_tapes"
	}
	return RunSummary{
		SchemaVersion:          SchemaVersionSummary,
		Generation:             Generation,
		ProtocolHash:           g10protocolv2.RouterProtocolHash,
		DiscoveryMode:          DiscoveryMode,
		CertifiedTapeCount:     len(bundle.CertifiedTapes),
		DiscoveryTapeCount:     len(bundle.DiscoveryTapes),
		AttemptCount:           len(ledger.Attempts),
		CertifiedPlaybookCount: certified,
		DiscoveryPlaybookCount: len(discovery),
		NotPersonalCount:       notPersonal,
		PlaybookLimit:          playbookLimit,
		LedgerStatusCounts:     ledgerStatusCounts(ledger.Attempts),
		PlaybookHash:           manifest.PlaybookHash,
		ExperimentHash:         ledger.LedgerHash,
		ManifestHash:           manifest.ManifestHash,
		NoCandidatesReason:     noReason,
		ExamActions:            g10protocolv2.ExamActions,
	}
}
