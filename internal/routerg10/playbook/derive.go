package playbook

import (
	"fmt"
	"sort"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

type deriveState struct {
	playbookLimit int
	thresholds    map[string]eventmap.FrozenThresholds
	playbooks     []FrozenEventPlaybook
	ledger        []ExperimentAttempt
	registered    map[string]bool
	covered       map[string]bool
	dedupSeen     map[string]bool
}

// fairGroupOrder round-robins families as the outer dimension, then cycles
// horizon/profile inside each family before revisiting a family.
func fairGroupOrder(groups []ResidualWinnerGroup) []ResidualWinnerGroup {
	if len(groups) == 0 {
		return nil
	}
	byBucket := map[string][]ResidualWinnerGroup{}
	families := map[string]bool{}
	horizons := map[string]bool{}
	profiles := map[string]bool{}
	for _, group := range groups {
		key := bucketKey(group)
		byBucket[key] = append(byBucket[key], group)
		families[group.CellKey.EventFamily] = true
		horizons[group.CellKey.Horizon] = true
		profiles[group.ProfileID] = true
	}
	for key := range byBucket {
		sortGroups(byBucket[key])
	}
	familyList := sortedKeys(families)
	horizonList := sortedKeys(horizons)
	profileList := sortedKeys(profiles)
	combos := len(horizonList) * len(profileList)
	if combos == 0 {
		return nil
	}
	maxShockClusters := 0
	for _, bucket := range byBucket {
		if len(bucket) > maxShockClusters {
			maxShockClusters = len(bucket)
		}
	}
	var out []ResidualWinnerGroup
	for round := 0; round < maxShockClusters*combos; round++ {
		horizon := horizonList[round%len(horizonList)]
		profile := profileList[(round/len(horizonList))%len(profileList)]
		shockIndex := round / combos
		added := false
		for _, family := range familyList {
			key := fmt.Sprintf("%s|%s|%s", family, horizon, profile)
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

func bucketKey(group ResidualWinnerGroup) string {
	return fmt.Sprintf("%s|%s|%s",
		group.CellKey.EventFamily,
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

// Derive runs bounded reverse Event Playbook derivation with fair round-robin scheduling.
func Derive(
	groups []ResidualWinnerGroup,
	thresholds map[string]eventmap.FrozenThresholds,
	playbookLimit int,
	resume *Checkpoint,
) ([]FrozenEventPlaybook, ExperimentLedger, error) {
	state := deriveState{
		playbookLimit: playbookLimit,
		thresholds:    thresholds,
		registered:    map[string]bool{},
		covered:       map[string]bool{},
		dedupSeen:     map[string]bool{},
	}
	if resume != nil {
		state.playbooks = append([]FrozenEventPlaybook(nil), resume.Playbooks...)
		state.ledger = append([]ExperimentAttempt(nil), resume.LedgerAttempts...)
		for _, pb := range state.playbooks {
			state.registered[pb.PlaybookHash] = true
			state.covered[groupKey(pb.ProfileID, ResidualCellKey{
				EventFamily: pb.EventFamily,
				Horizon:     pb.Horizon,
				DelayBars:   pb.DelayBars,
			}, pb.ShockCluster)] = true
		}
	}

	scheduled := fairGroupOrder(groups)
	for index, group := range scheduled {
		if len(state.playbooks) >= playbookLimit {
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
	for i := range state.playbooks {
		state.playbooks[i].ExperimentHash = ledger.LedgerHash
	}
	return state.playbooks, ledger, nil
}

func (s *deriveState) attemptGroup(group ResidualWinnerGroup, cursor string) error {
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
	if dedupKey := primaryDedupKey(group); s.dedupSeen[dedupKey] {
		entry.Status = LedgerStatusDeduped
		entry.StatusReason = "discovery_dedup_tracker_key"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	pb, err := BuildPlaybook(group, th, len(s.playbooks)+1)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	entry.PlaybookHash = pb.PlaybookHash
	entry.SourceWinnerCount = pb.SourceWinnerCount
	entry.SourceWinnerIDsHash = pb.SourceWinnerIDsHash
	if s.registered[pb.PlaybookHash] {
		entry.Status = LedgerStatusDuplicate
		entry.StatusReason = "duplicate_playbook_hash"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if err := ValidatePlaybook(pb); err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	s.registered[pb.PlaybookHash] = true
	s.covered[group.Key] = true
	s.dedupSeen[primaryDedupKey(group)] = true
	s.playbooks = append(s.playbooks, pb)
	entry.Status = LedgerStatusCandidate
	entry.StatusReason = protocol.PersonalWildCandidateStatus
	s.ledger = append(s.ledger, entry)
	return nil
}

func primaryDedupKey(group ResidualWinnerGroup) string {
	if len(group.Winners) == 0 {
		return group.Key
	}
	w := group.Winners[0]
	return DedupTrackerKey(w.Symbol, w.EventFamily, w.Horizon, w.DelayBars)
}

func (s *deriveState) newEntry(group ResidualWinnerGroup, cursor string) ExperimentAttempt {
	return ExperimentAttempt{
		AttemptOrdinal:   len(s.ledger) + 1,
		EnumeratorCursor: cursor,
		ProfileID:        group.ProfileID,
		EventFamily:      group.CellKey.EventFamily,
		Horizon:          group.CellKey.Horizon,
		ShockCluster:     group.ShockCluster,
		LedgerStage:      LedgerStageDiscovery,
	}
}

// BuildCoverage summarizes unique winner buckets represented by playbooks.
func BuildCoverage(groups []ResidualWinnerGroup, playbooks []FrozenEventPlaybook) (CoverageSummary, string, error) {
	summary := CoverageSummary{
		TotalBuckets:       len(groups),
		CoverageByProfile:  map[string]int{},
		CoverageByFamily:   map[string]int{},
		CoverageByHorizon:  map[string]int{},
	}
	covered := map[string]bool{}
	for _, pb := range playbooks {
		key := groupKey(pb.ProfileID, ResidualCellKey{
			EventFamily: pb.EventFamily,
			Horizon:     pb.Horizon,
			DelayBars:   pb.DelayBars,
		}, pb.ShockCluster)
		if covered[key] {
			continue
		}
		covered[key] = true
		summary.CoveredBuckets++
		summary.CoverageByProfile[pb.ProfileID]++
		summary.CoverageByFamily[pb.EventFamily]++
		summary.CoverageByHorizon[pb.Horizon]++
	}
	hash, _, err := datatruth.HashJSON(summary)
	return summary, hash, err
}
