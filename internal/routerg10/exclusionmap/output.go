package exclusionmap

import (
	"os"
	"path/filepath"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// WriteOutputs atomically writes map, ledger, manifest, and summary artifacts.
func WriteOutputs(
	outputDir string,
	exclusionMap TripleExclusionMap,
	ledger ExperimentLedger,
	manifest MapManifest,
	summary RunSummary,
) (map[string]string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	artifactHashes := map[string]string{}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputFileName), exclusionMap); err != nil {
		return nil, err
	}
	hash, err := HashFile(filepath.Join(outputDir, OutputFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[OutputFileName] = hash
	if err := writeJSONAtomic(filepath.Join(outputDir, LedgerFileName), ledger); err != nil {
		return nil, err
	}
	hash, err = HashFile(filepath.Join(outputDir, LedgerFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[LedgerFileName] = hash
	if err := WriteManifest(outputDir, manifest); err != nil {
		return nil, err
	}
	hash, err = HashFile(filepath.Join(outputDir, ManifestFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[ManifestFileName] = hash
	if err := writeJSONAtomic(filepath.Join(outputDir, SummaryFileName), summary); err != nil {
		return nil, err
	}
	hash, err = HashFile(filepath.Join(outputDir, SummaryFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[SummaryFileName] = hash
	return artifactHashes, nil
}

// BuildSummary aggregates one run receipt.
func BuildSummary(
	cfg RunConfig,
	constitutionHash string,
	triggeredSeedCount int,
	ledger ExperimentLedger,
	exclusionMap TripleExclusionMap,
	manifest MapManifest,
	failClosedReason string,
) RunSummary {
	rejectCounts := map[string]int{}
	acceptCounts := map[string]int{}
	accepted := 0
	rejected := 0
	for _, attempt := range ledger.Attempts {
		switch attempt.Status {
		case LedgerStatusAccepted:
			accepted++
			if attempt.AcceptReason != "" {
				acceptCounts[attempt.AcceptReason]++
			}
		case LedgerStatusRejected:
			rejected++
			for _, reason := range attempt.RejectReasons {
				rejectCounts[reason]++
			}
		}
	}
	return RunSummary{
		SchemaVersion:      SchemaVersionSummary,
		Generation:         Generation,
		ProtocolHash:       g10protocolv2.RouterProtocolHash,
		ConstitutionHash:   constitutionHash,
		TriggeredSeedCount: triggeredSeedCount,
		AttemptCount:       len(ledger.Attempts),
		AcceptedCount:      accepted,
		RejectedCount:      rejected,
		RejectReasonCounts: rejectCounts,
		AcceptReasonCounts: acceptCounts,
		CandidateLimit:     cfg.CandidateLimit,
		MapHash:            exclusionMap.MapHash,
		LedgerHash:         ledger.LedgerHash,
		ManifestHash:       manifest.ManifestHash,
		FailClosedReason:   failClosedReason,
	}
}
