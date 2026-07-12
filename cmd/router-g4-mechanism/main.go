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
	"lingxi/internal/routerg4/mechanism"
	"lingxi/internal/routerg4/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g4-mechanism", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	validationOutput := fs.String("validation-output", "", "validation output directory")
	candidateOutput := fs.String("candidate-output", "", "reverse-mine output directory")
	outputDir := fs.String("output-dir", "", "explicit mechanism output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "profit-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *validationOutput == "" ||
		*candidateOutput == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g4-mechanism: --catalog, --history-root, --validation-output, --candidate-output, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g4-mechanism: --max-files and --max-lines must be positive")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g4-mechanism: at least one --map-dir is required")
	}

	validationReportHash, err := loadValidationReportHash(*validationOutput)
	if err != nil {
		return err
	}
	candidateManifestHash, err := loadCandidateManifestHash(*candidateOutput)
	if err != nil {
		return err
	}

	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absValidation, err := filepath.Abs(*validationOutput)
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

	cfg := mechanism.DefaultRunConfig(
		absHistory, absValidation, absCandidate, absOutput, absMapDirs,
		*maxFiles, *maxLines, validationReportHash, candidateManifestHash,
	)
	runner := mechanism.Runner{
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
	fmt.Fprintf(os.Stdout,
		"{\"survivors\":%d,\"explained\":%d,\"research_pass\":%d,\"unexplained_research_pass\":%d,\"exam_actions\":%d}\n",
		result.MechanismReport.SurvivorCount,
		result.MechanismReport.ExplainedCount,
		result.StageSummary.ResearchPassLeaves,
		result.StageSummary.UnexplainedResearchPass,
		protocol.ExamActions,
	)
	return nil
}

func loadValidationReportHash(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "g4_validation_report.json"))
	if err != nil {
		return "", err
	}
	var payload struct {
		ReportHash string `json:"report_hash"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if payload.ReportHash == "" {
		return "", errors.New("router-g4-mechanism: validation report hash missing")
	}
	return payload.ReportHash, nil
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
		return "", errors.New("router-g4-mechanism: candidate manifest hash missing")
	}
	return payload.ManifestHash, nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
