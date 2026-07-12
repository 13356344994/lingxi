package profitmap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/protocol"
)

func buildValidCatalog(t *testing.T, root string, families []historydata.FamilyCatalog) historydata.HistoryCatalog {
	t.Helper()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	var files []historydata.FileStats
	for _, family := range families {
		for _, file := range family.Files {
			f := file
			f.Family = family.Name
			files = append(files, f)
		}
	}
	catalog, err := rebuildCatalog(root, files, now)
	if err != nil {
		t.Fatal(err)
	}
	catalog.DatasetID = "history.test.smoke"
	catalog.DataHash = protocol.HistoryDataHash
	return catalog
}

func rebuildCatalog(rootDir string, files []historydata.FileStats, now time.Time) (historydata.HistoryCatalog, error) {
	familyBuckets := map[string][]historydata.FileStats{}
	for _, file := range files {
		familyBuckets[file.Family] = append(familyBuckets[file.Family], file)
	}
	familyNames := make([]string, 0, len(familyBuckets))
	for family := range familyBuckets {
		familyNames = append(familyNames, family)
	}
	sort.Strings(familyNames)
	catalog := historydata.HistoryCatalog{
		SchemaVersion: historydata.CatalogSchemaVersion,
		RootDir:       rootDir,
		CreatedAt:     now.UTC(),
	}
	dataParts := make([]string, 0, len(files))
	totalLines := int64(0)
	for _, family := range familyNames {
		bucket := familyBuckets[family]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Path < bucket[j].Path })
		entry := historydata.FamilyCatalog{Name: family, Files: bucket}
		for _, file := range bucket {
			entry.FilesScanned++
			entry.LinesScanned += file.Lines
			totalLines += file.Lines
			dataParts = append(dataParts, fmt.Sprintf("%s\x00%s\x00%s\x00%d", file.Family, file.Path, file.FileHash, file.Lines))
		}
		catalog.Families = append(catalog.Families, entry)
	}
	catalog.FilesScanned = len(files)
	catalog.LinesScanned = totalLines
	catalog.DataHash = datatruth.HashStrings(dataParts...)
	catalog.DatasetID = "history.test." + catalog.DataHash[:12]
	return catalog, nil
}

func fixtureFileStats(t *testing.T, path, family, rel string) historydata.FileStats {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return historydata.FileStats{
		Family: family, Path: rel, Size: info.Size(),
		FileHash: hex.EncodeToString(sum[:]),
		Lines:    int64(strings.Count(string(raw), "\n")),
	}
}
