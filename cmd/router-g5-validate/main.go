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
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/portfoliosim"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g5-validate", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	policyDir := fs.String("policy-dir", "", "frozen policy output directory")
	featureDir := fs.String("feature-dir", "", "validation feature context directory")
	outputDir := fs.String("output-dir", "", "validation output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	profileID := fs.String("profile", "", "profile id: okx_1h or okx_5m")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *policyDir == "" || *featureDir == "" || *outputDir == "" || *checkpointPath == "" || *profileID == "" {
		return errors.New("router-g5-validate: --catalog, --history-root, --policy-dir, --feature-dir, --output-dir, --checkpoint, --profile are required")
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
	if err := os.MkdirAll(absOutput, 0o755); err != nil {
		return err
	}
	bundle, err := policy.LoadPolicyBundle(*policyDir)
	if err != nil {
		return err
	}
	cfg := portfoliosim.DefaultRunConfig(*profileID, *historyRoot, *policyDir, *featureDir, absOutput, bundle)
	runner := portfoliosim.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
	}
	result, err := runner.Run()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"status\":%q,\"report_hash\":%q,\"exam_actions\":%d}\n",
		result.Report.Status, result.Report.ReportHash, g5protocol.ExamActions)
	return nil
}

// exported for tests
func runWithContext(ctx context.Context, args []string) error {
	_ = ctx
	return run(args)
}
