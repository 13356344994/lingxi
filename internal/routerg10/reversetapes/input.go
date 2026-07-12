package reversetapes

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/exclusionmap"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/reverseevent"
)

// LoadInputBundle reads and verifies all upstream artifacts with hash binding.
func LoadInputBundle(cfg RunConfig) (InputBundle, error) {
	constitutionHash, err := exclusionmap.ConstitutionHash()
	if err != nil {
		return InputBundle{}, err
	}

	exclusionMapPath := filepath.Join(cfg.ExclusionMapDir, exclusionmap.OutputFileName)
	exclusionMap, err := exclusionmap.LoadExclusionMap(exclusionMapPath)
	if err != nil {
		return InputBundle{}, fmt.Errorf("reversetapes: exclusion map: %w", err)
	}
	exclusionManifestPath := filepath.Join(cfg.ExclusionMapDir, exclusionmap.ManifestFileName)
	exclusionManifest, err := loadExclusionManifest(exclusionManifestPath)
	if err != nil {
		return InputBundle{}, err
	}
	if err := exclusionmap.ValidateManifest(exclusionManifest); err != nil {
		return InputBundle{}, err
	}
	if exclusionManifest.MapHash != exclusionMap.MapHash {
		return InputBundle{}, errors.New("reversetapes: exclusion map/manifest hash mismatch")
	}

	entryManifestPath := filepath.Join(cfg.EntryProofDir, g10entryproof.OutputManifestFile)
	entryManifest, err := loadEntryManifest(entryManifestPath)
	if err != nil {
		return InputBundle{}, err
	}
	if err := g10entryproof.ValidateManifest(entryManifest); err != nil {
		return InputBundle{}, err
	}
	entryProofs, err := loadEntryProofs(filepath.Join(cfg.EntryProofDir, g10entryproof.OutputProofsFile))
	if err != nil {
		return InputBundle{}, err
	}

	execManifestPath := filepath.Join(cfg.ExecutionPolicyDir, g10executionpolicy.OutputManifestFile)
	execManifest, err := loadExecutionManifest(execManifestPath)
	if err != nil {
		return InputBundle{}, err
	}
	if err := g10executionpolicy.ValidateManifest(execManifest); err != nil {
		return InputBundle{}, err
	}
	policies, err := loadExecutionPolicies(filepath.Join(cfg.ExecutionPolicyDir, g10executionpolicy.OutputPoliciesFile))
	if err != nil {
		return InputBundle{}, err
	}

	exitProofs, err := loadExitProofs(cfg.ExitProofPath)
	if err != nil {
		return InputBundle{}, err
	}

	ann, err := exclusionmap.LoadAnnouncementEnvelope(cfg.AnnouncementDir)
	if err != nil {
		return InputBundle{}, fmt.Errorf("reversetapes: announcements: %w", err)
	}

	mapManifestHashes := map[string]string{}
	outcomeLabels := map[string]OutcomeLabel{}
	if len(cfg.MapDirs) > 0 {
		bundles, manifests, _, err := exclusionmap.LoadMapBundles(cfg.MapDirs)
		if err != nil {
			return InputBundle{}, err
		}
		for pid, hash := range manifests {
			mapManifestHashes[pid] = hash
		}
		for _, bundle := range bundles {
			labels, err := collectOutcomeLabels(bundle)
			if err != nil {
				return InputBundle{}, err
			}
			for id, label := range labels {
				outcomeLabels[id] = label
			}
		}
	}

	return InputBundle{
		ExclusionMap:         exclusionMap,
		ExclusionManifest:    exclusionManifest,
		EntryProofs:          entryProofs,
		EntryManifest:        entryManifest,
		ExecutionPolicies:    policies,
		ExecutionManifest:    execManifest,
		ExitProofs:           exitProofs,
		Announcements:        FromExclusionEnvelope(ann),
		OutcomeLabels:        outcomeLabels,
		MapManifestHashes:    mapManifestHashes,
		ConstitutionHash:     constitutionHash,
	}, validateInputBundleStrict(exclusionMap, exclusionManifest, entryProofs, entryManifest, policies, execManifest, exitProofs)
}

func validateInputBundleStrict(
	exclusionMap exclusionmap.TripleExclusionMap,
	exclusionManifest exclusionmap.MapManifest,
	entryProofs map[string]g10entryproof.EntryProof,
	entryManifest g10entryproof.Manifest,
	policies map[string]g10executionpolicy.FrozenPolicy,
	execManifest g10executionpolicy.Manifest,
	exitProofs map[string]g10exitproof.Proof,
) error {
	if exclusionManifest.MapHash != exclusionMap.MapHash {
		return errors.New("reversetapes: exclusion manifest/map hash mismatch")
	}
	if entryManifest.ProofCount != len(entryProofs) {
		return fmt.Errorf("reversetapes: entry proof count mismatch manifest=%d loaded=%d", entryManifest.ProofCount, len(entryProofs))
	}
	if execManifest.PolicyCount != len(policies) {
		return fmt.Errorf("reversetapes: execution policy count mismatch manifest=%d loaded=%d", execManifest.PolicyCount, len(policies))
	}
	if execManifest.AllTriggersDenominator <= 0 {
		return errors.New("reversetapes: execution manifest all_triggers_denominator required")
	}
	for _, ep := range entryProofs {
		if ep.CertificationOK && ep.EvidenceArtifactHash == "" {
			return fmt.Errorf("reversetapes: certified entry %s missing artifact hash", ep.TriggerID)
		}
	}
	for _, fp := range policies {
		if err := g10executionpolicy.ValidateFrozenPolicy(fp); err != nil {
			return fmt.Errorf("reversetapes: policy %s: %w", fp.TriggerID, err)
		}
	}
	for _, xp := range exitProofs {
		if xp.ReplaySource != g10exitproof.ReplaySourceHistorical {
			return fmt.Errorf("reversetapes: exit proof %s missing historical replay source", xp.TriggerID)
		}
	}
	return nil
}

func loadExclusionManifest(path string) (exclusionmap.MapManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return exclusionmap.MapManifest{}, err
	}
	var m exclusionmap.MapManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return exclusionmap.MapManifest{}, err
	}
	return m, nil
}

func loadEntryManifest(path string) (g10entryproof.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return g10entryproof.Manifest{}, err
	}
	var m g10entryproof.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return g10entryproof.Manifest{}, err
	}
	return m, nil
}

func loadExecutionManifest(path string) (g10executionpolicy.Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return g10executionpolicy.Manifest{}, err
	}
	var m g10executionpolicy.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return g10executionpolicy.Manifest{}, err
	}
	return m, nil
}

func loadEntryProofs(path string) (map[string]g10entryproof.EntryProof, error) {
	proofs, err := readJSONL(path, func() g10entryproof.EntryProof { return g10entryproof.EntryProof{} })
	if err != nil {
		return nil, err
	}
	out := make(map[string]g10entryproof.EntryProof, len(proofs))
	for _, ep := range proofs {
		if err := g10entryproof.ValidateEntryProof(ep); err != nil {
			return nil, fmt.Errorf("reversetapes: entry proof %s: %w", ep.TriggerID, err)
		}
		key := proofKey(ep.TriggerID)
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("reversetapes: duplicate entry proof %s", key)
		}
		out[key] = ep
	}
	return out, nil
}

func loadExecutionPolicies(path string) (map[string]g10executionpolicy.FrozenPolicy, error) {
	policies, err := readJSONL(path, func() g10executionpolicy.FrozenPolicy { return g10executionpolicy.FrozenPolicy{} })
	if err != nil {
		return nil, err
	}
	out := make(map[string]g10executionpolicy.FrozenPolicy, len(policies))
	for _, fp := range policies {
		if err := g10executionpolicy.ValidateFrozenPolicy(fp); err != nil {
			return nil, fmt.Errorf("reversetapes: execution policy %s: %w", fp.TriggerID, err)
		}
		key := proofKey(fp.TriggerID)
		if _, ok := out[key]; ok {
			return nil, fmt.Errorf("reversetapes: duplicate execution policy %s", key)
		}
		out[key] = fp
	}
	return out, nil
}

func loadExitProofs(path string) (map[string]g10exitproof.Proof, error) {
	if path == "" {
		return map[string]g10exitproof.Proof{}, nil
	}
	proofs, err := readJSONL(path, func() g10exitproof.Proof { return g10exitproof.Proof{} })
	if err != nil {
		return nil, err
	}
	out := make(map[string]g10exitproof.Proof, len(proofs))
	for _, xp := range proofs {
		if err := g10exitproof.ValidateProof(xp); err != nil {
			return nil, fmt.Errorf("reversetapes: exit proof %s: %w", xp.TriggerID, err)
		}
		key := proofKey(xp.TriggerID)
		out[key] = xp
	}
	return out, nil
}

func collectOutcomeLabels(bundle reverseevent.MapBundle) (map[string]OutcomeLabel, error) {
	out := map[string]OutcomeLabel{}
	for _, cell := range bundle.Discovery.Cells {
		for _, winner := range cell.Winners {
			label := OutcomeLabel{
				ConservativeNetBPS: winner.ConservativeBPS,
				GrossBPS:             winner.ConservativeBPS + 10,
				LabelOnly:            true,
			}
			out[proofKey(winner.EventID)] = label
		}
	}
	return out, nil
}

func proofKey(id string) string {
	return id
}

func readJSONL[T any](path string, newT func() T) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		item := newT()
		if err := json.Unmarshal(sc.Bytes(), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, sc.Err()
}

// CollectAcceptedSeeds returns accepted exclusion proofs scheduled for reverse reconstruction.
func CollectAcceptedSeeds(bundle InputBundle) []exclusionmap.ExclusionProof {
	var seeds []exclusionmap.ExclusionProof
	for _, proof := range bundle.ExclusionMap.Proofs {
		if !proof.Accepted {
			continue
		}
		if err := exclusionmap.ValidateProof(proof); err != nil {
			continue
		}
		seeds = append(seeds, proof)
	}
	return seeds
}

// BindProofs resolves upstream proofs for one exclusion seed by event_id.
func BindProofs(bundle InputBundle, proof exclusionmap.ExclusionProof) (EvaluationInput, []string, error) {
	key := proofKey(proof.EventID)
	var missing []string

	entry, ok := bundle.EntryProofs[key]
	if !ok {
		// fallback: trigger_id may differ; try event_id as trigger alias
		for _, ep := range bundle.EntryProofs {
			if ep.TriggerID == proof.EventID {
				entry = ep
				ok = true
				break
			}
		}
	}
	if !ok {
		missing = append(missing, "entry_proof")
	}

	exec, okExec := bundle.ExecutionPolicies[key]
	if !okExec {
		for _, fp := range bundle.ExecutionPolicies {
			if fp.TriggerID == proof.EventID {
				exec = fp
				okExec = true
				break
			}
		}
	}
	if !okExec {
		missing = append(missing, "execution_policy")
	}

	exit, okExit := bundle.ExitProofs[key]
	if !okExit {
		for _, xp := range bundle.ExitProofs {
			if xp.TriggerID == proof.EventID {
				exit = xp
				okExit = true
				break
			}
		}
	}
	if !okExit {
		missing = append(missing, "exit_proof")
	}

	if len(missing) > 0 {
		return EvaluationInput{}, missing, nil
	}

	if entry.EntryProofHash == "" || exec.ExecutionPolicyHash == "" || exit.ExitProofHash == "" {
		return EvaluationInput{}, []string{RejectProofHashMismatch}, nil
	}
	if proof.ExclusionProofHash == "" {
		return EvaluationInput{}, []string{RejectMissingProofBinding}, nil
	}

	label := bundle.OutcomeLabels[key]
	capital := g10protocolv2.CapitalTiersUSDT[0]

	return EvaluationInput{
		Exclusion:              proof,
		Entry:                  entry,
		Execution:              exec,
		Exit:                   exit,
		Announcements:          bundle.Announcements,
		OutcomeLabel:           label,
		ConstitutionHash:       bundle.ConstitutionHash,
		CapitalUSDT:            capital,
		AllTriggersDenominator: bundle.ExecutionManifest.AllTriggersDenominator,
	}, nil, nil
}

// ClassifyPersonalTapeFamily maps exclusion evidence to personal tape families.
func ClassifyPersonalTapeFamily(proof exclusionmap.ExclusionProof) string {
	if proof.Provenance.AnnouncementTapeHash != "" {
		for _, hit := range proof.ForbiddenHits {
			if hit == exclusionmap.RejectIncompletePIT {
				return FamilyUnknown
			}
		}
		if containsEvidence(proof, "listing", "delisting", "cross_venue") {
			return FamilyAnnouncementShock
		}
	}
	if proof.Retail.PrimaryEvidence == "min_lot_nonlinearity" {
		return FamilyMinLotNonlinearity
	}
	if proof.Bot.PrimaryEvidence == "event_sparsity" || proof.AcceptReason == exclusionmap.AcceptLowMotivationLowCapacity {
		return FamilyRareDynamicAbandon
	}
	switch proof.EventFamily {
	case eventmap.FamilySessionLiquidity:
		return FamilyMechanicalFlowTail
	case eventmap.FamilyExtremeReversal:
		return FamilyMechanicalFlowTail
	}
	if proof.Institution.PrimaryEvidence == "custody_authorization_limit" ||
		proof.Retail.PrimaryEvidence == "complex_operational_discipline" {
		return FamilyPrepositionedCapital
	}
	return FamilyUnknown
}

func containsEvidence(proof exclusionmap.ExclusionProof, needles ...string) bool {
	for _, axis := range []exclusionmap.AxisExclusion{proof.Institution, proof.Retail, proof.Bot} {
		for _, needle := range needles {
			if axis.PrimaryEvidence == needle {
				return true
			}
		}
	}
	return false
}

func (c RunConfig) Validate() error {
	if c.ExclusionMapDir == "" || c.EntryProofDir == "" || c.ExecutionPolicyDir == "" ||
		c.AnnouncementDir == "" || c.OutputDir == "" {
		return errors.New("reversetapes: exclusion-map, entry-proof, execution-policy, announcement, output-dir required")
	}
	if c.CandidateLimit <= 0 {
		return errors.New("reversetapes: candidate_limit must be positive")
	}
	return nil
}

// ConfigFingerprint hashes run config for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}
