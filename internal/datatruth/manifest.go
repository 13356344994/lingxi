package datatruth

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type DataManifest struct {
	SchemaVersion string    `json:"schema_version"`
	DatasetID     string    `json:"dataset_id"`
	EventHashes   []string  `json:"event_hashes"`
	UniverseHash  string    `json:"universe_hash,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type FeatureManifest struct {
	SchemaVersion string    `json:"schema_version"`
	FeatureSetID  string    `json:"feature_set_id"`
	DataHash      string    `json:"data_hash"`
	CodeHash      string    `json:"code_hash"`
	FeatureNames  []string  `json:"feature_names"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
}

type ExperimentManifest struct {
	SchemaVersion string         `json:"schema_version"`
	ExperimentID  string         `json:"experiment_id"`
	FeatureHash   string         `json:"feature_hash"`
	ProtocolHash  string         `json:"protocol_hash"`
	ExamID        string         `json:"exam_id,omitempty"`
	Parameters    map[string]any `json:"parameters,omitempty"`
	CreatedAt     time.Time      `json:"created_at,omitempty"`
}

type DeploymentManifest struct {
	SchemaVersion  string         `json:"schema_version"`
	DeploymentID   string         `json:"deployment_id"`
	ExperimentHash string         `json:"experiment_hash"`
	DecisionSchema DecisionSchema `json:"decision_schema"`
	CreatedAt      time.Time      `json:"created_at,omitempty"`
}

type DecisionSchema struct {
	DecisionID       string   `json:"decision_id"`
	DataHash         string   `json:"data_hash"`
	FeatureHash      string   `json:"feature_hash"`
	ExperimentHash   string   `json:"experiment_hash"`
	InputEventHashes []string `json:"input_event_hashes"`
}

func (m DataManifest) NormalizedHash() (string, error) {
	m.EventHashes = uniqueSorted(m.EventHashes)
	if err := require("dataset_id", m.DatasetID); err != nil {
		return "", err
	}
	if len(m.EventHashes) == 0 {
		return "", errors.New("data manifest requires event hashes")
	}
	return hashManifest(m)
}

func (m FeatureManifest) NormalizedHash() (string, error) {
	m.FeatureNames = uniqueSorted(m.FeatureNames)
	if err := require("feature_set_id", m.FeatureSetID); err != nil {
		return "", err
	}
	if err := require("data_hash", m.DataHash); err != nil {
		return "", err
	}
	if err := require("code_hash", m.CodeHash); err != nil {
		return "", err
	}
	return hashManifest(m)
}

func (m ExperimentManifest) NormalizedHash() (string, error) {
	if err := require("experiment_id", m.ExperimentID); err != nil {
		return "", err
	}
	if err := require("feature_hash", m.FeatureHash); err != nil {
		return "", err
	}
	if err := require("protocol_hash", m.ProtocolHash); err != nil {
		return "", err
	}
	return hashManifest(m)
}

func (m DeploymentManifest) NormalizedHash() (string, error) {
	if err := require("deployment_id", m.DeploymentID); err != nil {
		return "", err
	}
	if err := require("experiment_hash", m.ExperimentHash); err != nil {
		return "", err
	}
	if err := m.DecisionSchema.Validate(); err != nil {
		return "", err
	}
	return hashManifest(m)
}

func (s DecisionSchema) Validate() error {
	if err := ValidateDecisionID(s.DecisionID); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"data_hash", s.DataHash},
		{"feature_hash", s.FeatureHash},
		{"experiment_hash", s.ExperimentHash},
	} {
		if err := require(field.name, field.value); err != nil {
			return err
		}
	}
	if len(s.InputEventHashes) == 0 {
		return errors.New("decision schema requires input event hashes")
	}
	cp := append([]string(nil), s.InputEventHashes...)
	sort.Strings(cp)
	for _, h := range cp {
		if strings.TrimSpace(h) == "" {
			return errors.New("decision schema contains empty input event hash")
		}
	}
	return nil
}

func hashManifest(v any) (string, error) {
	hash, _, err := HashJSON(v)
	return hash, err
}

func require(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
