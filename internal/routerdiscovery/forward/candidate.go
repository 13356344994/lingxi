package forward

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/mechanism"
	"lingxi/internal/researchboundary"
)

var (
	ErrFrozenUntestableLeaf    = errors.New("forward: frozen untestable leaf")
	ErrNoPayer                 = errors.New("forward: missing payer")
	ErrNoForcedBehavior        = errors.New("forward: missing forced behavior")
	ErrNoMechanism             = errors.New("forward: missing mechanism")
	ErrCannotWaitRequired      = errors.New("forward: cannot_wait must be true")
	ErrNoCompetitionDefense    = errors.New("forward: missing competition defense")
	ErrNoDeadline              = errors.New("forward: missing deadline")
	ErrNoObservableInvalidator = errors.New("forward: missing observable invalidator")
	ErrNoDecisionTimeFields    = errors.New("forward: missing decision-time fields")
	ErrNonDecisionTimeField    = errors.New("forward: non decision-time field")
	ErrNoHistoryFamilies       = errors.New("forward: missing history families")
	ErrUnknownHistoryFamily    = errors.New("forward: unknown history family")
	ErrNoDirection             = errors.New("forward: missing direction")
	ErrNoSupportDomain         = errors.New("forward: missing support domain")
	ErrUntestableRevival       = errors.New("forward: untestable candidate cannot revive")
	ErrEmptyPolicy             = errors.New("forward: validation policy is empty")
)

// FrozenUntestableForcedLiquidationLeaves keeps the four forced-liquidation leaves
// permanently closed once they were marked UNTESTABLE.
var FrozenUntestableForcedLiquidationLeaves = map[string]struct{}{
	"panic_liquidation.relay.long_squeezed.short":       {},
	"panic_liquidation.relay.short_squeezed.long":       {},
	"panic_liquidation.exhaustion.long_squeezed.long":   {},
	"panic_liquidation.exhaustion.short_squeezed.short": {},
}

// Invalidator models a runtime-falsifiable condition.
type Invalidator struct {
	Metric     string `json:"metric"`
	Condition  string `json:"condition"`
	Observable bool   `json:"observable"`
}

// CandidateDefinition is a registry-independent forward candidate contract.
type CandidateDefinition struct {
	ID                     string                       `json:"id"`
	Payer                  mechanism.PayerRole          `json:"payer"`
	Mechanism              string                       `json:"mechanism"`
	ForcedBehavior         string                       `json:"forced_behavior"`
	CannotWait             bool                         `json:"cannot_wait"`
	CompetitionDefense     string                       `json:"competition_defense"`
	DeadlineUnixMs         int64                        `json:"deadline_unix_ms"`
	ObservableInvalidators []Invalidator                `json:"observable_invalidators"`
	DecisionTimeFields     []string                     `json:"decision_time_fields"`
	HistoryFamilies        []string                     `json:"history_families"`
	Direction              mechanism.Direction          `json:"direction"`
	SupportDomain          []string                     `json:"support_domain"`
	HistoricalTestability  researchboundary.Testability `json:"historical_testability,omitempty"`
	Narrative              string                       `json:"narrative,omitempty"`
}

// ValidationPolicy declares what the runtime already allows at decision-time.
type ValidationPolicy struct {
	DecisionTimeFieldSet map[string]struct{}
	HistoryFamilySet     map[string]struct{}
}

// CandidateDTO is a generic integration DTO for downstream wiring.
type CandidateDTO struct {
	ID                     string   `json:"id"`
	Payer                  string   `json:"payer"`
	Mechanism              string   `json:"mechanism"`
	ForcedBehavior         string   `json:"forced_behavior"`
	CannotWait             bool     `json:"cannot_wait"`
	CompetitionDefense     string   `json:"competition_defense"`
	DeadlineUnixMs         int64    `json:"deadline_unix_ms"`
	ObservableInvalidators []string `json:"observable_invalidators"`
	DecisionTimeFields     []string `json:"decision_time_fields"`
	HistoryFamilies        []string `json:"history_families"`
	Direction              string   `json:"direction"`
	SupportDomain          []string `json:"support_domain"`
	HistoricalTestability  string   `json:"historical_testability,omitempty"`
}

// Contract keeps forward candidates decoupled from any registry implementation.
type Contract interface {
	Validate(ValidationPolicy) error
	ToDTO() CandidateDTO
}

func (c CandidateDefinition) Validate(policy ValidationPolicy) error {
	id := strings.TrimSpace(c.ID)
	if _, blocked := FrozenUntestableForcedLiquidationLeaves[id]; blocked ||
		strings.HasPrefix(id, "panic_liquidation.") ||
		strings.EqualFold(strings.TrimSpace(c.Mechanism), "panic_liquidation") {
		return ErrFrozenUntestableLeaf
	}
	if c.HistoricalTestability == researchboundary.Untestable {
		return ErrUntestableRevival
	}
	if len(policy.DecisionTimeFieldSet) == 0 || len(policy.HistoryFamilySet) == 0 {
		return ErrEmptyPolicy
	}
	if c.Payer == mechanism.PayerUnknown {
		return ErrNoPayer
	}
	if strings.TrimSpace(c.Mechanism) == "" {
		return ErrNoMechanism
	}
	if strings.TrimSpace(c.ForcedBehavior) == "" {
		return ErrNoForcedBehavior
	}
	if !c.CannotWait {
		return ErrCannotWaitRequired
	}
	if strings.TrimSpace(c.CompetitionDefense) == "" {
		return ErrNoCompetitionDefense
	}
	if c.DeadlineUnixMs <= 0 {
		return ErrNoDeadline
	}
	if !hasObservableInvalidator(c.ObservableInvalidators) {
		return ErrNoObservableInvalidator
	}
	fields := normalizeStrings(c.DecisionTimeFields)
	if len(fields) == 0 {
		return ErrNoDecisionTimeFields
	}
	for _, field := range fields {
		if _, ok := policy.DecisionTimeFieldSet[field]; !ok {
			return fmt.Errorf("%w: %s", ErrNonDecisionTimeField, field)
		}
	}
	families := normalizeStrings(c.HistoryFamilies)
	if len(families) == 0 {
		return ErrNoHistoryFamilies
	}
	for _, family := range families {
		if _, ok := policy.HistoryFamilySet[family]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownHistoryFamily, family)
		}
	}
	if !c.Direction.IsDirectional() {
		return ErrNoDirection
	}
	if len(normalizeStrings(c.SupportDomain)) == 0 {
		return ErrNoSupportDomain
	}
	return nil
}

func (c CandidateDefinition) ToDTO() CandidateDTO {
	return CandidateDTO{
		ID:                     strings.TrimSpace(c.ID),
		Payer:                  string(c.Payer),
		Mechanism:              strings.TrimSpace(c.Mechanism),
		ForcedBehavior:         strings.TrimSpace(c.ForcedBehavior),
		CannotWait:             c.CannotWait,
		CompetitionDefense:     strings.TrimSpace(c.CompetitionDefense),
		DeadlineUnixMs:         c.DeadlineUnixMs,
		ObservableInvalidators: normalizeStrings(invalidatorNames(c.ObservableInvalidators)),
		DecisionTimeFields:     normalizeStrings(c.DecisionTimeFields),
		HistoryFamilies:        normalizeStrings(c.HistoryFamilies),
		Direction:              string(c.Direction),
		SupportDomain:          normalizeStrings(c.SupportDomain),
		HistoricalTestability:  string(c.HistoricalTestability),
	}
}

func NewValidationPolicy(decisionTimeFields, historyFamilies []string) ValidationPolicy {
	return ValidationPolicy{
		DecisionTimeFieldSet: toSet(decisionTimeFields),
		HistoryFamilySet:     toSet(historyFamilies),
	}
}

func hasObservableInvalidator(invalidators []Invalidator) bool {
	for _, inv := range invalidators {
		if !inv.Observable {
			continue
		}
		if strings.TrimSpace(inv.Metric) == "" || strings.TrimSpace(inv.Condition) == "" {
			continue
		}
		return true
	}
	return false
}

func invalidatorNames(invalidators []Invalidator) []string {
	out := make([]string, 0, len(invalidators))
	for _, inv := range invalidators {
		metric := strings.TrimSpace(inv.Metric)
		if metric != "" {
			out = append(out, metric)
		}
	}
	return out
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func normalizeStrings(values []string) []string {
	set := toSet(values)
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
