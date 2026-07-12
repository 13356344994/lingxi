package portfoliosim

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// LoadValidationFeatures reads validation-partition feature shards only.
func LoadValidationFeatures(dir string) (map[string][]features.DecisionContext, features.ContextManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "feature_context_manifest.json"))
	if err != nil {
		return nil, features.ContextManifest{}, err
	}
	var manifest features.ContextManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, features.ContextManifest{}, err
	}
	if manifest.ProtocolHash != g5protocol.RouterProtocolHash {
		return nil, features.ContextManifest{}, errors.New("portfoliosim: feature protocol hash mismatch")
	}
	if manifest.ExamActions != g5protocol.ExamActions {
		return nil, features.ContextManifest{}, errors.New("portfoliosim: feature exam_actions must be 0")
	}
	byTimeSymbol := map[string][]features.DecisionContext{}
	for _, shard := range manifest.Shards {
		rows, err := features.ReadContextShard(filepath.Join(dir, shard.FileName))
		if err != nil {
			return nil, features.ContextManifest{}, err
		}
		for _, row := range rows {
			if row.Partition != outcome.PartitionValidation {
				continue
			}
			if err := features.RejectForbiddenContext(row.Features); err != nil {
				return nil, features.ContextManifest{}, err
			}
			key := row.DecisionTime.UTC().Format(time.RFC3339Nano)
			byTimeSymbol[key] = append(byTimeSymbol[key], row)
		}
	}
	return byTimeSymbol, manifest, nil
}

// BuildDecisionInputs converts feature rows into policy decision inputs.
func BuildDecisionInputs(rows []features.DecisionContext, positions map[string]actionspace.PositionState, equity float64) []policy.DecisionInput {
	var out []policy.DecisionInput
	for _, row := range rows {
		state, ok := positions[row.Symbol]
		if !ok {
			state = actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: equity}
		} else {
			state.EquityUSDT = equity
		}
		out = append(out, policy.DecisionInput{
			Symbol: row.Symbol, DecisionTime: row.DecisionTime,
			Partition: row.Partition, Features: row.Features, Missing: row.Missing,
			StartState: state, StateLabel: actionspace.StateLabel(state),
		})
	}
	return out
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = SchemaVersionConfig
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildReport constructs g5_validation_report.json.
func BuildReport(cfg RunConfig, scenarios []ScenarioResult, status, reason string, checks GateChecks) (ValidationReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReport{}, err
	}
	coverage := map[string]int{}
	var turnover, noTrade, capacity, fundingPaid float64
	var fundingMissing bool
	for _, s := range scenarios {
		MergeActionCoverage(coverage, s.ActionCoverage)
		turnover += s.TurnoverUSDT
		fundingPaid += s.FundingPaidUSDT
		if s.FundingMissing {
			fundingMissing = true
		}
		noTrade += s.NoTradeFraction
		capacity += s.CapacityRejectRate
	}
	n := float64(len(scenarios))
	if n > 0 {
		noTrade /= n
		capacity /= n
	}
	report := ValidationReport{
		SchemaVersion:      SchemaVersionReport,
		Generation:         g5protocol.Generation,
		ProtocolHash:       g5protocol.RouterProtocolHash,
		HistoryDataHash:    g5protocol.HistoryDataHash,
		ProfileID:          cfg.ProfileID,
		PolicyHash:         cfg.PolicyHash,
		FeatureHash:        cfg.FeatureHash,
		ExperimentHash:     cfg.ExperimentHash,
		ConfigHash:         cfgHash,
		Status:             status,
		FailReason:         reason,
		SummaryNote:        checks.SummaryNote,
		GateChecks:         checks,
		Scenarios:          scenarios,
		ActionCoverage:     coverage,
		NoTradeFraction:    noTrade,
		TurnoverUSDT:       turnover,
		FundingPaidUSDT:  fundingPaid,
		FundingMissing:   fundingMissing,
		CapacityRejectRate: capacity,
		ExamActions:        g5protocol.ExamActions,
		CreatedAt:          time.Now().UTC(),
	}
	hash, err := hashReport(report)
	if err != nil {
		return ValidationReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func hashReport(r ValidationReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildReceipt constructs validation receipt with provenance hashes.
func BuildReceipt(
	cfg RunConfig,
	report ValidationReport,
	featureManifest features.ContextManifest,
	policyManifest reverselearn.PolicyManifest,
	canonical map[string]string,
) (ValidationReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt := ValidationReceipt{
		SchemaVersion:       SchemaVersionReceipt,
		Generation:          g5protocol.Generation,
		ProtocolHash:        g5protocol.RouterProtocolHash,
		HistoryDataHash:     g5protocol.HistoryDataHash,
		PolicyHash:          cfg.PolicyHash,
		FeatureHash:         cfg.FeatureHash,
		ExperimentHash:      cfg.ExperimentHash,
		FeatureManifestHash: featureManifest.ManifestHash,
		PolicyManifestHash:  policyManifest.ManifestHash,
		ConfigHash:          cfgHash,
		ReportHash:          report.ReportHash,
		FundingPaidUSDT:     report.FundingPaidUSDT,
		FundingMissing:      report.FundingMissing,
		CanonicalDataHashes: canonical,
		ExamActions:         g5protocol.ExamActions,
		CreatedAt:           time.Now().UTC(),
	}
	hash, err := hashReceipt(receipt)
	if err != nil {
		return ValidationReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

func hashReceipt(r ValidationReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// WriteJSONAtomic writes one json artifact atomically.
func WriteJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
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

// RejectWalkForward ensures validation never reads WF partition rows.
func RejectWalkForward(rows []features.DecisionContext) error {
	for _, row := range rows {
		if row.Partition == outcome.PartitionWalkFwd {
			return fmt.Errorf("portfoliosim: walk_forward partition forbidden")
		}
	}
	return nil
}

// RejectDiscoveryPartition rejects discovery rows in validation replay.
func RejectDiscoveryPartition(rows []features.DecisionContext) error {
	for _, row := range rows {
		if row.Partition == outcome.PartitionDiscovery {
			return fmt.Errorf("portfoliosim: discovery partition forbidden")
		}
	}
	return nil
}

// CatalogHash returns catalog data hash.
func CatalogHash(catalog historydata.HistoryCatalog) string {
	return catalog.DataHash
}

// ReadLinesJSONL is a helper for tests.
func ReadLinesJSONL(path string) ([]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var out []json.RawMessage
	for scanner.Scan() {
		out = append(out, append([]byte(nil), scanner.Bytes()...))
	}
	return out, scanner.Err()
}
