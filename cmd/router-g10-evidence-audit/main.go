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

	"lingxi/internal/routerg10/evidenceaudit"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-evidence-audit", flag.ContinueOnError)
	repo := fs.String("repo-root", "", "repository root containing frozen inputs")
	out := fs.String("output-dir", "", "new explicit research output directory")
	checkpoint := fs.String("checkpoint", "", "checkpoint path (normally output-dir/checkpoint.json)")
	protocol := fs.String("protocol", "", "frozen preregistration JSON")
	salt := fs.String("audit-salt", "", "sensitive audit HMAC key; omit to generate a local 0600 staging key")
	maxPrefix := fs.Int64("max-prefix-bytes-per-source", 0, "engineering TRUNCATED_SMOKE cap per JSONL source; zero freezes full prefix")
	lowCPU := fs.Bool("low-cpu", true, "set GOMAXPROCS=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *out == "" || *checkpoint == "" || *protocol == "" {
		return errors.New("router-g10-evidence-audit: --repo-root, --output-dir, --checkpoint, --protocol are required")
	}
	if *lowCPU {
		runtime.GOMAXPROCS(1)
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	cp, err := filepath.Abs(*checkpoint)
	if err != nil {
		return err
	}
	proto, err := filepath.Abs(*protocol)
	if err != nil {
		return err
	}
	result, err := evidenceaudit.Run(context.Background(), evidenceaudit.Config{RepoRoot: root, OutputDir: output, CheckpointPath: cp, ProtocolPath: proto, AuditSalt: *salt, MaxPrefixBytesPerSource: *maxPrefix}, func(source string, n int64) {
		fmt.Fprintf(os.Stderr, "progress source=%s complete=%d\n", source, n)
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"source_manifest_hash": result.Manifest.ManifestHash,
		"classification":       result.Summary["classification_by_route"],
		"scope_status":         result.Summary["scope_status"],
		"output":               output,
	})
}
