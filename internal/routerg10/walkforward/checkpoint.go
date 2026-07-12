package walkforward

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("walkforward: checkpoint path required")
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
	if cp.SchemaVersion != SchemaVersionCheckpoint {
		return Checkpoint{}, false, errors.New("walkforward: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("walkforward: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = SchemaVersionCheckpoint
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

// VerifyCheckpointBinding rejects drift across validation/survivor/config/maps.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, manifestHash, survivorsHash string, mapManifestHashes map[string]string) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != g10protocol.Generation || cp.ProtocolHash != g10protocol.RouterProtocolHash {
		return errors.New("walkforward: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != g10protocol.HistoryDataHash {
		return errors.New("walkforward: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("walkforward: checkpoint config mismatch")
	}
	if cp.PlaybookManifestHash != manifestHash {
		return errors.New("walkforward: checkpoint manifest mismatch")
	}
	if cp.ValidationReportHash != cfg.ValidationReportHash || cp.SurvivorsHash != survivorsHash {
		return errors.New("walkforward: checkpoint validation binding mismatch")
	}
	for profile, want := range mapManifestHashes {
		if cp.MapManifestHashes[profile] != want {
			return fmt.Errorf("walkforward: checkpoint map manifest mismatch for %s", profile)
		}
	}
	return nil
}
