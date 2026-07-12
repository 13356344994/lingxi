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
	"lingxi/internal/routerg4/reversemine"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g4-reverse-mine", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	candidateLimit := fs.Int("candidate-limit", 0, "max candidates (required, 1..1024)")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "profit-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g4-reverse-mine: --catalog, --history-root, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g4-reverse-mine: --max-files and --max-lines must be positive")
	}
	if *candidateLimit <= 0 || *candidateLimit > protocol.MaxTotalCandidates {
		return fmt.Errorf("router-g4-reverse-mine: --candidate-limit must be in 1..%d", protocol.MaxTotalCandidates)
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g4-reverse-mine: at least one --map-dir is required")
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
	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := reversemine.RunConfig{
		SchemaVersion:   reversemine.SchemaVersionConfig,
		HistoryRoot:     absHistory,
		OutputDir:       absOutput,
		MapDirs:         absMapDirs,
		MaxFiles:        *maxFiles,
		MaxLines:        *maxLines,
		CandidateLimit:  *candidateLimit,
		ProgressEvery:   protocol.DiscoveryProgressEvery,
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	runner := reversemine.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, candidates int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d candidates=%d\n", attempts, candidates)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"candidates\":%d,\"ledger_entries\":%d,\"exam_actions\":%d}\n",
		len(result.Candidates), len(result.Ledger.Entries), protocol.ExamActions)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
