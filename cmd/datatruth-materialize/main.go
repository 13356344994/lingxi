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
	"syscall"
	"time"

	contextdata "lingxi/internal/datatruth/context"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, progress io.Writer) error {
	fs := flag.NewFlagSet("datatruth-materialize", flag.ContinueOnError)
	mode := fs.String("mode", "materialize", "materialize or validate")
	kindRaw := fs.String("kind", "", "instrument|universe|mids|oi|xvenue|depth")
	input := fs.String("input", "", "legacy snapshot or JSONL input")
	outputDir := fs.String("output-dir", "", "root output directory")
	dayDir := fs.String("day-dir", "", "existing UTC day partition directory for validate")
	date := fs.String("date", "", "UTC date YYYY-MM-DD")
	baseCatalog := fs.String("base-catalog", "", "optional prior append-only PIT catalog")
	universeID := fs.String("universe-id", "okx-directional-shortlist", "universe identity")
	checkpoint := fs.String("checkpoint", "", "checkpoint path")
	maxLines := fs.Int64("max-lines", 0, "maximum input lines for this run; 0=until EOF")
	progressEvery := fs.Int64("progress-every", 100000, "checkpoint/progress interval")
	lateAfter := fs.Duration("late-after", 30*time.Second, "late arrival threshold")
	maxDedupe := fs.Int("max-dedupe", 4096, "bounded duplicate window per source")
	verdictOut := fs.String("verdict-out", "", "validate verdict JSON output")
	maxOIStaleness := fs.Duration("max-oi-staleness", 15*time.Minute, "maximum OI gap from UTC day end")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch *mode {
	case "materialize":
		kind, err := contextdata.ParseKind(*kindRaw)
		if err != nil {
			return err
		}
		result, err := contextdata.Materialize(ctx, contextdata.Config{
			Kind: kind, InputPath: *input, OutputDir: *outputDir, Date: *date,
			BaseCatalogPath: *baseCatalog, UniverseID: *universeID,
			CheckpointPath: *checkpoint, MaxLines: *maxLines,
			ProgressEvery: *progressEvery, LateAfter: *lateAfter,
			MaxDedupeEntries: *maxDedupe,
			OnProgress: func(item contextdata.Progress) {
				if progress != nil {
					raw, _ := json.Marshal(item)
					fmt.Fprintln(progress, string(raw))
				}
			},
		})
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		fmt.Fprintln(progress, string(raw))
		return nil
	case "validate":
		if *dayDir == "" {
			if *outputDir == "" || *date == "" {
				return fmt.Errorf("--day-dir or both --output-dir and --date are required")
			}
			*dayDir = filepath.Join(*outputDir, *date)
		}
		verdict, err := contextdata.ValidateDay(contextdata.ValidateConfig{
			DayDir: *dayDir, Date: *date, MaxOIStaleness: *maxOIStaleness,
			OnProgress: func(item contextdata.Progress) {
				if progress != nil {
					raw, _ := json.Marshal(item)
					fmt.Fprintln(progress, string(raw))
				}
			},
		})
		if err != nil {
			return err
		}
		if *verdictOut != "" {
			if err := contextdata.WriteDailyVerdict(*verdictOut, verdict); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(verdict)
		fmt.Fprintln(progress, string(raw))
		if verdict.Verdict != contextdata.VerdictPass {
			return fmt.Errorf("contextdata: daily verdict FAIL: %v", verdict.Reasons)
		}
		return nil
	default:
		return fmt.Errorf("unsupported --mode %q", *mode)
	}
}
