package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"lingxi/internal/routerg10/fingerprint"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-fingerprint", flag.ContinueOnError)
	recordRoot := fs.String("record-root", "", "records root directory")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	maxLines := fs.Int("max-lines", 0, "maximum lines per source (required, positive)")
	settlements := fs.String("settlements", "", "override settlements.jsonl path")
	makerAttempts := fs.String("maker-attempts", "", "override fast_maker_attempts.jsonl path")
	openDecisions := fs.String("open-decisions", "", "override open_decision_events.jsonl path")
	executableShadow := fs.String("executable-shadow", "", "override fast_executable_shadow_events.jsonl path")
	equitySnapshots := fs.String("equity-snapshots", "", "override equity_snapshots.jsonl path")
	instrumentMeta := fs.String("instrument-meta", "", "override instrument_meta.json path")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *recordRoot == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-fingerprint: --record-root, --output-dir, --checkpoint are required")
	}
	if *maxLines <= 0 {
		return errors.New("router-g10-fingerprint: --max-lines must be positive")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(fingerprint.RuntimeGOMAXPROCS)
	}
	absRoot, err := filepath.Abs(*recordRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	sources := fingerprint.DefaultSourcePaths(absRoot)
	if *settlements != "" {
		sources.Settlements = *settlements
	}
	if *makerAttempts != "" {
		sources.MakerAttempts = *makerAttempts
	}
	if *openDecisions != "" {
		sources.OpenDecisions = *openDecisions
	}
	if *executableShadow != "" {
		sources.ExecutableShadow = *executableShadow
	}
	if *equitySnapshots != "" {
		sources.EquitySnapshots = *equitySnapshots
	}
	if *instrumentMeta != "" {
		sources.InstrumentMeta = *instrumentMeta
	}
	cfg := fingerprint.RunConfig{
		SchemaVersion: fingerprint.ConfigSchema,
		Generation:    fingerprint.Generation,
		RecordRoot:    absRoot,
		OutputDir:     absOutput,
		Sources:       sources,
		MaxLines:      *maxLines,
		LowCPU:        *lowCPU,
	}
	runner := fingerprint.Runner{
		Config:         cfg,
		CheckpointPath: *checkpointPath,
		OnProgress: func(source string, lines int) {
			if lines%fingerprint.ProgressEvery == 0 || lines < fingerprint.ProgressEvery {
				fmt.Fprintf(os.Stderr, "progress source=%s lines=%d\n", source, lines)
			}
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "{\"cells\":%d,\"fingerprint_hash\":%q,\"critical_known\":%d,\"critical_unknown\":%d,\"output\":%q,\"coverage\":%q}\n",
		len(result.Fingerprint.Cells), result.Fingerprint.FingerprintHash,
		result.Coverage.CriticalKnownCount, result.Coverage.CriticalUnknownCount,
		result.OutputPath, result.CoveragePath)
	return nil
}
