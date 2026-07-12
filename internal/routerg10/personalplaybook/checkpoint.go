package personalplaybook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// Checkpoint binds resume state with reverse-tapes integrity.
type Checkpoint struct {
	SchemaVersion            string                 `json:"schema_version"`
	Generation               string                 `json:"generation"`
	ProtocolHash             string                 `json:"protocol_hash"`
	HistoryDataHash          string                 `json:"history_data_hash"`
	ConfigHash               string                 `json:"config_hash"`
	ReverseTapesManifestHash string                 `json:"reverse_tapes_manifest_hash"`
	ReverseTapesLedgerHash   string                 `json:"reverse_tapes_ledger_hash"`
	ConstitutionHash         string                 `json:"constitution_hash"`
	OutputDir                string                 `json:"output_dir"`
	NextWorkIndex            int                    `json:"next_work_index"`
	PlaybookCount            int                    `json:"playbook_count"`
	Playbooks                []PersonalWildPlaybook `json:"playbooks"`
	DiscoveryPlaybooks       []PersonalWildPlaybook `json:"discovery_playbooks"`
	LedgerAttempts           []ExperimentAttempt    `json:"ledger_attempts"`
	PlaybookHash             string                 `json:"playbook_hash"`
	LedgerHash               string                 `json:"ledger_hash"`
	Done                     bool                   `json:"done"`
	SavedAt                  time.Time              `json:"saved_at"`
	CheckpointHash           string                 `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("checkpoint path required")
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
		return Checkpoint{}, false, errors.New("checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("checkpoint hash mismatch (tamper/drift)")
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

// VerifyCheckpointBinding rejects drift across config and reverse-tapes hashes.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, bundle InputBundle) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != Generation || cp.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("checkpoint config hash mismatch")
	}
	if cp.ReverseTapesManifestHash != bundle.Manifest.ManifestHash {
		return errors.New("checkpoint reverse tapes manifest mismatch")
	}
	if cp.ReverseTapesLedgerHash != bundle.Ledger.LedgerHash {
		return errors.New("checkpoint reverse tapes ledger mismatch")
	}
	if cp.ConstitutionHash != bundle.ConstitutionHash {
		return errors.New("checkpoint constitution hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("checkpoint output dir mismatch")
	}
	return nil
}
