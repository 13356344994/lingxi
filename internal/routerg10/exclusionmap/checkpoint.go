package exclusionmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint binds resume state with config/output integrity.
type Checkpoint struct {
	SchemaVersion            string           `json:"schema_version"`
	Generation               string           `json:"generation"`
	ProtocolHash             string           `json:"protocol_hash"`
	HistoryDataHash          string           `json:"history_data_hash"`
	ConfigHash               string           `json:"config_hash"`
	CatalogHash              string           `json:"catalog_hash"`
	ConstitutionHash         string           `json:"constitution_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	AnnouncementTapeHash     string           `json:"announcement_tape_hash"`
	AnnouncementManifestHash string           `json:"announcement_manifest_hash"`
	OutputDir                string           `json:"output_dir"`
	NextWorkIndex            int              `json:"next_work_index"`
	AcceptedCount            int              `json:"accepted_count"`
	Proofs                   []ExclusionProof `json:"proofs"`
	Attempts                 []ExperimentAttempt `json:"attempts"`
	SeenEventIDs             []string         `json:"seen_event_ids"`
	MapHash                  string           `json:"map_hash,omitempty"`
	LedgerHash               string           `json:"ledger_hash,omitempty"`
	Done                     bool             `json:"done"`
	SavedAt                  time.Time        `json:"saved_at"`
	CheckpointHash           string           `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("exclusionmap: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("exclusionmap: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("exclusionmap: checkpoint hash mismatch (tamper/drift)")
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
	catalogHash, constitutionHash string,
	mapManifestHashes map[string]string,
	announcementTapeHash, announcementManifestHash string,
) error {
	if cp.Generation != cfg.Generation {
		return fmt.Errorf("exclusionmap: checkpoint generation %q != config %q", cp.Generation, cfg.Generation)
	}
	if cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("exclusionmap: checkpoint protocol hash mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("exclusionmap: checkpoint history data hash mismatch")
	}
	want, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != want {
		return errors.New("exclusionmap: checkpoint config hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("exclusionmap: checkpoint catalog hash mismatch")
	}
	if cp.ConstitutionHash != constitutionHash {
		return errors.New("exclusionmap: checkpoint constitution hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("exclusionmap: checkpoint output dir mismatch")
	}
	if cp.AnnouncementTapeHash != announcementTapeHash || cp.AnnouncementManifestHash != announcementManifestHash {
		return errors.New("exclusionmap: checkpoint announcement binding mismatch")
	}
	for profile, hash := range mapManifestHashes {
		if cp.MapManifestHashes[profile] != hash {
			return errors.New("exclusionmap: checkpoint map manifest hash mismatch for " + profile)
		}
	}
	return nil
}

// NewCheckpoint initializes a fresh checkpoint for one run.
func NewCheckpoint(
	cfg RunConfig,
	catalogHash, constitutionHash string,
	mapManifestHashes map[string]string,
	announcementTapeHash, announcementManifestHash string,
) Checkpoint {
	return Checkpoint{
		SchemaVersion:            CheckpointSchemaVersion,
		Generation:               cfg.Generation,
		ProtocolHash:             cfg.ProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		ConfigHash:               mustConfigHash(cfg),
		CatalogHash:              catalogHash,
		ConstitutionHash:         constitutionHash,
		MapManifestHashes:        copyStringMap(mapManifestHashes),
		AnnouncementTapeHash:     announcementTapeHash,
		AnnouncementManifestHash:   announcementManifestHash,
		OutputDir:                cfg.OutputDir,
	}
}

func mustConfigHash(cfg RunConfig) string {
	hash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ""
	}
	return hash
}
