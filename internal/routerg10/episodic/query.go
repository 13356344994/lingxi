package episodic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateQuery rejects queries that include forbidden future-outcome features.
func ValidateQuery(q QueryVector, rawFields map[string]float64) error {
	if rawFields != nil {
		for field := range rawFields {
			if isForbiddenQueryField(field) {
				return fmt.Errorf("%s: %q", RejectForbiddenQueryFeature, field)
			}
		}
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for key := range payload {
		if isForbiddenQueryField(key) {
			return fmt.Errorf("%s: %q", RejectForbiddenQueryFeature, key)
		}
	}
	if stateRaw, ok := payload["state"]; ok {
		var state map[string]json.RawMessage
		if err := json.Unmarshal(stateRaw, &state); err == nil {
			for key := range state {
				if isForbiddenQueryField(key) {
					return fmt.Errorf("%s: %q", RejectForbiddenQueryFeature, "state."+key)
				}
			}
		}
	}
	if execRaw, ok := payload["personal_exec"]; ok {
		var exec map[string]json.RawMessage
		if err := json.Unmarshal(execRaw, &exec); err == nil {
			for key := range exec {
				if isForbiddenQueryField(key) {
					return fmt.Errorf("%s: %q", RejectForbiddenQueryFeature, "personal_exec."+key)
				}
			}
		}
	}
	return nil
}

func isForbiddenQueryField(field string) bool {
	norm := strings.ToLower(strings.TrimSpace(field))
	for _, forbidden := range ForbiddenQueryFields {
		if norm == forbidden || strings.Contains(norm, forbidden) {
			return true
		}
	}
	switch norm {
	case "realized_pnl", "post_event_label", "post_event_labels", "label", "labels",
		"outcome", "outcome_win", "conservative_net_bps", "stress_net_bps", "win":
		return true
	}
	return false
}
