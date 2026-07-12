package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type Origin string

const (
	OriginForward  Origin = "forward_mechanism"
	OriginBackward Origin = "backward_mined"
)

type State string

const (
	StateNominated    State = "NOMINATED"
	StateFrozen       State = "FROZEN"
	StateResearchPass State = "RESEARCH_PASS"
	StateClosed       State = "CLOSED"
	StateUntestable   State = "UNTESTABLE"
)

type Candidate struct {
	ID                 string    `json:"id"`
	Fingerprint        string    `json:"fingerprint"`
	Origin             Origin    `json:"origin"`
	Generation         string    `json:"generation"`
	ProtocolHash       string    `json:"protocol_hash"`
	DiscoveryDataHash  string    `json:"discovery_data_hash"`
	SearchSpaceSize    int       `json:"search_space_size"`
	Rank               int       `json:"rank"`
	MatchingRule       string    `json:"matching_rule,omitempty"`
	ControlCount       int       `json:"control_count,omitempty"`
	Payer              string    `json:"payer"`
	Mechanism          string    `json:"mechanism"`
	ForcedBehavior     string    `json:"forced_behavior"`
	CannotWait         bool      `json:"cannot_wait"`
	CompetitionDefense string    `json:"competition_defense"`
	DeadlineRule       string    `json:"deadline_rule"`
	Direction          string    `json:"direction"`
	SupportDomain      []string  `json:"support_domain"`
	Invalidators       []string  `json:"invalidators"`
	CostModel          string    `json:"cost_model"`
	RequiredData       []string  `json:"required_data"`
	State              State     `json:"state"`
	EvidenceHash       string    `json:"evidence_hash,omitempty"`
	CoarseReason       string    `json:"coarse_reason,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Registry struct {
	SchemaVersion string      `json:"schema_version"`
	Candidates    []Candidate `json:"candidates"`
	Hash          string      `json:"hash"`
}

var ErrImmutable = errors.New("router candidate is immutable")

var blockedUntestable = map[string]bool{
	"panic_liquidation.relay.long_squeezed.short":       true,
	"panic_liquidation.relay.short_squeezed.long":       true,
	"panic_liquidation.exhaustion.long_squeezed.long":   true,
	"panic_liquidation.exhaustion.short_squeezed.short": true,
}

func Register(r Registry, candidate Candidate, now time.Time) (Registry, error) {
	if blockedUntestable[candidate.ID] || strings.HasPrefix(strings.TrimSpace(candidate.ID), "panic_liquidation.") ||
		strings.EqualFold(strings.TrimSpace(candidate.Mechanism), "panic_liquidation") {
		return r, fmt.Errorf("candidate %q is frozen UNTESTABLE/NO_GO", candidate.ID)
	}
	if err := validateCandidate(candidate); err != nil {
		return r, err
	}
	fingerprint, err := semanticFingerprint(candidate)
	if err != nil {
		return r, err
	}
	for _, existing := range r.Candidates {
		if existing.ID == candidate.ID {
			return r, fmt.Errorf("%w: duplicate candidate id %q", ErrImmutable, candidate.ID)
		}
		if existing.Fingerprint == fingerprint {
			return r, fmt.Errorf("%w: semantic duplicate of %q", ErrImmutable, existing.ID)
		}
	}
	if now.IsZero() {
		now = time.Now()
	}
	candidate.Fingerprint = fingerprint
	candidate.State = StateNominated
	candidate.CreatedAt = now.UTC()
	candidate.UpdatedAt = now.UTC()
	r.SchemaVersion = "lingxi.router-candidates.v1"
	r.Candidates = append(append([]Candidate(nil), r.Candidates...), normalizeCandidate(candidate))
	return finalize(r)
}

func Freeze(r Registry, id string, now time.Time) (Registry, error) {
	return transition(r, id, StateNominated, StateFrozen, "", "", now)
}

// RecordForwardFalsification is the only route to RESEARCH_PASS. Backward
// nominations cannot skip this transition.
func RecordForwardFalsification(r Registry, id string, pass bool, evidenceHash, coarseReason string, now time.Time) (Registry, error) {
	if strings.TrimSpace(evidenceHash) == "" {
		return r, errors.New("forward falsification requires evidence hash")
	}
	next := StateClosed
	if pass {
		next = StateResearchPass
		if coarseReason != "PASS" {
			return r, errors.New("passing forward falsification requires coarse reason PASS")
		}
	} else if coarseReason == "" || coarseReason == "PASS" {
		return r, errors.New("failing forward falsification requires non-PASS coarse reason")
	}
	return transition(r, id, StateFrozen, next, evidenceHash, coarseReason, now)
}

func MarkUntestable(r Registry, id, reason string, now time.Time) (Registry, error) {
	return transition(r, id, StateNominated, StateUntestable, "", reason, now)
}

func transition(r Registry, id string, from, to State, evidence, reason string, now time.Time) (Registry, error) {
	out := append([]Candidate(nil), r.Candidates...)
	found := false
	for i := range out {
		if out[i].ID != id {
			continue
		}
		found = true
		if out[i].State != from {
			return r, fmt.Errorf("%w: candidate %q state %s, want %s", ErrImmutable, id, out[i].State, from)
		}
		out[i].State = to
		out[i].EvidenceHash = strings.TrimSpace(evidence)
		out[i].CoarseReason = strings.TrimSpace(reason)
		if now.IsZero() {
			now = time.Now()
		}
		out[i].UpdatedAt = now.UTC()
	}
	if !found {
		return r, fmt.Errorf("candidate %q not found", id)
	}
	r.Candidates = out
	return finalize(r)
}

func validateCandidate(c Candidate) error {
	for name, value := range map[string]string{
		"id": c.ID, "generation": c.Generation, "protocol_hash": c.ProtocolHash,
		"discovery_data_hash": c.DiscoveryDataHash, "payer": c.Payer,
		"mechanism": c.Mechanism, "direction": c.Direction, "cost_model": c.CostModel,
		"forced_behavior": c.ForcedBehavior, "competition_defense": c.CompetitionDefense,
		"deadline_rule": c.DeadlineRule,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("candidate %s is required", name)
		}
	}
	if c.Origin != OriginForward && c.Origin != OriginBackward {
		return errors.New("candidate origin must be forward_mechanism or backward_mined")
	}
	if !c.CannotWait {
		return errors.New("candidate cannot_wait must be true")
	}
	if c.SearchSpaceSize <= 0 || c.Rank <= 0 || c.Rank > c.SearchSpaceSize {
		return errors.New("candidate search space and rank must be registered")
	}
	if len(c.SupportDomain) == 0 || len(c.Invalidators) == 0 || len(c.RequiredData) == 0 {
		return errors.New("candidate support, invalidators, and required data are mandatory")
	}
	if c.Origin == OriginBackward && (strings.TrimSpace(c.MatchingRule) == "" || c.ControlCount <= 0) {
		return errors.New("backward candidate requires matched controls and matching rule")
	}
	return nil
}

func semanticFingerprint(c Candidate) (string, error) {
	payload := struct {
		Origin             Origin   `json:"origin"`
		Payer              string   `json:"payer"`
		Mechanism          string   `json:"mechanism"`
		Direction          string   `json:"direction"`
		SupportDomain      []string `json:"support_domain"`
		Invalidators       []string `json:"invalidators"`
		CostModel          string   `json:"cost_model"`
		RequiredData       []string `json:"required_data"`
		MatchingRule       string   `json:"matching_rule,omitempty"`
		ForcedBehavior     string   `json:"forced_behavior"`
		CannotWait         bool     `json:"cannot_wait"`
		CompetitionDefense string   `json:"competition_defense"`
		DeadlineRule       string   `json:"deadline_rule"`
	}{
		Origin: c.Origin, Payer: strings.TrimSpace(c.Payer), Mechanism: strings.TrimSpace(c.Mechanism),
		Direction: strings.TrimSpace(c.Direction), SupportDomain: sorted(c.SupportDomain),
		Invalidators: sorted(c.Invalidators), CostModel: strings.TrimSpace(c.CostModel),
		RequiredData: sorted(c.RequiredData), MatchingRule: strings.TrimSpace(c.MatchingRule),
		ForcedBehavior: strings.TrimSpace(c.ForcedBehavior), CannotWait: c.CannotWait,
		CompetitionDefense: strings.TrimSpace(c.CompetitionDefense), DeadlineRule: strings.TrimSpace(c.DeadlineRule),
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func normalizeCandidate(c Candidate) Candidate {
	c.SupportDomain = sorted(c.SupportDomain)
	c.Invalidators = sorted(c.Invalidators)
	c.RequiredData = sorted(c.RequiredData)
	return c
}

func finalize(r Registry) (Registry, error) {
	sort.Slice(r.Candidates, func(i, j int) bool { return r.Candidates[i].ID < r.Candidates[j].ID })
	copyForHash := r
	copyForHash.Hash = ""
	hash, _, err := datatruth.HashJSON(copyForHash)
	if err != nil {
		return Registry{}, err
	}
	r.Hash = hash
	return r, nil
}

func sorted(values []string) []string {
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
