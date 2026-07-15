package datadrivenv3

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/datadrivenv3/fundingarchive"
	"lingxi/internal/datatruth"
)

var BenchmarkSymbols = []string{
	"ADA-USDT-SWAP", "AVAX-USDT-SWAP", "BTC-USDT-SWAP", "DOGE-USDT-SWAP",
	"ETH-USDT-SWAP", "LINK-USDT-SWAP", "SOL-USDT-SWAP", "XRP-USDT-SWAP",
}

type BenchmarkRun struct {
	Rows               int64   `json:"rows"`
	EmissionNanos      int64   `json:"emission_nanos"`
	StateRowsPerSecond float64 `json:"state_rows_per_second"`
	DatasetHash        string  `json:"dataset_hash"`
	PeakRSSBytes       int64   `json:"peak_rss_bytes"`
}
type BenchmarkReceipt struct {
	SchemaVersion        string          `json:"schema_version"`
	Status               string          `json:"status"`
	Symbols              []string        `json:"symbols"`
	CompleteUTCDays      int             `json:"complete_utc_days"`
	MinimumRowsPerSecond float64         `json:"minimum_state_rows_per_second"`
	Runs                 [2]BenchmarkRun `json:"runs"`
	Deterministic        bool            `json:"hash_deterministic"`
	LabelsRead           bool            `json:"real_reward_labels_read"`
	LegacyDatasetStatus  string          `json:"legacy_dataset_status"`
}

func RunBuilderBenchmark(root, receiptPath string) (BenchmarkReceipt, error) {
	protocol, err := VerifyProtocolChain(root)
	if err != nil {
		return BenchmarkReceipt{}, err
	}
	manifestHash, receiptHash, err := fundingIdentity(root)
	if err != nil {
		return BenchmarkReceipt{}, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(receiptPath), ".dataset-v2-benchmark-*")
	if err != nil {
		return BenchmarkReceipt{}, err
	}
	defer os.RemoveAll(temp)
	receipt := BenchmarkReceipt{SchemaVersion: "lingxi.data-driven-router-v3.builder-benchmark.v1", Status: "FAIL", Symbols: append([]string(nil), BenchmarkSymbols...), CompleteUTCDays: 30, MinimumRowsPerSecond: 10000, LabelsRead: false, LegacyDatasetStatus: StalledV1Status}
	for i := 0; i < 2; i++ {
		out := filepath.Join(temp, "benchmark-run-"+strconv.Itoa(i+1))
		optional := NewHistoricalOptionalLookup(root)
		cfg := BuildConfigV2{Root: root, OutputDir: out, Symbols: BenchmarkSymbols, ProtocolHashes: protocol, FundingManifestHash: manifestHash, FundingReceiptHash: receiptHash, Bounds: [3]time.Time{time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2102, 1, 1, 0, 0, 0, 0, time.UTC)}, CompleteDays: 30, RequireCompleteCrossSection: true, Optional: optional}
		result, err := BuildStateRowsV2(cfg)
		_ = optional.Close()
		if err != nil {
			return receipt, err
		}
		hash, err := benchmarkDatasetHash(result)
		if err != nil {
			return receipt, err
		}
		rate := float64(result.Rows) / (float64(result.EmissionNanos) / float64(time.Second))
		receipt.Runs[i] = BenchmarkRun{Rows: result.Rows, EmissionNanos: result.EmissionNanos, StateRowsPerSecond: rate, DatasetHash: hash, PeakRSSBytes: peakRSS()}
	}
	receipt.Deterministic = receipt.Runs[0].DatasetHash == receipt.Runs[1].DatasetHash
	if receipt.Deterministic && receipt.Runs[0].StateRowsPerSecond >= receipt.MinimumRowsPerSecond && receipt.Runs[1].StateRowsPerSecond >= receipt.MinimumRowsPerSecond {
		receipt.Status = "PASS"
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return receipt, err
	}
	if err := writeAtomic(receiptPath, append(raw, '\n')); err != nil {
		return receipt, err
	}
	if receipt.Status != "PASS" {
		return receipt, errors.New("builder benchmark gate failed; full build forbidden")
	}
	return receipt, nil
}
func benchmarkDatasetHash(result BuildResultV2) (string, error) {
	value := struct {
		Schema string
		Order  [FeatureCount]string
		Rows   int64
		Hashes []string
	}{Schema: result.StateSchema, Order: result.FeatureOrder, Rows: result.Rows}
	for _, s := range result.Shards {
		value.Hashes = append(value.Hashes, s.SHA256)
	}
	hash, _, err := datatruth.HashJSON(value)
	return hash, err
}
func fundingIdentity(root string) (string, string, error) {
	base := filepath.Join(root, "records/history/okx_funding_v3")
	raw, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil {
		return "", "", err
	}
	var manifest fundingarchive.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", "", err
	}
	declared := manifest.ManifestHash
	manifest.ManifestHash = ""
	computed, _, err := datatruth.HashJSON(manifest)
	if err != nil || declared == "" || computed != declared {
		return "", "", errors.New("funding manifest identity mismatch")
	}
	receiptRaw, err := os.ReadFile(filepath.Join(base, "receipt.json"))
	if err != nil {
		return "", "", err
	}
	var receipt struct {
		ManifestHash string `json:"manifest_hash"`
		PlanHash     string `json:"plan_hash"`
	}
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		return "", "", err
	}
	if receipt.ManifestHash != declared || receipt.PlanHash != manifest.PlanHash {
		return "", "", errors.New("funding receipt identity mismatch")
	}
	sum := sha256.Sum256(receiptRaw)
	return declared, hex.EncodeToString(sum[:]), nil
}
func peakRSS() int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmHWM:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					return kb * 1024
				}
			}
		}
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return int64(stats.Sys)
}
