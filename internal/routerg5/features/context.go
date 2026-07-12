package features

import (
	"fmt"
	"math"
	"time"
)

// DecisionContext is one timestamp+symbol soft feature row.
type DecisionContext struct {
	SchemaVersion string             `json:"schema_version"`
	FeatureHash   string             `json:"feature_hash"`
	ProfileID     string             `json:"profile_id"`
	Symbol        string             `json:"symbol"`
	DecisionTime  time.Time          `json:"decision_time"`
	Partition     string             `json:"partition,omitempty"`
	Features      map[string]float64 `json:"features"`
	Missing       []string           `json:"missing,omitempty"`
}

// BuildDecisionContext merges sky, symbol, rank, and sorter soft scores.
func BuildDecisionContext(
	schema FeatureSchema,
	profileID, symbol string,
	decisionTime time.Time,
	partition string,
	btc BTCFeatures,
	local SymbolLocal,
	ranks map[string]float64,
) (DecisionContext, error) {
	features := map[string]float64{}
	for k, v := range SoftSkyScores(btc) {
		features[k] = v
	}
	for k, v := range SoftSorterScores(local) {
		features[k] = v
	}
	for k, v := range ranks {
		if isFinite(v) {
			features[k] = v
		}
	}
	if err := ValidateFeatureMap(features); err != nil {
		return DecisionContext{}, err
	}
	missing := missingMasks(features)
	ordered := map[string]float64{}
	for _, key := range FeatureKeys {
		if v, ok := features[key]; ok && isFinite(v) {
			ordered[key] = v
		}
	}
	return DecisionContext{
		SchemaVersion: schema.SchemaVersion,
		FeatureHash:   schema.FeatureHash,
		ProfileID:     profileID,
		Symbol:        symbol,
		DecisionTime:  decisionTime.UTC(),
		Partition:     partition,
		Features:      ordered,
		Missing:       missing,
	}, nil
}

func missingMasks(features map[string]float64) []string {
	var out []string
	for _, key := range MaskKeys {
		if features[key] >= 0.5 {
			out = append(out, key)
		}
	}
	for _, key := range FeatureKeys {
		if key == "mask_btc_funding_missing" {
			continue
		}
		v, ok := features[key]
		if !ok || !isFinite(v) {
			out = append(out, "missing:"+key)
		}
	}
	sortStrings(out)
	return out
}

// RejectForbiddenContext rejects outcome/reward/current-bar leak keys.
func RejectForbiddenContext(features map[string]float64) error {
	if err := ValidateFeatureMap(features); err != nil {
		return err
	}
	for key := range features {
		if IsForbiddenFeatureKey(key) {
			return fmt.Errorf("features: forbidden context key %q", key)
		}
	}
	return nil
}

// ContextRowKey is a stable sort key for external ordering.
func ContextRowKey(row DecisionContext) string {
	return row.DecisionTime.UTC().Format(time.RFC3339Nano) + "\x00" + row.Symbol
}

// NaNFeature is exported for tests asserting non-leakage.
func NaNFeature() float64 { return math.NaN() }
