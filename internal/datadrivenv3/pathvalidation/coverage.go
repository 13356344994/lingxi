package pathvalidation

import (
	"strings"

	"lingxi/internal/datadrivenv3/modelapi"
)

// ForbiddenPredicateTokens mirrors protocol.json's state_schema.forbidden
// list. It is a defensive, belt-and-suspenders scan of every leaf predicate
// string: the dataset itself never emits these fields, but this validation
// owner never trusts a sealed policy artifact without checking.
var ForbiddenPredicateTokens = []string{
	"symbol_id", "absolute_date", "future_return", "pnl", "winner",
	"old_candidate_id", "old_route_id", "old_threshold", "current_open_bar", "post_decision_value",
}

// ScanForbiddenTokens returns every leaf whose predicate text contains a
// forbidden token, by leaf_id.
func ScanForbiddenTokens(policy modelapi.Policy) []string {
	var offenders []string
	for _, leaf := range policy.Leaves {
		lower := strings.ToLower(leaf.Predicate)
		for _, token := range ForbiddenPredicateTokens {
			if strings.Contains(lower, token) {
				offenders = append(offenders, leaf.LeafID)
				break
			}
		}
	}
	return offenders
}

// ActivationCoverage summarizes how often a policy actually routes a
// non-default, non-flat action across a partition's eligible rows. It is
// reported per policy, independent of any single leaf's verdict, because a
// policy that is legitimately flat almost everywhere (no_trade_dominance_is_
// allowed=true) is not itself a failure — only a *certified* path that is
// dormant out-of-sample is (see InactivityFailure on PathVerdict).
type ActivationCoverage struct {
	Owner                 string  `json:"owner"`
	Partition             string  `json:"partition"`
	EligibleRows          int     `json:"eligible_rows"`
	ReplayableRows        int     `json:"replayable_rows"` // MatchUnique or MatchDefault
	AmbiguousRows         int     `json:"ambiguous_rows"`
	ModelUnreplayableRows int     `json:"model_unreplayable_rows"`
	MatchedNonDefaultRows int     `json:"matched_non_default_rows"`
	ActiveActionRows      int     `json:"active_action_rows"`
	DefaultIntentRows     int     `json:"default_intent_rows"`
	NoTradeRows           int     `json:"no_trade_rows"`
	ActivationRate        float64 `json:"activation_rate"`
	MatchedCoverage       float64 `json:"matched_coverage"`
	NoTradeRate           float64 `json:"no_trade_rate"`
	DegenerateFixedFrac   float64 `json:"degenerate_fixed_fraction"`
}

// ComputeActivationCoverage tallies the outcomes already produced by Replay
// for every eligible row of one owner's policy in one partition.
func ComputeActivationCoverage(owner, partition string, outcomes []ReplayOutcome) ActivationCoverage {
	c := ActivationCoverage{Owner: owner, Partition: partition, EligibleRows: len(outcomes)}
	fixedCounts := map[string]int{}
	for _, o := range outcomes {
		switch o.Status {
		case MatchUnique:
			c.ReplayableRows++
			c.MatchedNonDefaultRows++
		case MatchDefault:
			c.ReplayableRows++
			c.DefaultIntentRows++
		case MatchAmbiguousTiebreak, MatchParseIncomplete:
			c.AmbiguousRows++
		case MatchModelUnreplayable:
			c.ModelUnreplayableRows++
		}
		if o.Status == MatchUnique || o.Status == MatchDefault {
			if o.Intent.TargetSignedPositionFraction == 0 {
				c.NoTradeRows++
			} else if o.Status == MatchUnique {
				c.ActiveActionRows++
			}
			fixedCounts[fixedActionKey(o.Intent.TargetSignedPositionFraction)]++
		}
	}
	if c.ReplayableRows > 0 {
		best := 0
		for _, n := range fixedCounts {
			if n > best {
				best = n
			}
		}
		c.DegenerateFixedFrac = float64(best) / float64(c.ReplayableRows)
		c.NoTradeRate = float64(c.NoTradeRows) / float64(c.ReplayableRows)
	}
	if c.EligibleRows > 0 {
		c.ActivationRate = float64(c.ActiveActionRows) / float64(c.EligibleRows)
		c.MatchedCoverage = float64(c.MatchedNonDefaultRows) / float64(c.EligibleRows)
	}
	return c
}

func fixedActionKey(fraction float64) string {
	if fraction > 0 {
		return "LONG"
	}
	if fraction < 0 {
		return "SHORT"
	}
	return "FLAT"
}

// IsInactivityFailure reports whether a path that passed every validation
// gate should still be downgraded because it barely activates in leaf_wf,
// even though the leaf_wf partition itself has ample eligible volume.
func IsInactivityFailure(wfMatchedRows, wfEligibleRows int) bool {
	// Reuse g1's already-frozen minimum-events gate. Introducing a second,
	// validation-owner-selected activity threshold would be post-seal tuning.
	return wfEligibleRows >= MinimumEvents && wfMatchedRows < MinimumEvents
}
