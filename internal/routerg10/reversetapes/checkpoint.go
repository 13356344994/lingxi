package reversetapes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint binds resume state with upstream hash integrity.
type Checkpoint struct {
	SchemaVersion            string                `json:"schema_version"`
	Generation               string                `json:"generation"`
	ProtocolHash             string                `json:"protocol_hash"`
	HistoryDataHash          string                `json:"history_data_hash"`
	ConfigHash               string                `json:"config_hash"`
	CatalogHash              string                `json:"catalog_hash"`
	ConstitutionHash         string                `json:"constitution_hash"`
	ExclusionMapHash         string                `json:"exclusion_map_hash"`
	ExclusionManifestHash    string                `json:"exclusion_manifest_hash"`
	EntryProofManifestHash   string                `json:"entry_proof_manifest_hash"`
	ExecutionManifestHash    string                `json:"execution_manifest_hash"`
	AnnouncementTapeHash     string                `json:"announcement_tape_hash"`
	AnnouncementManifestHash string                `json:"announcement_manifest_hash"`
	OutputDir                string                `json:"output_dir"`
	NextWorkIndex            int                   `json:"next_work_index"`
	ExecutableCount          int                   `json:"executable_count"`
	DiscoveryCount           int                   `json:"discovery_count"`
	ExecutableTapes          []ExecutableProfitTape `json:"executable_tapes"`
	DiscoveryTapes           []ExecutableProfitTape `json:"discovery_tapes"`
	Attempts                 []ExperimentAttempt   `json:"attempts"`
	UsedShockClusters        []string              `json:"used_shock_clusters"`
	LedgerHash               string                `json:"ledger_hash,omitempty"`
	Done                     bool                  `json:"done"`
	SavedAt                  time.Time             `json:"saved_at"`
	CheckpointHash           string                `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("reversetapes: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("reversetapes: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("reversetapes: checkpoint hash mismatch")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
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

// NewCheckpoint initializes checkpoint from verified inputs.
func NewCheckpoint(cfg RunConfig, catalogHash, constitutionHash string, bundle InputBundle) (Checkpoint, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return Checkpoint{}, err
	}
	return Checkpoint{
		SchemaVersion:            CheckpointSchemaVersion,
		Generation:               Generation,
		ProtocolHash:             cfg.ProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		ConfigHash:               cfgHash,
		CatalogHash:              catalogHash,
		ConstitutionHash:         constitutionHash,
		ExclusionMapHash:         bundle.ExclusionMap.MapHash,
		ExclusionManifestHash:    bundle.ExclusionManifest.ManifestHash,
		EntryProofManifestHash:   bundle.EntryManifest.ManifestHash,
		ExecutionManifestHash:    bundle.ExecutionManifest.ManifestHash,
		AnnouncementTapeHash:     bundle.Announcements.TapeHash,
		AnnouncementManifestHash: bundle.Announcements.ManifestHash,
		OutputDir:                cfg.OutputDir,
	}, nil
}

// VerifyCheckpointBinding checks checkpoint matches current run inputs.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, catalogHash, constitutionHash string, bundle InputBundle) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != cfgHash || cp.OutputDir != cfg.OutputDir {
		return errors.New("reversetapes: checkpoint config mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash || cp.CatalogHash != catalogHash {
		return errors.New("reversetapes: checkpoint catalog mismatch")
	}
	if cp.ConstitutionHash != constitutionHash {
		return errors.New("reversetapes: checkpoint constitution mismatch")
	}
	if cp.ExclusionMapHash != bundle.ExclusionMap.MapHash ||
		cp.ExclusionManifestHash != bundle.ExclusionManifest.ManifestHash ||
		cp.EntryProofManifestHash != bundle.EntryManifest.ManifestHash ||
		cp.ExecutionManifestHash != bundle.ExecutionManifest.ManifestHash ||
		cp.AnnouncementTapeHash != bundle.Announcements.TapeHash ||
		cp.AnnouncementManifestHash != bundle.Announcements.ManifestHash {
		return errors.New("reversetapes: checkpoint upstream hash mismatch")
	}
	return nil
}

func shockSetFromCheckpoint(cp Checkpoint) map[string]bool {
	out := map[string]bool{}
	for _, key := range cp.UsedShockClusters {
		out[key] = true
	}
	return out
}

func appendShockUsed(set map[string]bool, cp *Checkpoint, symbol, cluster string) {
	if cluster == "" {
		return
	}
	key := shockOverlapKey(symbol, cluster)
	if set[key] {
		return
	}
	set[key] = true
	cp.UsedShockClusters = append(cp.UsedShockClusters, key)
}
