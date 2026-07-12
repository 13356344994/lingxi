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
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/validation"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g4-validate", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	candidateOutput := fs.String("candidate-output", "", "reverse-mine output directory")
	outputDir := fs.String("output-dir", "", "explicit validation output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "profit-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *candidateOutput == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g4-validate: --catalog, --history-root, --candidate-output, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g4-validate: --max-files and --max-lines must be positive")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g4-validate: at least one --map-dir is required")
	}

	manifest, err := loadCandidateManifestHash(*candidateOutput)
	if err != nil {
		return err
	}

	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absCandidate, err := filepath.Abs(*candidateOutput)
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

	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}

	cfg := validation.DefaultRunConfig(absHistory, absCandidate, absOutput, absMapDirs, *maxFiles, *maxLines, manifest)
	runner := validation.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(lines int64, symbol string) {
			fmt.Fprintf(os.Stderr, "progress lines=%d symbol=%s\n", lines, symbol)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"pass\":%d,\"fail\":%d,\"inconclusive\":%d,\"exam_actions\":%d}\n",
		result.Report.PassCount, result.Report.FailCount, result.Report.InconclusiveCount, protocol.ExamActions)
	return nil
}

func loadCandidateManifestHash(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "candidate_manifest.json"))
	if err != nil {
		return "", err
	}
	var payload struct {
		ManifestHash string `json:"manifest_hash"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if payload.ManifestHash == "" {
		return "", errors.New("router-g4-validate: candidate manifest hash missing")
	}
	return payload.ManifestHash, nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
