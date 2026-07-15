package pathvalidation

import (
	"fmt"
	"strconv"
	"strings"

	"lingxi/internal/datadrivenv3/modelapi"
)

// ReplayStatus classifies how (or whether) a single validation/leaf_wf row
// could be routed through a sealed owner policy artifact without refitting
// or reading any sealed label.
type ReplayStatus string

const (
	// MatchUnique means exactly one leaf's replayable predicate matched the
	// row; the leaf's declared Intent is used exactly as the real model
	// would have produced it (terra, opus-gate-routing, grok-unique-regime).
	MatchUnique ReplayStatus = "MATCHED_UNIQUE_LEAF"
	// MatchDefault means no leaf's predicate matched and the policy's own
	// frozen DefaultIntent applies (this is a real, policy-declared branch,
	// not a fabricated one).
	MatchDefault ReplayStatus = "MATCHED_DEFAULT_INTENT"
	// MatchAmbiguousTiebreak means more than one leaf's replayable clauses
	// matched (or, for grok, zero matched) and the real model's tie-break
	// (nearest relative-regime centroid) depends on fitted parameters that
	// are not present in the sealed Policy artifact. This row is excluded
	// from every leaf's path statistics; it is never assigned to a leaf by
	// guessing.
	MatchAmbiguousTiebreak ReplayStatus = "AMBIGUOUS_UNREPLAYABLE_TIEBREAK"
	// MatchModelUnreplayable means the owner's entire policy artifact carries
	// no computable membership predicate (sonnet/episodic): its leaves are
	// aggregated, already-sealed neighbor-evidence summaries with no
	// observable-feature clause at all.
	MatchModelUnreplayable ReplayStatus = "MODEL_UNREPLAYABLE"
	// MatchParseIncomplete flags a defensive fallback: our predicate parser
	// did not resolve any leaf and found no ambiguity either. This should be
	// mathematically unreachable for terra/opus (exhaustive binary
	// partitions) when the parser is correct; synthetic fixture tests exist
	// specifically to make this case unreachable in practice. It is treated
	// like an ambiguous tie-break (excluded, not guessed) rather than a
	// fabricated match.
	MatchParseIncomplete ReplayStatus = "PARSE_INCOMPLETE_UNREPLAYABLE"
)

// ReplayOutcome is the result of attempting to route one decision-time state
// through one sealed owner policy.
type ReplayOutcome struct {
	LeafIndex    int
	Status       ReplayStatus
	Intent       modelapi.TargetIntent
	MatchCount   int
	FidelityNote string
}

// Replay dispatches to the correct family-specific predicate replay adapter
// for the given owner. It never mutates or refits the policy.
func Replay(owner string, policy modelapi.Policy, market modelapi.MarketState, account modelapi.AccountContext) (ReplayOutcome, error) {
	switch owner {
	case "terra":
		return replayTree(policy, market, account)
	case "opus":
		// The gate is replayable, but the actual Predict action is not: fitted
		// linear-expert weights/normalizers are absent from the sealed policy.
		// A representative leaf intent is an aggregate summary, not Predict.
		return ReplayOutcome{
			LeafIndex: -1, Status: MatchModelUnreplayable,
			FidelityNote: "opus fitted linear-expert parameters required by Predict are absent from the sealed policy",
		}, nil
	case "grok":
		return replayChangepoint(policy, market)
	case "sonnet":
		return replayEpisodic(policy)
	default:
		return ReplayOutcome{}, fmt.Errorf("unknown owner %q", owner)
	}
}

// --- terra: bounded decision tree, "IS MISSING"/"IS PRESENT"/threshold ------

func replayTree(policy modelapi.Policy, market modelapi.MarketState, account modelapi.AccountContext) (ReplayOutcome, error) {
	matched := make([]int, 0, 1)
	for i, leaf := range policy.Leaves {
		ok, err := evaluateTerraPredicate(leaf.Predicate, market, account)
		if err != nil {
			return ReplayOutcome{}, fmt.Errorf("leaf %s: %w", leaf.LeafID, err)
		}
		if ok {
			matched = append(matched, i)
		}
	}
	switch len(matched) {
	case 1:
		return ReplayOutcome{LeafIndex: matched[0], Status: MatchUnique, Intent: policy.Leaves[matched[0]].Intent, MatchCount: 1}, nil
	case 0:
		return ReplayOutcome{LeafIndex: -1, Status: MatchParseIncomplete, Intent: policy.DefaultIntent, MatchCount: 0}, nil
	default:
		return ReplayOutcome{LeafIndex: -1, Status: MatchParseIncomplete, Intent: policy.DefaultIntent, MatchCount: len(matched)}, nil
	}
}

func evaluateTerraPredicate(predicate string, market modelapi.MarketState, account modelapi.AccountContext) (bool, error) {
	for _, token := range strings.Split(predicate, " AND ") {
		ok, err := evaluateTerraToken(strings.TrimSpace(token), market, account)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evaluateTerraToken(token string, market modelapi.MarketState, account modelapi.AccountContext) (bool, error) {
	if token == "TRUE" {
		return true, nil
	}
	if idx := strings.Index(token, " IS MISSING OR "); idx >= 0 {
		name := token[:idx]
		rest := token[idx+len(" IS MISSING OR "):]
		prefix := name + " <= "
		if !strings.HasPrefix(rest, prefix) {
			return false, fmt.Errorf("unparseable disjunction clause %q", token)
		}
		threshold, err := strconv.ParseFloat(strings.TrimSpace(rest[len(prefix):]), 64)
		if err != nil {
			return false, err
		}
		present, value, ok := terraFieldValue(name, market, account)
		if !ok {
			return false, fmt.Errorf("unknown terra feature %q", name)
		}
		if !present {
			return true, nil
		}
		return value <= threshold, nil
	}
	if name, ok := suffixName(token, " IS PRESENT"); ok {
		present, _, known := terraFieldValue(name, market, account)
		if !known {
			return false, fmt.Errorf("unknown terra feature %q", name)
		}
		return present, nil
	}
	if name, ok := suffixName(token, " IS MISSING"); ok {
		present, _, known := terraFieldValue(name, market, account)
		if !known {
			return false, fmt.Errorf("unknown terra feature %q", name)
		}
		return !present, nil
	}
	name, op, value, ok := parseCompareClause(token)
	if !ok {
		return false, fmt.Errorf("unparseable terra clause %q", token)
	}
	present, actual, known := terraFieldValue(name, market, account)
	if !known {
		return false, fmt.Errorf("unknown terra feature %q", name)
	}
	if !present {
		return false, nil
	}
	return compareOp(actual, op, value), nil
}

// suffixName returns (name, true) when token is exactly name+suffix with a
// non-empty name that itself contains no comparison operator (guards against
// misreading a bare "X > v" token as ending in a coincidental substring).
func suffixName(token, suffix string) (string, bool) {
	if !strings.HasSuffix(token, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(token, suffix)
	if name == "" || strings.ContainsAny(name, "<>=") {
		return "", false
	}
	return name, true
}

func terraFieldValue(name string, market modelapi.MarketState, account modelapi.AccountContext) (present bool, value float64, ok bool) {
	for i, featureName := range modelapi.FeatureOrder {
		if featureName == name {
			present = market.MissingBits&(uint64(1)<<uint(i)) == 0
			if present {
				value = market.Features[i]
			}
			return present, value, true
		}
	}
	switch name {
	case "account_signed_position_fraction":
		return true, account.SignedPositionFraction, true
	case "account_gross_leverage":
		return true, account.GrossLeverage, true
	case "account_free_equity_usdt":
		return true, account.FreeEquityUSDT, true
	case "account_pending_action_count":
		return true, float64(account.PendingActionCount), true
	}
	return false, 0, false
}

// --- opus: hard top-1 gate tree, "name<threshold[missing->left|right]" -----

func replayGate(policy modelapi.Policy, market modelapi.MarketState) (ReplayOutcome, error) {
	matched := make([]int, 0, 1)
	for i, leaf := range policy.Leaves {
		ok, err := evaluateGatePredicate(leaf.Predicate, market)
		if err != nil {
			return ReplayOutcome{}, fmt.Errorf("leaf %s: %w", leaf.LeafID, err)
		}
		if ok {
			matched = append(matched, i)
		}
	}
	switch len(matched) {
	case 1:
		return ReplayOutcome{
			LeafIndex: matched[0], Status: MatchUnique, Intent: policy.Leaves[matched[0]].Intent, MatchCount: 1,
			FidelityNote: "opus leaf intent is the sealed policy's representative constant action for the routed expert region; the real model's continuous per-example linear-expert adjustment inside the region is not exposed by the sealed Policy artifact",
		}, nil
	case 0:
		return ReplayOutcome{LeafIndex: -1, Status: MatchParseIncomplete, Intent: policy.DefaultIntent, MatchCount: 0}, nil
	default:
		return ReplayOutcome{LeafIndex: -1, Status: MatchParseIncomplete, Intent: policy.DefaultIntent, MatchCount: len(matched)}, nil
	}
}

func evaluateGatePredicate(predicate string, market modelapi.MarketState) (bool, error) {
	if predicate == "ALL_STATES" {
		return true, nil
	}
	for _, token := range strings.Split(predicate, " AND ") {
		ok, err := evaluateGateToken(strings.TrimSpace(token), market)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func evaluateGateToken(token string, market modelapi.MarketState) (bool, error) {
	open := strings.LastIndex(token, "[")
	if open < 0 || !strings.HasSuffix(token, "]") {
		return false, fmt.Errorf("unparseable gate clause %q", token)
	}
	main := token[:open]
	note := token[open+1 : len(token)-1]
	missingLeft := note == "missing->left"
	if !missingLeft && note != "missing->right" {
		return false, fmt.Errorf("unknown missing-routing note %q", note)
	}
	var name, thresholdText, op string
	if idx := strings.Index(main, ">="); idx >= 0 {
		name, op, thresholdText = main[:idx], ">=", main[idx+2:]
	} else if idx := strings.Index(main, "<"); idx >= 0 {
		name, op, thresholdText = main[:idx], "<", main[idx+1:]
	} else {
		return false, fmt.Errorf("unparseable gate clause %q", token)
	}
	threshold, err := strconv.ParseFloat(thresholdText, 64)
	if err != nil {
		return false, err
	}
	idx, ok := featureIndex(name)
	if !ok {
		return false, fmt.Errorf("unknown gate feature %q", name)
	}
	present := market.MissingBits&(uint64(1)<<uint(idx)) == 0
	goLeft := missingLeft
	if present {
		goLeft = market.Features[idx] < threshold
	}
	if op == "<" {
		return goLeft, nil
	}
	return !goLeft, nil
}

// --- grok: change-point/rare-state regimes, "&&"-joined clauses ------------

func replayChangepoint(policy modelapi.Policy, market modelapi.MarketState) (ReplayOutcome, error) {
	matched := make([]int, 0, 2)
	for i, leaf := range policy.Leaves {
		if leaf.RejectionReason != "" {
			continue
		}
		ok, err := evaluateChangepointPredicate(leaf.Predicate, market)
		if err != nil {
			return ReplayOutcome{}, fmt.Errorf("leaf %s: %w", leaf.LeafID, err)
		}
		if ok {
			matched = append(matched, i)
		}
	}
	if len(matched) == 1 {
		return ReplayOutcome{LeafIndex: matched[0], Status: MatchUnique, Intent: policy.Leaves[matched[0]].Intent, MatchCount: 1}, nil
	}
	// Zero or multiple matches: the real model breaks the tie by nearest
	// relative-regime centroid distance, whose centroids are fitted
	// parameters that are not present in the sealed Policy artifact. This
	// row is unreplayable, not defaulted or guessed.
	return ReplayOutcome{
		LeafIndex: -1, Status: MatchAmbiguousTiebreak, Intent: policy.DefaultIntent, MatchCount: len(matched),
		FidelityNote: "grok regime tie-break/fallback uses fitted centroid distances that are not part of the sealed policy artifact",
	}, nil
}

func evaluateChangepointPredicate(predicate string, market modelapi.MarketState) (bool, error) {
	for _, clause := range strings.Split(predicate, " && ") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if strings.HasPrefix(clause, "regime_id=") || strings.HasPrefix(clause, "rare_state=") || strings.HasPrefix(clause, "reason=") {
			continue
		}
		if name, ok := parenName(clause, "present("); ok {
			idx, known := featureIndex(name)
			if !known {
				return false, fmt.Errorf("unknown feature %q", name)
			}
			if market.MissingBits&(uint64(1)<<uint(idx)) != 0 {
				return false, nil
			}
			continue
		}
		if name, ok := parenName(clause, "missing("); ok {
			idx, known := featureIndex(name)
			if !known {
				return false, fmt.Errorf("unknown feature %q", name)
			}
			if market.MissingBits&(uint64(1)<<uint(idx)) == 0 {
				return false, nil
			}
			continue
		}
		name, op, value, ok := parseCompareClause(clause)
		if !ok {
			return false, fmt.Errorf("unparseable changepoint clause %q", clause)
		}
		idx, known := featureIndex(name)
		if !known {
			return false, fmt.Errorf("unknown feature %q", name)
		}
		if market.MissingBits&(uint64(1)<<uint(idx)) != 0 {
			return false, nil
		}
		if !compareOp(market.Features[idx], op, value) {
			return false, nil
		}
	}
	return true, nil
}

func parenName(clause, prefix string) (string, bool) {
	if !strings.HasPrefix(clause, prefix) || !strings.HasSuffix(clause, ")") {
		return "", false
	}
	return clause[len(prefix) : len(clause)-1], true
}

// --- sonnet: aggregated episodic-neighbor evidence, no replayable clause ---

func replayEpisodic(_ modelapi.Policy) (ReplayOutcome, error) {
	// Every sonnet/episodic leaf predicate is a description of an aggregated
	// evidence bucket ("EPISODIC_NEIGHBOR_EVIDENCE k=... support=N"), not a
	// clause over observable market/account fields. The real model's Predict
	// method does not even consult these leaves; it performs a k-nearest-
	// neighbor lookup against the full sealed candidate pool of individual
	// discovery training rows, which this validation owner is forbidden to
	// read. There is no safe, non-fabricated way to determine which
	// validation/leaf_wf row would fall into which bucket, so this owner's
	// entire policy is marked UNREPLAYABLE rather than guessed.
	return ReplayOutcome{
		LeafIndex: -1, Status: MatchModelUnreplayable, Intent: modelapi.TargetIntent{TargetHorizonHours: 1},
		FidelityNote: "sonnet/episodic policy leaves are aggregated neighbor-evidence summaries with no observable-feature membership predicate; individual training rows behind them are sealed and cannot be read",
	}, nil
}

// --- shared clause parsing ----------------------------------------------

func parseCompareClause(clause string) (name, op string, value float64, ok bool) {
	for _, candidate := range []string{">=", "<=", ">", "<"} {
		if i := strings.Index(clause, candidate); i > 0 {
			name = strings.TrimSpace(clause[:i])
			var err error
			value, err = strconv.ParseFloat(strings.TrimSpace(clause[i+len(candidate):]), 64)
			if err != nil {
				continue
			}
			return name, candidate, value, true
		}
	}
	return "", "", 0, false
}

func compareOp(actual float64, op string, value float64) bool {
	switch op {
	case ">=":
		return actual >= value
	case "<=":
		return actual <= value
	case ">":
		return actual > value
	case "<":
		return actual < value
	}
	return false
}

func featureIndex(name string) (int, bool) {
	for i, n := range modelapi.FeatureOrder {
		if n == name {
			return i, true
		}
	}
	return -1, false
}
