// data-driven-v3-dataset refuses to construct state or reward material until
// the outcome-blind fitness gate has passed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/datadrivenv3"
	"lingxi/internal/datatruth"
)

const outputDir = "research/firstbattle/data_driven_router_v3"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "build-state-action-dataset:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("build-state-action-dataset", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	benchmark := fs.Bool("benchmark", false, "run only the frozen 8-symbol/30-day builder benchmark")
	benchmarkReceipt := fs.String("benchmark-receipt", "", "benchmark receipt path")
	preflightOnly := fs.Bool("preflight-only", false, "run and publish only the label-free data fitness preflight")
	if err := fs.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if *benchmark {
		path := *benchmarkReceipt
		if path == "" {
			path = filepath.Join(abs, outputDir, "dataset_builder_v2_benchmark_receipt.json")
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(abs, path)
		}
		_, err := datadrivenv3.RunBuilderBenchmark(abs, path)
		return err
	}
	fitness, err := datadrivenv3.Measure(abs)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(fitness, "", "  ")
	if err != nil {
		return err
	}
	hash, _, err := datatruthHash(fitness)
	if err != nil {
		return err
	}
	receipt := map[string]any{"schema_version": "lingxi.data-driven-router-v3.measurement-receipt.v1", "labels_unblinded": false, "fitness_hash": hash, "status": fitness.Status, "highest_blocker": fitness.HighestBlocker}
	if err := write(filepath.Join(abs, outputDir, "dataset_data_fitness.json"), raw); err != nil {
		return err
	}
	if err := write(filepath.Join(abs, outputDir, "dataset_data_fitness_measurement_receipt.json"), mustJSON(receipt)); err != nil {
		return err
	}
	md := "# V3 Dataset Data Fitness\n\nStatus: **" + fitness.Status + "**\n\nLabels unblinded: `false`\n\nHighest blocker: `" + fitness.HighestBlocker + "`\n\nThe outcome-blind preflight read only history catalog-approved price/funding/optional metadata and aggregate P0 cost evidence. No StateRow shards, reward cube, reward labels, outcomes, PnL, winners, or route results were read or created.\n"
	if err := write(filepath.Join(abs, outputDir, "dataset_data_fitness.md"), []byte(md)); err != nil {
		return err
	}
	journal := strings.Join(fitness.DecisionJournal, "\n") + "\nfitness_hash=" + hash + "\n"
	if err := write(filepath.Join(abs, outputDir, "dataset_decision_journal.txt"), []byte(journal)); err != nil {
		return err
	}
	if fitness.Status != "PASS" {
		return fmt.Errorf("%s: %s; stopped before dataset construction and reward access", fitness.Status, fitness.HighestBlocker)
	}
	if *preflightOnly {
		return nil
	}
	bounds, err := datasetBounds(fitness.PartitionBoundaries)
	if err != nil {
		return err
	}
	v2Dir := filepath.Join(abs, outputDir, datadrivenv3.DatasetDirectoryV3)
	optional := datadrivenv3.NewHistoricalOptionalLookup(abs)
	defer optional.Close()
	result, err := datadrivenv3.BuildStateRowsV2(datadrivenv3.BuildConfigV2{
		Root: abs, OutputDir: v2Dir, Bounds: bounds,
		ProtocolHashes:      fitness.ProtocolHashes,
		FundingManifestHash: fitness.FundingManifestHash, FundingReceiptHash: fitness.FundingReceiptHash,
		Optional: optional, Progress: os.Stdout,
		ThrottleEveryRows: 10000, ThrottleSleep: 250 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	evaluatorHash, err := datadrivenv3.SHA256File(filepath.Join(abs, "internal/datadrivenv3/reward_evaluator.go"))
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"schema_version": result.SchemaVersion, "labels_unblinded": false,
		"state_schema_version": result.StateSchema, "rows": result.Rows, "shards": result.Shards,
		"checkpoint": result.Checkpoint, "fitness_hash": hash,
		"funding_manifest_hash": fitness.FundingManifestHash, "funding_receipt_hash": fitness.FundingReceiptHash,
		"reward_evaluator_code_hash": evaluatorHash, "reward_label_bundle": "SEALED_NOT_READ_OR_CREATED",
		"legacy_dataset_status": map[string]string{
			"dataset_state_okx_1h":    datadrivenv3.StalledV1Status,
			"dataset_state_okx_1h_v2": "BLOCKED_PURGE_MISSING",
		},
	}
	manifestHash, _, err := datatruthHash(manifest)
	if err != nil {
		return err
	}
	manifest["manifest_hash"] = manifestHash
	if err := write(filepath.Join(v2Dir, "dataset_manifest.json"), mustJSON(manifest)); err != nil {
		return err
	}
	datasetReceipt := map[string]any{"schema_version": "lingxi.data-driven-router-v3.dataset-receipt.v1", "labels_unblinded": false, "manifest_hash": manifestHash, "rows": result.Rows, "shards": len(result.Shards), "reward_label_bundle": "SEALED_NOT_READ_OR_CREATED"}
	if err := write(filepath.Join(v2Dir, "dataset_receipt.json"), mustJSON(datasetReceipt)); err != nil {
		return err
	}
	return nil
}

func datatruthHash(v any) (string, []byte, error) {
	// Kept local so this command's receipt is canonicalized by the repository primitive.
	return datatruth.HashJSON(v)
}
func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return b
}
func write(path string, b []byte) error { return os.WriteFile(path, append(b, '\n'), 0o644) }

func datasetBounds(values map[string]string) ([3]time.Time, error) {
	var out [3]time.Time
	for i, key := range []string{"validation_start", "leaf_wf_start", "router_holdout_start"} {
		value, err := time.Parse(time.RFC3339, values[key])
		if err != nil {
			return out, err
		}
		out[i] = value
	}
	return out, nil
}
