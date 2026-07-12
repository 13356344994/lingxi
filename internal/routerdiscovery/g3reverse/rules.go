package g3reverse

import (
	"errors"
	"fmt"
	"strings"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

// RuleOp is the comparison operator applied to one pre-decision feature.
type RuleOp string

const (
	OpLTE RuleOp = "<="
	OpGTE RuleOp = ">="
)

// RuleCondition is one feature threshold predicate inside a Rule.
type RuleCondition struct {
	FeatureKey string  `json:"feature_key"`
	Op         RuleOp  `json:"op"`
	Threshold  float64 `json:"threshold"`
	Quantile   float64 `json:"quantile"`
}

// Rule is one derived single- or dual-condition reverse-induction rule.
// SkyState is the frozen coarse pattern ret=X/te=*/rv=Y/dd=*/fund=Z.
type Rule struct {
	Ordinal       int                 `json:"ordinal"`
	SkyState      string              `json:"sky_state"`
	SorterDomain  string              `json:"sorter_domain"`
	Horizon       Horizon             `json:"horizon"`
	Direction     mechanism.Direction `json:"direction"`
	Conditions    []RuleCondition     `json:"conditions"`
	CanonicalHash string              `json:"canonical_hash"`
}

// Fires reports whether the rule matches on this event. The full group must
// match; a missing or non-finite required feature never fires.
func (r Rule) Fires(e Event) (fires bool, hasFeature bool) {
	if !matchesCoarseSkyPattern(e.SkyState, r.SkyState) || e.SorterDomain != r.SorterDomain ||
		e.Horizon != r.Horizon || e.Direction != r.Direction {
		return false, false
	}
	if len(r.Conditions) == 0 {
		return false, false
	}
	for _, cond := range r.Conditions {
		v, ok := e.Features[cond.FeatureKey]
		if !ok || !isFinite(v) {
			return false, false
		}
		switch cond.Op {
		case OpLTE:
			if v > cond.Threshold {
				return false, true
			}
		case OpGTE:
			if v < cond.Threshold {
				return false, true
			}
		default:
			return false, true
		}
	}
	return true, true
}

// ValidateRule rejects forbidden / excluded / post-decision feature inputs.
func ValidateRule(r Rule) error {
	if r.SkyState == "" || r.SorterDomain == "" || !r.Horizon.Valid() || !r.Direction.IsDirectional() {
		return errors.New("g3reverse: rule group identity is incomplete")
	}
	if err := validateCoarseSkyPattern(r.SkyState); err != nil {
		return err
	}
	if !stringIn(r.SorterDomain, protocol.G3SorterDomains) {
		return fmt.Errorf("g3reverse: sorter domain %q is not allowed", r.SorterDomain)
	}
	n := len(r.Conditions)
	if n < protocol.MinFeaturesPerRule || n > protocol.MaxFeaturesPerRule {
		return fmt.Errorf("g3reverse: rule must have %d..%d conditions", protocol.MinFeaturesPerRule, protocol.MaxFeaturesPerRule)
	}
	seen := map[string]bool{}
	for _, cond := range r.Conditions {
		if err := validateFeatureKey(cond.FeatureKey); err != nil {
			return err
		}
		if seen[cond.FeatureKey] {
			return fmt.Errorf("g3reverse: duplicate feature in rule: %s", cond.FeatureKey)
		}
		seen[cond.FeatureKey] = true
		if cond.Op != OpLTE && cond.Op != OpGTE {
			return fmt.Errorf("g3reverse: invalid rule operator %q", cond.Op)
		}
		if !isFinite(cond.Threshold) {
			return errors.New("g3reverse: rule threshold must be finite")
		}
	}
	return nil
}

var skyAxisOrder = []string{"ret", "te", "rv", "dd", "fund"}

func parseSkyState(state string) (map[string]string, error) {
	parts := strings.Split(state, "/")
	if len(parts) != len(skyAxisOrder) {
		return nil, errors.New("g3reverse: sky state must contain exactly ret/te/rv/dd/fund")
	}
	values := make(map[string]string, len(parts))
	for i, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || kv[0] != skyAxisOrder[i] || kv[1] == "" {
			return nil, errors.New("g3reverse: sky state axes must use strict ret/te/rv/dd/fund order")
		}
		values[kv[0]] = kv[1]
	}
	return values, nil
}

func validateCoarseSkyPattern(pattern string) error {
	values, err := parseSkyState(pattern)
	if err != nil {
		return err
	}
	for _, axis := range skyAxisOrder {
		value := values[axis]
		wildcardAllowed := stringIn(axis, protocol.CoarseSkyWildcardAxes)
		if wildcardAllowed {
			if value != "*" {
				return fmt.Errorf("g3reverse: coarse sky axis %s must be wildcard", axis)
			}
			continue
		}
		if value == "*" || !stringIn(value, protocol.CoarseSkyAxisLabels) {
			return fmt.Errorf("g3reverse: coarse sky axis %s has invalid value %q", axis, value)
		}
	}
	return nil
}

func coarseSkyPattern(full string) (string, error) {
	values, err := parseSkyState(full)
	if err != nil {
		return "", err
	}
	for _, axis := range skyAxisOrder {
		if !stringIn(values[axis], protocol.CoarseSkyAxisLabels) {
			return "", fmt.Errorf("g3reverse: full sky axis %s has invalid value %q", axis, values[axis])
		}
	}
	return fmt.Sprintf("ret=%s/te=*/rv=%s/dd=*/fund=%s",
		values["ret"], values["rv"], values["fund"]), nil
}

func matchesCoarseSkyPattern(full, pattern string) bool {
	if err := validateCoarseSkyPattern(pattern); err != nil {
		return false
	}
	fullValues, err := parseSkyState(full)
	if err != nil {
		return false
	}
	for _, axis := range skyAxisOrder {
		if !stringIn(fullValues[axis], protocol.CoarseSkyAxisLabels) {
			return false
		}
	}
	patternValues, _ := parseSkyState(pattern)
	for _, axis := range skyAxisOrder {
		expected := patternValues[axis]
		if expected != "*" && fullValues[axis] != expected {
			return false
		}
	}
	return true
}

func stringIn(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateFeatureKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("g3reverse: empty feature key")
	}
	lower := strings.ToLower(key)
	if strings.Contains(lower, "rubik_taker_5m") || strings.Contains(lower, "rubik5m") {
		return fmt.Errorf("g3reverse: forbidden feature family rubik_taker_5m: %s", key)
	}
	if strings.Contains(lower, "post_decision") || strings.Contains(lower, "postdecision") {
		return fmt.Errorf("g3reverse: post-decision feature forbidden: %s", key)
	}
	for _, forbidden := range protocol.ForbiddenFeatureInputs {
		if lower == strings.ToLower(forbidden) || strings.Contains(lower, strings.ToLower(forbidden)) {
			return fmt.Errorf("g3reverse: forbidden feature input %q", key)
		}
	}
	allowed := false
	for _, name := range protocol.FeatureCatalog {
		if key == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("g3reverse: feature %q not in FeatureCatalog", key)
	}
	return nil
}

// CanonicalRuleHash is the stable identity used for dedupe (keep first).
func CanonicalRuleHash(r Rule) (string, error) {
	payload := struct {
		SkyState     string              `json:"sky_state"`
		SorterDomain string              `json:"sorter_domain"`
		Horizon      Horizon             `json:"horizon"`
		Direction    mechanism.Direction `json:"direction"`
		Conditions   []struct {
			FeatureKey string  `json:"feature_key"`
			Op         RuleOp  `json:"op"`
			Threshold  float64 `json:"threshold"`
		} `json:"conditions"`
	}{
		SkyState: r.SkyState, SorterDomain: r.SorterDomain,
		Horizon: r.Horizon, Direction: r.Direction,
	}
	for _, cond := range r.Conditions {
		payload.Conditions = append(payload.Conditions, struct {
			FeatureKey string  `json:"feature_key"`
			Op         RuleOp  `json:"op"`
			Threshold  float64 `json:"threshold"`
		}{FeatureKey: cond.FeatureKey, Op: cond.Op, Threshold: cond.Threshold})
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashRules commits the ordered registered rule list (not padded).
func HashRules(rules []Rule) (string, error) {
	hash, _, err := datatruth.HashJSON(rules)
	return hash, err
}
