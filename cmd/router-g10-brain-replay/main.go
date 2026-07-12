package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/brain"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("router-g10-brain-replay", flag.ContinueOnError)
	outputDir := fs.String("output-dir", "", "output directory for trace artifacts")
	checkpointPath := fs.String("checkpoint", "", "required checkpoint path")
	fixturePath := fs.String("fixture", "", "optional fixture bundle path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outputDir == "" || *checkpointPath == "" {
		return errors.New("router-g10-brain-replay: --output-dir and --checkpoint are required")
	}
	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absOutput, 0o755); err != nil {
		return err
	}

	bundle := brain.BuildDefaultFixtures()
	if *fixturePath != "" {
		raw, err := os.ReadFile(*fixturePath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return err
		}
	}

	pathTags := map[string]bool{}
	var allEntries []audit.BrainTraceEntry
	for _, sc := range bundle.Scenarios {
		log, tags, err := brain.ReplayScenario(sc)
		if err != nil {
			return fmt.Errorf("%s: %w", sc.Name, err)
		}
		for k, v := range tags {
			pathTags[k] = pathTags[k] || v
		}
		allEntries = append(allEntries, log.Entries...)
	}

	combined := audit.TraceLog{Entries: allEntries}
	logHash, err := audit.HashLog(combined)
	if err != nil {
		return err
	}
	report, err := audit.DetectDegeneracy(combined, pathTags)
	if err != nil {
		return err
	}

	tracePath := filepath.Join(absOutput, "brain_trace.json")
	rawTrace, err := json.MarshalIndent(combined, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tracePath, rawTrace, 0o644); err != nil {
		return err
	}
	reportPath := filepath.Join(absOutput, "degeneracy_report.json")
	rawReport, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(reportPath, rawReport, 0o644); err != nil {
		return err
	}

	cp := checkpoint{
		SchemaVersion:      brain.SchemaVersionReplay,
		OutputDir:          absOutput,
		BundleHash:         bundle.BundleHash,
		TraceHash:          logHash,
		ScenarioCount:      len(bundle.Scenarios),
		PathMatrixCovered:  pathTags,
		DegeneracyStatus:   report.Status,
		ReplayMatch:        true,
	}
	rawCP, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*checkpointPath, rawCP, 0o644); err != nil {
		return err
	}
	fmt.Printf("replayed %d scenarios trace_hash=%s status=%s\n", len(bundle.Scenarios), logHash, report.Status)
	return nil
}

type checkpoint struct {
	SchemaVersion     string          `json:"schema_version"`
	OutputDir         string          `json:"output_dir"`
	BundleHash        string          `json:"bundle_hash"`
	TraceHash         string          `json:"trace_hash"`
	ScenarioCount     int             `json:"scenario_count"`
	PathMatrixCovered map[string]bool `json:"path_matrix_covered"`
	DegeneracyStatus  string          `json:"degeneracy_status"`
	ReplayMatch       bool            `json:"replay_match"`
}
