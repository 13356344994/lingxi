package mechanism

import (
	"errors"
	"fmt"
	"sort"

	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
	"lingxi/internal/routerg4/validation"
)

// FeatureKeysFromConditions returns sorted unique feature keys from survivor conditions.
func FeatureKeysFromConditions(conditions []reversemine.Condition) []string {
	seen := map[string]bool{}
	var keys []string
	for _, cond := range conditions {
		if seen[cond.FeatureKey] {
			continue
		}
		seen[cond.FeatureKey] = true
		keys = append(keys, cond.FeatureKey)
	}
	sort.Strings(keys)
	return keys
}

// DecideAttribution is the automatic G4 path. Template membership can only
// nominate a hypothesis; it cannot establish an explained mechanism.
func DecideAttribution(features []string, claimedMechanism string) AttributionDecision {
	return DecideAttributionWithIndependentEvidence(features, claimedMechanism, false)
}

// DecideAttributionWithIndependentEvidence exposes the future/manual evidence
// fixture path. This generation never calls it with independentEvidence=true.
func DecideAttributionWithIndependentEvidence(
	features []string,
	claimedMechanism string,
	independentEvidence bool,
) AttributionDecision {
	status := protocol.ClassifyAttributionWithIndependentEvidence(
		features, claimedMechanism, independentEvidence,
	)
	decision := AttributionDecision{
		AttributionStatus: status,
		FeatureKeys:       append([]string(nil), features...),
		ClaimedMechanism:  claimedMechanism,
	}
	switch status {
	case protocol.AttributionExplained:
		template := matchedTemplate(features)
		if template == nil {
			decision.AttributionStatus = protocol.AttributionUnexplained
			decision.FinalStatus = FinalStatusUnexplainedResearchPass
			decision.Reason = "template_resolution_failed"
			return decision
		}
		decision.TemplateID = template.ID
		decision.Mechanism = template.Mechanism
		decision.Payer = template.Payer
		decision.ForcedBehavior = template.ForcedBehavior
		decision.CannotWait = true
		decision.CompetitionDefense = template.CapacityDefense
		decision.Deadline = template.Deadline
		decision.Invalidators = []string{template.Deadline}
		decision.FinalStatus = ""
		decision.Reason = oneMechanismFamilyResolution
	case protocol.AttributionMechanismHypothesis:
		template := matchedTemplate(features)
		if template == nil {
			decision.AttributionStatus = protocol.AttributionUnexplained
			decision.FinalStatus = protocol.UnexplainedResearchPassStatus
			decision.Reason = "template_resolution_failed"
			return decision
		}
		decision.TemplateID = template.ID
		decision.Mechanism = template.Mechanism
		decision.Payer = template.Payer
		decision.ForcedBehavior = template.ForcedBehavior
		decision.CannotWait = true
		decision.CompetitionDefense = template.CapacityDefense
		decision.Deadline = template.Deadline
		decision.Invalidators = []string{template.Deadline}
		decision.FinalStatus = protocol.HypothesisResearchResultStatus
		decision.Reason = "frozen_template_hypothesis_only"
	case protocol.AttributionUnexplained:
		decision.FinalStatus = protocol.UnexplainedResearchPassStatus
		decision.Reason = protocol.NarrativeRejectionRule
	case protocol.AttributionAmbiguous:
		decision.FinalStatus = FinalStatusClosed
		decision.Reason = protocol.CrossTemplateResolution
	case protocol.AttributionGraveyard:
		decision.FinalStatus = FinalStatusClosed
		decision.Reason = protocol.GraveyardPostValidationPolicy
	default:
		decision.FinalStatus = FinalStatusClosed
		decision.Reason = "unknown_attribution_status"
	}
	return decision
}

func matchedTemplate(features []string) *protocol.MechanismTemplate {
	matched := 0
	var chosen protocol.MechanismTemplate
	for _, template := range protocol.MechanismTemplates {
		if templateMatches(features, template) {
			matched++
			chosen = template
		}
	}
	if matched != 1 {
		return nil
	}
	return &chosen
}

func templateMatches(features []string, template protocol.MechanismTemplate) bool {
	for _, required := range template.RequiredAllFeatures {
		if !contains(features, required) {
			return false
		}
	}
	if len(template.RequiredAnyFeatures) == 0 {
		return true
	}
	for _, alternative := range template.RequiredAnyFeatures {
		if contains(features, alternative) {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// BuildAttributionDecisions classifies all validation survivors.
func BuildAttributionDecisions(
	survivors []validation.SurvivorEntry,
	candidates []reversemine.Candidate,
) ([]AttributionDecision, error) {
	byHash := map[string]reversemine.Candidate{}
	for _, c := range candidates {
		byHash[c.CanonicalHash] = c
	}
	decisions := make([]AttributionDecision, 0, len(survivors))
	for _, survivor := range survivors {
		candidate, ok := byHash[survivor.CanonicalHash]
		if !ok {
			return nil, fmt.Errorf("mechanism: survivor %s missing frozen candidate", survivor.CanonicalHash)
		}
		if survivor.Status != validation.SurvivorStatus {
			return nil, fmt.Errorf("mechanism: survivor %s status %q is not validation pass survivor", survivor.CanonicalHash, survivor.Status)
		}
		decision := DecideAttribution(FeatureKeysFromConditions(survivor.Conditions), "")
		decision.Ordinal = survivor.Ordinal
		decision.CanonicalHash = survivor.CanonicalHash
		decision.ProfileID = survivor.ProfileID
		decisions = append(decisions, decision)
		_ = candidate
	}
	return decisions, nil
}

// BuildSurvivorWorkItems joins survivors with attribution decisions and candidates.
func BuildSurvivorWorkItems(
	survivors []validation.SurvivorEntry,
	candidates []reversemine.Candidate,
	decisions []AttributionDecision,
) ([]SurvivorWorkItem, error) {
	if len(survivors) != len(decisions) {
		return nil, errors.New("mechanism: survivor/decision count mismatch")
	}
	byHash := map[string]int{}
	for i, c := range candidates {
		byHash[c.CanonicalHash] = i
	}
	items := make([]SurvivorWorkItem, len(survivors))
	for i, survivor := range survivors {
		idx, ok := byHash[survivor.CanonicalHash]
		if !ok {
			return nil, fmt.Errorf("mechanism: survivor %s missing candidate", survivor.CanonicalHash)
		}
		items[i] = SurvivorWorkItem{
			Survivor:       survivor,
			Candidate:      candidates[idx],
			Decision:       decisions[i],
			CandidateIndex: idx,
		}
	}
	return items, nil
}

const oneMechanismFamilyResolution = "exactly_one_frozen_template"
