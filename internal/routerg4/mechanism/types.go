package mechanism

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/reversemine"
	"lingxi/internal/routerg4/validation"
)

const (
	SchemaVersionMechanismReport = "lingxi.router-g4-mechanism.report.v1"
	SchemaVersionStrictWFReport  = "lingxi.router-g4-mechanism.strict-wf.v1"
	SchemaVersionFinalLeaves     = "lingxi.router-g4-mechanism.leaves.v1"
	SchemaVersionStageSummary    = "lingxi.router-g4-mechanism.stage.v1"
	SchemaVersionReceipt         = "lingxi.router-g4-mechanism.receipt.v1"
	SchemaVersionConfig          = "lingxi.router-g4-mechanism.config.v1"

	FinalStatusUnexplainedResearchPass = "UNEXPLAINED_RESEARCH_PASS"
	FinalStatusMechanismHypothesis     = "MECHANISM_HYPOTHESIS_RESEARCH_RESULT"
	FinalStatusResearchPass            = "RESEARCH_PASS"
	FinalStatusClosed                  = "CLOSED"
	FinalStatusInconclusive            = "INCONCLUSIVE"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion         string   `json:"schema_version"`
	HistoryRoot           string   `json:"history_root"`
	ValidationOutputDir   string   `json:"validation_output_dir"`
	CandidateOutputDir    string   `json:"candidate_output_dir"`
	OutputDir             string   `json:"output_dir"`
	MapDirs               []string `json:"map_dirs"`
	MaxFiles              int      `json:"max_files"`
	MaxLines              int      `json:"max_lines"`
	ProgressEvery         int      `json:"progress_every"`
	Generation            string   `json:"generation"`
	ProtocolHash          string   `json:"protocol_hash"`
	HistoryDataHash       string   `json:"history_data_hash"`
	ValidationReportHash  string   `json:"validation_report_hash"`
	CandidateManifestHash string   `json:"candidate_manifest_hash"`
}

// AttributionDecision records one survivor mechanism classification.
type AttributionDecision struct {
	Ordinal           int      `json:"ordinal"`
	CanonicalHash     string   `json:"canonical_hash"`
	ProfileID         string   `json:"profile_id"`
	AttributionStatus string   `json:"attribution_status"`
	FinalStatus       string   `json:"final_status"`
	Reason            string   `json:"reason,omitempty"`
	TemplateID        string   `json:"template_id,omitempty"`
	Mechanism         string   `json:"mechanism,omitempty"`
	Payer             string   `json:"payer,omitempty"`
	ForcedBehavior    string   `json:"forced_behavior,omitempty"`
	CannotWait        bool     `json:"cannot_wait"`
	CompetitionDefense string  `json:"competition_defense,omitempty"`
	Deadline          string   `json:"deadline,omitempty"`
	Invalidators      []string `json:"invalidators,omitempty"`
	FeatureKeys       []string `json:"feature_keys"`
	ClaimedMechanism  string   `json:"claimed_mechanism,omitempty"`
}

// MechanismReport is g4_mechanism_report.json.
type MechanismReport struct {
	SchemaVersion         string                `json:"schema_version"`
	Generation            string                `json:"generation"`
	ProtocolHash          string                `json:"protocol_hash"`
	ValidationReportHash  string                `json:"validation_report_hash"`
	ValidationSurvivorsHash string              `json:"validation_survivors_hash"`
	CandidateManifestHash string                `json:"candidate_manifest_hash"`
	SurvivorCount         int                   `json:"survivor_count"`
	ExplainedCount        int                   `json:"explained_count"`
	UnexplainedCount      int                   `json:"unexplained_count"`
	MechanismHypothesisCount int                `json:"mechanism_hypothesis_count"`
	AmbiguousCount        int                   `json:"ambiguous_count"`
	GraveyardCount        int                   `json:"graveyard_count"`
	Decisions             []AttributionDecision `json:"decisions"`
	ReportHash            string                `json:"report_hash"`
	ExamActions           int                   `json:"exam_actions"`
	CreatedAt             time.Time             `json:"created_at"`
}

// WFGateChecks records strict walk-forward gate outcomes.
type WFGateChecks struct {
	MinFiringsMet                 bool `json:"min_firings_met"`
	MinUniqueDaysMet              bool `json:"min_unique_days_met"`
	MinUniqueSymbolsMet           bool `json:"min_unique_symbols_met"`
	MinIndependentBlocksMet       bool `json:"min_independent_blocks_met"`
	FeeNetMeanPositive            bool `json:"fee_net_mean_positive"`
	ConservativeMeanPositive      bool `json:"conservative_mean_positive"`
	DelayMeanPositive             bool `json:"delay_mean_positive"`
	StressMeanPositive            bool `json:"stress_mean_positive"`
	MaxContributionOK             bool `json:"max_contribution_ok"`
	CapacityFailFractionOK        bool `json:"capacity_fail_fraction_ok"`
	SignTestEligible              bool `json:"sign_test_eligible"`
	SignTestPass                  bool `json:"sign_test_pass,omitempty"`
	HolmPass                      bool `json:"holm_pass,omitempty"`
}

// WFResult is one explained candidate strict WF outcome.
type WFResult struct {
	Ordinal           int                   `json:"ordinal"`
	CanonicalHash     string                `json:"canonical_hash"`
	ProfileID         string                `json:"profile_id"`
	Status            string                `json:"status"`
	Reason            string                `json:"reason,omitempty"`
	Firing            validation.FiringStats `json:"firing"`
	GateChecks        WFGateChecks          `json:"gate_checks"`
	NonzeroBlockCount int                   `json:"nonzero_block_count"`
	PositiveBlockCount int                  `json:"positive_block_count"`
	SignPValue        float64               `json:"sign_p_value,omitempty"`
	HolmLimit         float64               `json:"holm_limit,omitempty"`
	TemplateID        string                `json:"template_id"`
	Mechanism         string                `json:"mechanism"`
}

// StrictWFReport is g4_strict_wf_report.json.
type StrictWFReport struct {
	SchemaVersion         string     `json:"schema_version"`
	Generation            string     `json:"generation"`
	ProtocolHash          string     `json:"protocol_hash"`
	MechanismReportHash   string     `json:"mechanism_report_hash"`
	ValidationReportHash  string     `json:"validation_report_hash"`
	CandidateManifestHash string     `json:"candidate_manifest_hash"`
	ExplainedCount        int        `json:"explained_count"`
	ResearchPassCount     int        `json:"research_pass_count"`
	ClosedCount           int        `json:"closed_count"`
	InconclusiveCount     int        `json:"inconclusive_count"`
	FamilyAlpha           float64    `json:"family_alpha"`
	Results               []WFResult `json:"results"`
	ReportHash            string     `json:"report_hash"`
	ExamActions           int        `json:"exam_actions"`
	CreatedAt             time.Time  `json:"created_at"`
}

// FinalLeaf is one post-mechanism routed leaf decision.
type FinalLeaf struct {
	Ordinal           int                    `json:"ordinal"`
	CanonicalHash     string                 `json:"canonical_hash"`
	ProfileID         string                 `json:"profile_id"`
	CellKey           profitmap.CellKey      `json:"cell_key"`
	Status            string                 `json:"status"`
	AttributionStatus string                 `json:"attribution_status"`
	TradeAllowed      bool                   `json:"trade_allowed"`
	ExamAllowed       bool                   `json:"exam_allowed"`
	Certified         bool                   `json:"certified"`
	Reason            string                 `json:"reason,omitempty"`
	TemplateID        string                 `json:"template_id,omitempty"`
	Mechanism         string                 `json:"mechanism,omitempty"`
	Payer             string                 `json:"payer,omitempty"`
	ForcedBehavior    string                 `json:"forced_behavior,omitempty"`
	CannotWait        bool                   `json:"cannot_wait"`
	CompetitionDefense string                `json:"competition_defense,omitempty"`
	Deadline          string                 `json:"deadline,omitempty"`
	Invalidators      []string               `json:"invalidators,omitempty"`
	Conditions        []reversemine.Condition `json:"conditions"`
	WF                *WFResult              `json:"wf,omitempty"`
}

// FinalLeaves is g4_final_leaves.json.
type FinalLeaves struct {
	SchemaVersion         string      `json:"schema_version"`
	Generation            string      `json:"generation"`
	ProtocolHash          string      `json:"protocol_hash"`
	MechanismReportHash   string      `json:"mechanism_report_hash"`
	StrictWFReportHash    string      `json:"strict_wf_report_hash"`
	ValidationReportHash  string      `json:"validation_report_hash"`
	Leaves                []FinalLeaf `json:"leaves"`
	LeavesHash            string      `json:"leaves_hash"`
	ExamActions           int         `json:"exam_actions"`
}

// StageSummary is g4_stage_summary.json.
type StageSummary struct {
	SchemaVersion           string `json:"schema_version"`
	Generation              string `json:"generation"`
	ProtocolHash            string `json:"protocol_hash"`
	ValidationReportHash    string `json:"validation_report_hash"`
	ValidationSurvivorsHash string `json:"validation_survivors_hash"`
	ValidationReceiptHash   string `json:"validation_receipt_hash"`
	MechanismReportHash     string `json:"mechanism_report_hash"`
	StrictWFReportHash      string `json:"strict_wf_report_hash"`
	FinalLeavesHash         string `json:"final_leaves_hash"`
	SurvivorCount           int    `json:"survivor_count"`
	ExplainedCount          int    `json:"explained_count"`
	UnexplainedResearchPass int    `json:"unexplained_research_pass_count"`
	MechanismHypothesisResult int  `json:"mechanism_hypothesis_research_result_count"`
	ResearchPassLeaves      int    `json:"research_pass_leaves"`
	ClosedCount             int    `json:"closed_count"`
	InconclusiveCount       int    `json:"inconclusive_count"`
	SystemStatus            string `json:"system_status"`
	ExamActions             int    `json:"exam_actions"`
}

// MechanismReceipt binds all provenance hashes.
type MechanismReceipt struct {
	SchemaVersion           string            `json:"schema_version"`
	Generation              string            `json:"generation"`
	ProtocolHash            string            `json:"protocol_hash"`
	HistoryDataHash         string            `json:"history_data_hash"`
	ValidationReportHash    string            `json:"validation_report_hash"`
	ValidationSurvivorsHash string            `json:"validation_survivors_hash"`
	ValidationReceiptHash   string            `json:"validation_receipt_hash"`
	CandidateManifestHash   string            `json:"candidate_manifest_hash"`
	CandidatesHash          string            `json:"candidates_hash"`
	LedgerHash              string            `json:"ledger_hash"`
	MapManifestHashes       map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes       map[string]string `json:"map_artifact_hashes"`
	CatalogHash             string            `json:"catalog_hash"`
	ConfigHash              string            `json:"config_hash"`
	MechanismReportHash     string            `json:"mechanism_report_hash"`
	StrictWFReportHash      string            `json:"strict_wf_report_hash"`
	FinalLeavesHash         string            `json:"final_leaves_hash"`
	StageSummaryHash        string            `json:"stage_summary_hash"`
	CanonicalDataHashes     map[string]string `json:"canonical_data_hashes"`
	ExamActions             int               `json:"exam_actions"`
	CreatedAt               time.Time         `json:"created_at"`
	ReceiptHash             string            `json:"receipt_hash"`
}

// RunResult holds mechanism stage outputs.
type RunResult struct {
	MechanismReport MechanismReport
	StrictWFReport  StrictWFReport
	FinalLeaves     FinalLeaves
	StageSummary    StageSummary
	Receipt         MechanismReceipt
}

// ValidationBundle is sealed validation output consumed by mechanism.
type ValidationBundle struct {
	Dir       string
	Report    validation.ValidationReport
	Survivors validation.ValidationSurvivors
	Receipt   validation.ValidationReceipt
}

// InputBundle binds verified validation, candidate, and map inputs.
type InputBundle struct {
	Validation ValidationBundle
	Candidates validation.CandidateBundle
	Maps       []validation.MapBundle
}

// SurvivorWorkItem is one validation pass survivor scheduled for attribution/WF.
type SurvivorWorkItem struct {
	Survivor   validation.SurvivorEntry
	Candidate  reversemine.Candidate
	Decision   AttributionDecision
	CandidateIndex int
}
