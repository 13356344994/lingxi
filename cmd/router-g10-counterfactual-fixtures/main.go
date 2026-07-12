package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/routerg10/counterfactual"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-counterfactual-fixtures", flag.ContinueOnError)
	outputDir := fs.String("output-dir", "", "output directory for fixture bundle")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-counterfactual-fixtures: --output-dir and --checkpoint are required")
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	bundle, err := counterfactual.WriteFixtures(absOutput)
	if err != nil {
		return err
	}
	cp := checkpoint{
		SchemaVersion: counterfactual.SchemaVersionFixture,
		OutputDir:     absOutput,
		BundleHash:    bundle.BundleHash,
		ScenarioCount: len(bundle.Scenarios),
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*checkpointPath, raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d scenarios bundle_hash=%s\n", len(bundle.Scenarios), bundle.BundleHash)
	return nil
}

type checkpoint struct {
	SchemaVersion string `json:"schema_version"`
	OutputDir     string `json:"output_dir"`
	BundleHash    string `json:"bundle_hash"`
	ScenarioCount int    `json:"scenario_count"`
}
