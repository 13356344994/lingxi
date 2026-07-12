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
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/walkforward"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-walk-forward", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	playbookOutputDir := fs.String("playbook-output-dir", "", "playbooks output directory")
	validationDir := fs.String("validation-dir", "", "validation output directory")
	outputDir := fs.String("output-dir", "", "walk-forward output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	playbookManifestHash := fs.String("playbook-manifest-hash", "", "required sealed event_playbook_manifest hash")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	fingerprintPath := fs.String("fingerprint-path", "", "path to personal_execution_fingerprint.json")
	fingerprintHash := fs.String("fingerprint-hash", "", "required fingerprint hash binding")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow truncated smoke map inputs")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	var residualMapDirs multiFlag
	fs.Var(&residualMapDirs, "residual-map-dir", "residual-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *playbookOutputDir == "" || *validationDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-walk-forward: --catalog, --history-root, --playbook-output-dir, --validation-dir, --output-dir, --checkpoint are required")
	}
	if *playbookManifestHash == "" {
		return errors.New("router-g10-walk-forward: --playbook-manifest-hash is required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g10-walk-forward: --max-files and --max-lines must be positive")
	}
	if len(residualMapDirs) == 0 {
		return errors.New("router-g10-walk-forward: at least one --residual-map-dir is required")
	}
	if *fingerprintPath == "" || *fingerprintHash == "" {
		return errors.New("router-g10-walk-forward: --fingerprint-path and --fingerprint-hash are required")
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
	absPlaybooks, err := filepath.Abs(*playbookOutputDir)
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
	absMapDirs := make([]string, len(residualMapDirs))
	for i, dir := range residualMapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := walkforward.DefaultRunConfig(absHistory, absPlaybooks, absValidation, absOutput, absMapDirs)
	cfg.MaxFiles = *maxFiles
	cfg.MaxLines = *maxLines
	cfg.AllowTruncatedSmoke = *allowTruncatedSmoke
	cfg.PlaybookManifestHash = *playbookManifestHash
	cfg.FingerprintPath = *fingerprintPath
	cfg.FingerprintHash = *fingerprintHash
	runner := walkforward.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(survivorIndex int, playbookHash, profileID string) {
			hash := playbookHash
			if len(hash) > 12 {
				hash = hash[:12]
			}
			fmt.Fprintf(os.Stderr, "progress survivor=%d playbook=%s profile=%s\n", survivorIndex, hash, profileID)
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

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
