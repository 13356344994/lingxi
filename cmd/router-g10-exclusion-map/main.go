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
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-exclusion-map", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	mapDir := fs.String("map-dir", "", "G6 event-map output directory (repeatable)")
	announcementDir := fs.String("announcement-dir", "", "G10 announcements output directory")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	candidateLimit := fs.Int("candidate-limit", 0, "maximum accepted exclusion proofs (required, positive)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow an explicitly labeled TRUNCATED_SMOKE subset")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mapDirs, err := parseMapDirs(args, *mapDir)
	if err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *announcementDir == "" ||
		*outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-exclusion-map: --catalog, --history-root, --announcement-dir, --output-dir, --checkpoint are required")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g10-exclusion-map: at least one --map-dir is required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 || *candidateLimit <= 0 {
		return errors.New("router-g10-exclusion-map: --max-files, --max-lines, and --candidate-limit must be positive")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(exclusionmap.RuntimeGOMAXPROCS)
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	absAnnouncement, err := filepath.Abs(*announcementDir)
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
	cfg := exclusionmap.RunConfig{
		SchemaVersion:       exclusionmap.SchemaVersionConfig,
		HistoryRoot:         absHistory,
		OutputDir:           absOutput,
		MapDirs:             absMapDirs,
		AnnouncementDir:     absAnnouncement,
		MaxFiles:            *maxFiles,
		MaxLines:            *maxLines,
		CandidateLimit:      *candidateLimit,
		ProgressEvery:       exclusionmap.ProgressEvery,
		AllowTruncatedSmoke: *allowTruncatedSmoke,
		Generation:          g10protocolv2.Generation,
		ProtocolHash:        g10protocolv2.RouterProtocolHash,
		HistoryDataHash:     g10protocolv2.HistoryDataHash,
	}
	runner := exclusionmap.Runner{
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
		"{\"accepted\":%d,\"rejected\":%d,\"map_hash\":%q,\"manifest_hash\":%q,\"constitution_hash\":%q}\n",
		result.Summary.AcceptedCount, result.Summary.RejectedCount,
		result.Map.MapHash, result.Manifest.ManifestHash, result.Map.ConstitutionHash,
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
