package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/researchboundary"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("history-requirements", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "research/firstbattle/history_data_requirements.json", "public requirements manifest")
	availableRaw := fs.String("available", "", "comma-separated available historical data families")
	catalogPath := fs.String("catalog", "", "optional HistoryCatalog used to derive available non-empty families")
	output := fs.String("output", "", "public testability verdict JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return fmt.Errorf("--output is required")
	}
	manifest, err := researchboundary.LoadRequirements(*manifestPath)
	if err != nil {
		return err
	}
	available := map[string]bool{}
	for _, family := range strings.Split(*availableRaw, ",") {
		if family = strings.TrimSpace(family); family != "" {
			available[family] = true
		}
	}
	if *catalogPath != "" {
		raw, err := os.ReadFile(*catalogPath)
		if err != nil {
			return err
		}
		var catalog historydata.HistoryCatalog
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return err
		}
		if catalog.DataHash == "" || catalog.SchemaHash == "" {
			return fmt.Errorf("history catalog is not complete")
		}
		for _, family := range catalog.Families {
			if family.FilesScanned > 0 && family.LinesScanned > 0 {
				available[family.Name] = true
			}
		}
	}
	verdicts := researchboundary.EvaluateRequirements(manifest, available)
	raw, err := json.MarshalIndent(struct {
		SchemaVersion string                                `json:"schema_version"`
		ManifestHash  string                                `json:"manifest_hash"`
		Verdicts      []researchboundary.TestabilityVerdict `json:"verdicts"`
	}{
		SchemaVersion: "lingxi.history-testability.v1",
		ManifestHash:  *manifest.ManifestHash,
		Verdicts:      verdicts,
	}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	tmp := *output + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, *output)
}
