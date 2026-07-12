package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/walkforward"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g6-walk-forward", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	cardOutputDir := fs.String("card-output-dir", "", "reverse-events output directory")
	validationDir := fs.String("validation-dir", "", "validation output directory")
	outputDir := fs.String("output-dir", "", "walk-forward output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	cardManifestHash := fs.String("card-manifest-hash", "", "required sealed event_card_manifest hash")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow truncated smoke map inputs")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "event-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *cardOutputDir == "" || *validationDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g6-walk-forward: --catalog, --history-root, --card-output-dir, --validation-dir, --output-dir, --checkpoint are required")
	}
	if *cardManifestHash == "" {
		return errors.New("router-g6-walk-forward: --card-manifest-hash is required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g6-walk-forward: --max-files and --max-lines must be positive")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g6-walk-forward: at least one --map-dir is required")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(protocol.RuntimeGOMAXPROCS)
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	absCards, err := filepath.Abs(*cardOutputDir)
	if err != nil {
		return err
	}
	absValidation, err := filepath.Abs(*validationDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absOutput, 0o755); err != nil {
		return err
	}
	raw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := walkforward.DefaultRunConfig(absHistory, absCards, absValidation, absOutput, absMapDirs)
	cfg.MaxFiles = *maxFiles
	cfg.MaxLines = *maxLines
	cfg.AllowTruncatedSmoke = *allowTruncatedSmoke
	cfg.CardManifestHash = *cardManifestHash
	runner := walkforward.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(survivorIndex int, cardHash, profileID string) {
			fmt.Fprintf(os.Stderr, "progress survivor=%d card=%s profile=%s\n", survivorIndex, cardHash[:min(12, len(cardHash))], profileID)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"status\":%q,\"survivors\":%d,\"pass\":%d,\"fail\":%d,\"exam_actions\":%d}\n",
		result.WFReport.Status, result.WFReport.SurvivorCount, result.WFReport.PassCount,
		result.WFReport.FailCount, protocol.ExamActions)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
