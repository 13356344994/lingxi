package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/walkforward"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g5-walk-forward", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	policyDir := fs.String("policy-dir", "", "frozen policy output directory")
	featureDir := fs.String("feature-dir", "", "walk_forward feature context directory")
	validationDir := fs.String("validation-dir", "", "validation output directory with g5_validation_report.json")
	outputDir := fs.String("output-dir", "", "walk-forward output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	profileID := fs.String("profile", "", "profile id: okx_1h or okx_5m")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *policyDir == "" || *featureDir == "" ||
		*validationDir == "" || *outputDir == "" || *checkpointPath == "" || *profileID == "" {
		return errors.New("router-g5-walk-forward: --catalog, --history-root, --policy-dir, --feature-dir, --validation-dir, --output-dir, --checkpoint, --profile are required")
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
	cfg := walkforward.DefaultRunConfig(*profileID, *historyRoot, *policyDir, *featureDir, *validationDir, absOutput, bundle, "")
	runner := walkforward.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
	}
	result, err := runner.Run()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"status\":%q,\"report_hash\":%q,\"exam_actions\":%d}\n",
		result.WFReport.Status, result.WFReport.ReportHash, g5protocol.ExamActions)
	return nil
}
