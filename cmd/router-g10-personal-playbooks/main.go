package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalplaybook"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-personal-playbooks", flag.ContinueOnError)
	reverseTapesDir := fs.String("reverse-tapes-dir", "", "G10 reverse-tapes output directory")
	reverseTapesManifestHash := fs.String("reverse-tapes-manifest-hash", "", "expected reverse_tapes_manifest.json hash")
	outputDir := fs.String("output-dir", "", "explicit output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	playbookLimit := fs.Int("playbook-limit", 0, "max personal wild playbooks (required, 1..64)")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *reverseTapesDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-personal-playbooks: --reverse-tapes-dir, --output-dir, --checkpoint are required")
	}
	if *playbookLimit <= 0 || *playbookLimit > personalplaybook.MaxPlaybookCandidates {
		return fmt.Errorf("router-g10-personal-playbooks: --playbook-limit must be in 1..%d", personalplaybook.MaxPlaybookCandidates)
	}
	if *lowCPU {
		runtime.GOMAXPROCS(personalplaybook.RuntimeGOMAXPROCS)
	}
	absReverse, err := filepath.Abs(*reverseTapesDir)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absOutput, 0o755); err != nil {
		return err
	}
	cfg := personalplaybook.RunConfig{
		SchemaVersion:            personalplaybook.SchemaVersionConfig,
		ReverseTapesDir:            absReverse,
		ReverseTapesManifestHash: *reverseTapesManifestHash,
		OutputDir:                  absOutput,
		PlaybookLimit:              *playbookLimit,
		ProgressEvery:              personalplaybook.ProgressEvery,
		Generation:                 g10protocolv2.Generation,
		ProtocolHash:               g10protocolv2.RouterProtocolHash,
		HistoryDataHash:            g10protocolv2.HistoryDataHash,
	}
	runner := personalplaybook.Runner{
		Config:         cfg,
		CheckpointPath: *checkpointPath,
		OnProgress: func(attempts, playbooks int) {
			fmt.Fprintf(os.Stderr, "progress attempts=%d playbooks=%d\n", attempts, playbooks)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	certified := 0
	for _, pb := range result.Playbooks {
		if pb.Status == personalplaybook.StatusPersonalWildCandidate {
			certified++
		}
	}
	fmt.Fprintf(os.Stdout,
		"{\"certified_playbooks\":%d,\"discovery_playbooks\":%d,\"ledger_attempts\":%d,\"manifest_hash\":%q,\"ledger_hash\":%q}\n",
		certified, len(result.Discovery), len(result.Ledger.Attempts),
		result.Manifest.ManifestHash, result.Ledger.LedgerHash,
	)
	return nil
}
