package history

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

func ValidateCatalog(catalog HistoryCatalog) error {
	var files []FileStats
	for _, family := range catalog.Families {
		files = append(files, family.Files...)
	}
	rebuilt, err := buildCatalog(catalog.RootDir, files, catalog.CreatedAt)
	if err != nil {
		return err
	}
	if catalog.SchemaVersion != CatalogSchemaVersion ||
		catalog.DatasetID != rebuilt.DatasetID ||
		catalog.DataHash != rebuilt.DataHash ||
		catalog.SchemaHash != rebuilt.SchemaHash ||
		catalog.FilesScanned != rebuilt.FilesScanned ||
		catalog.LinesScanned != rebuilt.LinesScanned {
		return errors.New("history: catalog hashes/counts do not match catalog families")
	}
	return nil
}

const CatalogSchemaVersion = "lingxi.history.catalog.v1"

type HistoryCatalog struct {
	SchemaVersion string          `json:"schema_version"`
	DatasetID     string          `json:"dataset_id"`
	RootDir       string          `json:"root_dir"`
	Families      []FamilyCatalog `json:"families"`
	FilesScanned  int             `json:"files_scanned"`
	LinesScanned  int64           `json:"lines_scanned"`
	DataHash      string          `json:"data_hash"`
	SchemaHash    string          `json:"schema_hash"`
	CreatedAt     time.Time       `json:"created_at"`
}

type FamilyCatalog struct {
	Name         string      `json:"name"`
	Files        []FileStats `json:"files"`
	FilesScanned int         `json:"files_scanned"`
	LinesScanned int64       `json:"lines_scanned"`
	Fields       []string    `json:"fields,omitempty"`
	MinTime      time.Time   `json:"min_time,omitempty"`
	MaxTime      time.Time   `json:"max_time,omitempty"`
}

type FileStats struct {
	Family      string    `json:"family"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	Lines       int64     `json:"lines"`
	Fields      []string  `json:"fields,omitempty"`
	MinTime     time.Time `json:"min_time,omitempty"`
	MaxTime     time.Time `json:"max_time,omitempty"`
	FileHash    string    `json:"file_hash"`
	BytesHashed int64     `json:"bytes_hashed"`
	Empty       bool      `json:"empty"`
}

func buildCatalog(rootDir string, files []FileStats, now time.Time) (HistoryCatalog, error) {
	familyBuckets := map[string][]FileStats{}
	for _, file := range files {
		familyBuckets[file.Family] = append(familyBuckets[file.Family], file)
	}
	familyNames := make([]string, 0, len(familyBuckets))
	for family := range familyBuckets {
		familyNames = append(familyNames, family)
	}
	sort.Strings(familyNames)

	catalog := HistoryCatalog{
		SchemaVersion: CatalogSchemaVersion,
		RootDir:       rootDir,
		CreatedAt:     now.UTC(),
	}
	dataParts := make([]string, 0, len(files))
	totalLines := int64(0)
	for _, family := range familyNames {
		bucket := familyBuckets[family]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Path < bucket[j].Path })
		entry := FamilyCatalog{Name: family, Files: bucket}
		fieldSet := map[string]struct{}{}
		for _, file := range bucket {
			entry.FilesScanned++
			entry.LinesScanned += file.Lines
			totalLines += file.Lines
			mergeMinMax(&entry.MinTime, &entry.MaxTime, file.MinTime)
			mergeMinMax(&entry.MinTime, &entry.MaxTime, file.MaxTime)
			for _, field := range file.Fields {
				fieldSet[field] = struct{}{}
			}
			dataParts = append(dataParts, fmt.Sprintf("%s\x00%s\x00%s\x00%d", file.Family, file.Path, file.FileHash, file.Lines))
		}
		entry.Fields = sortedKeys(fieldSet)
		catalog.Families = append(catalog.Families, entry)
	}
	catalog.FilesScanned = len(files)
	catalog.LinesScanned = totalLines
	catalog.DataHash = datatruth.HashStrings(dataParts...)
	schemaHash, err := hashSchema(catalog.Families)
	if err != nil {
		return HistoryCatalog{}, err
	}
	catalog.SchemaHash = schemaHash
	catalog.DatasetID = datasetID(catalog)
	return catalog, nil
}

func hashSchema(families []FamilyCatalog) (string, error) {
	type schemaFile struct {
		Family string   `json:"family"`
		Path   string   `json:"path"`
		Fields []string `json:"fields"`
	}
	payload := struct {
		SchemaVersion string       `json:"schema_version"`
		Files         []schemaFile `json:"files"`
	}{
		SchemaVersion: CatalogSchemaVersion,
		Files:         make([]schemaFile, 0),
	}
	for _, family := range families {
		for _, file := range family.Files {
			payload.Files = append(payload.Files, schemaFile{
				Family: file.Family,
				Path:   file.Path,
				Fields: append([]string(nil), file.Fields...),
			})
		}
	}
	sort.Slice(payload.Files, func(i, j int) bool {
		if payload.Files[i].Family == payload.Files[j].Family {
			return payload.Files[i].Path < payload.Files[j].Path
		}
		return payload.Files[i].Family < payload.Files[j].Family
	})
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func datasetID(catalog HistoryCatalog) string {
	families := make([]string, 0, len(catalog.Families))
	for _, family := range catalog.Families {
		families = append(families, family.Name)
	}
	sort.Strings(families)
	joined := strings.Join(families, "+")
	if joined == "" {
		joined = "unknown"
	}
	dataSuffix := catalog.DataHash
	if len(dataSuffix) > 12 {
		dataSuffix = dataSuffix[:12]
	}
	return fmt.Sprintf("history.%s.%s", joined, dataSuffix)
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mergeMinMax(minimum, maximum *time.Time, value time.Time) {
	if value.IsZero() {
		return
	}
	if minimum.IsZero() || value.Before(*minimum) {
		*minimum = value.UTC()
	}
	if maximum.IsZero() || value.After(*maximum) {
		*maximum = value.UTC()
	}
}
