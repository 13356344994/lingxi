package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/researchboundary"
)

func TestRunWritesConservativePublicLedger(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "_study.py")
	if err := os.WriteFile(script, []byte(`H="/records/history"; open(H+"/okx_1h/BTC.jsonl")`), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "ledger.json")
	catalogPath := writeCatalog(t, dir, []historydata.FamilyCatalog{
		{Name: "okx_1h", FilesScanned: 1, LinesScanned: 1},
		{Name: "okx_5m", FilesScanned: 1, LinesScanned: 1},
	})
	if err := run([]string{
		"--roots", filepath.Join(dir, "*.py"),
		"--families", "okx_1h,okx_5m",
		"--catalog", catalogPath,
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var ledger researchboundary.Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	for _, family := range ledger.Families {
		if family.Status != researchboundary.StatusResearchOnly {
			t.Fatalf("public scanner must never declare data sealed: %+v", family)
		}
	}
}

func TestCatalogFamiliesCannotEscapeClassification(t *testing.T) {
	dir := t.TempDir()
	catalogPath := writeCatalog(t, dir, []historydata.FamilyCatalog{{Name: "new_family", FilesScanned: 1, LinesScanned: 1}})
	output := filepath.Join(dir, "ledger.json")
	if err := run([]string{
		"--roots", filepath.Join(dir, "*.py"),
		"--catalog", catalogPath,
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var ledger researchboundary.Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range ledger.Families {
		if family.Family == "new_family" {
			found = true
			if family.Status != researchboundary.StatusResearchOnly {
				t.Fatalf("new catalog family escaped conservative classification: %+v", family)
			}
		}
	}
	if !found {
		t.Fatal("new catalog family missing from contamination ledger")
	}
}

func writeCatalog(t *testing.T, dir string, families []historydata.FamilyCatalog) string {
	t.Helper()
	path := filepath.Join(dir, "catalog.json")
	catalog := historydata.HistoryCatalog{
		SchemaVersion: historydata.CatalogSchemaVersion,
		DatasetID:     "history.test", DataHash: "data", SchemaHash: "schema", CreatedAt: time.Now(),
		Families: families,
	}
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
