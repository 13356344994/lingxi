package entryproof

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint records resume state for bounded runs.
type Checkpoint struct {
	SchemaVersion  string    `json:"schema_version"`
	ConfigHash     string    `json:"config_hash"`
	OutputDir      string    `json:"output_dir"`
	LinesProcessed int       `json:"lines_processed"`
	ProofCount     int       `json:"proof_count"`
	ManifestHash   string    `json:"manifest_hash"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LoadCheckpoint reads checkpoint if present.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, false, nil
		}
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, fmt.Errorf("entryproof: decode checkpoint: %w", err)
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return Checkpoint{}, false, fmt.Errorf("entryproof: checkpoint schema drift")
	}
	return cp, true, nil
}

// SaveCheckpoint writes checkpoint atomically.
func SaveCheckpoint(path string, cp Checkpoint) error {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// BuildManifest seals proof outputs.
func BuildManifest(cfg RunConfig, proofs []EntryProof, sourceStatus map[string]string) (Manifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return Manifest{}, err
	}
	proofsHash, err := HashProofs(proofs)
	if err != nil {
		return Manifest{}, err
	}
	certified, discovery, blocked := 0, 0, 0
	for _, ep := range proofs {
		switch {
		case ep.CertificationOK:
			certified++
		case ep.EntryStatus == EntryUnprovenStatus:
			discovery++
		default:
			blocked++
		}
	}
	m := Manifest{
		SchemaVersion:  ManifestSchemaVersion,
		Generation:     cfg.Generation,
		ConfigHash:     cfgHash,
		ProofCount:     len(proofs),
		CertifiedCount: certified,
		DiscoveryCount: discovery,
		BlockedCount:   blocked,
		ProofsHash:     proofsHash,
		SourceStatus:   sourceStatus,
		CreatedAt:      time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return Manifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m Manifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashProofs canonicalizes proof list hash.
func HashProofs(proofs []EntryProof) (string, error) {
	payload := make([]EntryProof, len(proofs))
	for i, ep := range proofs {
		payload[i] = ep
		payload[i].EntryProofHash = ""
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("entryproof: manifest schema drift")
	}
	if m.ManifestHash == "" {
		return fmt.Errorf("entryproof: manifest_hash required")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if want != m.ManifestHash {
		return fmt.Errorf("entryproof: manifest_hash mismatch")
	}
	return nil
}
