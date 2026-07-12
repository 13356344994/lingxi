package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"lingxi/internal/datatruth/history"
)

type familyFlags []string

func (f *familyFlags) String() string { return strings.Join(*f, ",") }

func (f *familyFlags) Set(value string) error {
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		*f = append(*f, token)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("history-inventory", flag.ContinueOnError)
	root := fs.String("root", "records/history", "history root directory")
	maxFiles := fs.Int("max-files", 0, "maximum files to scan (0 means all)")
	maxLines := fs.Int64("max-lines", 0, "maximum lines to scan for this run (0 means until completion)")
	progressEvery := fs.Int64("progress-every", 1000, "progress/checkpoint interval by lines")
	checkpoint := fs.String("checkpoint", "", "checkpoint path")
	catalogOut := fs.String("output", "", "optional catalog JSON output; stdout receives only a compact summary")
	var families familyFlags
	fs.Var(&families, "family", "family to scan (repeat or comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *checkpoint == "" {
		return fmt.Errorf("--checkpoint is required and must be outside immutable history inputs")
	}

	result, err := history.Inventory(ctx, history.Config{
		RootDir:       *root,
		Families:      families,
		MaxFiles:      *maxFiles,
		MaxLines:      *maxLines,
		ProgressEvery: *progressEvery,
		Checkpoint:    *checkpoint,
		OnProgress: func(item history.Progress) {
			if output != nil {
				raw, _ := json.Marshal(item)
				fmt.Fprintln(output, string(raw))
			}
		},
	})
	if err != nil {
		return err
	}
	if !result.Completed {
		raw, _ := json.Marshal(result)
		fmt.Fprintln(output, string(raw))
		return nil
	}
	if *catalogOut != "" {
		raw, err := json.MarshalIndent(result.Catalog, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		if err := os.MkdirAll(filepath.Dir(*catalogOut), 0o755); err != nil {
			return err
		}
		tmp := *catalogOut + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, *catalogOut); err != nil {
			return err
		}
		summary := map[string]any{
			"completed": true, "dataset_id": result.Catalog.DatasetID,
			"data_hash": result.Catalog.DataHash, "schema_hash": result.Catalog.SchemaHash,
			"files_scanned": result.Catalog.FilesScanned, "lines_scanned": result.Catalog.LinesScanned,
		}
		raw, _ = json.Marshal(summary)
		fmt.Fprintln(output, string(raw))
		return nil
	}
	raw, _ := json.Marshal(result.Catalog)
	fmt.Fprintln(output, string(raw))
	return nil
}
