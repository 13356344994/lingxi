package features

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
)

const shardSchemaVersion = "lingxi.routerg5.features.shard.v1"

// ShardManifest describes one output shard file.
type ShardManifest struct {
	SchemaVersion string    `json:"schema_version"`
	ProfileID     string    `json:"profile_id"`
	ShardIndex    int       `json:"shard_index"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	RowCount      int       `json:"row_count"`
	FileName      string    `json:"file_name"`
	FileHash      string    `json:"file_hash"`
}

// ContextManifest binds all feature context shards for one run.
type ContextManifest struct {
	SchemaVersion     string          `json:"schema_version"`
	Generation        string          `json:"generation"`
	ProtocolHash      string          `json:"protocol_hash"`
	HistoryDataHash   string          `json:"history_data_hash"`
	ProfileID         string          `json:"profile_id"`
	FeatureHash       string          `json:"feature_hash"`
	ConfigHash        string          `json:"config_hash"`
	CatalogHash       string          `json:"catalog_hash"`
	BoundariesHash    string          `json:"boundaries_hash"`
	SelectedFilesHash string          `json:"selected_files_hash"`
	ShardCount        int             `json:"shard_count"`
	TotalRows         int             `json:"total_rows"`
	Shards            []ShardManifest `json:"shards"`
	ExamActions       int             `json:"exam_actions"`
	CreatedAt         time.Time       `json:"created_at"`
	ManifestHash      string          `json:"manifest_hash"`
}

const contextManifestSchema = "lingxi.routerg5.features.context-manifest.v1"

// WriteContextShard writes sorted jsonl rows atomically.
func WriteContextShard(outputDir string, shard ShardManifest, rows []DecisionContext) (ShardManifest, error) {
	sort.Slice(rows, func(i, j int) bool {
		return ContextRowKey(rows[i]) < ContextRowKey(rows[j])
	})
	path := filepath.Join(outputDir, shard.FileName)
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return ShardManifest{}, err
	}
	writer := bufio.NewWriter(file)
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			file.Close()
			os.Remove(tmp)
			return ShardManifest{}, err
		}
		if _, err := writer.Write(raw); err != nil {
			file.Close()
			os.Remove(tmp)
			return ShardManifest{}, err
		}
		if err := writer.WriteByte('\n'); err != nil {
			file.Close()
			os.Remove(tmp)
			return ShardManifest{}, err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(tmp)
		return ShardManifest{}, err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return ShardManifest{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return ShardManifest{}, err
	}
	hash, err := hashFile(path)
	if err != nil {
		return ShardManifest{}, err
	}
	out := shard
	out.SchemaVersion = shardSchemaVersion
	out.RowCount = len(rows)
	out.FileHash = hash
	return out, nil
}

// WriteContextManifest atomically writes feature_context_manifest.json.
func WriteContextManifest(outputDir string, manifest ContextManifest) error {
	m, err := sealManifest(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(outputDir, "feature_context_manifest.json")
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sealManifest(m ContextManifest) (ContextManifest, error) {
	m.SchemaVersion = contextManifestSchema
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		return ContextManifest{}, err
	}
	payload.ManifestHash = hash
	return payload, nil
}

func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ReadContextShard loads rows from one shard jsonl (for tests).
func ReadContextShard(path string) ([]DecisionContext, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var rows []DecisionContext
	for scanner.Scan() {
		var row DecisionContext
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}
