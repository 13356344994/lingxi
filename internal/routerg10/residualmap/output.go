package residualmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	g10protocol "lingxi/internal/routerg10/protocol"
)

// WriteOutputs atomically writes map, ledger, manifest, and summary artifacts.
func WriteOutputs(
	outputDir string,
	residualMap PersonalResidualMap,
	ledger ExperimentLedger,
	manifest MapManifest,
	summary RunSummary,
) (map[string]string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	artifactHashes := map[string]string{}
	if err := writeJSON(filepath.Join(outputDir, OutputFileName), residualMap); err != nil {
		return nil, err
	}
	hash, err := HashFile(filepath.Join(outputDir, OutputFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[OutputFileName] = hash
	if err := writeJSON(filepath.Join(outputDir, LedgerFileName), ledger); err != nil {
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
	if err := writeJSON(filepath.Join(outputDir, SummaryFileName), summary); err != nil {
		return nil, err
	}
	hash, err = HashFile(filepath.Join(outputDir, SummaryFileName))
	if err != nil {
		return nil, err
	}
	artifactHashes[SummaryFileName] = hash
	return artifactHashes, nil
}

func writeJSON(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// BuildResidualMap seals accepted opportunities into one map artifact.
func BuildResidualMap(cfg RunConfig, fingerprintHash string, opportunities []ResidualOpportunity) (PersonalResidualMap, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return PersonalResidualMap{}, err
	}
	m := PersonalResidualMap{
		SchemaVersion:   SchemaVersion,
		Generation:      Generation,
		ProtocolHash:    g10protocol.RouterProtocolHash,
		FingerprintHash: fingerprintHash,
		BuiltAt:         time.Now().UTC(),
		ConfigHash:      cfgHash,
		Opportunities:   append([]ResidualOpportunity(nil), opportunities...),
	}
	hash, err := HashResidualMap(m)
	if err != nil {
		return PersonalResidualMap{}, err
	}
	m.MapHash = hash
	return m, nil
}

// BuildSummary aggregates one run receipt.
func BuildSummary(
	cfg RunConfig,
	fingerprintHash string,
	winnerSeedCount int,
	ledger ExperimentLedger,
	residualMap PersonalResidualMap,
	manifest MapManifest,
) RunSummary {
	rejectCounts := map[string]int{}
	accepted := 0
	rejected := 0
	for _, attempt := range ledger.Attempts {
		switch attempt.Status {
		case LedgerStatusAccepted:
			accepted++
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
		ProtocolHash:       g10protocol.RouterProtocolHash,
		FingerprintHash:    fingerprintHash,
		WinnerSeedCount:    winnerSeedCount,
		TriggeredSeedCount: winnerSeedCount,
		AttemptCount:       len(ledger.Attempts),
		AcceptedCount:      accepted,
		RejectedCount:      rejected,
		RejectReasonCounts: rejectCounts,
		CandidateLimit:     cfg.CandidateLimit,
		MapHash:            residualMap.MapHash,
		LedgerHash:         ledger.LedgerHash,
		ManifestHash:       manifest.ManifestHash,
	}
}

// LoadResidualMap reads personal_residual_map.json.
func LoadResidualMap(path string) (PersonalResidualMap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PersonalResidualMap{}, err
	}
	var m PersonalResidualMap
	if err := json.Unmarshal(raw, &m); err != nil {
		return PersonalResidualMap{}, err
	}
	want, err := HashResidualMap(m)
	if err != nil {
		return PersonalResidualMap{}, err
	}
	if m.MapHash == "" || m.MapHash != want {
		return PersonalResidualMap{}, errHashMismatch("map")
	}
	return m, nil
}

func errHashMismatch(kind string) error {
	return &hashMismatchError{kind: kind}
}

type hashMismatchError struct{ kind string }

func (e *hashMismatchError) Error() string {
	return "residualmap: " + e.kind + " hash mismatch (tamper/drift)"
}
