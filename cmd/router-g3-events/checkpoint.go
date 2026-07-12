package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g3-events.checkpoint.v2"

type checkpointFile struct {
	SchemaVersion     string                         `json:"schema_version"`
	Generation        string                         `json:"generation"`
	ProtocolHash      string                         `json:"protocol_hash"`
	CatalogDataHash   string                         `json:"catalog_data_hash"`
	CatalogSchemaHash string                         `json:"catalog_schema_hash"`
	DatasetID         string                         `json:"dataset_id"`
	SelectedFileHash  string                         `json:"selected_file_hash"`
	ConfigHash        string                         `json:"config_hash"`
	CutpointsHash     string                         `json:"cutpoints_hash"`
	BoundariesHash    string                         `json:"boundaries_hash"`
	OutputDir         string                         `json:"output_dir"`
	Partitions        map[string]partitionCheckpoint `json:"partitions"`
	Done              bool                           `json:"done"`
	ManifestHash      string                         `json:"manifest_hash,omitempty"`
	SavedAt           time.Time                      `json:"saved_at"`
	CheckpointHash    string                         `json:"checkpoint_hash"`
}

type partitionCheckpoint struct {
	Start      time.Time      `json:"start"`
	End        time.Time      `json:"end"`
	InputHash  string         `json:"input_hash"`
	EventHash  string         `json:"event_hash"`
	EventCount int            `json:"event_count"`
	EventsPath string         `json:"events_path"`
	Rejections map[string]int `json:"rejections"`
}

type checkpointBinding struct {
	CatalogDataHash   string
	CatalogSchemaHash string
	DatasetID         string
	SelectedFileHash  string
	ConfigHash        string
	CutpointsHash     string
	BoundariesHash    string
	OutputDir         string
}

func newCheckpoint(binding checkpointBinding) checkpointFile {
	return checkpointFile{
		SchemaVersion: checkpointSchemaVersion,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		CatalogDataHash: binding.CatalogDataHash, CatalogSchemaHash: binding.CatalogSchemaHash,
		DatasetID: binding.DatasetID, SelectedFileHash: binding.SelectedFileHash,
		ConfigHash: binding.ConfigHash, CutpointsHash: binding.CutpointsHash,
		BoundariesHash: binding.BoundariesHash, OutputDir: binding.OutputDir,
		Partitions: map[string]partitionCheckpoint{},
	}
}

func (cp checkpointFile) validateBinding(binding checkpointBinding) error {
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash ||
		cp.CatalogDataHash != binding.CatalogDataHash ||
		cp.CatalogSchemaHash != binding.CatalogSchemaHash ||
		cp.DatasetID != binding.DatasetID ||
		cp.SelectedFileHash != binding.SelectedFileHash ||
		cp.ConfigHash != binding.ConfigHash ||
		cp.CutpointsHash != binding.CutpointsHash ||
		cp.BoundariesHash != binding.BoundariesHash ||
		filepath.Clean(cp.OutputDir) != filepath.Clean(binding.OutputDir) {
		return errors.New("router-g3-events: checkpoint input or config drift")
	}
	return nil
}

func partitionInputHash(name string, start, end time.Time, selectedFileHash, configHash string) (string, error) {
	payload := struct {
		Partition        string    `json:"partition"`
		Start            time.Time `json:"start"`
		End              time.Time `json:"end"`
		SelectedFileHash string    `json:"selected_file_hash"`
		ConfigHash       string    `json:"config_hash"`
	}{name, start, end, selectedFileHash, configHash}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func validatePartitionRecord(root, name string, record partitionCheckpoint, selectedFileHash, configHash string) ([]g3reverse.Event, error) {
	wantInput, err := partitionInputHash(name, record.Start, record.End, selectedFileHash, configHash)
	if err != nil {
		return nil, err
	}
	if record.InputHash != wantInput || record.EventHash == "" || record.EventsPath == "" {
		return nil, errors.New("router-g3-events: checkpoint partition binding mismatch")
	}
	events, err := readEventJSONL(filepath.Join(root, record.EventsPath))
	if err != nil {
		return nil, err
	}
	eventHash, err := g3reverse.HashEvents(events)
	if err != nil {
		return nil, err
	}
	if len(events) != record.EventCount || eventHash != record.EventHash {
		return nil, fmt.Errorf("router-g3-events: checkpoint partition %s event file drift", name)
	}
	return events, nil
}

func readEventJSONL(path string) ([]g3reverse.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var events []g3reverse.Event
	for scanner.Scan() {
		var event g3reverse.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func loadCheckpoint(path string) (checkpointFile, bool, error) {
	if path == "" {
		return checkpointFile{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkpointFile{}, false, nil
		}
		return checkpointFile{}, false, err
	}
	var cp checkpointFile
	if err := json.Unmarshal(raw, &cp); err != nil {
		return checkpointFile{}, false, err
	}
	if cp.SchemaVersion != checkpointSchemaVersion {
		return checkpointFile{}, false, errors.New("router-g3-events: checkpoint schema mismatch")
	}
	want := cp.CheckpointHash
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return checkpointFile{}, false, err
	}
	if want == "" || hash != want {
		return checkpointFile{}, false, errors.New("router-g3-events: checkpoint hash mismatch")
	}
	cp.CheckpointHash = want
	return cp, true, nil
}

func saveCheckpoint(path string, cp checkpointFile) error {
	if path == "" {
		return errors.New("router-g3-events: checkpoint path is required")
	}
	cp.SchemaVersion = checkpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
