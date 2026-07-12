package reversemine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

// BuildCandidateManifest seals all bindings before validation.
func BuildCandidateManifest(
	cfg RunConfig,
	catalogHash string,
	mapManifestHashes map[string]string,
	mapArtifactHashes map[string]string,
	candidates []Candidate,
	ledger CandidateLedger,
	coverage CoverageSummary,
	coverageHash string,
) (CandidateManifest, error) {
	if err := protocol.ValidateReverseMineBinding(
		protocol.ReverseMineSearchSpaceID,
		protocol.DerivationAlgorithmVersion,
	); err != nil {
		return CandidateManifest{}, err
	}
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return CandidateManifest{}, err
	}
	candidatesHash, err := HashCandidates(candidates)
	if err != nil {
		return CandidateManifest{}, err
	}
	m := CandidateManifest{
		SchemaVersion:          SchemaVersionManifest,
		Generation:             protocol.Generation,
		ProtocolHash:           protocol.RouterProtocolHash,
		HistoryDataHash:        protocol.HistoryDataHash,
		SearchSpaceID:          protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm:    protocol.DerivationAlgorithmVersion,
		ConfigHash:             cfgHash,
		CatalogHash:            catalogHash,
		MapManifestHashes:      cloneStringMap(mapManifestHashes),
		MapArtifactHashes:      cloneStringMap(mapArtifactHashes),
		CandidateLimit:         cfg.CandidateLimit,
		ActualCandidateCount:   len(candidates),
		CandidatesHash:         candidatesHash,
		LedgerHash:             ledger.LedgerHash,
		CoverageHash:           coverageHash,
		Coverage:               coverage,
		FreezeBeforeValidation: protocol.FreezeBeforeValidation,
		ExamActions:            protocol.ExamActions,
		CreatedAt:              time.Now().UTC(),
	}
	hash, err := hashCandidateManifest(m)
	if err != nil {
		return CandidateManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashCandidateManifest(m CandidateManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateCandidateManifest checks sealed manifest integrity.
func ValidateCandidateManifest(m CandidateManifest) error {
	if m.SchemaVersion != SchemaVersionManifest {
		return errors.New("reversemine: candidate manifest schema mismatch")
	}
	if m.Generation != protocol.Generation || m.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("reversemine: candidate manifest protocol mismatch")
	}
	if m.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("reversemine: candidate manifest history hash mismatch")
	}
	if err := protocol.ValidateReverseMineBinding(m.SearchSpaceID, m.DerivationAlgorithm); err != nil {
		return fmt.Errorf("reversemine: candidate manifest binding: %w", err)
	}
	if !m.FreezeBeforeValidation {
		return errors.New("reversemine: freeze_before_validation must be true")
	}
	if m.CoverageHash == "" {
		return errors.New("reversemine: candidate manifest coverage hash missing")
	}
	computedCoverageHash, _, err := datatruth.HashJSON(m.Coverage)
	if err != nil || computedCoverageHash != m.CoverageHash {
		return errors.New("reversemine: candidate manifest coverage payload mismatch")
	}
	if m.ExamActions != protocol.ExamActions {
		return errors.New("reversemine: exam_actions must be zero")
	}
	want, err := hashCandidateManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return errors.New("reversemine: candidate manifest hash mismatch")
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeJSONAtomic(path string, payload any) error {
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

// WriteOutputs atomically writes discovery artifacts.
func WriteOutputs(outputDir string, candidates []Candidate, ledger CandidateLedger, manifest CandidateManifest, summary MineSummary) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_candidate_ledger.json"), ledger); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "candidates.json"), struct {
		Candidates  []Candidate `json:"candidates"`
		ExamActions int         `json:"exam_actions"`
	}{Candidates: candidates, ExamActions: protocol.ExamActions}); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "candidate_manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "reverse_mine_summary.json"), summary); err != nil {
		return err
	}
	return nil
}

// LoadCandidateManifest reads and verifies candidate_manifest.json.
func LoadCandidateManifest(path string) (CandidateManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return CandidateManifest{}, err
	}
	var manifest CandidateManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return CandidateManifest{}, err
	}
	if err := ValidateCandidateManifest(manifest); err != nil {
		return CandidateManifest{}, err
	}
	return manifest, nil
}

// VerifyOutputArtifacts checks on-disk artifact hashes against manifest.
func VerifyOutputArtifacts(outputDir string, manifest CandidateManifest) error {
	ledgerPath := filepath.Join(outputDir, "g4_candidate_ledger.json")
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		return fmt.Errorf("reversemine: ledger artifact: %w", err)
	}
	var ledger CandidateLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return err
	}
	if ledger.LedgerHash != manifest.LedgerHash {
		return errors.New("reversemine: ledger artifact hash mismatch")
	}
	candidatesPath := filepath.Join(outputDir, "candidates.json")
	raw, err = os.ReadFile(candidatesPath)
	if err != nil {
		return err
	}
	var payload struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	for _, candidate := range payload.Candidates {
		if err := ValidateCandidate(candidate); err != nil {
			return fmt.Errorf("reversemine: loaded candidate binding/integrity: %w", err)
		}
	}
	got, err := HashCandidates(payload.Candidates)
	if err != nil {
		return err
	}
	if got != manifest.CandidatesHash {
		return errors.New("reversemine: candidates artifact hash mismatch")
	}
	return nil
}
