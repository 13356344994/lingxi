package outcome

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"lingxi/internal/datatruth"
)

const trainingLabelsArtifact = "oracle_training_labels.jsonl"

// WriteTrainingLabels atomically writes discovery training labels jsonl.
func WriteTrainingLabels(outputDir string, entries []TrainingLabelEntry) (string, error) {
	sortTrainingLabels(entries)
	path := filepath.Join(outputDir, trainingLabelsArtifact)
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	writer := bufio.NewWriter(file)
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			file.Close()
			os.Remove(tmp)
			return "", err
		}
		if _, err := writer.Write(raw); err != nil {
			file.Close()
			os.Remove(tmp)
			return "", err
		}
		if err := writer.WriteByte('\n'); err != nil {
			file.Close()
			os.Remove(tmp)
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return hashLabelsFile(path)
}

func sortTrainingLabels(entries []TrainingLabelEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].DecisionTime.Equal(entries[j].DecisionTime) {
			return entries[i].DecisionTime.Before(entries[j].DecisionTime)
		}
		if entries[i].Symbol != entries[j].Symbol {
			return entries[i].Symbol < entries[j].Symbol
		}
		return entries[i].Label < entries[j].Label
	})
}

func hashLabelsFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ReadTrainingLabels loads discovery training labels from jsonl.
func ReadTrainingLabels(path string) ([]TrainingLabelEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []TrainingLabelEntry
	for scanner.Scan() {
		var entry TrainingLabelEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, scanner.Err()
}

// HashTrainingLabels returns a stable hash for label entries.
func HashTrainingLabels(entries []TrainingLabelEntry) (string, error) {
	canonical := append([]TrainingLabelEntry(nil), entries...)
	sortTrainingLabels(canonical)
	hash, _, err := datatruth.HashJSON(canonical)
	return hash, err
}
