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
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-residual-map", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	fingerprintPath := fs.String("fingerprint", "", "path to personal_execution_fingerprint.json")
	mapDir := fs.String("map-dir", "", "G6 event-map output directory (repeatable)")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	candidateLimit := fs.Int("candidate-limit", 0, "maximum accepted residual candidates (required, positive)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow an explicitly labeled TRUNCATED_SMOKE subset")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mapDirs, err := parseMapDirs(args, *mapDir)
	if err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *fingerprintPath == "" ||
		*outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-residual-map: --catalog, --history-root, --fingerprint, --output-dir, --checkpoint are required")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g10-residual-map: at least one --map-dir is required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 || *candidateLimit <= 0 {
		return errors.New("router-g10-residual-map: --max-files, --max-lines, and --candidate-limit must be positive")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(residualmap.RuntimeGOMAXPROCS)
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	absFingerprint, err := filepath.Abs(*fingerprintPath)
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
	cfg := residualmap.RunConfig{
		SchemaVersion:       residualmap.SchemaVersionConfig,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		FingerprintPath:     absFingerprint,
		MapDirs:             absMapDirs,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		CandidateLimit:      *candidateLimit,
		ProgressEvery:       residualmap.ProgressEvery,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	runner := residualmap.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, accepted int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d accepted=%d\n", attempts, accepted)
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"accepted\":%d,\"rejected\":%d,\"map_hash\":%q,\"manifest_hash\":%q}\n",
		result.Summary.AcceptedCount, result.Summary.RejectedCount,
		result.Map.MapHash, result.Manifest.ManifestHash)
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
