package fingerprint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint binds resume offsets with config/output integrity.
type Checkpoint struct {
	SchemaVersion   string              `json:"schema_version"`
	Generation      string              `json:"generation"`
	ConfigHash      string              `json:"config_hash"`
	OutputDir       string              `json:"output_dir"`
	SourceOffsets   map[string]int      `json:"source_offsets"`
	SourceStatus    map[string]SourceStatus `json:"source_status"`
	Equity          EquitySummary       `json:"equity"`
	Accumulator     StoreSnapshot       `json:"accumulator"`
	Done            bool                `json:"done"`
	SavedAt         time.Time           `json:"saved_at"`
	CheckpointHash  string              `json:"checkpoint_hash"`
}

// StoreSnapshot is the serializable aggregation state.
type StoreSnapshot struct {
	Cells []cellSnapshot `json:"cells"`
}

type cellSnapshot struct {
	Key        CellKey              `json:"key"`
	FeeBPS     []float64            `json:"fee_bps"`
	EntrySlip  []float64            `json:"entry_slip_bps"`
	ExitSlip   []float64            `json:"exit_slip_bps"`
	MakerWait  []float64            `json:"maker_wait_ms"`
	LatencyMS  []float64            `json:"latency_ms"`
	Funding    []float64            `json:"funding_rate"`
	MakerFill  rateCounter          `json:"maker_fill"`
	CancelRisk rateCounter          `json:"cancel_risk"`
	Samples    int                  `json:"samples"`
	Instrument InstrumentConstraints `json:"instrument"`
}

func snapshotStore(s *store) StoreSnapshot {
	keys := make([]string, 0, len(s.cells))
	for k := range s.cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := StoreSnapshot{Cells: make([]cellSnapshot, 0, len(keys))}
	for _, id := range keys {
		acc := s.cells[id]
		out.Cells = append(out.Cells, cellSnapshot{
			Key:        acc.key,
			FeeBPS:     append([]float64(nil), acc.feeBPS.values...),
			EntrySlip:  append([]float64(nil), acc.entrySlip.values...),
			ExitSlip:   append([]float64(nil), acc.exitSlip.values...),
			MakerWait:  append([]float64(nil), acc.makerWait.values...),
			LatencyMS:  append([]float64(nil), acc.latencyMS.values...),
			Funding:    append([]float64(nil), acc.funding.values...),
			MakerFill:  acc.makerFill,
			CancelRisk: acc.cancelRisk,
			Samples:    acc.samples,
			Instrument: acc.instrument,
		})
	}
	return out
}

func restoreStore(snap StoreSnapshot) *store {
	s := newStore()
	for _, cs := range snap.Cells {
		acc := s.cell(cs.Key)
		acc.feeBPS.values = append(acc.feeBPS.values, cs.FeeBPS...)
		acc.entrySlip.values = append(acc.entrySlip.values, cs.EntrySlip...)
		acc.exitSlip.values = append(acc.exitSlip.values, cs.ExitSlip...)
		acc.makerWait.values = append(acc.makerWait.values, cs.MakerWait...)
		acc.latencyMS.values = append(acc.latencyMS.values, cs.LatencyMS...)
		acc.funding.values = append(acc.funding.values, cs.Funding...)
		acc.makerFill = cs.MakerFill
		acc.cancelRisk = cs.CancelRisk
		acc.samples = cs.Samples
		acc.instrument = cs.Instrument
	}
	return s
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("fingerprint: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("fingerprint: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("fingerprint: checkpoint hash mismatch (tamper/drift)")
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
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig) error {
	if cp.Generation != cfg.Generation {
		return fmt.Errorf("fingerprint: checkpoint generation %q != config %q", cp.Generation, cfg.Generation)
	}
	want, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != want {
		return errors.New("fingerprint: checkpoint config hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("fingerprint: checkpoint output dir mismatch")
	}
	return nil
}

// ConfigFingerprint hashes bounded CLI knobs.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}
