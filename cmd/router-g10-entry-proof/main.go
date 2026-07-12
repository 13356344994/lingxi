package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"lingxi/internal/routerg10/entryproof"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-entry-proof", flag.ContinueOnError)
	recordRoot := fs.String("record-root", "", "record root for trigger sources")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum trigger lines (required, positive)")
	triggersPath := fs.String("triggers", "", "override triggers.jsonl path")
	fixturesOnly := fs.Bool("fixtures-only", false, "write sealed entry+exit fixtures bundle only")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fixturesOnly {
		if *outputDir == "" {
			return errors.New("router-g10-entry-proof: --output-dir required for --fixtures-only")
		}
		absOutput, err := filepath.Abs(*outputDir)
		if err != nil {
			return err
		}
		bundle, err := entryproof.WriteFixtures(absOutput)
		if err != nil {
			return err
		}
		fmt.Printf("wrote entry fixtures bundle_hash=%s proofs=%d\n", bundle.BundleHash, len(bundle.Proofs))
		return nil
	}
	if *outputDir == "" || *checkpointPath == "" || *maxLines <= 0 {
		return errors.New("router-g10-entry-proof: --output-dir, --checkpoint, --max-lines are required")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(entryproof.RuntimeGOMAXPROCS)
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	sources := entryproof.SourcePaths{}
	if *triggersPath != "" {
		sources.Triggers = *triggersPath
	} else if *recordRoot != "" {
		sources.Triggers = filepath.Join(*recordRoot, "entry_triggers.jsonl")
	} else {
		return errors.New("router-g10-entry-proof: --triggers or --record-root required")
	}
	cfg := entryproof.RunConfig{
		SchemaVersion: entryproof.ConfigSchema,
		Generation:    entryproof.Generation,
		RecordRoot:    *recordRoot,
		OutputDir:     absOutput,
		Sources:       sources,
		MaxLines:      *maxLines,
		LowCPU:        *lowCPU,
	}
	runner := entryproof.Runner{Config: cfg, CheckpointPath: *checkpointPath}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"proofs\":%d,\"certified\":%d,\"discovery\":%d,\"manifest_hash\":%q,\"output\":%q}\n",
		len(result.Proofs), result.Manifest.CertifiedCount, result.Manifest.DiscoveryCount,
		result.Manifest.ManifestHash, absOutput,
	)
	return nil
}
