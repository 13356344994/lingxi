package playbook

import (
	"sort"
)

func countWinners(groups []ResidualWinnerGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Winners)
	}
	return total
}

// BuildSummary constructs the audit receipt for one playbooks run.
func BuildSummary(
	groups []ResidualWinnerGroup,
	playbooks []FrozenEventPlaybook,
	ledger ExperimentLedger,
	manifest PlaybookManifest,
	winnerSeedCount int,
	playbookLimit int,
	noCandidatesReason string,
	residualFailClosedReason string,
) RunSummary {
	profiles := map[string]bool{}
	coverageByFamily := map[string]int{}
	coverageByHorizon := map[string]int{}
	for _, pb := range playbooks {
		profiles[pb.ProfileID] = true
		coverageByFamily[pb.EventFamily]++
		coverageByHorizon[pb.Horizon]++
	}
	profileIDs := make([]string, 0, len(profiles))
	for profile := range profiles {
		profileIDs = append(profileIDs, profile)
	}
	sort.Strings(profileIDs)
	return RunSummary{
		SchemaVersion:        SchemaVersionSummary,
		Generation:           manifest.Generation,
		ProtocolHash:         manifest.ProtocolHash,
		DiscoveryMode:        manifest.DiscoveryMode,
		ProfileIDs:           profileIDs,
		WinnerSeedCount:      winnerSeedCount,
		AttemptCount:         len(ledger.Attempts),
		ActualPlaybookCount:  len(playbooks),
		PlaybookLimit:        playbookLimit,
		CoverageByFamily:     coverageByFamily,
		CoverageByHorizon:    coverageByHorizon,
		CoverageByProfile:    manifest.Coverage.CoverageByProfile,
		LedgerStatusCounts:   ledgerStatusCounts(ledger.Attempts),
		PlaybookHash:         manifest.PlaybookHash,
		ExperimentHash:       manifest.ExperimentHash,
		CoverageHash:         manifest.CoverageHash,
		ManifestHash:         manifest.ManifestHash,
		NoCandidatesReason:   noCandidatesReason,
		ResidualFailClosedReason: residualFailClosedReason,
		RoundRobinDimensions: RoundRobinDimensions(),
		ExamActions:          manifest.ExamActions,
	}
}
