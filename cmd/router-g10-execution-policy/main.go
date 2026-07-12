package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"lingxi/internal/routerg10/executionpolicy"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-execution-policy", flag.ContinueOnError)
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum trigger lines (required, positive)")
	triggersPath := fs.String("triggers", "", "override triggers.jsonl path")
	fixturesOnly := fs.Bool("fixtures-only", false, "write sealed execution-policy fixtures only")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fixturesOnly {
		if *outputDir == "" {
			return errors.New("router-g10-execution-policy: --output-dir required for --fixtures-only")
		}
		absOutput, err := filepath.Abs(*outputDir)
		if err != nil {
			return err
		}
		bundle, err := executionpolicy.WriteFixtures(absOutput)
		if err != nil {
			return err
		}
		fmt.Printf("wrote execution fixtures bundle_hash=%s policies=%d\n", bundle.BundleHash, len(bundle.Policies))
		return nil
	}
	if *outputDir == "" || *maxLines <= 0 {
		return errors.New("router-g10-execution-policy: --output-dir and --max-lines are required")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(executionpolicy.RuntimeGOMAXPROCS)
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	if *triggersPath == "" {
		return errors.New("router-g10-execution-policy: --triggers required")
	}
	cfg := executionpolicy.RunConfig{
		SchemaVersion: executionpolicy.ConfigSchema,
		Generation:    executionpolicy.Generation,
		OutputDir:     absOutput,
		Sources:       executionpolicy.SourcePaths{Triggers: *triggersPath},
		MaxLines:      *maxLines,
		LowCPU:        *lowCPU,
	}
	runner := executionpolicy.Runner{Config: cfg, CheckpointPath: *checkpointPath}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"policies\":%d,\"denominator\":%d,\"maker_gate_forbid\":%d,\"manifest_hash\":%q,\"output\":%q}\n",
		len(result.Policies), result.Manifest.AllTriggersDenominator,
		result.Manifest.MakerGateForbidCount, result.Manifest.ManifestHash, absOutput,
	)
	return nil
}
