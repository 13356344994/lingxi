// Package backward implements the research-only backward (bonus-mining) discovery
// lab described by the dual-discovery router plan: it looks for already fully
// realized, fully net-of-cost outcomes inside the public research partition and
// proposes matched-control-validated candidate nominations. It never approves a
// leaf, never reads sealed exam material, and never scans unbounded real history.
//
// A backward miner is fundamentally dangerous: given enough search combinations
// it can always find a subset of history that looks profitable in hindsight. This
// package's entire structure exists to make that failure mode structurally hard:
// every combination in the preregistered search space is charged against a
// multiple-testing budget whether or not it nominates anything, every nomination
// must carry matched loss/neutral controls (winners alone are refused), every
// candidate must resolve to a non-unknown payer mechanism, and the output type
// has no state that means "approved" — only CANDIDATE_NOMINATION. Promotion to
// RESEARCH_PASS can only happen later, through the existing candidate registry's
// forward falsification step, which this package does not implement or bypass.
package backward

import (
	"fmt"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
)

// OriginBackwardMined is the only origin tag this package ever produces. It is
// the same string as routerdiscovery/registry.OriginBackward, kept as a local
// constant so this package does not need to import the registry to be useful
// standalone; callers that do register nominations with the shared registry
// should use registry.OriginBackward there instead of retyping this string.
const OriginBackwardMined = "backward_mined"

// KindCandidateNomination is the only "status" a Nomination can ever carry.
// There is deliberately no RESEARCH_PASS, PASS, or APPROVED value anywhere in
// this package: backward mining can only ever produce a candidate nomination
// for later independent forward falsification.
const KindCandidateNomination = "CANDIDATE_NOMINATION"

// minIndependentClusters is the minimum number of distinct clusters required
// before a cluster-block-bootstrap confidence interval may be reported as
// valid. A single cluster cannot demonstrate that an effect is robust across
// independent shocks/regimes, so fewer than this must fail closed.
const minIndependentClusters = 2

// defaultSeed keeps bootstrap/permutation draws reproducible across runs when
// the caller does not pin one explicitly.
const defaultSeed = int64(20260711)

// OutcomeClass classifies an event's net-after-cost outcome relative to the
// configured neutral band.
type OutcomeClass string

const (
	OutcomeWinner  OutcomeClass = "winner"
	OutcomeLoss    OutcomeClass = "loss"
	OutcomeNeutral OutcomeClass = "neutral"
)

// CombinationOp is the comparison operator a search-space combination applies
// to one pre-decision feature.
type CombinationOp string

const (
	OpGTE CombinationOp = ">="
	OpLTE CombinationOp = "<="
)

// Event is one already-completed, pre-decision-time-bounded research
// observation. DecisionTime is when a live decision would have had to be
// made; FeatureAsOf is when the Features snapshot was actually available.
// FeatureAsOf must never be after DecisionTime — an event violating that is
// a decision-time leakage bug and is excluded before any mining happens.
// NetOutcomeBPS must already be fully net of realistic cost (fees, spread,
// slippage) — this package does not deduct cost itself, it only classifies
// and matches on whatever net figure the caller supplies.
type Event struct {
	ID            string              `json:"id"`
	Symbol        string              `json:"symbol"`
	Cluster       string              `json:"cluster"`
	Stratum       string              `json:"stratum"`
	DecisionTime  time.Time           `json:"decision_time"`
	FeatureAsOf   time.Time           `json:"feature_as_of"`
	Payer         mechanism.PayerRole `json:"payer"`
	Direction     mechanism.Direction `json:"direction"`
	Volatility    float64             `json:"volatility"`
	Liquidity     float64             `json:"liquidity"`
	Features      map[string]float64  `json:"features"`
	NetOutcomeBPS float64             `json:"net_outcome_bps"`
}

// Combination is one preregistered search-space entry: a single pre-decision
// feature threshold rule plus the matched-control rule that must be applied
// to any winner it selects. MatchingRule is mandatory — a combination with no
// declared matching rule can never nominate a candidate.
type Combination struct {
	Key          string              `json:"key"`
	FeatureKey   string              `json:"feature_key"`
	Op           CombinationOp       `json:"op"`
	Threshold    float64             `json:"threshold"`
	MatchingRule string              `json:"matching_rule"`
	Mechanism    string              `json:"mechanism"`
	Direction    mechanism.Direction `json:"direction"`
}

// Fires reports whether the combination's rule matches on this event, and
// whether the event carried the required feature at all (an event missing
// the feature never fires, and is not silently treated as zero).
func (c Combination) Fires(e Event) (fires bool, hasFeature bool) {
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
		return fmt.Errorf("backward: search space commitments are incomplete")
	}
	want := space.ManifestHash
	sealed, err := SealSearchSpace(space)
	if err != nil {
		return err
	}
	if sealed.ManifestHash != want {
		return fmt.Errorf("backward: search space manifest hash mismatch")
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
	// ReasonUnregisteredSearchSpace fires when a combination (or a tampered
	// variant of one sharing its key) is not byte-for-byte a member of the
	// preregistered search space.
	ReasonUnregisteredSearchSpace = "unregistered_search_space"
	// ReasonNoWinners fires when no event fires the combination's rule while
	// classifying as a winner.
	ReasonNoWinners = "no_winners"
	// ReasonNoPayerMechanism fires when a winner has no identified payer, or
	// winners disagree on payer (no single coherent mechanism).
	ReasonNoPayerMechanism = "no_payer_mechanism"
	// ReasonOnlyWinnerNoControls fires when winners exist but none of them
	// have any matched loss/neutral control — i.e. the search only explains
	// winners, which is exactly the failure mode this package refuses.
	ReasonOnlyWinnerNoControls = "only_winner_no_matched_controls"
	ReasonSelectionTaxFailed   = "selection_tax_failed"
)

// Nomination is the sole output of a successful combination evaluation. It
// can only ever be a candidate nomination (Kind is always
// KindCandidateNomination) — it is not, and cannot become, a RESEARCH_PASS
// verdict. Turning a nomination into RESEARCH_PASS requires independently
// re-registering it and passing forward falsification elsewhere (see
// routerdiscovery/registry.RecordForwardFalsification), which this package
// deliberately does not call.
type Nomination struct {
	Key                   string             `json:"key"`
	Origin                string             `json:"origin"`
	Kind                  string             `json:"kind"`
	DiscoveryDataHash     string             `json:"discovery_data_hash"`
	SearchSpaceID         string             `json:"search_space_id"`
	SearchSpaceSize       int                `json:"search_space_size"`
	Rank                  int                `json:"rank"`
	MatchingRule          string             `json:"matching_rule"`
	Symbol                string             `json:"symbol,omitempty"`
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

// Report is the full result of one backward-mining run. It always accounts
// for every combination in the search space via Nominations+Rejected, and
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
