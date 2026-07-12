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
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-playbooks", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	playbookLimit := fs.Int("playbook-limit", 0, "max Event Playbooks (required, 1..64)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow truncated smoke residual map inputs")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	var residualMapDirs multiFlag
	fs.Var(&residualMapDirs, "residual-map-dir", "residual-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-playbooks: --catalog, --history-root, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g10-playbooks: --max-files and --max-lines must be positive")
	}
	if *playbookLimit <= 0 || *playbookLimit > playbook.MaxPlaybookCandidates {
		return fmt.Errorf("router-g10-playbooks: --playbook-limit must be in 1..%d", playbook.MaxPlaybookCandidates)
	}
	if len(residualMapDirs) == 0 {
		return errors.New("router-g10-playbooks: at least one --residual-map-dir is required")
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
	absMapDirs := make([]string, len(residualMapDirs))
	for i, dir := range residualMapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := playbook.RunConfig{
		SchemaVersion:       playbook.SchemaVersionConfig,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		ResidualMapDirs:     absMapDirs,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		PlaybookLimit:       *playbookLimit,
		ProgressEvery:       1000,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
		Generation:          protocol.Generation,
		ProtocolHash:        protocol.RouterProtocolHash,
		HistoryDataHash:     protocol.HistoryDataHash,
	}
	runner := playbook.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, playbooks int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d playbooks=%d\n", attempts, playbooks)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"playbooks\":%d,\"ledger_attempts\":%d,\"exam_actions\":%d}\n",
		len(result.Playbooks), len(result.Ledger.Attempts), protocol.ExamActions)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
