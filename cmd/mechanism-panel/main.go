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

	"lingxi/internal/mechanismpanel"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, progress io.Writer) error {
	fs := flag.NewFlagSet("mechanism-panel", flag.ContinueOnError)
	liq := fs.String("liquidations", "", "OKX liquidation JSONL")
	xvenue := fs.String("xvenue-liquidations", "", "cross-venue liquidation JSONL")
	mids := fs.String("market-mids", "", "market mids JSONL")
	xmeta := fs.String("xmeta-oi", "", "xmeta OI JSONL")
	depth := fs.String("depth", "", "depth JSONL")
	instruments := fs.String("instruments", "", "point-in-time instrument catalog JSON")
	featuresOut := fs.String("features-out", "", "entry feature JSONL output")
	labelsOut := fs.String("labels-out", "", "future label JSONL output")
	date := fs.String("date", "", "UTC date YYYY-MM-DD")
	maxLines := fs.Int64("max-lines", 0, "maximum merged input rows, 0=unlimited")
	progressEvery := fs.Int64("progress-every", 100000, "progress interval in rows")
	checkpoint := fs.String("checkpoint", "", "checkpoint path")
	finalize := fs.Bool("finalize", false, "flush open episodes at natural EOF")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *liq == "" || *featuresOut == "" || *labelsOut == "" {
		return fmt.Errorf("--liquidations, --features-out and --labels-out are required")
	}
	for _, path := range []string{*featuresOut, *labelsOut, *checkpoint} {
		if path == "" {
			continue
		}
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
	}
	cfg := mechanismpanel.Config{
		Sources: mechanismpanel.SourcePaths{
			Liquidation: *liq, XVenueLiquidation: *xvenue, MarketMids: *mids,
			XMetaOI: *xmeta, Depth: *depth, InstrumentCatalog: *instruments,
		},
		Aggregator: mechanismpanel.DefaultAggregatorConfig(),
		Date:       *date, MaxLines: *maxLines, ProgressEveryN: *progressEvery,
		CheckpointPath: *checkpoint, FinalizeAtEOF: *finalize,
	}
	cfg.OnProgress = func(stats mechanismpanel.ProgressStats) {
		if progress == nil {
			return
		}
		raw, _ := json.Marshal(stats)
		fmt.Fprintln(progress, string(raw))
	}
	var featureWriter, labelWriter *mechanismpanel.JSONLWriter
	pipeline, err := mechanismpanel.NewPipeline(
		cfg,
		mechanismpanel.FeatureSinkFunc(func(f mechanismpanel.EntryFeatures) error {
			if featureWriter == nil {
				return fmt.Errorf("feature writer is not initialized")
			}
			return featureWriter.Write(f)
		}),
		mechanismpanel.LabelSinkFunc(func(l mechanismpanel.FutureLabel) error {
			if labelWriter == nil {
				return fmt.Errorf("label writer is not initialized")
			}
			return labelWriter.Write(l)
		}),
	)
	if err != nil {
		return err
	}
	defer pipeline.Close()

	var confirmedFeatures, confirmedLabels int64
	if *checkpoint != "" {
		cp, ok, err := mechanismpanel.LoadCheckpoint(*checkpoint)
		if err != nil {
			return err
		}
		if ok {
			confirmedFeatures = cp.FeaturesEmitted
			confirmedLabels = cp.LabelsEmitted
		}
	}
	featureWriter, err = mechanismpanel.NewJSONLWriterResumeAt(*featuresOut, confirmedFeatures)
	if err != nil {
		return err
	}
	defer func() {
		if featureWriter != nil {
			_ = featureWriter.Close()
		}
	}()
	labelWriter, err = mechanismpanel.NewJSONLWriterResumeAt(*labelsOut, confirmedLabels)
	if err != nil {
		return err
	}
	defer func() {
		if labelWriter != nil {
			_ = labelWriter.Close()
		}
	}()

	result, err := pipeline.Run(ctx)
	if err != nil {
		return err
	}
	if err := featureWriter.Close(); err != nil {
		return err
	}
	featureWriter = nil
	if err := labelWriter.Close(); err != nil {
		return err
	}
	labelWriter = nil
	raw, _ := json.Marshal(result)
	fmt.Fprintln(progress, string(raw))
	return nil
}
