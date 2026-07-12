package g3reverse

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"lingxi/internal/datatruth"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

// InductionResult is the full reverse-induction output for one sealed run.
type InductionResult struct {
	Rules    []Rule
	Ledger   AlphaLedger
	Manifest SearchManifest
	Summary  InductionSummary
}

// InductionSummary is the audit receipt written beside sealed artifacts.
type InductionSummary struct {
	Generation                    string         `json:"generation"`
	ProtocolHash                  string         `json:"protocol_hash"`
	DerivationAlgorithmConfigHash string         `json:"derivation_algorithm_config_hash"`
	Partition                     string         `json:"partition"`
	InductionEventCount           int            `json:"induction_event_count"`
	WinnerCount                   int            `json:"winner_count"`
	ActualDerivedRuleCount        int            `json:"actual_derived_rule_count"`
	SingleRegistered              int            `json:"single_registered"`
	DualRegistered                int            `json:"dual_registered"`
	ObservedGroupCount            int            `json:"observed_group_count"`
	CoveredGroupCount             int            `json:"covered_group_count"`
	RulesPerGroup                 map[string]int `json:"rules_per_group"`
	CoverageHash                  string         `json:"coverage_hash"`
	LedgerStatusCounts            map[string]int `json:"ledger_status_counts"`
	CanonicalDerivedRulesHash     string         `json:"canonical_derived_rules_hash"`
	AlphaLedgerHash               string         `json:"alpha_ledger_hash"`
	SearchManifestHash            string         `json:"search_manifest_hash"`
	ExamActions                   int            `json:"exam_actions"`
}

// DerivationProgress reports bounded streaming progress at every group boundary
// and, when configured, at attempt intervals.
type DerivationProgress struct {
	Phase              string         `json:"phase"`
	GroupsCompleted    int            `json:"groups_completed"`
	GroupsTotal        int            `json:"groups_total"`
	Attempts           int            `json:"attempts"`
	SingleRegistered   int            `json:"single_registered"`
	DualRegistered     int            `json:"dual_registered"`
	TotalRegistered    int            `json:"total_registered"`
	ObservedGroupCount int            `json:"observed_group_count"`
	CoveredGroupCount  int            `json:"covered_group_count"`
	RulesPerGroup      map[string]int `json:"rules_per_group"`
	GroupSkyState      string         `json:"group_sky_state,omitempty"`
	GroupDomain        string         `json:"group_sorter_domain,omitempty"`
	GroupHorizon       Horizon        `json:"group_horizon,omitempty"`
	GroupDirection     string         `json:"group_direction,omitempty"`
}

// DeriveOptions configures streaming progress without changing derivation.
type DeriveOptions struct {
	ProgressEvery int
	OnProgress    func(DerivationProgress)
}

type ruleGroupKey struct {
	SkyState      string
	ReturnBucket  string
	VolBucket     string
	FundingBucket string
	SorterDomain  string
	Horizon       Horizon
	Direction     mechanism.Direction
}

// Derive runs the frozen G3 reverse-induction derivation algorithm.
// WinnerIndex is used only for hypothesis generation; thresholds come from
// winner-subset available-feature quantiles; truncation never reorders by PnL.
func Derive(events []Event, winners WinnerIndex, bindings SearchManifestBindings, signingKey []byte, keyID string) (InductionResult, error) {
	return DeriveWithOptions(events, winners, bindings, signingKey, keyID, DeriveOptions{})
}

// DeriveWithOptions streams one attempt at a time in frozen order. It retains
// only registered rules/hashes plus the current group's winner samples and
// attempt hashes; cartesian items after a registration stop are not considered.
func DeriveWithOptions(
	events []Event,
	winners WinnerIndex,
	bindings SearchManifestBindings,
	signingKey []byte,
	keyID string,
	options DeriveOptions,
) (InductionResult, error) {
	if err := validateWinnerIndex(events, winners); err != nil {
		return InductionResult{}, err
	}
	inductionHash, err := HashEvents(events)
	if err != nil {
		return InductionResult{}, err
	}
	if bindings.InductionEventHash != "" && bindings.InductionEventHash != inductionHash {
		return InductionResult{}, errors.New("g3reverse: induction event hash binding mismatch")
	}
	if bindings.WinnerIndexHash != "" && bindings.WinnerIndexHash != winners.IndexHash {
		return InductionResult{}, errors.New("g3reverse: winner index hash binding mismatch")
	}
	bindings.InductionEventHash = inductionHash
	bindings.WinnerIndexHash = winners.IndexHash

	byID := make(map[string]*Event, len(events))
	for i := range events {
		byID[events[i].ID] = &events[i]
	}

	groupSet := make(map[ruleGroupKey]struct{})
	for _, entry := range winners.Entries {
		pattern, err := coarseSkyPattern(entry.SkyState)
		if err != nil {
			return InductionResult{}, fmt.Errorf("g3reverse: winner %s sky state: %w", entry.EventID, err)
		}
		axes, _ := parseSkyState(pattern)
		key := ruleGroupKey{
			SkyState: pattern, ReturnBucket: axes["ret"], VolBucket: axes["rv"],
			FundingBucket: axes["fund"], SorterDomain: entry.SorterDomain,
			Horizon: entry.Horizon, Direction: mechanism.Direction(entry.Direction),
		}
		groupSet[key] = struct{}{}
	}
	groupKeys := make([]ruleGroupKey, 0, len(groupSet))
	for key := range groupSet {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		return groupLess(groupKeys[i], groupKeys[j])
	})

	rules, ledgerEntries, stats, err := streamDerivation(
		groupKeys, winners.Entries, byID, options,
	)
	if err != nil {
		return InductionResult{}, err
	}
	ledger, err := SealLedger(AlphaLedger{
		Generation:                    protocol.Generation,
		ProtocolHash:                  protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Entries:                       ledgerEntries,
	})
	if err != nil {
		return InductionResult{}, err
	}
	coverageHash, err := hashCoverage(stats.observedGroups, stats.coveredGroups, stats.rulesPerGroup)
	if err != nil {
		return InductionResult{}, err
	}
	bindings.CoverageHash = coverageHash
	manifest, err := NewSearchManifest(rules, ledger, bindings)
	if err != nil {
		return InductionResult{}, err
	}
	if len(signingKey) > 0 {
		manifest, err = SignSearchManifest(manifest, signingKey, keyID)
		if err != nil {
			return InductionResult{}, err
		}
	}
	summary := InductionSummary{
		Generation:                    protocol.Generation,
		ProtocolHash:                  protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Partition:                     string(PartitionInduction),
		InductionEventCount:           len(events),
		WinnerCount:                   len(winners.Entries),
		ActualDerivedRuleCount:        len(rules),
		SingleRegistered:              stats.singleRegistered,
		DualRegistered:                stats.dualRegistered,
		ObservedGroupCount:            stats.observedGroups,
		CoveredGroupCount:             stats.coveredGroups,
		RulesPerGroup:                 cloneRulesPerGroup(stats.rulesPerGroup),
		CoverageHash:                  coverageHash,
		LedgerStatusCounts:            ledgerStatusCounts(ledger.Entries),
		CanonicalDerivedRulesHash:     manifest.CanonicalDerivedRulesHash,
		AlphaLedgerHash:               ledger.LedgerHash,
		SearchManifestHash:            manifest.ManifestHash,
		ExamActions:                   protocol.ExamActions,
	}
	return InductionResult{Rules: rules, Ledger: ledger, Manifest: manifest, Summary: summary}, nil
}

func validateWinnerIndex(events []Event, winners WinnerIndex) error {
	if winners.Generation != protocol.Generation || winners.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("g3reverse: winner index generation/protocol must match frozen G3 protocol")
	}
	suppliedHash, err := HashWinnerEntries(winners.Entries)
	if err != nil {
		return err
	}
	if winners.IndexHash == "" || winners.IndexHash != suppliedHash {
		return errors.New("g3reverse: winner index entries hash mismatch")
	}
	seenIDs := make(map[string]bool, len(winners.Entries))
	for _, entry := range winners.Entries {
		if seenIDs[entry.EventID] {
			return fmt.Errorf("g3reverse: duplicate winner index event id %s", entry.EventID)
		}
		seenIDs[entry.EventID] = true
	}
	rebuilt, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		return err
	}
	if suppliedHash != rebuilt.IndexHash {
		return errors.New("g3reverse: winner index entries do not match complete induction winner index")
	}
	byID := make(map[string]*Event, len(events))
	for i := range events {
		byID[events[i].ID] = &events[i]
	}
	for _, entry := range winners.Entries {
		event, ok := byID[entry.EventID]
		if !ok {
			return fmt.Errorf("g3reverse: winner index references missing event %s", entry.EventID)
		}
		if event.OutcomeClass != OutcomeWinner {
			return fmt.Errorf("g3reverse: winner index entry %s is not a real winner", entry.EventID)
		}
		if entry.SkyState != event.SkyState || entry.SorterDomain != event.SorterDomain ||
			entry.Horizon != event.Horizon || entry.Direction != string(event.Direction) ||
			entry.Symbol != event.Symbol || entry.OutcomeClass != event.OutcomeClass {
			return fmt.Errorf("g3reverse: winner index entry %s does not match event", entry.EventID)
		}
	}
	return nil
}

// HashWinnerEntries canonically hashes the supplied ordered winner entries.
// Ordering is part of the commitment, so duplicate, omission, reordering, or
// field tampering changes the hash.
func HashWinnerEntries(entries []WinnerIndexEntry) (string, error) {
	hash, _, err := datatruth.HashJSON(entries)
	return hash, err
}

func winnerEventsForGroup(
	entries []WinnerIndexEntry,
	byID map[string]*Event,
	key ruleGroupKey,
) []Event {
	var out []Event
	for _, entry := range entries {
		pattern, err := coarseSkyPattern(entry.SkyState)
		if err != nil || pattern != key.SkyState || entry.SorterDomain != key.SorterDomain ||
			entry.Horizon != key.Horizon || entry.Direction != string(key.Direction) {
			continue
		}
		event := byID[entry.EventID]
		out = append(out, *event)
	}
	return out
}

func groupLess(a, b ruleGroupKey) bool {
	if a.ReturnBucket != b.ReturnBucket {
		return a.ReturnBucket < b.ReturnBucket
	}
	if a.VolBucket != b.VolBucket {
		return a.VolBucket < b.VolBucket
	}
	if a.FundingBucket != b.FundingBucket {
		return a.FundingBucket < b.FundingBucket
	}
	if a.SorterDomain != b.SorterDomain {
		return a.SorterDomain < b.SorterDomain
	}
	if a.Horizon != b.Horizon {
		return a.Horizon < b.Horizon
	}
	return a.Direction < b.Direction
}

type streamingDerivation struct {
	rules            []Rule
	ledger           []LedgerEntry
	registeredHashes map[string]bool
	attempts         int
	singleRegistered int
	dualRegistered   int
	observedGroups   int
	covered          map[string]bool
	rulesPerGroup    map[string]int
	options          DeriveOptions
}

type derivationStats struct {
	singleRegistered int
	dualRegistered   int
	observedGroups   int
	coveredGroups    int
	rulesPerGroup    map[string]int
}

func streamDerivation(
	groupKeys []ruleGroupKey,
	winnerEntries []WinnerIndexEntry,
	byID map[string]*Event,
	options DeriveOptions,
) ([]Rule, []LedgerEntry, derivationStats, error) {
	if options.ProgressEvery <= 0 {
		options.ProgressEvery = protocol.ReverseInductionProgressEvery
	}
	if len(groupKeys) > protocol.MaxCoarseObservedGroups {
		return nil, nil, derivationStats{}, errors.New("g3reverse: observed coarse groups exceed protocol maximum")
	}
	state := streamingDerivation{
		rules:            make([]Rule, 0, protocol.MaxDerivedSearchRules),
		ledger:           make([]LedgerEntry, 0, protocol.MaxDerivedSearchRules),
		registeredHashes: make(map[string]bool, protocol.MaxDerivedSearchRules),
		observedGroups:   len(groupKeys),
		covered:          make(map[string]bool, len(groupKeys)),
		rulesPerGroup:    make(map[string]int, len(groupKeys)),
		options:          options,
	}
	for _, key := range groupKeys {
		state.rulesPerGroup[groupKeyID(key)] = 0
	}
	ops := []RuleOp{OpLTE, OpGTE}
	missingSingles := make(map[string]bool)

singleRounds:
	for _, feature := range protocol.FeatureDerivationOrder {
		for _, q := range protocol.ThresholdQuantiles {
			for _, op := range ops {
				for groupIndex, key := range groupKeys {
					if len(state.covered) == len(groupKeys) ||
						state.singleRegistered >= protocol.MaxSingleFeatureRules {
						break singleRounds
					}
					groupID := groupKeyID(key)
					if state.covered[groupID] {
						continue
					}
					winnerEvents := winnerEventsForGroup(winnerEntries, byID, key)
					values, ids := winnerFeatureValues(winnerEvents, feature)
					if len(values) == 0 {
						missingKey := feature + "\x00" + groupID
						if !missingSingles[missingKey] {
							missingSingles[missingKey] = true
							if err := state.attempt(key, "single", []RuleCondition{{FeatureKey: feature}}, nil, map[string]bool{}); err != nil {
								return nil, nil, derivationStats{}, err
							}
						}
						continue
					}
					sourceHash, err := hashWinnerIDs(ids)
					if err != nil {
						return nil, nil, derivationStats{}, err
					}
					conditions := []RuleCondition{{
						FeatureKey: feature, Op: op,
						Threshold: empiricalQuantile(values, q), Quantile: q,
					}}
					if err := state.attempt(key, "single", conditions, &sourceSummary{
						hash: sourceHash, count: len(ids),
					}, map[string]bool{}); err != nil {
						return nil, nil, derivationStats{}, err
					}
					state.report("single", groupIndex+1, len(groupKeys), key)
				}
				state.reportRound("single")
			}
		}
	}

	missingDuals := make(map[string]bool)
dualRounds:
	for _, pair := range prioritizedDualPairs() {
		for _, firstQ := range protocol.ThresholdQuantiles {
			for _, secondQ := range protocol.ThresholdQuantiles {
				for _, firstOp := range ops {
					for _, secondOp := range ops {
						for groupIndex, key := range groupKeys {
							if state.dualRegistered >= protocol.MaxDualFeatureRules ||
								len(state.rules) >= protocol.MaxDerivedSearchRules {
								break dualRounds
							}
							winnerEvents := winnerEventsForGroup(winnerEntries, byID, key)
							firstValues, secondValues, ids := winnerFeaturePairValues(
								winnerEvents, pair.first, pair.second,
							)
							if len(ids) == 0 {
								missingKey := pair.first + "\x00" + pair.second + "\x00" + groupKeyID(key)
								if !missingDuals[missingKey] {
									missingDuals[missingKey] = true
									if err := state.attempt(key, "dual", []RuleCondition{
										{FeatureKey: pair.first}, {FeatureKey: pair.second},
									}, nil, map[string]bool{}); err != nil {
										return nil, nil, derivationStats{}, err
									}
								}
								continue
							}
							sourceHash, err := hashWinnerIDs(ids)
							if err != nil {
								return nil, nil, derivationStats{}, err
							}
							conditions := []RuleCondition{
								{FeatureKey: pair.first, Op: firstOp, Threshold: empiricalQuantile(firstValues, firstQ), Quantile: firstQ},
								{FeatureKey: pair.second, Op: secondOp, Threshold: empiricalQuantile(secondValues, secondQ), Quantile: secondQ},
							}
							if err := state.attempt(key, "dual", conditions, &sourceSummary{
								hash: sourceHash, count: len(ids),
							}, map[string]bool{}); err != nil {
								return nil, nil, derivationStats{}, err
							}
							state.report("dual", groupIndex+1, len(groupKeys), key)
						}
						state.reportRound("dual")
					}
				}
			}
		}
	}

	charge := 0.0
	if len(state.rules) > 0 {
		charge = protocol.ReverseInductionAlpha / float64(len(state.rules))
	}
	for i := range state.ledger {
		if state.ledger[i].Status == LedgerStatusRegistered {
			state.ledger[i].AlphaCharge = charge
		}
	}
	stats := derivationStats{
		singleRegistered: state.singleRegistered,
		dualRegistered:   state.dualRegistered,
		observedGroups:   state.observedGroups,
		coveredGroups:    len(state.covered),
		rulesPerGroup:    cloneRulesPerGroup(state.rulesPerGroup),
	}
	return state.rules, state.ledger, stats, nil
}

type sourceSummary struct {
	hash  string
	count int
}

type dualFeaturePair struct {
	first    string
	second   string
	priority int
}

func prioritizedDualPairs() []dualFeaturePair {
	var pairs []dualFeaturePair
	seen := map[string]bool{}
	features := protocol.FeatureDerivationOrder
	for firstIndex := 0; firstIndex < len(features); firstIndex++ {
		for secondIndex := firstIndex + 1; secondIndex < len(features); secondIndex++ {
			first, second := features[firstIndex], features[secondIndex]
			firstFamily, secondFamily := mechanismFamily(first), mechanismFamily(second)
			priority := -1
			switch {
			case firstFamily != "" && firstFamily == secondFamily:
				priority = 0
			case (firstFamily != "" && isConditioner(second)) ||
				(secondFamily != "" && isConditioner(first)):
				priority = 1
			default:
				continue
			}
			if second < first {
				first, second = second, first
			}
			key := first + "\x00" + second
			if seen[key] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, dualFeaturePair{first: first, second: second, priority: priority})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].priority != pairs[j].priority {
			return pairs[i].priority < pairs[j].priority
		}
		if pairs[i].first != pairs[j].first {
			return pairs[i].first < pairs[j].first
		}
		return pairs[i].second < pairs[j].second
	})
	return pairs
}

func mechanismFamily(feature string) string {
	for _, attribution := range protocol.MechanismAttributions {
		if stringIn(feature, attribution.Features) {
			return attribution.Family
		}
	}
	return ""
}

func isConditioner(feature string) bool {
	return stringIn(feature, protocol.ConditionerFeatures)
}

func groupKeyID(key ruleGroupKey) string {
	return fmt.Sprintf("%s|%s|%s|%s", key.SkyState, key.SorterDomain, key.Horizon, key.Direction)
}

func cloneRulesPerGroup(source map[string]int) map[string]int {
	cloned := make(map[string]int, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func hashCoverage(observed, covered int, rulesPerGroup map[string]int) (string, error) {
	payload := struct {
		ObservedGroupCount int            `json:"observed_group_count"`
		CoveredGroupCount  int            `json:"covered_group_count"`
		RulesPerGroup      map[string]int `json:"rules_per_group"`
	}{
		ObservedGroupCount: observed,
		CoveredGroupCount:  covered,
		RulesPerGroup:      cloneRulesPerGroup(rulesPerGroup),
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func (state *streamingDerivation) attempt(
	key ruleGroupKey,
	kind string,
	conditions []RuleCondition,
	source *sourceSummary,
	groupSeen map[string]bool,
) error {
	state.attempts++
	entry := LedgerEntry{
		Ordinal:                       len(state.ledger) + 1,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		AlphaStage:                    "reverse_induction",
		SkyState:                      key.SkyState,
		SorterDomain:                  key.SorterDomain,
		Horizon:                       key.Horizon,
		Direction:                     key.Direction,
		RuleKind:                      kind,
		Conditions:                    append([]RuleCondition(nil), conditions...),
	}
	if source == nil || source.count == 0 || len(conditions) == 0 || conditions[0].Op == "" {
		entry.Status = LedgerStatusConsidered
		state.ledger = append(state.ledger, entry)
		state.reportInterval(kind, key)
		return nil
	}
	entry.SourceWinnerIDsHash = source.hash
	entry.SourceWinnerCount = source.count
	entry.AvailableFeatureSampleCount = source.count
	rule := Rule{
		SkyState: key.SkyState, SorterDomain: key.SorterDomain,
		Horizon: key.Horizon, Direction: key.Direction,
		Conditions: append([]RuleCondition(nil), conditions...),
	}
	hash, err := CanonicalRuleHash(rule)
	if err != nil {
		return err
	}
	entry.CanonicalRuleHash = hash
	if state.registeredHashes[hash] || groupSeen[hash] {
		entry.Status = LedgerStatusDuplicate
		state.ledger = append(state.ledger, entry)
		state.reportInterval(kind, key)
		return nil
	}
	groupSeen[hash] = true
	if err := ValidateRule(rule); err != nil {
		entry.Status = LedgerStatusConsidered
		state.ledger = append(state.ledger, entry)
		state.reportInterval(kind, key)
		return nil
	}
	rule.Ordinal = len(state.rules) + 1
	rule.CanonicalHash = hash
	state.rules = append(state.rules, rule)
	state.registeredHashes[hash] = true
	if kind == "single" {
		state.singleRegistered++
	} else {
		state.dualRegistered++
	}
	groupID := groupKeyID(key)
	state.covered[groupID] = true
	state.rulesPerGroup[groupID]++
	entry.Status = LedgerStatusRegistered
	state.ledger = append(state.ledger, entry)
	state.reportInterval(kind, key)
	return nil
}

func (state *streamingDerivation) reportInterval(phase string, key ruleGroupKey) {
	if state.options.OnProgress != nil && state.attempts%state.options.ProgressEvery == 0 {
		state.report(phase, 0, 0, key)
	}
}

func (state *streamingDerivation) report(phase string, completed, total int, key ruleGroupKey) {
	if state.options.OnProgress == nil {
		return
	}
	state.options.OnProgress(DerivationProgress{
		Phase: phase, GroupsCompleted: completed, GroupsTotal: total,
		Attempts: state.attempts, SingleRegistered: state.singleRegistered,
		DualRegistered: state.dualRegistered, TotalRegistered: len(state.rules),
		ObservedGroupCount: state.observedGroups, CoveredGroupCount: len(state.covered),
		RulesPerGroup: cloneRulesPerGroup(state.rulesPerGroup),
		GroupSkyState: key.SkyState, GroupDomain: key.SorterDomain,
		GroupHorizon: key.Horizon, GroupDirection: string(key.Direction),
	})
}

func (state *streamingDerivation) reportRound(phase string) {
	if state.options.OnProgress == nil {
		return
	}
	state.options.OnProgress(DerivationProgress{
		Phase: phase, GroupsCompleted: state.observedGroups, GroupsTotal: state.observedGroups,
		Attempts: state.attempts, SingleRegistered: state.singleRegistered,
		DualRegistered: state.dualRegistered, TotalRegistered: len(state.rules),
		ObservedGroupCount: state.observedGroups, CoveredGroupCount: len(state.covered),
		RulesPerGroup: cloneRulesPerGroup(state.rulesPerGroup),
	})
}

func winnerFeatureValues(winners []Event, feature string) ([]float64, []string) {
	var values []float64
	var ids []string
	for _, event := range winners {
		v, ok := event.Features[feature]
		if !ok || !isFinite(v) {
			continue
		}
		if featureMissing(event, feature) {
			continue
		}
		values = append(values, v)
		ids = append(ids, event.ID)
	}
	return values, ids
}

func winnerFeaturePairValues(winners []Event, first, second string) ([]float64, []float64, []string) {
	var firstValues, secondValues []float64
	var ids []string
	for _, event := range winners {
		firstValue, firstOK := event.Features[first]
		secondValue, secondOK := event.Features[second]
		if !firstOK || !secondOK || !isFinite(firstValue) || !isFinite(secondValue) ||
			featureMissing(event, first) || featureMissing(event, second) {
			continue
		}
		firstValues = append(firstValues, firstValue)
		secondValues = append(secondValues, secondValue)
		ids = append(ids, event.ID)
	}
	return firstValues, secondValues, ids
}

func hashWinnerIDs(ids []string) (string, error) {
	hash, _, err := datatruth.HashJSON(ids)
	return hash, err
}

func featureMissing(event Event, feature string) bool {
	for _, name := range event.MissingFeatures {
		if name == feature {
			return true
		}
	}
	return false
}

// empiricalQuantile uses linear interpolation (numpy type-7), matching sky cuts.
func empiricalQuantile(values []float64, q float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n == 1 {
		return sorted[0]
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	frac := pos - float64(lo)
	if lo+1 >= n {
		return sorted[n-1]
	}
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}
