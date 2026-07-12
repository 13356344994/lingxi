package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg3/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g3-research.checkpoint.v1"

type checkpointFile struct {
	SchemaVersion           string    `json:"schema_version"`
	Generation              string    `json:"generation"`
	ProtocolHash            string    `json:"protocol_hash"`
	EventManifestHash       string    `json:"event_manifest_hash"`
	SearchManifestHash      string    `json:"search_manifest_hash"`
	DiscoveryReceiptHash    string    `json:"discovery_receipt_hash"`
	RulesHash               string    `json:"rules_hash"`
	LedgerHash              string    `json:"ledger_hash"`
	RecheckReportHash       string    `json:"recheck_report_hash"`
	NominationsHash         string    `json:"nominations_hash"`
	ForwardEventHash        string    `json:"forward_event_hash"`
	WalkForwardEventHash    string    `json:"walk_forward_event_hash"`
	PartitionBoundariesHash string    `json:"partition_boundaries_hash"`
	CatalogDataHash         string    `json:"catalog_data_hash"`
	ConfigHash              string    `json:"config_hash"`
	InputHash               string    `json:"input_hash"`
	OutputDir               string    `json:"output_dir"`
	MaxLines                int       `json:"max_lines"`
	Done                    bool      `json:"done"`
	RegistryHash            string    `json:"registry_hash,omitempty"`
	ResearchReportHash      string    `json:"research_report_hash,omitempty"`
	StageSummaryHash        string    `json:"stage_summary_hash,omitempty"`
	AttributionReportHash   string    `json:"attribution_report_hash,omitempty"`
	SavedAt                 time.Time `json:"saved_at"`
	CheckpointHash          string    `json:"checkpoint_hash"`
}

type checkpointBinding struct {
	EventManifestHash       string
	SearchManifestHash      string
	DiscoveryReceiptHash    string
	RulesHash               string
	LedgerHash              string
	RecheckReportHash       string
	NominationsHash         string
	ForwardEventHash        string
	WalkForwardEventHash    string
	PartitionBoundariesHash string
	CatalogDataHash         string
	ConfigHash              string
	InputHash               string
	OutputDir               string
	MaxLines                int
}

type stageSummaryFile struct {
	SchemaVersion        string `json:"schema_version"`
	Generation           string `json:"generation"`
	ProtocolHash         string `json:"protocol_hash"`
	RegisteredCandidates int    `json:"registered_candidates"`
	FrozenRuleCount      int    `json:"frozen_rule_count"`
	RecheckPassCount     int    `json:"recheck_pass_count"`
	AttributedCount      int    `json:"attributed_count"`
	NarrativeOnlyCount   int    `json:"narrative_only_count"`
	AmbiguousCount       int    `json:"ambiguous_count"`
	RejectedCount        int    `json:"rejected_count"`
	ClosedCount          int    `json:"closed_count"`
	InconclusiveCount    int    `json:"inconclusive_count"`
	ResearchPassLeaves   int    `json:"research_pass_leaves"`
	SystemStatus         string `json:"system_status"`
	ExamActions          int    `json:"exam_actions"`
	EventManifestHash    string `json:"event_manifest_hash"`
	SearchManifestHash   string `json:"search_manifest_hash"`
	DiscoveryReceiptHash string `json:"discovery_receipt_hash"`
	RecheckDataHash      string `json:"recheck_data_hash"`
	RecheckReportHash    string `json:"recheck_report_hash"`
	ForwardEventHash     string `json:"forward_event_hash"`
	WalkForwardEventHash string `json:"walk_forward_event_hash"`
}

func newCheckpoint(binding checkpointBinding) checkpointFile {
	return checkpointFile{
		SchemaVersion: checkpointSchemaVersion,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		EventManifestHash: binding.EventManifestHash, SearchManifestHash: binding.SearchManifestHash,
		DiscoveryReceiptHash: binding.DiscoveryReceiptHash,
		RulesHash:            binding.RulesHash, LedgerHash: binding.LedgerHash,
		RecheckReportHash: binding.RecheckReportHash, NominationsHash: binding.NominationsHash,
		ForwardEventHash: binding.ForwardEventHash, WalkForwardEventHash: binding.WalkForwardEventHash,
		PartitionBoundariesHash: binding.PartitionBoundariesHash, CatalogDataHash: binding.CatalogDataHash,
		ConfigHash: binding.ConfigHash, InputHash: binding.InputHash,
		OutputDir: binding.OutputDir, MaxLines: binding.MaxLines,
	}
}

func (cp checkpointFile) validateBinding(binding checkpointBinding) error {
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash ||
		cp.EventManifestHash != binding.EventManifestHash ||
		cp.SearchManifestHash != binding.SearchManifestHash ||
		cp.DiscoveryReceiptHash != binding.DiscoveryReceiptHash ||
		cp.RulesHash != binding.RulesHash ||
		cp.LedgerHash != binding.LedgerHash ||
		cp.RecheckReportHash != binding.RecheckReportHash ||
		cp.NominationsHash != binding.NominationsHash ||
		cp.ForwardEventHash != binding.ForwardEventHash ||
		cp.WalkForwardEventHash != binding.WalkForwardEventHash ||
		cp.PartitionBoundariesHash != binding.PartitionBoundariesHash ||
		cp.CatalogDataHash != binding.CatalogDataHash ||
		cp.ConfigHash != binding.ConfigHash ||
		cp.InputHash != binding.InputHash ||
		cp.MaxLines != binding.MaxLines ||
		filepath.Clean(cp.OutputDir) != filepath.Clean(binding.OutputDir) {
		return errors.New("router-g3-research: checkpoint input or config drift")
	}
	return nil
}

func verifyCompletedOutputs(outputDir string, cp checkpointFile) error {
	checks := map[string]string{
		"g3_candidate_registry.json": cp.RegistryHash,
		"g3_research_report.json":    cp.ResearchReportHash,
		"g3_stage_summary.json":      cp.StageSummaryHash,
		"g3_attribution_report.json": cp.AttributionReportHash,
	}
	for name, want := range checks {
		got, err := hashJSONFile(filepath.Join(outputDir, name))
		if err != nil {
			return err
		}
		if want == "" || got != want {
			return errors.New("router-g3-research: completed checkpoint output drift")
		}
	}
	return nil
}

func readStageSummary(path string) (stageSummaryFile, error) {
	var summary stageSummaryFile
	if err := readJSON(path, &summary); err != nil {
		return stageSummaryFile{}, err
	}
	return summary, nil
}

func configFingerprint(maxLines int) (string, error) {
	payload := struct {
		SchemaVersion string  `json:"schema_version"`
		MaxLines      int     `json:"max_lines"`
		ForwardAlpha  float64 `json:"forward_alpha"`
		WFAlpha       float64 `json:"wf_alpha"`
		Generation    string  `json:"generation"`
		ProtocolHash  string  `json:"protocol_hash"`
	}{
		SchemaVersion: checkpointSchemaVersion, MaxLines: maxLines,
		ForwardAlpha: protocol.ForwardAlpha, WFAlpha: protocol.WalkForwardAlpha,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func inputFingerprint(
	eventManifestHash, searchManifestHash, discoveryReceiptHash, rulesHash, ledgerHash,
	recheckReportHash, nominationsHash, forwardHash, wfHash, boundariesHash, catalogDataHash string,
) (string, error) {
	payload := struct {
		EventManifestHash       string `json:"event_manifest_hash"`
		SearchManifestHash      string `json:"search_manifest_hash"`
		DiscoveryReceiptHash    string `json:"discovery_receipt_hash"`
		RulesHash               string `json:"rules_hash"`
		LedgerHash              string `json:"ledger_hash"`
		RecheckReportHash       string `json:"recheck_report_hash"`
		NominationsHash         string `json:"nominations_hash"`
		ForwardEventHash        string `json:"forward_event_hash"`
		WalkForwardEventHash    string `json:"walk_forward_event_hash"`
		PartitionBoundariesHash string `json:"partition_boundaries_hash"`
		CatalogDataHash         string `json:"catalog_data_hash"`
	}{
		EventManifestHash: eventManifestHash, SearchManifestHash: searchManifestHash,
		DiscoveryReceiptHash: discoveryReceiptHash,
		RulesHash:            rulesHash, LedgerHash: ledgerHash,
		RecheckReportHash: recheckReportHash, NominationsHash: nominationsHash,
		ForwardEventHash: forwardHash, WalkForwardEventHash: wfHash,
		PartitionBoundariesHash: boundariesHash, CatalogDataHash: catalogDataHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func loadCheckpoint(path string) (checkpointFile, bool, error) {
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
		return checkpointFile{}, false, errors.New("router-g3-research: checkpoint schema mismatch")
	}
	want := cp.CheckpointHash
	cp.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(cp)
	if err != nil {
		return checkpointFile{}, false, err
	}
	if want == "" || hash != want {
		return checkpointFile{}, false, errors.New("router-g3-research: checkpoint hash mismatch")
	}
	cp.CheckpointHash = want
	return cp, true, nil
}

func saveCheckpoint(path string, cp checkpointFile) error {
	if path == "" {
		return errors.New("router-g3-research: checkpoint path is required")
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
