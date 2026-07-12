package features

import (
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/datatruth"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const SchemaVersion = "lingxi.routerg5.features.schema.v1"

// Frozen feature keys in deterministic order. FeatureHash binds this list.
var FeatureKeys = []string{
	"btc_return",
	"btc_vol",
	"btc_trend",
	"btc_drawdown",
	"btc_funding_state",
	"sky_confidence",
	"mask_btc_funding_missing",
	"symbol_return",
	"symbol_rv",
	"symbol_beta",
	"btc_relative_return",
	"liquidity_percentile",
	"quote_volume_rank",
	"return_rank",
	"vol_rank",
	"liquidity_rank",
	"beta_rank",
	"btc_relative_strength_rank",
	"liquidity_score",
	"vol_cost_penalty",
	"capacity_headroom",
	"cost_estimate",
}

// MaskKeys are 0/1 indicators; 1 means missing/unavailable.
var MaskKeys = []string{
	"mask_btc_funding_missing",
}

// FeatureSchema is the frozen G5 decision feature contract.
type FeatureSchema struct {
	SchemaVersion string   `json:"schema_version"`
	ProfileMode   string   `json:"profile_mode"`
	Keys          []string `json:"keys"`
	MaskKeys      []string `json:"mask_keys"`
	SkyOutputs    []string `json:"sky_outputs"`
	SorterOutputs []string `json:"sorter_outputs"`
	RankOutputs   []string `json:"rank_outputs"`
	FeatureHash   string   `json:"feature_hash"`
}

// FrozenFeatureSchema returns the protocol-bound schema with computed hash.
func FrozenFeatureSchema() (FeatureSchema, error) {
	keys := append([]string(nil), FeatureKeys...)
	masks := append([]string(nil), MaskKeys...)
	sky := append([]string(nil), g5protocol.SkyOutputs...)
	sorter := append([]string(nil), g5protocol.SorterOutputs...)
	ranks := append([]string(nil), g5protocol.CrossSectionalRanks...)
	ranks = appendUniqueSorted(ranks, "quote_volume_rank", "btc_relative_strength_rank", "liquidity_percentile")

	payload := FeatureSchema{
		SchemaVersion: SchemaVersion,
		ProfileMode:   g5protocol.FeaturePolicyMode,
		Keys:          keys,
		MaskKeys:      masks,
		SkyOutputs:    sky,
		SorterOutputs: sorter,
		RankOutputs:   ranks,
	}
	hash, err := hashSchema(payload)
	if err != nil {
		return FeatureSchema{}, err
	}
	payload.FeatureHash = hash
	return payload, nil
}

// FeatureHash returns the frozen hash for the G5 feature schema.
func FeatureHash() (string, error) {
	s, err := FrozenFeatureSchema()
	if err != nil {
		return "", err
	}
	return s.FeatureHash, nil
}

func hashSchema(s FeatureSchema) (string, error) {
	payload := s
	payload.FeatureHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// IsForbiddenFeatureKey rejects outcome/reward/forward/post-decision inputs.
func IsForbiddenFeatureKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return true
	}
	for _, forbidden := range g5protocol.ForbiddenFeatureInputs {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	if strings.Contains(lower, "current_bar") || strings.Contains(lower, "current_open") {
		return true
	}
	return false
}

// ValidateFeatureMap ensures keys are allowed and no forbidden inputs leak in.
func ValidateFeatureMap(features map[string]float64) error {
	for key := range features {
		if IsForbiddenFeatureKey(key) {
			return fmt.Errorf("features: forbidden key %q", key)
		}
	}
	for _, key := range FeatureKeys {
		if IsForbiddenFeatureKey(key) {
			return fmt.Errorf("features: schema contains forbidden key %q", key)
		}
	}
	return nil
}

func appendUniqueSorted(base []string, extra ...string) []string {
	seen := map[string]bool{}
	for _, k := range base {
		seen[k] = true
	}
	for _, k := range extra {
		if !seen[k] {
			base = append(base, k)
			seen[k] = true
		}
	}
	sortStrings(base)
	return base
}

func sortStrings(in []string) {
	sort.Strings(in)
}
