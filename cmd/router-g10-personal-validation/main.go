package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	historydata "lingxi/internal/datatruth/history"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalvalidation"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-personal-validation", flag.ContinueOnError)
	catalogPath := fs.String("catalog", "", "path to history_catalog.json")
	historyRoot := fs.String("history-root", "", "history root directory")
	playbookOutputDir := fs.String("playbook-output-dir", "", "personal playbooks output directory")
	reverseTapesDir := fs.String("reverse-tapes-dir", "", "reverse tapes output directory")
	reverseTapesManifestHash := fs.String("reverse-tapes-manifest-hash", "", "required reverse_tapes_manifest hash")
	announcementDir := fs.String("announcement-dir", "", "announcements output directory")
	announcementManifestHash := fs.String("announcement-manifest-hash", "", "required announcement manifest hash")
	entryProofDir := fs.String("entry-proof-dir", "", "entry proof output directory")
	executionPolicyDir := fs.String("execution-policy-dir", "", "execution policy output directory")
	exitProofPath := fs.String("exit-proof-path", "", "exit proof jsonl path")
	exclusionMapDir := fs.String("exclusion-map-dir", "", "exclusion map output directory")
	outputDir := fs.String("output-dir", "", "validation output directory")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	playbookManifestHash := fs.String("playbook-manifest-hash", "", "required personal_playbook_manifest hash")
	constitutionHash := fs.String("constitution-hash", "", "required constitution hash")
	maxFiles := fs.Int("max-files", 0, "maximum catalog files (required, positive)")
	maxLines := fs.Int("max-lines", 0, "maximum lines per symbol file (required, positive)")
	allowTruncatedSmoke := fs.Bool("allow-truncated-smoke", false, "allow truncated smoke map inputs")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1 for bounded server load")
	var mapDirs multiFlag
	fs.Var(&mapDirs, "map-dir", "event-map output directory (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogPath == "" || *historyRoot == "" || *playbookOutputDir == "" || *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-personal-validation: --catalog, --history-root, --playbook-output-dir, --output-dir, --checkpoint are required")
	}
	if *playbookManifestHash == "" || *constitutionHash == "" {
		return errors.New("router-g10-personal-validation: --playbook-manifest-hash and --constitution-hash are required")
	}
	if *reverseTapesDir == "" || *reverseTapesManifestHash == "" {
		return errors.New("router-g10-personal-validation: --reverse-tapes-dir and --reverse-tapes-manifest-hash are required")
	}
	if *announcementDir == "" || *announcementManifestHash == "" {
		return errors.New("router-g10-personal-validation: --announcement-dir and --announcement-manifest-hash are required")
	}
	if *maxFiles <= 0 || *maxLines <= 0 {
		return errors.New("router-g10-personal-validation: --max-files and --max-lines must be positive")
	}
	if len(mapDirs) == 0 {
		return errors.New("router-g10-personal-validation: at least one --map-dir is required")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(personalvalidation.RuntimeGOMAXPROCS)
	}
	constHash, err := g10constitution.Hash()
	if err != nil {
		return err
	}
	if *constitutionHash != constHash {
		return errors.New("router-g10-personal-validation: constitution hash mismatch")
	}
	absHistory, err := filepath.Abs(*historyRoot)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	absPlaybooks, err := filepath.Abs(*playbookOutputDir)
	if err != nil {
		return err
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
	absMapDirs := make([]string, len(mapDirs))
	for i, dir := range mapDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		absMapDirs[i] = abs
	}
	cfg := personalvalidation.RunConfig{
		SchemaVersion:            personalvalidation.SchemaVersionConfig,
		HistoryRoot:              absHistory,
		CatalogPath:              *catalogPath,
		PlaybookOutputDir:        absPlaybooks,
		ReverseTapesDir:          *reverseTapesDir,
		ReverseTapesManifestHash: *reverseTapesManifestHash,
		AnnouncementDir:          *announcementDir,
		AnnouncementManifestHash: *announcementManifestHash,
		EntryProofDir:            *entryProofDir,
		ExecutionPolicyDir:       *executionPolicyDir,
		ExitProofPath:            *exitProofPath,
		ExclusionMapDir:          *exclusionMapDir,
		OutputDir:                absOutput,
		MapDirs:                  absMapDirs,
		MaxFiles:                 *maxFiles,
		MaxLines:                 *maxLines,
		ProgressEvery:            personalvalidation.ProgressEvery,
		AllowTruncatedSmoke:      *allowTruncatedSmoke,
		Generation:               g10protocolv2.Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		HistoryDataHash:          g10protocolv2.HistoryDataHash,
		PlaybookManifestHash:     *playbookManifestHash,
		ConstitutionHash:         *constitutionHash,
	}
	runner := personalvalidation.Runner{
		Config:         cfg,
		Catalog:        catalog,
		CheckpointPath: *checkpointPath,
		OnProgress: func(lines int64, playbookHash, symbol string) {
			hash := playbookHash
			if len(hash) > 12 {
				hash = hash[:12]
			}
			fmt.Fprintf(os.Stderr, "progress playbook=%s symbol=%s lines=%d\n", hash, symbol, lines)
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout,
		"{\"playbooks\":%d,\"pass\":%d,\"fail\":%d,\"inconclusive\":%d,\"exam_actions\":%d,\"trade_allowed\":false}\n",
		result.Report.PlaybookCount, result.Report.PassCount, result.Report.FailCount,
		result.Report.InconclusiveCount, g10protocolv2.ExamActions,
	)
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
