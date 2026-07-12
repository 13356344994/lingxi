package candidates

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg3/protocol"
)

const (
	StatusAttributed    = "ATTRIBUTED"
	StatusNarrativeOnly = "NARRATIVE_ONLY"
	StatusAmbiguous     = "AMBIGUOUS"
	StatusRejected      = "REJECTED"
	CostModel           = "g3_discovery_cost_12bps"
)

type Definition struct {
	SkyState           string
	SorterDomain       string
	Mechanism          string
	RuleHash           string
	Direction          string
	Payer              string
	ForcedBehavior     string
	CannotWait         bool
	CompetitionDefense string
	DeadlineRule       string
	Invalidators       []string
	RequiredData       []string
	CostModel          string
	Generation         string
	ProtocolHash       string
	DiscoveryDataHash  string
	SearchManifestHash string
	RecheckDataHash    string
	Origin             registry.Origin
	SearchSpaceSize    int
	Rank               int
	MatchingRule       string
	ControlCount       int
}

type AttributionResult struct {
	Status      string
	Family      string
	Reason      string
	Attribution protocol.MechanismAttribution
}

type AttributionDecision struct {
	Status            string `json:"status"`
	Reason            string `json:"reason"`
	CanonicalRuleHash string `json:"canonical_rule_hash"`
	CandidateID       string `json:"candidate_id,omitempty"`
	MechanismFamily   string `json:"mechanism_family,omitempty"`
}

func CanonicalID(def Definition) string {
	return strings.Join([]string{
		clean(def.SkyState), clean(def.SorterDomain), clean(def.Mechanism),
		clean(def.RuleHash), clean(def.Direction),
	}, "/")
}

func ValidateCanonicalID(id string, def Definition) error {
	if id != CanonicalID(def) {
		return fmt.Errorf("routerg3: leaf id %q does not encode conditional path %q", id, CanonicalID(def))
	}
	return nil
}

func AttributeRule(rule g3reverse.Rule) (AttributionResult, error) {
	if err := g3reverse.ValidateRule(rule); err != nil {
		return AttributionResult{}, err
	}
	families := map[string]protocol.MechanismAttribution{}
	mechanismFeatures := 0
	for _, cond := range rule.Conditions {
		family, ok := mechanismFamilyForFeature(cond.FeatureKey)
		if !ok {
			continue
		}
		mechanismFeatures++
		families[family.Family] = family
	}
	switch {
	case mechanismFeatures == 0:
		return AttributionResult{
			Status: StatusNarrativeOnly,
			Reason: protocol.ZeroMechanismFeaturesResolution,
		}, nil
	case len(families) > 1:
		return AttributionResult{
			Status: StatusAmbiguous,
			Reason: protocol.DifferentMechanismFamiliesResolution,
		}, nil
	default:
		var attr protocol.MechanismAttribution
		for _, family := range families {
			attr = family
			break
		}
		return AttributionResult{
			Status: StatusAttributed, Family: attr.Family, Attribution: attr,
		}, nil
	}
}

func FromRecheckPassNomination(
	nomination g3reverse.Nomination,
	verdict g3reverse.RecheckVerdict,
	rule g3reverse.Rule,
	manifest g3reverse.SearchManifest,
) (Definition, error) {
	def, decision, err := DecideNomination(nomination, verdict, rule, manifest)
	if err != nil {
		return Definition{}, err
	}
	if decision.Status != StatusAttributed {
		return Definition{}, fmt.Errorf("routerg3: %s", decision.Reason)
	}
	return def, nil
}

// DecideNomination distinguishes frozen semantic closures from malformed or
// tampered provenance. Only NARRATIVE_ONLY, AMBIGUOUS, and graveyard REJECTED
// are normal closed decisions; structural/provenance failures are errors.
func DecideNomination(
	nomination g3reverse.Nomination,
	verdict g3reverse.RecheckVerdict,
	rule g3reverse.Rule,
	manifest g3reverse.SearchManifest,
) (Definition, AttributionDecision, error) {
	decision := AttributionDecision{CanonicalRuleHash: nomination.CanonicalRuleHash}
	if nomination.Kind != g3reverse.KindCandidateNomination {
		return Definition{}, decision, fmt.Errorf("routerg3: only %s nominations are accepted", g3reverse.KindCandidateNomination)
	}
	if verdict.Status != g3reverse.RecheckVerdictPass {
		return Definition{}, decision, fmt.Errorf("routerg3: nomination %s recheck status %q is not %s",
			nomination.CanonicalRuleHash, verdict.Status, g3reverse.RecheckVerdictPass)
	}
	if verdict.CanonicalRuleHash != nomination.CanonicalRuleHash {
		return Definition{}, decision, errors.New("routerg3: recheck verdict rule hash mismatch")
	}
	if nomination.Generation != protocol.Generation || nomination.ProtocolHash != protocol.RouterProtocolHash {
		return Definition{}, decision, errors.New("routerg3: nomination is not bound to frozen G3 generation/protocol")
	}
	if manifest.Generation != protocol.Generation || manifest.ProtocolHash != protocol.RouterProtocolHash {
		return Definition{}, decision, errors.New("routerg3: search manifest is not bound to frozen G3 generation/protocol")
	}
	if nomination.SearchManifestHash != manifest.ManifestHash {
		return Definition{}, decision, errors.New("routerg3: nomination search manifest hash mismatch")
	}
	if rule.CanonicalHash != nomination.CanonicalRuleHash {
		return Definition{}, decision, errors.New("routerg3: frozen rule hash mismatch")
	}
	computedRuleHash, err := g3reverse.CanonicalRuleHash(rule)
	if err != nil {
		return Definition{}, decision, err
	}
	if computedRuleHash != rule.CanonicalHash {
		return Definition{}, decision, errors.New("routerg3: frozen rule canonical hash is invalid")
	}
	if nomination.RuleOrdinal != rule.Ordinal ||
		nomination.SkyState != rule.SkyState ||
		nomination.SorterDomain != rule.SorterDomain ||
		nomination.Horizon != rule.Horizon ||
		nomination.Direction != string(rule.Direction) {
		return Definition{}, decision, errors.New("routerg3: nomination does not match frozen rule identity")
	}
	if nomination.ActualDerivedRuleCount != manifest.ActualDerivedRuleCount ||
		nomination.Rank <= 0 || nomination.Rank > nomination.ActualDerivedRuleCount {
		return Definition{}, decision, errors.New("routerg3: nomination search count/rank mismatch")
	}
	if verdict.SearchManifestHash != manifest.ManifestHash ||
		verdict.Generation != protocol.Generation ||
		verdict.ProtocolHash != protocol.RouterProtocolHash ||
		strings.TrimSpace(verdict.RecheckDataHash) == "" {
		return Definition{}, decision, errors.New("routerg3: recheck verdict provenance mismatch")
	}
	if nomination.ControlCount <= 0 || strings.TrimSpace(nomination.MatchingRule) == "" {
		return Definition{}, decision, errors.New("routerg3: nomination requires matched controls")
	}
	attr, err := AttributeRule(rule)
	if err != nil {
		return Definition{}, decision, err
	}
	switch attr.Status {
	case StatusNarrativeOnly:
		decision.Status, decision.Reason = StatusNarrativeOnly, attr.Reason
		return Definition{}, decision, nil
	case StatusAmbiguous:
		decision.Status, decision.Reason = StatusAmbiguous, attr.Reason
		return Definition{}, decision, nil
	}
	if err := rejectGraveyard(attr.Attribution.Mechanism); err != nil {
		decision.Status, decision.Reason = StatusRejected, err.Error()
		return Definition{}, decision, nil
	}
	for _, cond := range rule.Conditions {
		if err := rejectGraveyardFeature(cond.FeatureKey); err != nil {
			decision.Status, decision.Reason = StatusRejected, err.Error()
			return Definition{}, decision, nil
		}
	}
	def := Definition{
		SkyState: nomination.SkyState, SorterDomain: nomination.SorterDomain,
		Mechanism: attr.Attribution.Mechanism, RuleHash: rule.CanonicalHash,
		Direction: nomination.Direction,
		Payer:     attr.Attribution.Payer, ForcedBehavior: attr.Attribution.ForcedBehavior,
		CannotWait: true, CompetitionDefense: attr.Attribution.CompetitionDefense,
		DeadlineRule: attr.Attribution.DeadlineTemplate,
		Invalidators: append([]string(nil), attr.Attribution.InvalidatorTemplate...),
		RequiredData: requiredDataForFamily(attr.Family),
		CostModel:    CostModel, Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DiscoveryDataHash: verdict.RecheckDataHash, SearchManifestHash: manifest.ManifestHash,
		RecheckDataHash: verdict.RecheckDataHash,
		Origin:          registry.OriginBackward, SearchSpaceSize: nomination.ActualDerivedRuleCount,
		Rank: nomination.Rank, MatchingRule: nomination.MatchingRule, ControlCount: nomination.ControlCount,
	}
	decision.Status = StatusAttributed
	decision.Reason = protocol.OneMechanismFamilyResolution
	decision.CandidateID = CanonicalID(def)
	decision.MechanismFamily = attr.Family
	return def, decision, nil
}

func ToRegistry(def Definition) (registry.Candidate, error) {
	if err := validateSkyPattern(def.SkyState); err != nil {
		return registry.Candidate{}, err
	}
	if clean(def.SorterDomain) == "" ||
		clean(def.Mechanism) == "" || clean(def.RuleHash) == "" ||
		(def.Direction != "long" && def.Direction != "short") ||
		clean(def.Payer) == "" || clean(def.ForcedBehavior) == "" || !def.CannotWait ||
		clean(def.CompetitionDefense) == "" || clean(def.DeadlineRule) == "" ||
		len(def.Invalidators) == 0 || len(def.RequiredData) == 0 || clean(def.CostModel) == "" {
		return registry.Candidate{}, errors.New("routerg3: incomplete conditional candidate")
	}
	if err := rejectGraveyard(def.Mechanism); err != nil {
		return registry.Candidate{}, err
	}
	if def.Origin != registry.OriginBackward {
		return registry.Candidate{}, errors.New("routerg3: G3 reverse-induction candidates must be backward_mined")
	}
	if def.Generation != protocol.Generation || def.ProtocolHash != protocol.RouterProtocolHash {
		return registry.Candidate{}, errors.New("routerg3: candidate is not bound to frozen G3 generation/protocol")
	}
	if def.SearchSpaceSize <= 0 || def.Rank <= 0 || def.Rank > def.SearchSpaceSize {
		return registry.Candidate{}, errors.New("routerg3: search provenance is required")
	}
	if strings.TrimSpace(def.MatchingRule) == "" || def.ControlCount <= 0 {
		return registry.Candidate{}, errors.New("routerg3: backward candidate requires matched controls")
	}
	if strings.TrimSpace(def.DiscoveryDataHash) == "" || strings.TrimSpace(def.SearchManifestHash) == "" {
		return registry.Candidate{}, errors.New("routerg3: discovery/recheck evidence hashes are required")
	}
	return registry.Candidate{
		ID: CanonicalID(def), Origin: def.Origin, Generation: def.Generation,
		ProtocolHash: def.ProtocolHash, DiscoveryDataHash: def.DiscoveryDataHash,
		SearchSpaceSize: def.SearchSpaceSize, Rank: def.Rank,
		MatchingRule: def.MatchingRule, ControlCount: def.ControlCount,
		Payer: def.Payer, Mechanism: def.Mechanism, Direction: def.Direction,
		ForcedBehavior: def.ForcedBehavior, CannotWait: def.CannotWait,
		CompetitionDefense: def.CompetitionDefense, DeadlineRule: def.DeadlineRule,
		SupportDomain: []string{
			"sky_pattern=" + strings.TrimSpace(def.SkyState),
			"sorter=" + clean(def.SorterDomain),
			"rule_hash=" + clean(def.RuleHash),
		},
		Invalidators: def.Invalidators, CostModel: def.CostModel, RequiredData: def.RequiredData,
	}, nil
}

func validateSkyPattern(pattern string) error {
	parts := strings.Split(strings.TrimSpace(pattern), "/")
	axes := []string{"ret", "te", "rv", "dd", "fund"}
	if len(parts) != len(axes) {
		return errors.New("routerg3: sky pattern must contain exact ret/te/rv/dd/fund axes")
	}
	for i, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || kv[0] != axes[i] || kv[1] == "" {
			return errors.New("routerg3: sky pattern axes must use strict ret/te/rv/dd/fund order")
		}
		if axes[i] == "te" || axes[i] == "dd" {
			if kv[1] != "*" {
				return fmt.Errorf("routerg3: sky pattern axis %s must be wildcard", axes[i])
			}
			continue
		}
		if kv[1] == "*" || !axisLabelAllowed(kv[1]) {
			return fmt.Errorf("routerg3: sky pattern axis %s has invalid wildcard or label", axes[i])
		}
	}
	return nil
}

func axisLabelAllowed(label string) bool {
	for _, allowed := range protocol.CoarseSkyAxisLabels {
		if label == allowed {
			return true
		}
	}
	return false
}

func Register(r registry.Registry, def Definition, now time.Time) (registry.Registry, error) {
	candidate, err := ToRegistry(def)
	if err != nil {
		return r, err
	}
	return registry.Register(r, candidate, now)
}

func mechanismFamilyForFeature(featureKey string) (protocol.MechanismAttribution, bool) {
	for _, attr := range protocol.MechanismAttributions {
		for _, feature := range attr.Features {
			if feature == featureKey {
				return attr, true
			}
		}
	}
	return protocol.MechanismAttribution{}, false
}

func isConditionerFeature(featureKey string) bool {
	for _, feature := range protocol.ConditionerFeatures {
		if feature == featureKey {
			return true
		}
	}
	return false
}

func requiredDataForFamily(family string) []string {
	switch family {
	case "cross_venue_funding_pressure":
		return []string{"okx_1h", "okx_funding", "bybit_funding"}
	case "urgent_taker_impact":
		return []string{"okx_1h", "rubik_taker_1h"}
	case "crowded_leverage":
		return []string{"okx_1h", "rubik_lsratio_1h"}
	default:
		return []string{"okx_1h"}
	}
}

func rejectGraveyard(mechanism string) error {
	lower := strings.ToLower(strings.TrimSpace(mechanism))
	for _, blocked := range protocol.G1G2MechanismGraveyard {
		if lower == blocked || strings.Contains(lower, blocked) {
			return fmt.Errorf("routerg3: mechanism %q is in frozen g1/g2 semantic graveyard", mechanism)
		}
	}
	if strings.Contains(lower, "panic_liquidation") || strings.HasPrefix(lower, "panic_liquidation") {
		return fmt.Errorf("routerg3: mechanism %q is frozen UNTESTABLE/NO_GO", mechanism)
	}
	return nil
}

func rejectGraveyardFeature(featureKey string) error {
	lower := strings.ToLower(featureKey)
	for _, blocked := range protocol.G1G2MechanismGraveyard {
		if strings.Contains(lower, strings.ReplaceAll(blocked, "_", "")) {
			return fmt.Errorf("routerg3: feature %q implies graveyard mechanism", featureKey)
		}
	}
	return nil
}

func clean(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "_", "/", "_", "|", "_")
	return replacer.Replace(value)
}

// IsConditionerOnlyRule reports whether every condition feature is a conditioner.
func IsConditionerOnlyRule(rule g3reverse.Rule) bool {
	if len(rule.Conditions) == 0 {
		return false
	}
	for _, cond := range rule.Conditions {
		if _, ok := mechanismFamilyForFeature(cond.FeatureKey); ok {
			return false
		}
		if !isConditionerFeature(cond.FeatureKey) {
			return false
		}
	}
	return true
}
