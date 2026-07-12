package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/researchboundary"
)

var defaultFamilies = []string{
	"okx_1h",
	"okx_5m",
	"okx_funding",
	"bybit_funding",
	"rubik_taker_1h",
	"rubik_taker_5m",
	"rubik_lsratio_1h",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("history-contamination", flag.ContinueOnError)
	roots := fs.String("roots", "records/_*.py", "comma-separated glob patterns for existing research artifacts")
	familiesRaw := fs.String("families", strings.Join(defaultFamilies, ","), "comma-separated history families")
	output := fs.String("output", "", "public contamination ledger JSON")
	commitmentsPath := fs.String("commitments", "", "optional public commitments-only JSON (no paths or ranges)")
	catalogPath := fs.String("catalog", "", "HistoryCatalog; every catalog family is classified fail-closed")
	maxFiles := fs.Int("max-files", 0, "maximum scripts to inspect; 0=all matches")
	maxBytes := fs.Int64("max-bytes-per-file", 4*1024*1024, "bounded bytes read from each artifact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return fmt.Errorf("--output is required")
	}
	if *catalogPath == "" {
		return fmt.Errorf("--catalog is required so every discovered history family is classified")
	}
	families := splitCSV(*familiesRaw)
	raw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	var catalog historydata.HistoryCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	if catalog.DataHash == "" || catalog.SchemaHash == "" {
		return fmt.Errorf("history catalog is incomplete")
	}
	for _, family := range catalog.Families {
		families = append(families, family.Name)
	}
	families = uniqueStrings(families)
	evidence, err := researchboundary.ScanUsage(researchboundary.ScanConfig{
		Roots: splitCSV(*roots), Families: families, MaxFiles: *maxFiles, MaxBytesPerFile: *maxBytes,
	})
	if err != nil {
		return err
	}
	var commitments []researchboundary.SealedCommitment
	if *commitmentsPath != "" {
		raw, err := os.ReadFile(*commitmentsPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &commitments); err != nil {
			return err
		}
	}
	ledger, err := researchboundary.BuildLedger(families, evidence, commitments, time.Now().UTC())
	if err != nil {
		return err
	}
	ledger.CatalogDataHash = catalog.DataHash
	ledger.CatalogSchemaHash = catalog.SchemaHash
	raw, err = json.MarshalIndent(ledger, "", "  ")
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

func splitCSV(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
