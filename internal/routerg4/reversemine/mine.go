package reversemine

import (
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/routerg4/protocol"
)

type mineState struct {
	matrix         WinnerFeatureMatrix
	candidateLimit int
	candidates     []Candidate
	ledger         []LedgerEntry
	registered     map[string]bool
	coveredCells   map[string]bool
	singleCount    int
	dualCount      int
}

func cellID(cell CellWorkItem) string {
	k := cell.Key
	return strings.Join([]string{
		cell.ProfileID, k.Horizon, k.Direction, fmt.Sprint(k.CapitalTier),
		k.SkyCoarse, k.Liquidity, k.SymbolClass,
	}, "|")
}

type budgetBucket struct {
	key   string
	cells []CellWorkItem
}

// fairCellOrder round-robins priority/profile/horizon/direction/capital
// buckets, then cells inside each bucket, preventing sorted prefixes from
// consuming the candidate budget.
func fairCellOrder(cells []CellWorkItem) []CellWorkItem {
	byKey := map[string][]CellWorkItem{}
	for _, cell := range cells {
		key := fmt.Sprintf("%02d|%s|%s|%s|%09d",
			cell.PriorityTier, cell.ProfileID, cell.Key.Horizon,
			cell.Key.Direction, cell.Key.CapitalTier)
		byKey[key] = append(byKey[key], cell)
	}
	keys := make([]string, 0, len(byKey))
	maxCells := 0
	for key, bucketCells := range byKey {
		keys = append(keys, key)
		sortCells(bucketCells)
		byKey[key] = bucketCells
		if len(bucketCells) > maxCells {
			maxCells = len(bucketCells)
		}
	}
	sort.Strings(keys)
	out := make([]CellWorkItem, 0, len(cells))
	for round := 0; round < maxCells; round++ {
		for _, key := range keys {
			if round < len(byKey[key]) {
				out = append(out, byKey[key][round])
			}
		}
	}
	return out
}

// Derive runs bounded reverse mining with bucket/cell round-robin allocation.
func Derive(
	cells []CellWorkItem,
	matrix WinnerFeatureMatrix,
	candidateLimit int,
	resume *Checkpoint,
) ([]Candidate, CandidateLedger, error) {
	state := mineState{
		matrix: matrix, candidateLimit: candidateLimit,
		registered: map[string]bool{}, coveredCells: map[string]bool{},
	}
	if resume != nil {
		state.candidates = append([]Candidate(nil), resume.Candidates...)
		state.ledger = append([]LedgerEntry(nil), resume.LedgerEntries...)
		state.singleCount = resume.SingleFeatureCount
		state.dualCount = resume.DualFeatureCount
		for _, candidate := range state.candidates {
			state.registered[candidate.CanonicalHash] = true
			state.coveredCells[cellID(CellWorkItem{
				ProfileID: candidate.ProfileID, Key: candidate.CellKey,
			})] = true
		}
	}
	scheduled := fairCellOrder(cells)
	ops := []RuleOp{OpLTE, OpGTE}

	profileFeatures := map[string][]string{}
	protocolCells := make([]protocol.DiscoveryCell, 0, len(scheduled))
	cellByID := make(map[string]CellWorkItem, len(scheduled))
	for _, cell := range scheduled {
		if _, ok := profileFeatures[cell.ProfileID]; !ok {
			profileFeatures[cell.ProfileID] = ProfileFeatureOrder(cell.ProfileID)
		}
		id := cellID(cell)
		cellByID[id] = cell
		protocolCells = append(protocolCells, protocol.DiscoveryCell{
			ProfileID: cell.ProfileID, Horizon: cell.Key.Horizon,
			Direction: cell.Key.Direction, CapitalTier: cell.Key.CapitalTier,
			CellID: id,
		})
	}
	// Over-generate deterministic assignments because missing feature samples
	// are ledgered but do not consume a registered-candidate slot.
	maxAssignments := candidateLimit * 128
	assignments := protocol.FeatureCellRoundRobin(profileFeatures, protocolCells, maxAssignments)
	for assignmentIndex, assignment := range assignments {
		if len(state.candidates) >= candidateLimit ||
			state.singleCount >= protocol.MaxSingleFeatureRules {
			break
		}
		cell, ok := cellByID[assignment.Cell.CellID]
		if !ok {
			continue
		}
		values, ids := featureSamples(matrix[cellID(cell)], assignment.Feature)
		if state.coveredCells[cellID(cell)] || len(values) == 0 || len(ids) == 0 {
			for _, alternative := range scheduled {
				if alternative.ProfileID != assignment.Cell.ProfileID ||
					state.coveredCells[cellID(alternative)] {
					continue
				}
				altValues, altIDs := featureSamples(matrix[cellID(alternative)], assignment.Feature)
				if len(altValues) > 0 && len(altIDs) > 0 {
					cell = alternative
					values, ids = altValues, altIDs
					break
				}
			}
		}
		if len(values) == 0 || len(ids) == 0 {
			continue
		}
		qop := (assignment.AllocationRound - 1) % (len(protocol.ThresholdQuantiles) * len(ops))
		q := protocol.ThresholdQuantiles[qop/len(ops)]
		op := ops[qop%len(ops)]
		cursor := fmt.Sprintf("single|assignment=%d|round=%d|feature=%s",
			assignmentIndex, assignment.AllocationRound, assignment.Feature)
		if err := state.attemptSingle(cell, assignment.Feature, q, op, cursor); err != nil {
			return nil, CandidateLedger{}, err
		}
	}

	dualScheduled := uncoveredFirst(scheduled, state.coveredCells)
	for allocationRound := 0; len(state.candidates) < candidateLimit &&
		state.dualCount < protocol.MaxDualFeatureRules; allocationRound++ {
		registeredThisRound := 0
		for cellIndex, cell := range dualScheduled {
			if len(state.candidates) >= candidateLimit ||
				state.dualCount >= protocol.MaxDualFeatureRules {
				break
			}
			pairs := dualFeaturePairs(cell.ProfileID)
			if len(pairs) == 0 {
				continue
			}
			for offset := 0; offset < len(pairs); offset++ {
				pairIndex := (cellIndex + allocationRound + offset) % len(pairs)
				pair := pairs[pairIndex]
				_, _, ids := commonFeatureSamples(matrix[cellID(cell)], pair[0], pair[1])
				if len(ids) == 0 {
					continue
				}
				before := len(state.candidates)
				cursor := fmt.Sprintf("dual|round=%d|cell=%d|pair=%d",
					allocationRound, cellIndex, pairIndex)
				if err := state.attemptDual(
					cell, pair[0], pair[1],
					protocol.ThresholdQuantiles[0], protocol.ThresholdQuantiles[0],
					OpLTE, OpLTE, cursor,
				); err != nil {
					return nil, CandidateLedger{}, err
				}
				if len(state.candidates) > before {
					registeredThisRound++
					break
				}
			}
		}
		if registeredThisRound == 0 {
			break
		}
	}

	ledger, err := SealLedger(CandidateLedger{
		SchemaVersion:       SchemaVersionLedger,
		Generation:          protocol.Generation,
		ProtocolHash:        protocol.RouterProtocolHash,
		SearchSpaceID:       protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm: protocol.DerivationAlgorithmVersion,
		WinnerSeedMode:      WinnerSeedMode,
		Entries:             state.ledger,
	})
	if err != nil {
		return nil, CandidateLedger{}, err
	}
	return state.candidates, ledger, nil
}

type singleQueue struct {
	profile string
	feature string
	cells   []CellWorkItem
}

func buildSingleQueues(cells []CellWorkItem, matrix WinnerFeatureMatrix) []singleQueue {
	byProfile := map[string][]singleQueue{}
	for _, profile := range []string{"okx_1h", "okx_5m"} {
		for _, feature := range ProfileFeatureOrder(profile) {
			var eligible []CellWorkItem
			for _, cell := range cells {
				if cell.ProfileID != profile {
					continue
				}
				if values, ids := featureSamples(matrix[cellID(cell)], feature); len(values) > 0 && len(ids) > 0 {
					eligible = append(eligible, cell)
				}
			}
			if len(eligible) > 0 {
				byProfile[profile] = append(byProfile[profile], singleQueue{
					profile: profile, feature: feature, cells: fairCellOrder(eligible),
				})
			}
		}
	}
	return interleaveSingleQueues(byProfile)
}

func interleaveSingleQueues(byProfile map[string][]singleQueue) []singleQueue {
	profiles := []string{"okx_1h", "okx_5m"}
	maxFeatures := 0
	for _, queues := range byProfile {
		if len(queues) > maxFeatures {
			maxFeatures = len(queues)
		}
	}
	var out []singleQueue
	for featureRound := 0; featureRound < maxFeatures; featureRound++ {
		for _, profile := range profiles {
			if featureRound < len(byProfile[profile]) {
				out = append(out, byProfile[profile][featureRound])
			}
		}
	}
	return out
}

type dualQueue struct {
	profile string
	pair    [2]string
	cells   []CellWorkItem
}

func buildDualQueues(cells []CellWorkItem, matrix WinnerFeatureMatrix) []dualQueue {
	byProfile := map[string][]dualQueue{}
	for _, profile := range []string{"okx_1h", "okx_5m"} {
		for _, pair := range dualFeaturePairs(profile) {
			var eligible []CellWorkItem
			for _, cell := range cells {
				if cell.ProfileID != profile {
					continue
				}
				_, _, ids := commonFeatureSamples(matrix[cellID(cell)], pair[0], pair[1])
				if len(ids) > 0 {
					eligible = append(eligible, cell)
				}
			}
			if len(eligible) > 0 {
				byProfile[profile] = append(byProfile[profile], dualQueue{
					profile: profile, pair: pair, cells: fairCellOrder(eligible),
				})
			}
		}
	}
	profiles := []string{"okx_1h", "okx_5m"}
	maxPairs := 0
	for _, queues := range byProfile {
		if len(queues) > maxPairs {
			maxPairs = len(queues)
		}
	}
	var out []dualQueue
	for pairRound := 0; pairRound < maxPairs; pairRound++ {
		for _, profile := range profiles {
			if pairRound < len(byProfile[profile]) {
				out = append(out, byProfile[profile][pairRound])
			}
		}
	}
	return out
}

func (s *mineState) attemptSingle(cell CellWorkItem, feature string, q float64, op RuleOp, cursor string) error {
	entry := s.newEntry(cell, "single", cursor)
	values, ids := featureSamples(s.matrix[cellID(cell)], feature)
	if len(ids) == 0 {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = "missing_feature_samples"
		entry.Conditions = []Condition{{FeatureKey: feature, Quantile: q, Op: op}}
		s.ledger = append(s.ledger, entry)
		return nil
	}
	sourceHash, err := HashWinnerIDs(ids)
	if err != nil {
		return err
	}
	conditions := []Condition{{
		FeatureKey: feature, Op: op, Threshold: EmpiricalQuantile(values, q), Quantile: q,
	}}
	return s.register(cell, conditions, ids, sourceHash, entry)
}

func (s *mineState) attemptDual(
	cell CellWorkItem,
	first, second string,
	firstQ, secondQ float64,
	firstOp, secondOp RuleOp,
	cursor string,
) error {
	entry := s.newEntry(cell, "dual", cursor)
	firstValues, secondValues, ids := commonFeatureSamples(
		s.matrix[cellID(cell)], first, second,
	)
	if len(ids) == 0 {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = "missing_common_feature_samples"
		entry.Conditions = []Condition{{FeatureKey: first}, {FeatureKey: second}}
		s.ledger = append(s.ledger, entry)
		return nil
	}
	sourceHash, err := HashWinnerIDs(ids)
	if err != nil {
		return err
	}
	conditions := []Condition{
		{FeatureKey: first, Op: firstOp, Threshold: EmpiricalQuantile(firstValues, firstQ), Quantile: firstQ},
		{FeatureKey: second, Op: secondOp, Threshold: EmpiricalQuantile(secondValues, secondQ), Quantile: secondQ},
	}
	return s.register(cell, conditions, ids, sourceHash, entry)
}

func (s *mineState) newEntry(cell CellWorkItem, kind, cursor string) LedgerEntry {
	return LedgerEntry{
		AttemptOrdinal: len(s.ledger) + 1, EnumeratorCursor: cursor,
		ProfileID: cell.ProfileID, CellKey: cell.Key,
		LedgerStage: LedgerStageDiscovery, RuleKind: kind,
		PriorityTier: cell.PriorityTier,
	}
}

func (s *mineState) register(
	cell CellWorkItem,
	conditions []Condition,
	ids []string,
	sourceHash string,
	entry LedgerEntry,
) error {
	entry.Conditions = conditions
	entry.SourceWinnerCount = len(ids)
	entry.SourceWinnerIDsHash = sourceHash
	candidate, err := buildCandidate(
		len(s.candidates)+1, cell, conditions, len(ids), sourceHash,
	)
	if err != nil {
		return err
	}
	entry.CanonicalRuleHash = candidate.CanonicalHash
	if s.registered[candidate.CanonicalHash] {
		entry.Status = LedgerStatusDuplicate
		entry.StatusReason = "duplicate_rule"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if err := ValidateCandidate(candidate); err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	s.registered[candidate.CanonicalHash] = true
	s.candidates = append(s.candidates, candidate)
	s.coveredCells[cellID(cell)] = true
	if len(conditions) == 1 {
		s.singleCount++
	} else {
		s.dualCount++
	}
	entry.Status = LedgerStatusUnexplained
	entry.StatusReason = CandidateStatus
	s.ledger = append(s.ledger, entry)
	return nil
}

func uncoveredFirst(cells []CellWorkItem, covered map[string]bool) []CellWorkItem {
	out := make([]CellWorkItem, 0, len(cells))
	for _, cell := range cells {
		if !covered[cellID(cell)] {
			out = append(out, cell)
		}
	}
	for _, cell := range cells {
		if covered[cellID(cell)] {
			out = append(out, cell)
		}
	}
	return out
}

func featureSamples(winners map[string]map[string]float64, feature string) ([]float64, []string) {
	ids := sortedWinnerIDs(winners)
	values := make([]float64, 0, len(ids))
	used := make([]string, 0, len(ids))
	for _, id := range ids {
		if value, ok := winners[id][feature]; ok && isFinite(value) {
			values = append(values, value)
			used = append(used, id)
		}
	}
	return values, used
}

func commonFeatureSamples(
	winners map[string]map[string]float64,
	first, second string,
) ([]float64, []float64, []string) {
	ids := sortedWinnerIDs(winners)
	firstValues := make([]float64, 0, len(ids))
	secondValues := make([]float64, 0, len(ids))
	used := make([]string, 0, len(ids))
	for _, id := range ids {
		firstValue, firstOK := winners[id][first]
		secondValue, secondOK := winners[id][second]
		if firstOK && secondOK && isFinite(firstValue) && isFinite(secondValue) {
			firstValues = append(firstValues, firstValue)
			secondValues = append(secondValues, secondValue)
			used = append(used, id)
		}
	}
	return firstValues, secondValues, used
}

func sortedWinnerIDs(winners map[string]map[string]float64) []string {
	ids := make([]string, 0, len(winners))
	for id := range winners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
