package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datadrivenv3/fundingarchive"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "data-driven-v3-funding-backfill:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("data-driven-v3-funding-backfill", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	startText := fs.String("start", "2023-12-31", "first UTC date")
	endText := fs.String("end", "2026-07-09", "last UTC date")
	rateLimit := fs.Duration("rate-limit", 100*time.Millisecond, "delay between daily requests")
	smoke := fs.Bool("smoke", false, "verify only the first three dates while freezing the full plan")
	if err := fs.Parse(args); err != nil {
		return err
	}
	start, err := time.Parse("2006-01-02", *startText)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	end, err := time.Parse("2006-01-02", *endText)
	if err != nil {
		return fmt.Errorf("end: %w", err)
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	manifest, err := fundingarchive.Run(context.Background(), fundingarchive.Config{
		Root: abs, Start: start, End: end, RateLimit: *rateLimit, Progress: os.Stderr,
		MaxDays: func() int {
			if *smoke {
				return 3
			}
			return 0
		}(),
	})
	if err != nil {
		return err
	}
	fmt.Printf("complete_days=%d gap_days=%d rows=%d bytes=%d manifest_hash=%s\n", manifest.CompleteDays, manifest.GapDays, manifest.Rows, manifest.Bytes, manifest.ManifestHash)
	return nil
}
