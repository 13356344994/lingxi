package researchboundary

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type Testability string

const (
	Testable   Testability = "TESTABLE"
	Untestable Testability = "UNTESTABLE"
	Rejected   Testability = "REJECTED"
)

type DataRequirements struct {
	Required  []string `json:"required"`
	Optional  []string `json:"optional,omitempty"`
	Forbidden []string `json:"forbidden,omitempty"`
}

type RequirementItem struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Generation     string           `json:"generation"`
	Requirements   DataRequirements `json:"requirements"`
	MissingAction  string           `json:"missing_action"`
	SearchDeadline time.Time        `json:"historical_data_search_deadline"`
}

type RequirementsManifest struct {
	SchemaVersion string            `json:"schema_version"`
	ProtocolID    string            `json:"protocol_id"`
	Items         []RequirementItem `json:"items"`
	ManifestHash  *string           `json:"manifest_hash"`
}

type TestabilityVerdict struct {
	ID               string      `json:"id"`
	Status           Testability `json:"status"`
	Missing          []string    `json:"missing,omitempty"`
	ForbiddenPresent []string    `json:"forbidden_present,omitempty"`
	Reason           string      `json:"reason"`
}

func LoadRequirements(path string) (RequirementsManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RequirementsManifest{}, err
	}
	var manifest RequirementsManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RequirementsManifest{}, err
	}
	return manifest, manifest.Validate()
}

func (m RequirementsManifest) Validate() error {
	if strings.TrimSpace(m.SchemaVersion) == "" || strings.TrimSpace(m.ProtocolID) == "" {
		return fmt.Errorf("researchboundary: schema_version and protocol_id are required")
	}
	if len(m.Items) == 0 {
		return fmt.Errorf("researchboundary: requirements manifest has no items")
	}
	seen := map[string]bool{}
	for _, item := range m.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Kind) == "" ||
			strings.TrimSpace(item.Generation) == "" || strings.TrimSpace(item.MissingAction) == "" ||
			item.SearchDeadline.IsZero() {
			return fmt.Errorf("researchboundary: incomplete requirement item %q", item.ID)
		}
		if seen[item.ID] {
			return fmt.Errorf("researchboundary: duplicate requirement item %q", item.ID)
		}
		seen[item.ID] = true
		if len(normalizedStrings(item.Requirements.Required)) == 0 {
			return fmt.Errorf("researchboundary: item %q has no required data", item.ID)
		}
	}
	if m.ManifestHash == nil || strings.TrimSpace(*m.ManifestHash) == "" {
		return fmt.Errorf("researchboundary: manifest_hash is required")
	}
	hash, err := m.ComputedHash()
	if err != nil {
		return err
	}
	if hash != *m.ManifestHash {
		return fmt.Errorf("researchboundary: manifest_hash=%s want=%s", *m.ManifestHash, hash)
	}
	return nil
}

func (m RequirementsManifest) ComputedHash() (string, error) {
	m.ManifestHash = nil
	hash, _, err := datatruth.HashJSON(m)
	return hash, err
}

func EvaluateRequirements(m RequirementsManifest, available map[string]bool) []TestabilityVerdict {
	var verdicts []TestabilityVerdict
	for _, item := range m.Items {
		var missing, forbidden []string
		for _, family := range normalizedStrings(item.Requirements.Required) {
			if !available[family] {
				missing = append(missing, family)
			}
		}
		for _, family := range normalizedStrings(item.Requirements.Forbidden) {
			if available[family] {
				forbidden = append(forbidden, family)
			}
		}
		verdict := TestabilityVerdict{ID: item.ID, Status: Testable, Reason: "all_required_history_available"}
		switch {
		case len(forbidden) > 0:
			verdict.Status = Rejected
			verdict.Reason = "forbidden_data_present"
			verdict.ForbiddenPresent = forbidden
		case len(missing) > 0:
			verdict.Status = Untestable
			verdict.Reason = item.MissingAction
			verdict.Missing = missing
		}
		verdicts = append(verdicts, verdict)
	}
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].ID < verdicts[j].ID })
	return verdicts
}

func normalizedStrings(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
