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
	"lingxi/internal/routerg6/reverseevent"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g6-reverse-events", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	cardLimit := fs.Int("card-limit", 0, "max EventCards (required, 1..256)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow truncated smoke map inputs")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "event-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g6-reverse-events: --catalog, --history-root, --output-dir, --checkpoint are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g6-reverse-events: --max-files and --max-lines must be positive")
	}
	if *cardLimit <= 0 || *cardLimit > reverseevent.MaxCardCandidates {
		return fmt.Errorf("router-g6-reverse-events: --card-limit must be in 1..%d", reverseevent.MaxCardCandidates)
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g6-reverse-events: at least one --map-dir is required")
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
	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := reverseevent.RunConfig{
		SchemaVersion:       reverseevent.SchemaVersionConfig,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		MapDirs:             absMapDirs,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		CardLimit:           *cardLimit,
		ProgressEvery:       1000,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
		Generation:          protocol.Generation,
		ProtocolHash:        protocol.RouterProtocolHash,
		HistoryDataHash:     protocol.HistoryDataHash,
	}
	runner := reverseevent.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, cards int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d cards=%d\n", attempts, cards)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"cards\":%d,\"ledger_attempts\":%d,\"exam_actions\":%d}\n",
		len(result.Cards), len(result.Ledger.Attempts), protocol.ExamActions)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
