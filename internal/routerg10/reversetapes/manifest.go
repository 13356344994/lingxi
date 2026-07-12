package reversetapes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// BuildManifest constructs sealed manifest from run outputs.
func BuildManifest(
	cfg RunConfig,
	catalogHash, constitutionHash string,
	bundle InputBundle,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
	executableCount, discoveryCount, rejectedCount int,
) (TapeManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return TapeManifest{}, err
	}
	m := TapeManifest{
		SchemaVersion:            SchemaVersionManifest,
		Generation:               Generation,
		ProtocolHash:             g10protocolv2.RouterProtocolHash,
		HistoryDataHash:          cfg.HistoryDataHash,
		ConfigHash:               cfgHash,
		CatalogHash:              catalogHash,
		ConstitutionHash:         constitutionHash,
		ExclusionMapHash:         bundle.ExclusionMap.MapHash,
		ExclusionManifestHash:    bundle.ExclusionManifest.ManifestHash,
		EntryProofManifestHash:   bundle.EntryManifest.ManifestHash,
		ExecutionManifestHash:    bundle.ExecutionManifest.ManifestHash,
		AnnouncementTapeHash:     bundle.Announcements.TapeHash,
		AnnouncementManifestHash: bundle.Announcements.ManifestHash,
		MapManifestHashes:        copyStringMap(bundle.MapManifestHashes),
		CandidateLimit:           cfg.CandidateLimit,
		ExecutableCount:          executableCount,
		DiscoveryCount:           discoveryCount,
		RejectedCount:            rejectedCount,
		LedgerHash:               ledger.LedgerHash,
		ArtifactHashes:           copyStringMap(artifactHashes),
		CreatedAt:                time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return TapeManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m TapeManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest checks sealed manifest integrity.
func ValidateManifest(m TapeManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return fmt.Errorf("reversetapes: manifest schema %q", m.SchemaVersion)
	}
	if m.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("reversetapes: manifest protocol mismatch")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errors.New("reversetapes: manifest hash mismatch")
	}
	return nil
}

// BuildSummary constructs run summary receipt.
func BuildSummary(cfg RunConfig, constitutionHash string, ledger ExperimentLedger, manifest TapeManifest, seedCount int, failClosed string) RunSummary {
	rejectCounts := map[string]int{}
	for _, attempt := range ledger.Attempts {
		if attempt.Status == LedgerStatusRejected {
			for _, r := range attempt.RejectReasons {
				rejectCounts[r]++
			}
			if len(attempt.RejectReasons) == 0 && attempt.StatusReason != "" {
				rejectCounts[attempt.StatusReason]++
			}
		}
	}
	exec, disc, rej := 0, 0, 0
	for _, a := range ledger.Attempts {
		switch a.Status {
		case LedgerStatusAccepted:
			exec++
		case LedgerStatusDiscovery:
			disc++
		case LedgerStatusRejected:
			rej++
		}
	}
	return RunSummary{
		SchemaVersion:      SchemaVersionSummary,
		Generation:         Generation,
		ProtocolHash:       g10protocolv2.RouterProtocolHash,
		ConstitutionHash:   constitutionHash,
		SeedCount:          seedCount,
		AttemptCount:       len(ledger.Attempts),
		ExecutableCount:    exec,
		DiscoveryCount:     disc,
		RejectedCount:      rej,
		RejectReasonCounts: rejectCounts,
		CandidateLimit:     cfg.CandidateLimit,
		LedgerHash:         ledger.LedgerHash,
		ManifestHash:       manifest.ManifestHash,
		FailClosedReason:   failClosed,
	}
}

// WriteManifest atomically writes reverse_tapes_manifest.json.
func WriteManifest(outputDir string, manifest TapeManifest) error {
	return writeJSONAtomic(filepath.Join(outputDir, ManifestFileName), manifest)
}

// WriteSummary atomically writes reverse_tapes_summary.json.
func WriteSummary(outputDir string, summary RunSummary) error {
	return writeJSONAtomic(filepath.Join(outputDir, SummaryFileName), summary)
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// WriteTapesJSONL writes tape records as JSONL.
func WriteTapesJSONL(path string, tapes []ExecutableProfitTape) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, tape := range tapes {
		if err := enc.Encode(tape); err != nil {
			return err
		}
	}
	return nil
}
