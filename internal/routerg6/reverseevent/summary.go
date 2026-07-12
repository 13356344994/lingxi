package reverseevent

import (
	"sort"

	"lingxi/internal/routerg6/protocol"
)

// BuildSummary constructs g6_reverse_events_summary.json payload.
func BuildSummary(
	groups []WinnerGroup,
	cards []FrozenEventCard,
	ledger ExperimentLedger,
	manifest EventCardManifest,
	winnerSeedCount int,
	cardLimit int,
) RunSummary {
	profiles := map[string]bool{}
	for _, group := range groups {
		profiles[group.ProfileID] = true
	}
	profileIDs := make([]string, 0, len(profiles))
	for id := range profiles {
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)
	coverageByFamily := map[string]int{}
	coverageByDirection := map[string]int{}
	coverageByHorizon := map[string]int{}
	coverageByProfile := map[string]int{}
	for _, card := range cards {
		coverageByFamily[card.EventFamily]++
		coverageByDirection[card.Direction]++
		coverageByHorizon[card.Horizon]++
		coverageByProfile[card.ProfileID]++
	}
	return RunSummary{
		SchemaVersion:        SchemaVersionSummary,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		DiscoveryMode:        protocol.DiscoveryMode,
		ProfileIDs:           profileIDs,
		WinnerSeedCount:      winnerSeedCount,
		AttemptCount:         len(ledger.Attempts),
		ActualCardCount:      len(cards),
		CardLimit:            cardLimit,
		CoverageByFamily:     coverageByFamily,
		CoverageByDirection:  coverageByDirection,
		CoverageByHorizon:    coverageByHorizon,
		CoverageByProfile:    coverageByProfile,
		LedgerStatusCounts:   ledgerStatusCounts(ledger.Attempts),
		EventCardHash:        manifest.EventCardHash,
		ExperimentHash:       manifest.ExperimentHash,
		CoverageHash:         manifest.CoverageHash,
		ManifestHash:         manifest.ManifestHash,
		RoundRobinDimensions: RoundRobinDimensions(),
		ExamActions:          protocol.ExamActions,
	}
}
