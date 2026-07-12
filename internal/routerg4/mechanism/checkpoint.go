package mechanism

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g4-mechanism.checkpoint.v1"

// Checkpoint binds resume state with catalog/config/map/validation integrity.
type Checkpoint struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	ConfigHash               string            `json:"config_hash"`
	CatalogHash              string            `json:"catalog_hash"`
	ValidationReportHash     string            `json:"validation_report_hash"`
	ValidationSurvivorsHash  string            `json:"validation_survivors_hash"`
	ValidationReceiptHash    string            `json:"validation_receipt_hash"`
	CandidateManifestHash    string            `json:"candidate_manifest_hash"`
	CandidatesHash           string            `json:"candidates_hash"`
	LedgerHash               string            `json:"ledger_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	OutputDir                string            `json:"output_dir"`
	NextSymbolIndexByProfile map[string]int    `json:"next_symbol_index_by_profile"`
	WFStatsByHash            map[string]checkpointWFStats `json:"wf_stats_by_hash"`
	LinesProcessed           int64             `json:"lines_processed"`
	Done                     bool              `json:"done"`
	SavedAt                  time.Time         `json:"saved_at"`
	CheckpointHash           string            `json:"checkpoint_hash"`
}

type checkpointWFStats struct {
	Count         int     `json:"count"`
	GrossSum      float64 `json:"gross_sum"`
	FeeSum        float64 `json:"fee_sum"`
	ConsSum       float64 `json:"cons_sum"`
	DelaySum      float64 `json:"delay_sum"`
	StressSum     float64 `json:"stress_sum"`
	CapacityFails int     `json:"capacity_fails"`
	PositiveSum   float64 `json:"positive_sum"`
	MaxPositive   float64 `json:"max_positive"`
	HorizonNanos  int64   `json:"horizon_nanos"`
	BlockSums     map[string]float64 `json:"block_sums"`
	BlockCounts   map[string]int     `json:"block_counts"`
	Days          []string           `json:"days"`
	Symbols       []string           `json:"symbols"`
	Reservoir     []conservativeReservoirItem `json:"reservoir"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("mechanism: checkpoint path required")
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
	if cp.SchemaVersion != checkpointSchemaVersion {
		return Checkpoint{}, false, errors.New("mechanism: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("mechanism: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = checkpointSchemaVersion
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

// VerifyCheckpointBinding rejects drift across catalog/config/maps/validation.
func VerifyCheckpointBinding(
	cp Checkpoint,
	cfg RunConfig,
	catalogHash string,
	validationBundle ValidationBundle,
	candidatesHash, ledgerHash string,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("mechanism: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("mechanism: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("mechanism: checkpoint config hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("mechanism: checkpoint catalog hash mismatch")
	}
	if cp.ValidationReportHash != validationBundle.Report.ReportHash {
		return errors.New("mechanism: checkpoint validation report hash mismatch")
	}
	if cp.ValidationSurvivorsHash != validationBundle.Survivors.SurvivorsHash {
		return errors.New("mechanism: checkpoint validation survivors hash mismatch")
	}
	if cp.ValidationReceiptHash != validationBundle.Receipt.ReceiptHash {
		return errors.New("mechanism: checkpoint validation receipt hash mismatch")
	}
	if cp.CandidateManifestHash != cfg.CandidateManifestHash {
		return errors.New("mechanism: checkpoint candidate manifest hash mismatch")
	}
	if cp.CandidatesHash != candidatesHash {
		return errors.New("mechanism: checkpoint candidates hash mismatch")
	}
	if cp.LedgerHash != ledgerHash {
		return errors.New("mechanism: checkpoint ledger hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("mechanism: checkpoint output dir mismatch")
	}
	for profile, want := range mapManifestHashes {
		if got := cp.MapManifestHashes[profile]; got != want {
			return fmt.Errorf("mechanism: checkpoint map manifest hash mismatch for %s", profile)
		}
	}
	return nil
}

func exportWFStats(acc *wfAccumulator) checkpointWFStats {
	if acc == nil {
		return checkpointWFStats{BlockSums: map[string]float64{}, BlockCounts: map[string]int{}}
	}
	days := make([]string, 0, len(acc.days))
	for day := range acc.days {
		days = append(days, day)
	}
	symbols := make([]string, 0, len(acc.symbols))
	for symbol := range acc.symbols {
		symbols = append(symbols, symbol)
	}
	return checkpointWFStats{
		Count: acc.count, GrossSum: acc.grossSum, FeeSum: acc.feeSum,
		ConsSum: acc.consSum, DelaySum: acc.delaySum, StressSum: acc.stressSum,
		CapacityFails: acc.capacityFails, PositiveSum: acc.positiveSum, MaxPositive: acc.maxPositive,
		HorizonNanos: int64(acc.horizon),
		BlockSums: cloneFloatMap(acc.blockSums), BlockCounts: cloneIntMap(acc.blockCounts),
		Days: days, Symbols: symbols, Reservoir: append([]conservativeReservoirItem(nil), acc.reservoir...),
	}
}

func restoreWFStats(stats checkpointWFStats) *wfAccumulator {
	acc := newWFAccumulator(time.Duration(stats.HorizonNanos))
	acc.count = stats.Count
	acc.grossSum = stats.GrossSum
	acc.feeSum = stats.FeeSum
	acc.consSum = stats.ConsSum
	acc.delaySum = stats.DelaySum
	acc.stressSum = stats.StressSum
	acc.capacityFails = stats.CapacityFails
	acc.positiveSum = stats.PositiveSum
	acc.maxPositive = stats.MaxPositive
	acc.blockSums = cloneFloatMap(stats.BlockSums)
	acc.blockCounts = cloneIntMap(stats.BlockCounts)
	for _, day := range stats.Days {
		acc.days[day] = true
	}
	for _, symbol := range stats.Symbols {
		acc.symbols[symbol] = true
	}
	acc.reservoir = append([]conservativeReservoirItem(nil), stats.Reservoir...)
	return acc
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func restoreAccumulatorsFromCheckpoint(stats map[string]checkpointWFStats) map[string]*wfAccumulator {
	out := map[string]*wfAccumulator{}
	for hash, item := range stats {
		out[hash] = restoreWFStats(item)
	}
	return out
}

// ValidateCompletedOutputs reloads and verifies written artifacts.
func ValidateCompletedOutputs(outputDir string, receipt MechanismReceipt) error {
	readReport := func(name string, dst any, validate func() error) error {
		raw, err := os.ReadFile(filepath.Join(outputDir, name))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return err
		}
		return validate()
	}
	var mechanismReport MechanismReport
	if err := readReport("g4_mechanism_report.json", &mechanismReport, func() error {
		return ValidateMechanismReport(mechanismReport)
	}); err != nil {
		return err
	}
	var finalLeaves FinalLeaves
	if err := readReport("g4_final_leaves.json", &finalLeaves, func() error {
		want, err := hashFinalLeaves(finalLeaves)
		if err != nil {
			return err
		}
		if finalLeaves.LeavesHash != want {
			return errors.New("mechanism: final leaves hash mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	var strictWF StrictWFReport
	if err := readReport("g4_strict_wf_report.json", &strictWF, func() error {
		want, err := hashStrictWFReport(strictWF)
		if err != nil {
			return err
		}
		if strictWF.ReportHash != want {
			return errors.New("mechanism: strict wf report hash mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if receipt.MechanismReportHash != mechanismReport.ReportHash ||
		receipt.StrictWFReportHash != strictWF.ReportHash ||
		receipt.FinalLeavesHash != finalLeaves.LeavesHash {
		return errors.New("mechanism: completed receipt binding mismatch")
	}
	return nil
}
