package residualmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// Checkpoint binds resume state with config/output integrity.
type Checkpoint struct {
	SchemaVersion     string                `json:"schema_version"`
	Generation        string                `json:"generation"`
	ProtocolHash      string                `json:"protocol_hash"`
	HistoryDataHash   string                `json:"history_data_hash"`
	ConfigHash        string                `json:"config_hash"`
	CatalogHash       string                `json:"catalog_hash"`
	FingerprintHash   string                `json:"fingerprint_hash"`
	MapManifestHashes map[string]string     `json:"map_manifest_hashes"`
	OutputDir         string                `json:"output_dir"`
	NextWorkIndex     int                   `json:"next_work_index"`
	AcceptedCount     int                   `json:"accepted_count"`
	Opportunities     []ResidualOpportunity `json:"opportunities"`
	Attempts          []ExperimentAttempt   `json:"attempts"`
	SeenEventIDs      []string              `json:"seen_event_ids"`
	MapHash           string                `json:"map_hash,omitempty"`
	LedgerHash        string                `json:"ledger_hash,omitempty"`
	Done              bool                  `json:"done"`
	SavedAt           time.Time             `json:"saved_at"`
	CheckpointHash    string                `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("residualmap: checkpoint path required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, false, nil
		}
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return Checkpoint{}, false, errors.New("residualmap: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("residualmap: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = CheckpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	hash, err := hashCheckpoint(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashCheckpoint(cp Checkpoint) (string, error) {
	payload := cp
	payload.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// VerifyCheckpointBinding rejects config/output drift on resume.
func VerifyCheckpointBinding(
	cp Checkpoint,
	cfg RunConfig,
	catalogHash string,
	fingerprintHash string,
	mapManifestHashes map[string]string,
) error {
	if cp.Generation != cfg.Generation {
		return fmt.Errorf("residualmap: checkpoint generation %q != config %q", cp.Generation, cfg.Generation)
	}
	if cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("residualmap: checkpoint protocol hash mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("residualmap: checkpoint history data hash mismatch")
	}
	want, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != want {
		return errors.New("residualmap: checkpoint config hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("residualmap: checkpoint catalog hash mismatch")
	}
	if cp.FingerprintHash != fingerprintHash {
		return errors.New("residualmap: checkpoint fingerprint hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("residualmap: checkpoint output dir mismatch")
	}
	for profile, hash := range mapManifestHashes {
		if cp.MapManifestHashes[profile] != hash {
			return fmt.Errorf("residualmap: checkpoint map manifest mismatch for %s", profile)
		}
	}
	return nil
}

// NewCheckpoint seeds a fresh checkpoint envelope.
func NewCheckpoint(cfg RunConfig, catalogHash, fingerprintHash string, mapManifestHashes map[string]string) Checkpoint {
	return Checkpoint{
		Generation:        cfg.Generation,
		ProtocolHash:      cfg.ProtocolHash,
		HistoryDataHash:   cfg.HistoryDataHash,
		ConfigHash:        mustConfigHash(cfg),
		CatalogHash:       catalogHash,
		FingerprintHash:   fingerprintHash,
		MapManifestHashes: copyStringMap(mapManifestHashes),
		OutputDir:         cfg.OutputDir,
	}
}

func mustConfigHash(cfg RunConfig) string {
	hash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ""
	}
	return hash
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DefaultProtocolHash returns frozen G10 protocol hash.
func DefaultProtocolHash() string { return g10protocol.RouterProtocolHash }

// DefaultHistoryDataHash returns frozen history data hash.
func DefaultHistoryDataHash() string { return g10protocol.HistoryDataHash }
