package reverselearn

import (
	"time"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const (
	SchemaVersionLedger   = "lingxi.router-g5-reverse-learn.ledger.v1"
	SchemaVersionPolicy   = "lingxi.router-g5-reverse-learn.policy.v1"
	SchemaVersionManifest = "lingxi.router-g5-reverse-learn.manifest.v1"
	SchemaVersionReport   = "lingxi.router-g5-reverse-learn.report.v1"
	SchemaVersionConfig   = "lingxi.router-g5-reverse-learn.config.v1"

	LedgerStageDiscovery = "discovery_reverse_learn"

	StatusTrainingRejectedDegenerate = "TRAINING_REJECTED_DEGENERATE"
	StatusFrozenPolicy               = "FROZEN_POLICY"
	StatusNoFrozenPolicy             = "NO_FROZEN_POLICY"
)

// RunConfig binds CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	ProfileID           string `json:"profile_id"`
	CubeDir             string `json:"cube_dir"`
	FeatureDir          string `json:"feature_dir"`
	OutputDir           string `json:"output_dir"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
}

// TrainingExample is one joined discovery label + decision context row.
type TrainingExample struct {
	Symbol       string
	DecisionTime time.Time
	StateLabel   string
	StartState   actionspace.PositionState
	Action       actionspace.Action
	Label        string
	RewardBPS    float64
	Features     map[string]float64
	Missing      []string
}

// ExperimentAttempt records one lambda grid attempt.
type ExperimentAttempt struct {
	AttemptOrdinal      int     `json:"attempt_ordinal"`
	Lambda              float64 `json:"lambda"`
	Seed                int     `json:"seed"`
	Epochs              int     `json:"epochs"`
	TopActionRegretBPS  float64 `json:"top_action_regret_bps"`
	DirectionAccuracy   float64 `json:"direction_accuracy"`
	RewardMeanBPS       float64 `json:"reward_mean_bps"`
	TrainRows           int     `json:"train_rows"`
	HoldoutRows         int     `json:"holdout_rows"`
	PolicyHash          string  `json:"policy_hash"`
	Status              string  `json:"status"`
}

// ExperimentLedger is the append-only discovery experiment ledger.
type ExperimentLedger struct {
	SchemaVersion string              `json:"schema_version"`
	Generation    string              `json:"generation"`
	ProtocolHash  string              `json:"protocol_hash"`
	FeatureHash   string              `json:"feature_hash"`
	LedgerStage   string              `json:"ledger_stage"`
	Attempts      []ExperimentAttempt `json:"attempts"`
	LedgerHash    string              `json:"ledger_hash"`
}

// Normalization binds train-only standardization.
type Normalization struct {
	Means map[string]float64 `json:"means"`
	Stds  map[string]float64 `json:"stds"`
}

// FrozenPolicy is the sealed reverse-learn policy artifact.
type FrozenPolicy struct {
	SchemaVersion   string              `json:"schema_version"`
	Generation      string              `json:"generation"`
	ProtocolHash    string              `json:"protocol_hash"`
	FeatureHash     string              `json:"feature_hash"`
	PolicyHash      string              `json:"policy_hash"`
	ExperimentHash  string              `json:"experiment_hash"`
	ModelFamily     string              `json:"model_family"`
	ProfileID       string              `json:"profile_id"`
	Lambda          float64             `json:"lambda"`
	Seed            int                 `json:"seed"`
	Epochs          int                 `json:"epochs"`
	FeatureKeys     []string            `json:"feature_keys"`
	ActionEncoding  ActionEncodingSpec  `json:"action_encoding"`
	Weights         []float64           `json:"weights"`
	Normalization   Normalization       `json:"normalization"`
	HorizonLabels   []string            `json:"horizon_labels"`
	ExamActions     int                 `json:"exam_actions"`
}

// ActionEncodingSpec is the frozen action feature contract.
type ActionEncodingSpec struct {
	Verbs         []string `json:"verbs"`
	Directions    []string `json:"directions"`
	StartStates   []string `json:"start_states"`
	NumericFields []string `json:"numeric_fields"`
	Dim           int      `json:"dim"`
}

// TrainingReport summarizes discovery-only training metrics.
type TrainingReport struct {
	SchemaVersion         string  `json:"schema_version"`
	Generation            string  `json:"generation"`
	ProtocolHash          string  `json:"protocol_hash"`
	FeatureHash           string  `json:"feature_hash"`
	Status                string  `json:"status,omitempty"`
	PolicyHash            string  `json:"policy_hash"`
	ExperimentHash        string  `json:"experiment_hash"`
	ProfileID             string  `json:"profile_id"`
	SelectedLambda        float64 `json:"selected_lambda"`
	TopActionRegretBPS    float64 `json:"top_action_regret_bps"`
	DirectionAccuracy     float64 `json:"direction_accuracy"`
	RewardMeanBPS         float64 `json:"reward_mean_bps"`
	TrainRows             int     `json:"train_rows"`
	HoldoutRows           int     `json:"holdout_rows"`
	JoinedRows            int     `json:"joined_rows"`
	AttemptCount          int     `json:"attempt_count"`
	FixedDirectionRejected bool   `json:"fixed_direction_rejected"`
	FixedHorizonRejected   bool   `json:"fixed_horizon_rejected"`
	ExamActions           int     `json:"exam_actions"`
}

// PolicyManifest binds cube/feature inputs and sealed outputs.
type PolicyManifest struct {
	SchemaVersion       string            `json:"schema_version"`
	Generation          string            `json:"generation"`
	ProtocolHash        string            `json:"protocol_hash"`
	HistoryDataHash     string            `json:"history_data_hash"`
	ProfileID           string            `json:"profile_id"`
	Status              string            `json:"status,omitempty"`
	FeatureHash         string            `json:"feature_hash"`
	PolicyHash          string            `json:"policy_hash"`
	ExperimentHash      string            `json:"experiment_hash"`
	CubeManifestHash    string            `json:"cube_manifest_hash"`
	FeatureManifestHash string            `json:"feature_manifest_hash"`
	TrainingLabelHash   string            `json:"training_label_hash"`
	ConfigHash          string            `json:"config_hash"`
	ArtifactHashes      map[string]string `json:"artifact_hashes"`
	ExamActions         int               `json:"exam_actions"`
	CreatedAt           time.Time         `json:"created_at"`
	ManifestHash        string            `json:"manifest_hash"`
}

// DefaultGeneration returns frozen G5 generation.
func DefaultGeneration() string { return g5protocol.Generation }

// DefaultProtocolHash returns frozen G5 protocol hash.
func DefaultProtocolHash() string { return g5protocol.RouterProtocolHash }

// DefaultHistoryDataHash returns frozen history data hash.
func DefaultHistoryDataHash() string { return g5protocol.HistoryDataHash }
