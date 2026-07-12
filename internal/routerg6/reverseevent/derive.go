package reverseevent

import (
	"fmt"
	"sort"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

type deriveState struct {
	cardLimit   int
	thresholds  map[string]eventmap.FrozenThresholds
	cards       []FrozenEventCard
	ledger      []ExperimentAttempt
	registered  map[string]bool
	covered     map[string]bool
	usedWindows map[string]eventmap.WinnerEntry
}

// fairGroupOrder round-robins families as the outer dimension, then cycles
// direction/horizon/profile inside each family before revisiting a family.
func fairGroupOrder(groups []WinnerGroup) []WinnerGroup {
	if len(groups) == 0 {
		return nil
	}
	byBucket := map[string][]WinnerGroup{}
	families := map[string]bool{}
	directions := map[string]bool{}
	horizons := map[string]bool{}
	profiles := map[string]bool{}
	for _, group := range groups {
		key := bucketKey(group)
		byBucket[key] = append(byBucket[key], group)
		families[group.CellKey.EventFamily] = true
		directions[group.CellKey.Direction] = true
		horizons[group.CellKey.Horizon] = true
		profiles[group.ProfileID] = true
	}
	for key := range byBucket {
		sortGroups(byBucket[key])
	}
	familyList := sortedKeys(families)
	directionList := sortedKeys(directions)
	horizonList := sortedKeys(horizons)
	profileList := sortedKeys(profiles)
	combos := len(directionList) * len(horizonList) * len(profileList)
	if combos == 0 {
		return nil
	}
	maxShockClusters := 0
	for _, bucket := range byBucket {
		if len(bucket) > maxShockClusters {
			maxShockClusters = len(bucket)
		}
	}
	var out []WinnerGroup
	for round := 0; round < maxShockClusters*combos; round++ {
		direction := directionList[round%len(directionList)]
		horizon := horizonList[(round/len(directionList))%len(horizonList)]
		profile := profileList[(round/combos)%len(profileList)]
		shockIndex := round / combos
		added := false
		for _, family := range familyList {
			key := fmt.Sprintf("%s|%s|%s|%s", family, direction, horizon, profile)
			bucket := byBucket[key]
			if shockIndex >= len(bucket) {
				continue
			}
			out = append(out, bucket[shockIndex])
			added = true
		}
		if !added {
			break
		}
	}
	return out
}

func bucketKey(group WinnerGroup) string {
	return fmt.Sprintf("%s|%s|%s|%s",
		group.CellKey.EventFamily,
		group.CellKey.Direction,
		group.CellKey.Horizon,
		group.ProfileID,
	)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Derive runs bounded reverse EventCard derivation with fair round-robin scheduling.
func Derive(
	groups []WinnerGroup,
	thresholds map[string]eventmap.FrozenThresholds,
	cardLimit int,
	resume *Checkpoint,
) ([]FrozenEventCard, ExperimentLedger, error) {
	state := deriveState{
		cardLimit:   cardLimit,
		thresholds:  thresholds,
		registered:  map[string]bool{},
		covered:     map[string]bool{},
		usedWindows: map[string]eventmap.WinnerEntry{},
	}
	if resume != nil {
		state.cards = append([]FrozenEventCard(nil), resume.Cards...)
		state.ledger = append([]ExperimentAttempt(nil), resume.LedgerAttempts...)
		for _, card := range state.cards {
			state.registered[card.EventCardHash] = true
			state.covered[groupKey(card.ProfileID, eventmap.CellKey{
				EventFamily: card.EventFamily,
				Horizon:     card.Horizon,
				Direction:   card.Direction,
				DelayBars:   card.DelayBars,
				CapitalTier: card.CapitalTiers[0],
			}, card.ShockCluster)] = true
		}
	}

	scheduled := fairGroupOrder(groups)
	for index, group := range scheduled {
		if len(state.cards) >= cardLimit {
			break
		}
		cursor := fmt.Sprintf("group|index=%d|key=%s", index, group.Key)
		if err := state.attemptGroup(group, cursor); err != nil {
			return nil, ExperimentLedger{}, err
		}
	}

	ledger, err := SealLedger(ExperimentLedger{
		SchemaVersion: SchemaVersionLedger,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		LedgerStage:   LedgerStageDiscovery,
		DiscoveryMode: protocol.DiscoveryMode,
		Attempts:      state.ledger,
	})
	if err != nil {
		return nil, ExperimentLedger{}, err
	}
	for i := range state.cards {
		state.cards[i].ExperimentHash = ledger.LedgerHash
	}
	return state.cards, ledger, nil
}

func (s *deriveState) attemptGroup(group WinnerGroup, cursor string) error {
	entry := s.newEntry(group, cursor)
	th, ok := s.thresholds[group.ProfileID]
	if !ok {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = "missing_thresholds"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	cooldownBars, err := NonOverlapCooldownBars(group)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	spec, err := eventmap.ProfileSpecFor(group.ProfileID)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if violations := NonOverlapViolations(group, cooldownBars, spec.BarDuration); len(violations) > 0 {
		entry.Status = LedgerStatusNonOverlap
		entry.StatusReason = "window_nonoverlap_violation"
		entry.SourceWinnerCount = len(group.Winners)
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if s.covered[group.Key] {
		entry.Status = LedgerStatusDuplicate
		entry.StatusReason = "bucket_already_covered"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	card, err := BuildEventCard(group, th, len(s.cards)+1)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	entry.EventCardHash = card.EventCardHash
	entry.SourceWinnerCount = card.SourceWinnerCount
	entry.SourceWinnerIDsHash = card.SourceWinnerIDsHash
	if s.registered[card.EventCardHash] {
		entry.Status = LedgerStatusDuplicate
		entry.StatusReason = "duplicate_card_hash"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if err := ValidateEventCard(card); err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	s.registered[card.EventCardHash] = true
	s.covered[group.Key] = true
	s.cards = append(s.cards, card)
	entry.Status = LedgerStatusUnexplained
	entry.StatusReason = protocol.UnexplainedCandidateStatus
	s.ledger = append(s.ledger, entry)
	return nil
}

func (s *deriveState) newEntry(group WinnerGroup, cursor string) ExperimentAttempt {
	return ExperimentAttempt{
		AttemptOrdinal:   len(s.ledger) + 1,
		EnumeratorCursor: cursor,
		ProfileID:        group.ProfileID,
		EventFamily:      group.CellKey.EventFamily,
		Horizon:          group.CellKey.Horizon,
		Direction:        group.CellKey.Direction,
		ShockCluster:     group.ShockCluster,
		LedgerStage:      LedgerStageDiscovery,
	}
}

// BuildCoverage summarizes unique winner buckets represented by cards.
func BuildCoverage(groups []WinnerGroup, cards []FrozenEventCard) (CoverageSummary, string, error) {
	summary := CoverageSummary{
		TotalBuckets:        len(groups),
		CoverageByProfile:   map[string]int{},
		CoverageByFamily:    map[string]int{},
		CoverageByDirection: map[string]int{},
		CoverageByHorizon:   map[string]int{},
	}
	covered := map[string]bool{}
	for _, card := range cards {
		key := groupKey(card.ProfileID, eventmap.CellKey{
			EventFamily: card.EventFamily,
			Horizon:     card.Horizon,
			Direction:   card.Direction,
			DelayBars:   card.DelayBars,
			CapitalTier: card.CapitalTiers[0],
		}, card.ShockCluster)
		if covered[key] {
			continue
		}
		covered[key] = true
		summary.CoveredBuckets++
		summary.CoverageByProfile[card.ProfileID]++
		summary.CoverageByFamily[card.EventFamily]++
		summary.CoverageByDirection[card.Direction]++
		summary.CoverageByHorizon[card.Horizon]++
	}
	hash, _, err := datatruth.HashJSON(summary)
	return summary, hash, err
}
