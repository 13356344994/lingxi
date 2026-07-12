package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"lingxi/internal/routerg10/announcements"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-announcements", flag.ContinueOnError)
	archiveRoot := fs.String("archive-root", "", "historical announcement archive root")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum lines per archive source (required, positive)")
	pageSize := fs.Int("page-size", announcements.DefaultPageSize, "archive pagination page size hint")
	okxArchive := fs.String("okx-archive", "", "override okx_announcements.jsonl path")
	binanceArchive := fs.String("binance-archive", "", "override binance_announcements.jsonl path")
	bybitArchive := fs.String("bybit-archive", "", "override bybit_announcements.jsonl path")
	gateArchive := fs.String("gate-archive", "", "override gate_announcements.jsonl path")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *archiveRoot == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-announcements: --archive-root, --output-dir, --checkpoint are required")
	}
	if *maxLines <= 0 {
		return errors.New("router-g10-announcements: --max-lines must be positive")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(announcements.RuntimeGOMAXPROCS)
	}
	absRoot, err := filepath.Abs(*archiveRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	sources := announcements.DefaultSourcePaths(absRoot)
	if *okxArchive != "" {
		sources.OKX = *okxArchive
	}
	if *binanceArchive != "" {
		sources.Binance = *binanceArchive
	}
	if *bybitArchive != "" {
		sources.Bybit = *bybitArchive
	}
	if *gateArchive != "" {
		sources.Gate = *gateArchive
	}
	cfg := announcements.RunConfig{
		SchemaVersion: announcements.ConfigSchema,
		Generation:    announcements.Generation,
		ArchiveRoot:   absRoot,
		OutputDir:     absOutput,
		Sources:       sources,
		MaxLines:      *maxLines,
		PageSize:      *pageSize,
		LowCPU:        *lowCPU,
	}
	if err := announcements.ValidateRunConfig(cfg); err != nil {
		return err
	}
	runner := announcements.Runner{
		Config:         cfg,
		CheckpointPath: *checkpointPath,
		OnProgress: func(source string, lines int) {
			if lines%announcements.ProgressEvery == 0 || lines < announcements.ProgressEvery {
				fmt.Fprintf(os.Stderr, "progress source=%s lines=%d\n", source, lines)
			}
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"announcements\":%d,\"symbols\":%d,\"incomplete\":%t,\"tape_hash\":%q,\"chain_hash\":%q,\"manifest_hash\":%q,\"output\":%q}\n",
		result.Tape.Coverage.TotalAnnouncements,
		result.Tape.Coverage.SymbolCount,
		result.Tape.Incomplete,
		result.Tape.TapeHash,
		result.Chain.ChainHash,
		result.Manifest.ManifestHash,
		absOutput,
	)
	return nil
}
