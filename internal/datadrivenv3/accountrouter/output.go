package accountrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// WriteAtomic publishes the account report only after every artifact and its
// hash have been written to a private sibling directory.
func WriteAtomic(output string, report Report) (Report, error) {
	if _, err := os.Stat(output); err == nil {
		return Report{}, errors.New("atomic output destination already exists")
	} else if !os.IsNotExist(err) {
		return Report{}, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return Report{}, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), "."+filepath.Base(output)+".partial-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(stage)
	if report.Verdict == "ROUTER_CANDIDATE" {
		manifest := struct {
			SchemaVersion         string    `json:"schema_version"`
			LeafID                string    `json:"leaf_id"`
			Fraction              *float64  `json:"fraction"`
			HorizonHours          int       `json:"horizon_hours"`
			Delays                []int     `json:"delays"`
			CostStressMultipliers []float64 `json:"cost_stress_multipliers"`
			SelectionRule         string    `json:"selection_rule"`
			SelectionPartition    string    `json:"selection_partition"`
			ConfirmationPartition string    `json:"confirmation_partition"`
			PolicyHash            string    `json:"sealed_policy_hash"`
			TradeAllowed          bool      `json:"trade_allowed"`
		}{
			SchemaVersion: SchemaVersion, LeafID: report.LeafID, Fraction: report.SelectedFraction,
			HorizonHours: report.HorizonHours, Delays: []int{0, 1, 2, 3}, CostStressMultipliers: []float64{1, 1.5},
			SelectionRule:      "all scenarios safe; base cost positive at delays 0..3; 1.5x cost positive only at frozen delay 0",
			SelectionPartition: report.SelectionPartition, ConfirmationPartition: report.ConfirmationPartition,
			PolicyHash: report.SealedPolicyHash, TradeAllowed: false,
		}
		raw, err := marshal(manifest)
		if err != nil {
			return Report{}, err
		}
		if err := os.WriteFile(filepath.Join(stage, "router_candidate_manifest.json"), raw, 0o644); err != nil {
			return Report{}, err
		}
		sum := sha256.Sum256(raw)
		report.RouterCandidateManifest = "router_candidate_manifest.json"
		report.RouterCandidateManifestHash = hex.EncodeToString(sum[:])
	}
	raw, err := marshal(report)
	if err != nil {
		return Report{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, "account_router_report.json"), raw, 0o644); err != nil {
		return Report{}, err
	}
	sum := sha256.Sum256(raw)
	hashes, err := marshal(map[string]string{"account_router_report.json": hex.EncodeToString(sum[:]), "router_holdout_unblinded": "false"})
	if err != nil {
		return Report{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, "hashes.json"), hashes, 0o644); err != nil {
		return Report{}, err
	}
	if err := os.Rename(stage, output); err != nil {
		return Report{}, err
	}
	return report, nil
}

func marshal(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	return append(raw, '\n'), err
}
