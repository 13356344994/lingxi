package personalvalidation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/announcements"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg10/reversetapes"
	"lingxi/internal/routerg6/reverseevent"
)

// LoadPlaybookBundle reads and verifies sealed personal playbooks outputs.
func LoadPlaybookBundle(dir string) (PlaybookBundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return PlaybookBundle{}, err
	}
	manifest, err := personalplaybook.LoadManifest(filepath.Join(abs, personalplaybook.ManifestFileName))
	if err != nil {
		return PlaybookBundle{}, fmt.Errorf("personalvalidation: manifest: %w", err)
	}
	if err := personalplaybook.VerifyOutputArtifacts(abs, manifest); err != nil {
		return PlaybookBundle{}, err
	}
	raw, err := os.ReadFile(filepath.Join(abs, personalplaybook.OutputPlaybooksFile))
	if err != nil {
		return PlaybookBundle{}, err
	}
	var payload struct {
		Playbooks []personalplaybook.PersonalWildPlaybook `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PlaybookBundle{}, err
	}
	raw, err = os.ReadFile(filepath.Join(abs, personalplaybook.LedgerFileName))
	if err != nil {
		return PlaybookBundle{}, err
	}
	var ledger personalplaybook.ExperimentLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return PlaybookBundle{}, err
	}
	if err := personalplaybook.ValidateLedger(ledger); err != nil {
		return PlaybookBundle{}, err
	}
	certified := make([]personalplaybook.PersonalWildPlaybook, 0, len(payload.Playbooks))
	for _, pb := range payload.Playbooks {
		if pb.Status != personalplaybook.StatusPersonalWildCandidate {
			continue
		}
		if err := personalplaybook.ValidatePlaybook(pb); err != nil {
			return PlaybookBundle{}, fmt.Errorf("playbook %s: %w", pb.PlaybookHash, err)
		}
		certified = append(certified, pb)
	}
	return PlaybookBundle{
		Dir: abs, Manifest: manifest, Playbooks: certified, Ledger: ledger,
	}, nil
}

// LoadProofBundle verifies constitution, announcements, and reverse-tapes binding.
func LoadProofBundle(cfg RunConfig) (ProofBundle, string, map[string]string, error) {
	constHash, err := g10constitution.Hash()
	if err != nil {
		return ProofBundle{}, "", nil, err
	}
	if cfg.ConstitutionHash != "" && cfg.ConstitutionHash != constHash {
		return ProofBundle{}, "", nil, errors.New("personalvalidation: constitution hash mismatch")
	}

	var annItems []announcements.Announcement
	annIncomplete := false
	if cfg.AnnouncementDir != "" {
		env, err := exclusionmap.LoadAnnouncementEnvelope(cfg.AnnouncementDir)
		if err != nil {
			return ProofBundle{}, "", nil, fmt.Errorf("announcements: %w", err)
		}
		if env.Incomplete {
			return ProofBundle{}, "", nil, errors.New("personalvalidation: incomplete announcement archive fail-closed")
		}
		if cfg.AnnouncementManifestHash != "" && env.ManifestHash != cfg.AnnouncementManifestHash {
			return ProofBundle{}, "", nil, errors.New("personalvalidation: announcement manifest hash mismatch")
		}
		annItems = env.PITItems()
		annIncomplete = env.Incomplete
	}

	entryProofs := map[string]g10entryproof.EntryProof{}
	if cfg.EntryProofDir != "" {
		entryProofs, err = loadEntryProofs(cfg.EntryProofDir)
		if err != nil {
			return ProofBundle{}, "", nil, err
		}
	}
	execByTrigger := map[string]g10executionpolicy.FrozenPolicy{}
	execByHash := map[string]g10executionpolicy.FrozenPolicy{}
	if cfg.ExecutionPolicyDir != "" {
		execByTrigger, execByHash, err = loadExecutionPolicies(cfg.ExecutionPolicyDir)
		if err != nil {
			return ProofBundle{}, "", nil, err
		}
	}
	exitProofs := map[string]g10exitproof.Proof{}
	if cfg.ExitProofPath != "" {
		exitProofs, err = loadExitProofs(cfg.ExitProofPath)
		if err != nil {
			return ProofBundle{}, "", nil, err
		}
	}
	exclusionProofs := map[string]exclusionmap.ExclusionProof{}
	exclusionCrowding := map[string]exclusionmap.CrowdingStress{}
	if cfg.ExclusionMapDir != "" {
		exclusionProofs, exclusionCrowding, err = loadExclusionProofs(cfg.ExclusionMapDir)
		if err != nil {
			return ProofBundle{}, "", nil, fmt.Errorf("exclusion map: %w", err)
		}
	}

	mapHashes := map[string]string{}
	if cfg.ReverseTapesDir != "" {
		rtManifestPath := filepath.Join(cfg.ReverseTapesDir, reversetapes.ManifestFileName)
		raw, err := os.ReadFile(rtManifestPath)
		if err != nil {
			return ProofBundle{}, "", nil, fmt.Errorf("reverse tapes manifest: %w", err)
		}
		var rtManifest reversetapes.TapeManifest
		if err := json.Unmarshal(raw, &rtManifest); err != nil {
			return ProofBundle{}, "", nil, err
		}
		if err := reversetapes.ValidateManifest(rtManifest); err != nil {
			return ProofBundle{}, "", nil, err
		}
		if cfg.ReverseTapesManifestHash != "" && rtManifest.ManifestHash != cfg.ReverseTapesManifestHash {
			return ProofBundle{}, "", nil, errors.New("personalvalidation: reverse tapes manifest hash mismatch")
		}
		if rtManifest.ConstitutionHash != constHash {
			return ProofBundle{}, "", nil, errors.New("personalvalidation: reverse tapes constitution mismatch")
		}
		mapHashes = rtManifest.MapManifestHashes
	}

	return ProofBundle{
		Announcements:           annItems,
		ConstitutionHash:        constHash,
		EntryProofs:             entryProofs,
		ExecutionPolicies:       execByTrigger,
		ExecutionPoliciesByHash: execByHash,
		ExitProofs:              exitProofs,
		ExclusionCrowding:       exclusionCrowding,
		ExclusionProofs:         exclusionProofs,
		AnnouncementIncomplete:  annIncomplete,
	}, constHash, mapHashes, nil
}

// VerifyBindings checks playbook/map/catalog/profile consistency.
func VerifyBindings(
	cfg RunConfig,
	catalog historydata.HistoryCatalog,
	bundle PlaybookBundle,
	mapManifestHashes map[string]string,
) error {
	if cfg.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("personalvalidation: config protocol mismatch")
	}
	if cfg.HistoryDataHash != g10protocolv2.HistoryDataHash || catalog.DataHash != g10protocolv2.HistoryDataHash {
		return errors.New("personalvalidation: history data hash mismatch")
	}
	if bundle.Manifest.ManifestHash != cfg.PlaybookManifestHash {
		return errors.New("personalvalidation: playbook manifest hash mismatch")
	}
	if bundle.Manifest.ConstitutionHash != cfg.ConstitutionHash {
		return errors.New("personalvalidation: constitution hash mismatch in manifest")
	}
	got, err := personalplaybook.HashPlaybooks(bundle.Playbooks)
	if err != nil {
		return err
	}
	if got != bundle.Manifest.PlaybookHash {
		return errors.New("personalvalidation: playbooks hash mismatch")
	}
	if bundle.Manifest.ExperimentHash != bundle.Ledger.LedgerHash {
		return errors.New("personalvalidation: experiment hash mismatch")
	}
	for profile, want := range mapManifestHashes {
		if want == "" {
			continue
		}
		found := false
		for _, dir := range cfg.MapDirs {
			bundle, err := reverseevent.LoadMapBundle(dir)
			if err != nil {
				return fmt.Errorf("personalvalidation: map bundle %s: %w", dir, err)
			}
			if bundle.Manifest.ProfileID != profile {
				continue
			}
			if bundle.Manifest.ManifestHash != want {
				return fmt.Errorf("personalvalidation: map manifest hash mismatch for profile %q", profile)
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("personalvalidation: missing map-dir for profile %q", profile)
		}
	}
	for _, dir := range cfg.MapDirs {
		bundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return fmt.Errorf("personalvalidation: map bundle %s: %w", dir, err)
		}
		want, ok := mapManifestHashes[bundle.Manifest.ProfileID]
		if !ok || want == "" {
			return fmt.Errorf("personalvalidation: unexpected map profile %q not in reverse tapes manifest", bundle.Manifest.ProfileID)
		}
		if bundle.Manifest.ManifestHash != want {
			return fmt.Errorf("personalvalidation: map manifest hash mismatch for profile %q", bundle.Manifest.ProfileID)
		}
	}
	return nil
}

// VerifyFormalHistoryScope rejects truncated smoke inputs for formal validation.
func VerifyFormalHistoryScope(catalog historydata.HistoryCatalog, maxFiles int, allowTruncatedSmoke bool, profileIDs []string) error {
	if allowTruncatedSmoke {
		return nil
	}
	for _, profileID := range profileIDs {
		if err := RequireFullCatalogSelection(catalog, profileID, maxFiles, allowTruncatedSmoke); err != nil {
			return err
		}
	}
	return nil
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.PlaybookOutputDir == "" || c.OutputDir == "" {
		return errors.New("history_root, playbook_output_dir, and output_dir are required")
	}
	if c.PlaybookManifestHash == "" {
		return errors.New("playbook_manifest_hash is required")
	}
	if c.ConstitutionHash == "" {
		return errors.New("constitution_hash is required")
	}
	if c.ReverseTapesDir == "" || c.ReverseTapesManifestHash == "" {
		return errors.New("reverse_tapes_dir and reverse_tapes_manifest_hash are required")
	}
	if c.AnnouncementDir == "" || c.AnnouncementManifestHash == "" {
		return errors.New("announcement_dir and announcement_manifest_hash are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("at least one map-dir is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("max_files and max_lines must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("progress_every must be positive")
	}
	return c.ValidateFormalProofBindings()
}

// ValidateFormalProofBindings requires bound proof artifacts for formal validation runs.
func (c RunConfig) ValidateFormalProofBindings() error {
	if c.AllowTruncatedSmoke {
		return nil
	}
	if c.EntryProofDir == "" {
		return errors.New("entry_proof_dir is required for formal validation")
	}
	if c.ExecutionPolicyDir == "" {
		return errors.New("execution_policy_dir is required for formal validation")
	}
	if c.ExitProofPath == "" {
		return errors.New("exit_proof_path is required for formal validation")
	}
	if c.ExclusionMapDir == "" {
		return errors.New("exclusion_map_dir is required for formal validation")
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

func uniqueProfileIDs(playbooks []personalplaybook.PersonalWildPlaybook) []string {
	seen := map[string]bool{}
	var out []string
	for _, pb := range playbooks {
		if pb.ProfileID == "" || seen[pb.ProfileID] {
			continue
		}
		seen[pb.ProfileID] = true
		out = append(out, pb.ProfileID)
	}
	return out
}
