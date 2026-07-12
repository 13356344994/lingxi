package residualmap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// BuildManifest constructs a tamper-evident manifest from run outputs.
func BuildManifest(
	cfg RunConfig,
	catalogHash string,
	fingerprintHash string,
	fingerprintPath string,
	mapManifestHashes map[string]string,
	mapArtifactHashes map[string]string,
	residualMap PersonalResidualMap,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
) (MapManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return MapManifest{}, err
	}
	boundArtifacts := make(map[string]string, len(artifactHashes))
	for name, hash := range artifactHashes {
		boundArtifacts[name] = hash
	}
	accepted := 0
	rejected := 0
	for _, attempt := range ledger.Attempts {
		switch attempt.Status {
		case LedgerStatusAccepted:
			accepted++
		case LedgerStatusRejected:
			rejected++
		}
	}
	m := MapManifest{
		SchemaVersion:     SchemaVersionManifest,
		Generation:        Generation,
		ProtocolHash:      g10protocol.RouterProtocolHash,
		HistoryDataHash:   g10protocol.HistoryDataHash,
		ConfigHash:        cfgHash,
		CatalogHash:       catalogHash,
		FingerprintHash:   fingerprintHash,
		FingerprintPath:   fingerprintPath,
		MapManifestHashes: copyStringMap(mapManifestHashes),
		MapArtifactHashes: copyStringMap(mapArtifactHashes),
		CandidateLimit:    cfg.CandidateLimit,
		AcceptedCount:     accepted,
		RejectedCount:     rejected,
		MapHash:           residualMap.MapHash,
		LedgerHash:        ledger.LedgerHash,
		ArtifactHashes:    boundArtifacts,
		CreatedAt:         time.Now().UTC(),
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

// ValidateManifest checks manifest self-hash and required bindings.
func ValidateManifest(m MapManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return fmt.Errorf("residualmap: manifest schema %q", m.SchemaVersion)
	}
	if m.Generation != Generation {
		return fmt.Errorf("residualmap: manifest generation %q", m.Generation)
	}
	if m.ProtocolHash != g10protocol.RouterProtocolHash {
		return errorsNew("residualmap: manifest protocol hash mismatch")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errorsNew("residualmap: manifest hash mismatch (tamper/drift)")
	}
	return nil
}

// HashFile returns sha256 hex of one file.
func HashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// LoadManifest reads residual_map_manifest.json.
func LoadManifest(path string) (MapManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MapManifest{}, err
	}
	var m MapManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return MapManifest{}, err
	}
	return m, ValidateManifest(m)
}

// WriteManifest atomically writes residual_map_manifest.json.
func WriteManifest(outputDir string, manifest MapManifest) error {
	path := filepath.Join(outputDir, ManifestFileName)
	raw, err := json.MarshalIndent(manifest, "", "  ")
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

func errorsNew(msg string) error { return fmt.Errorf("%s", msg) }
