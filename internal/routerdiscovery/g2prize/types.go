// Package g2prize implements the G2 research-only historical prize/bonus
// map: a backward-mining lab stratified by SkyState + SorterDomain + symbol
// + liquidity bucket + direction, evaluated independently at the 1h/4h/24h
// horizons. It is the g2 counterpart of routerdiscovery/backward and
// intentionally does not import or modify that g1 package: g1 stays frozen
// forever, g2 gets its own generation, protocol hash and search-space
// definition (per the G2 plan's hard boundary).
//
// The failure mode this package exists to structurally prevent is the same
// one g1's backward lab prevents: given enough slicing, any history looks
// profitable in hindsight somewhere. Every combination in the preregistered
// search space is charged against a shared multiple-testing budget whether
// or not it nominates anything; every nomination must carry matched
// loss/neutral controls drawn from the *same* SkyState+SorterDomain+symbol+
// liquidity-bucket+direction+horizon+cluster slice (winners alone are
// refused, and a control can never be borrowed from a different regime or
// domain); every candidate must resolve to one coherent payer mechanism;
// and the only output kind is CANDIDATE_NOMINATION — there is no
// RESEARCH_PASS anywhere in this package. Promotion to RESEARCH_PASS can
// only happen later, through independent forward falsification elsewhere,
// which this package does not implement or bypass.
package g2prize

import (
	"fmt"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
)

// OriginBackwardMined mirrors routerdiscovery/registry.OriginBackward's
// string value so a g2 nomination can be wired into the shared registry by
// callers without this package importing the registry package itself.
const OriginBackwardMined = "backward_mined"

// KindCandidateNomination is the only "status" a Nomination can ever carry.
// There is deliberately no RESEARCH_PASS, PASS, or APPROVED value anywhere
// in this package.
const KindCandidateNomination = "CANDIDATE_NOMINATION"

// minIndependentClusters is the minimum number of distinct clusters required
// before a cluster-block-bootstrap confidence interval may be reported as
// valid. A single cluster cannot demonstrate an effect is robust across
// independent shocks/regimes, so fewer than this must fail closed.
const minIndependentClusters = 2

// defaultSeed keeps bootstrap/permutation draws reproducible across runs
// when the caller does not pin one explicitly. It is distinct from g1's
// backward.defaultSeed by design: g2 draws are never silently identical to
// frozen g1 draws.
const defaultSeed = int64(20260712)

// Horizon is one of the three preregistered outcome windows this map is
// allowed to evaluate. There is no "custom" horizon: widening this set
// requires a new sealed search space, not a caller-supplied value.
type Horizon string

const (
	Horizon1h  Horizon = "1h"
	Horizon4h  Horizon = "4h"
	Horizon24h Horizon = "24h"
)

// Valid reports whether h is one of the three frozen horizons.
func (h Horizon) Valid() bool {
	switch h {
	case Horizon1h, Horizon4h, Horizon24h:
		return true
	default:
		return false
	}
}

// Hours returns the horizon's length in hours, or 0 for an invalid horizon.
func (h Horizon) Hours() int {
	switch h {
	case Horizon1h:
		return 1
	case Horizon4h:
		return 4
	case Horizon24h:
		return 24
	default:
		return 0
	}
}

// OutcomeClass classifies an event's net-after-cost outcome relative to the
// configured neutral band.
type OutcomeClass string

const (
	OutcomeWinner  OutcomeClass = "winner"
	OutcomeLoss    OutcomeClass = "loss"
	OutcomeNeutral OutcomeClass = "neutral"
)

func classify(net, band float64) OutcomeClass {
	switch {
	case net > band:
		return OutcomeWinner
	case net < -band:
		return OutcomeLoss
	default:
		return OutcomeNeutral
	}
}

// CombinationOp is the comparison operator a search-space combination
// applies to one pre-decision feature.
type CombinationOp string

const (
	OpGTE CombinationOp = ">="
	OpLTE CombinationOp = "<="
)

// Event is one already-completed, pre-decision-time-bounded research
// observation, already stratified by the SkyState/sorter layers that sit
// upstream of this package. DecisionTime is when a live decision would have
// had to be made; FeatureAsOf is when the Features snapshot was actually
// available and must never be after DecisionTime.
//
// SkyState and SorterDomain are opaque, upstream-assigned regime/domain
// labels: this package never computes or reclassifies them, it only uses
// them (together with Symbol, LiquidityBucket, Direction and Horizon) as
// the exact stratification key that treatment/control matching may never
// cross. Cluster is the independent shock/episode identity used for
// block-bootstrap resampling; it is deliberately a separate axis from
// Symbol because a single shock cluster can span several symbols.
//
// NetOutcomeBPS must already be fully net of realistic cost — this package
// does not deduct cost itself, it only classifies and matches on whatever
// net figure the caller supplies.
type Event struct {
	ID              string              `json:"id"`
	Symbol          string              `json:"symbol"`
	Cluster         string              `json:"cluster"`
	SkyState        string              `json:"sky_state"`
	SorterDomain    string              `json:"sorter_domain"`
	LiquidityBucket string              `json:"liquidity_bucket"`
	Horizon         Horizon             `json:"horizon"`
	DecisionTime    time.Time           `json:"decision_time"`
	FeatureAsOf     time.Time           `json:"feature_as_of"`
	Payer           mechanism.PayerRole `json:"payer"`
	Direction       mechanism.Direction `json:"direction"`
	Features        map[string]float64  `json:"features"`
	NetOutcomeBPS   float64             `json:"net_outcome_bps"`
}

// Combination is one preregistered search-space entry. It fixes the leaf
// identity dimensions the plan requires every candidate to encode —
// SkyState, SorterDomain, Mechanism and Direction, plus the Horizon this
// entry evaluates — and a single pre-decision feature threshold rule.
// MatchingRule is mandatory: a combination with no declared matching rule
// can never nominate a candidate. Symbol and liquidity bucket are
// deliberately *not* part of a combination's identity: they remain a
// matching-only stratification axis so one combination's map can span every
// symbol/liquidity bucket inside its SkyState+SorterDomain slice while still
// only ever comparing like-for-like pairs.
type Combination struct {
	Key          string              `json:"key"`
	SkyState     string              `json:"sky_state"`
	SorterDomain string              `json:"sorter_domain"`
	Horizon      Horizon             `json:"horizon"`
	FeatureKey   string              `json:"feature_key"`
	Op           CombinationOp       `json:"op"`
	Threshold    float64             `json:"threshold"`
	MatchingRule string              `json:"matching_rule"`
	Mechanism    string              `json:"mechanism"`
	Direction    mechanism.Direction `json:"direction"`
}

// Fires reports whether the combination's rule matches on this event, and
// whether the event carried the required feature at all (an event missing
// the feature never fires, and is not silently treated as zero). An event
// outside the combination's SkyState/SorterDomain/Horizon slice never
// fires, regardless of its feature value.
func (c Combination) Fires(e Event) (fires bool, hasFeature bool) {
	if !matchesSkyPattern(e.SkyState, c.SkyState) || e.SorterDomain != c.SorterDomain || e.Horizon != c.Horizon {
		return false, false
	}
	v, ok := e.Features[c.FeatureKey]
	if !ok || !isFinite(v) {
		return false, false
	}
	switch c.Op {
	case OpGTE:
		return v >= c.Threshold, true
	case OpLTE:
		return v <= c.Threshold, true
	default:
		return false, true
	}
}

func matchesSkyPattern(state, pattern string) bool {
	if !strings.Contains(pattern, "=") {
		return state == pattern
	}
	stateParts := strings.Split(state, "/")
	patternParts := strings.Split(pattern, "/")
	if len(stateParts) != len(patternParts) {
		return false
	}
	for i := range stateParts {
		stateKV := strings.SplitN(stateParts[i], "=", 2)
		patternKV := strings.SplitN(patternParts[i], "=", 2)
		if len(stateKV) != 2 || len(patternKV) != 2 || stateKV[0] != patternKV[0] {
			return false
		}
		if patternKV[1] != "*" && patternKV[1] != stateKV[1] {
			return false
		}
	}
	return true
}

// SearchSpace is the full, preregistered set of combinations this run is
// permitted to evaluate. Every combination in it consumes multiple-testing
// budget when Run executes, whether or not it survives to a nomination.
type SearchSpace struct {
	ID             string        `json:"id"`
	Generation     string        `json:"generation"`
	ProtocolHash   string        `json:"protocol_hash"`
	SourceDataHash string        `json:"source_data_hash"`
	Combinations   []Combination `json:"combinations"`
	DefinitionHash string        `json:"definition_hash"`
	ManifestHash   string        `json:"manifest_hash"`
}

// SealSearchSpace stamps space with its definition and manifest hashes. It
// must be called exactly once, before a search space is frozen and
// published; Validate re-derives both hashes and refuses any space whose
// stamped hashes do not match its actual content.
func SealSearchSpace(space SearchSpace) (SearchSpace, error) {
	definitionHash, err := searchDefinitionHash(space)
	if err != nil {
		return SearchSpace{}, err
	}
	space.DefinitionHash = definitionHash
	space.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(space)
	if err != nil {
		return SearchSpace{}, err
	}
	space.ManifestHash = hash
	return space, nil
}

func (space SearchSpace) Validate() error {
	if strings.TrimSpace(space.ID) == "" || strings.TrimSpace(space.Generation) == "" || strings.TrimSpace(space.ProtocolHash) == "" ||
		strings.TrimSpace(space.SourceDataHash) == "" || strings.TrimSpace(space.DefinitionHash) == "" ||
		strings.TrimSpace(space.ManifestHash) == "" {
		return fmt.Errorf("g2prize: search space commitments are incomplete")
	}
	want := space.ManifestHash
	sealed, err := SealSearchSpace(space)
	if err != nil {
		return err
	}
	if sealed.ManifestHash != want {
		return fmt.Errorf("g2prize: search space manifest hash mismatch")
	}
	return nil
}

func searchDefinitionHash(space SearchSpace) (string, error) {
	payload := struct {
		ID             string        `json:"id"`
		SourceDataHash string        `json:"source_data_hash"`
		Combinations   []Combination `json:"combinations"`
	}{
		ID: space.ID, SourceDataHash: space.SourceDataHash,
		Combinations: append([]Combination(nil), space.Combinations...),
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ConfidenceInterval is a two-sided 95% interval produced by cluster block
// bootstrap resampling.
type ConfidenceInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// RejectedCombination records why a preregistered combination did not
// produce a nomination. Every combination that was searched is accounted
// for here or in Report.Nominations — none are silently dropped, so a
// winner-only search cannot be laundered into an empty, quiet report.
type RejectedCombination struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

const (
	// ReasonUnregisteredSearchSpace fires when a combination (or a
	// tampered variant of one sharing its key) is not byte-for-byte a
	// member of the preregistered search space.
	ReasonUnregisteredSearchSpace = "unregistered_search_space"
	// ReasonNoWinners fires when no event fires the combination's rule
	// while classifying as a winner.
	ReasonNoWinners = "no_winners"
	// ReasonNoPayerMechanism fires when a winner has no identified payer,
	// or winners disagree on payer (no single coherent mechanism).
	ReasonNoPayerMechanism = "no_payer_mechanism"
	// ReasonOnlyWinnerNoControls fires when winners exist but none of them
	// have any matched loss/neutral control inside their own
	// SkyState+SorterDomain+symbol+liquidity-bucket+direction+horizon+
	// cluster slice — i.e. the search only explains winners, which is
	// exactly the failure mode this package refuses.
	ReasonOnlyWinnerNoControls = "only_winner_no_matched_controls"
	ReasonSelectionTaxFailed   = "selection_tax_failed"
)

// Nomination is the sole output of a successful combination evaluation. It
// can only ever be a candidate nomination (Kind is always
// KindCandidateNomination) — it is not, and cannot become, a RESEARCH_PASS
// verdict.
type Nomination struct {
	Key                   string             `json:"key"`
	Origin                string             `json:"origin"`
	Kind                  string             `json:"kind"`
	DiscoveryDataHash     string             `json:"discovery_data_hash"`
	SearchSpaceID         string             `json:"search_space_id"`
	SearchSpaceSize       int                `json:"search_space_size"`
	Rank                  int                `json:"rank"`
	MatchingRule          string             `json:"matching_rule"`
	SkyState              string             `json:"sky_state"`
	SorterDomain          string             `json:"sorter_domain"`
	Horizon               Horizon            `json:"horizon"`
	Symbol                string             `json:"symbol,omitempty"`
	LiquidityBucket       string             `json:"liquidity_bucket,omitempty"`
	TreatmentCount        int                `json:"treatment_count"`
	WinnerCount           int                `json:"winner_count"`
	TreatmentLossCount    int                `json:"treatment_loss_count"`
	TreatmentNeutralCount int                `json:"treatment_neutral_count"`
	ControlCount          int                `json:"control_count"`
	LossControlCount      int                `json:"loss_control_count"`
	NeutralControlCount   int                `json:"neutral_control_count"`
	Payer                 string             `json:"payer"`
	Mechanism             string             `json:"mechanism"`
	Direction             string             `json:"direction"`
	NetEffectMeanBPS      float64            `json:"net_effect_mean_bps"`
	PermutationPValue     float64            `json:"permutation_p_value"`
	PermutationDraws      int                `json:"permutation_draws"`
	ClusterCount          int                `json:"cluster_count"`
	ClusterBootstrapValid bool               `json:"cluster_bootstrap_valid"`
	ClusterBootstrapCI95  ConfidenceInterval `json:"cluster_bootstrap_ci95"`
	SelectionTaxAlpha     float64            `json:"selection_tax_alpha"`
	SurvivesSelectionTax  bool               `json:"survives_selection_tax"`
	Generation            string             `json:"generation,omitempty"`
	ProtocolHash          string             `json:"protocol_hash,omitempty"`
}

// Report is the full result of one g2 prize-map run. It always accounts for
// every combination in the search space via Nominations+Rejected, and
// always reports how many input events were excluded and why.
type Report struct {
	GeneratedAt         time.Time             `json:"generated_at"`
	Origin              string                `json:"origin"`
	SearchSpaceID       string                `json:"search_space_id"`
	SearchSpaceSize     int                   `json:"search_space_size"`
	DiscoveryDataHash   string                `json:"discovery_data_hash"`
	EventsInput         int                   `json:"events_input"`
	EventsEligible      int                   `json:"events_eligible"`
	ExcludedEvents      map[string]int        `json:"excluded_events,omitempty"`
	FamilyWiseAlpha     float64               `json:"family_wise_alpha"`
	PerCombinationAlpha float64               `json:"per_combination_alpha"`
	Nominations         []Nomination          `json:"nominations,omitempty"`
	Rejected            []RejectedCombination `json:"rejected_combinations,omitempty"`
}
