package researchboundary

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"lingxi/internal/datatruth"
)

type RegimeDimension struct {
	Name          string    `json:"name"`
	Inputs        []string  `json:"inputs"`
	LookbackHours int       `json:"lookback_hours"`
	Binning       string    `json:"binning"`
	Quantiles     []float64 `json:"quantiles,omitempty"`
	FitOn         string    `json:"fit_on"`
	MissingAction string    `json:"missing_action"`
}

type RegimeContract struct {
	SchemaVersion    string            `json:"schema_version"`
	ProtocolID       string            `json:"protocol_id"`
	DecisionTimeRule string            `json:"decision_time_rule"`
	Dimensions       []RegimeDimension `json:"dimensions"`
	ForbiddenInputs  []string          `json:"forbidden_inputs"`
	FreezeRule       string            `json:"freeze_rule"`
	ManifestHash     *string           `json:"manifest_hash"`
}

func LoadRegimeContract(path string) (RegimeContract, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RegimeContract{}, err
	}
	var contract RegimeContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		return RegimeContract{}, err
	}
	return contract, contract.Validate()
}

func (c RegimeContract) Validate() error {
	if strings.TrimSpace(c.SchemaVersion) == "" || strings.TrimSpace(c.ProtocolID) == "" ||
		strings.TrimSpace(c.DecisionTimeRule) == "" || strings.TrimSpace(c.FreezeRule) == "" {
		return fmt.Errorf("researchboundary: incomplete regime contract")
	}
	if len(c.Dimensions) == 0 || len(c.ForbiddenInputs) == 0 {
		return fmt.Errorf("researchboundary: regime dimensions and forbidden inputs are required")
	}
	seen := map[string]bool{}
	for _, dimension := range c.Dimensions {
		if strings.TrimSpace(dimension.Name) == "" || len(dimension.Inputs) == 0 ||
			dimension.LookbackHours <= 0 || dimension.FitOn != "research_partition_only" ||
			dimension.MissingAction != "OOD_NO_TRADE" {
			return fmt.Errorf("researchboundary: invalid regime dimension %q", dimension.Name)
		}
		if seen[dimension.Name] {
			return fmt.Errorf("researchboundary: duplicate regime dimension %q", dimension.Name)
		}
		seen[dimension.Name] = true
		if dimension.Binning == "research_quantiles" {
			if len(dimension.Quantiles) == 0 {
				return fmt.Errorf("researchboundary: quantile dimension %q has no quantiles", dimension.Name)
			}
			for _, quantile := range dimension.Quantiles {
				if quantile <= 0 || quantile >= 1 {
					return fmt.Errorf("researchboundary: invalid quantile for %q", dimension.Name)
				}
			}
		}
	}
	if c.ManifestHash == nil || strings.TrimSpace(*c.ManifestHash) == "" {
		return fmt.Errorf("researchboundary: regime manifest_hash is required")
	}
	hash, err := c.ComputedHash()
	if err != nil {
		return err
	}
	if hash != *c.ManifestHash {
		return fmt.Errorf("researchboundary: regime manifest_hash=%s want=%s", *c.ManifestHash, hash)
	}
	return nil
}

func (c RegimeContract) ComputedHash() (string, error) {
	c.ManifestHash = nil
	hash, _, err := datatruth.HashJSON(c)
	return hash, err
}
