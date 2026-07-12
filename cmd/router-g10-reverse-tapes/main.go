package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	historydata "lingxi/internal/datatruth/history"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/reversetapes"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-reverse-tapes", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	exclusionMapDir := fs.String("exclusion-map-dir", "", "G10 exclusion-map output directory")
	entryProofDir := fs.String("entry-proof-dir", "", "G10 entry-proof output directory")
	executionPolicyDir := fs.String("execution-policy-dir", "", "G10 execution-policy output directory")
	exitProofPath := fs.String("exit-proof-path", "", "path to exit proofs JSONL")
	announcementDir := fs.String("announcement-dir", "", "G10 announcements output directory")
	mapDir := fs.String("map-dir", "", "G6 event-map output directory (repeatable)")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required when map-dir set, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required when map-dir set, positive)")
	candidateLimit := fs.Int("candidate-limit", 0, "maximum executable profit tapes (required, positive)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow TRUNCATED_SMOKE subset")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mapDirs, err := parseMapDirs(args, *mapDir)
	if err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *exclusionMapDir == "" ||
		*entryProofDir == "" || *executionPolicyDir == "" || *announcementDir == "" ||
		*outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-reverse-tapes: --catalog, --history-root, --exclusion-map-dir, --entry-proof-dir, --execution-policy-dir, --announcement-dir, --output-dir, --checkpoint are required")
	}
	if *candidateLimit <= 0 {
		return errors.New("router-g10-reverse-tapes: --candidate-limit must be positive")
	}
	if len(mapDirs) > 0 && (*maxFiles <= 0 || *maxLines <= 0) {
		return errors.New("router-g10-reverse-tapes: --max-files and --max-lines required when --map-dir is set")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(reversetapes.RuntimeGOMAXPROCS)
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		absMapDirs[i], err = filepath.Abs(dir)
		if err != nil {
			return err
		}
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
	cfg := reversetapes.RunConfig{
		SchemaVersion:       reversetapes.SchemaVersionConfig,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		ExclusionMapDir:     *exclusionMapDir,
		EntryProofDir:       *entryProofDir,
		ExecutionPolicyDir:  *executionPolicyDir,
		ExitProofPath:       *exitProofPath,
		AnnouncementDir:     *announcementDir,
		MapDirs:             absMapDirs,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		CandidateLimit:      *candidateLimit,
		ProgressEvery:       reversetapes.ProgressEvery,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
		Generation:          g10protocolv2.Generation,
		ProtocolHash:        g10protocolv2.RouterProtocolHash,
		HistoryDataHash:     g10protocolv2.HistoryDataHash,
	}
	runner := reversetapes.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, executable, discovery int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d executable=%d discovery=%d\n", attempts, executable, discovery)
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"executable\":%d,\"discovery\":%d,\"rejected\":%d,\"manifest_hash\":%q,\"ledger_hash\":%q}\n",
		result.Summary.ExecutableCount, result.Summary.DiscoveryCount, result.Summary.RejectedCount,
		result.Manifest.ManifestHash, result.Ledger.LedgerHash,
	)
	return nil
}

func parseMapDirs(args []string, single string) ([]string, error) {
	var dirs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--map-dir" && i+1 < len(args) {
			dirs = append(dirs, args[i+1])
			i++
		}
	}
	if single != "" {
		dirs = append(dirs, single)
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out, nil
}
