package exclusionmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// BuildManifest constructs a sealed manifest from run outputs.
func BuildManifest(
	cfg RunConfig,
	catalogHash, constitutionHash string,
	mapManifestHashes, mapArtifactHashes map[string]string,
	announcementTapeHash, announcementManifestHash string,
	exclusionMap TripleExclusionMap,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
) (MapManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return MapManifest{}, err
	}
	accepted, rejected := countLedger(ledger.Attempts)
	m := MapManifest{
		SchemaVersion:            SchemaVersionManifest,
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		ConfigHash:               cfgHash,
		CatalogHash:              catalogHash,
		ConstitutionHash:         constitutionHash,
		MapManifestHashes:        copyStringMap(mapManifestHashes),
		MapArtifactHashes:        copyStringMap(mapArtifactHashes),
		AnnouncementTapeHash:     announcementTapeHash,
		AnnouncementManifestHash: announcementManifestHash,
		CandidateLimit:           cfg.CandidateLimit,
		AcceptedCount:            accepted,
		RejectedCount:            rejected,
		MapHash:                  exclusionMap.MapHash,
		LedgerHash:               ledger.LedgerHash,
		ArtifactHashes:           copyStringMap(artifactHashes),
		CreatedAt:                time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return MapManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m MapManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func countLedger(attempts []ExperimentAttempt) (accepted, rejected int) {
	for _, attempt := range attempts {
		switch attempt.Status {
		case LedgerStatusAccepted:
			accepted++
		case LedgerStatusRejected:
			rejected++
		}
	}
	return accepted, rejected
}

// WriteManifest atomically writes exclusion_map_manifest.json.
func WriteManifest(outputDir string, manifest MapManifest) error {
	return writeJSONAtomic(filepath.Join(outputDir, ManifestFileName), manifest)
}

// HashFile returns sha256 hex of one file.
func HashFile(path string) (string, error) {
	return eventmap.HashFile(path)
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

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m MapManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return fmt.Errorf("exclusionmap: manifest schema %q", m.SchemaVersion)
	}
	if m.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("exclusionmap: manifest protocol mismatch")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errors.New("exclusionmap: manifest hash mismatch (tamper/drift)")
	}
	return nil
}
