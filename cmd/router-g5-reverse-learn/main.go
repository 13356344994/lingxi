package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g5-reverse-learn", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	profileID := fs.String("profile", "", "profile id: okx_1h or okx_5m")
	cubeDir := fs.String("cube-dir", "", "cube oracle output directory")
	featureDir := fs.String("feature-dir", "", "feature context output directory")
	outputDir := fs.String("output-dir", "", "policy output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *profileID == "" || *cubeDir == "" || *featureDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g5-reverse-learn: --catalog, --profile, --cube-dir, --feature-dir, --output-dir, --checkpoint are required")
	}
	raw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	cfg := reverselearn.RunConfig{
		SchemaVersion:   reverselearn.SchemaVersionConfig,
		ProfileID:       *profileID,
		CubeDir:         *cubeDir,
		FeatureDir:      *featureDir,
		OutputDir:       absOutput,
		Generation:      reverselearn.DefaultGeneration(),
		ProtocolHash:      reverselearn.DefaultProtocolHash(),
		HistoryDataHash:   reverselearn.DefaultHistoryDataHash(),
	}
	runner := reverselearn.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	if reverselearn.IsNoFrozenPolicy(result.Manifest) {
		fmt.Fprintf(os.Stdout, "{\"profile\":%q,\"status\":%q,\"experiment_hash\":%q,\"exam_actions\":%d}\n",
			result.Manifest.ProfileID, reverselearn.StatusNoFrozenPolicy, result.Manifest.ExperimentHash, g5protocol.ExamActions)
		return nil
	}
	fmt.Fprintf(os.Stdout, "{\"profile\":%q,\"policy_hash\":%q,\"experiment_hash\":%q,\"exam_actions\":%d}\n",
		result.Policy.ProfileID, result.Policy.PolicyHash, result.Policy.ExperimentHash, g5protocol.ExamActions)
	return nil
}
