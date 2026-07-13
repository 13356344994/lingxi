package personalvalidation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/exclusionmap"
)

func loadEntryProofs(dir string) (map[string]g10entryproof.EntryProof, error) {
	path := filepath.Join(dir, g10entryproof.OutputProofsFile)
	proofs, err := readProofJSONL(path, func() g10entryproof.EntryProof { return g10entryproof.EntryProof{} })
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]g10entryproof.EntryProof{}, nil
		}
		return nil, fmt.Errorf("entry proofs: %w", err)
	}
	out := make(map[string]g10entryproof.EntryProof, len(proofs))
	for _, ep := range proofs {
		if err := g10entryproof.ValidateEntryProof(ep); err != nil {
			return nil, fmt.Errorf("entry proof %s: %w", ep.TriggerID, err)
		}
		if ep.EvidenceGrade == g10entryproof.GradeExecutableShadowOrTrade && strings.TrimSpace(ep.ShadowSourceID) == "" {
			return nil, fmt.Errorf("entry proof %s: grade C requires shadow_source_id", ep.TriggerID)
		}
		out[proofKey(ep.TriggerID)] = ep
	}
	return out, nil
}

func loadExecutionPolicies(dir string) (map[string]g10executionpolicy.FrozenPolicy, map[string]g10executionpolicy.FrozenPolicy, error) {
	path := filepath.Join(dir, g10executionpolicy.OutputPoliciesFile)
	policies, err := readProofJSONL(path, func() g10executionpolicy.FrozenPolicy { return g10executionpolicy.FrozenPolicy{} })
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]g10executionpolicy.FrozenPolicy{}, map[string]g10executionpolicy.FrozenPolicy{}, nil
		}
		return nil, nil, fmt.Errorf("execution policies: %w", err)
	}
	byTrigger := make(map[string]g10executionpolicy.FrozenPolicy, len(policies))
	byHash := make(map[string]g10executionpolicy.FrozenPolicy, len(policies))
	for _, fp := range policies {
		if err := g10executionpolicy.ValidateFrozenPolicy(fp); err != nil {
			return nil, nil, fmt.Errorf("execution policy %s: %w", fp.TriggerID, err)
		}
		byTrigger[proofKey(fp.TriggerID)] = fp
		if fp.ExecutionPolicyHash != "" {
			byHash[fp.ExecutionPolicyHash] = fp
		}
	}
	return byTrigger, byHash, nil
}

func loadExitProofs(path string) (map[string]g10exitproof.Proof, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]g10exitproof.Proof{}, nil
	}
	proofs, err := readProofJSONL(path, func() g10exitproof.Proof { return g10exitproof.Proof{} })
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]g10exitproof.Proof{}, nil
		}
		return nil, fmt.Errorf("exit proofs: %w", err)
	}
	out := make(map[string]g10exitproof.Proof, len(proofs))
	for _, xp := range proofs {
		if err := g10exitproof.ValidateProof(xp); err != nil {
			return nil, fmt.Errorf("exit proof %s: %w", xp.TriggerID, err)
		}
		out[proofKey(xp.TriggerID)] = xp
	}
	return out, nil
}

func loadExclusionProofs(dir string) (map[string]exclusionmap.ExclusionProof, map[string]exclusionmap.CrowdingStress, error) {
	path := filepath.Join(dir, exclusionmap.OutputFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]exclusionmap.ExclusionProof{}, map[string]exclusionmap.CrowdingStress{}, nil
		}
		return nil, nil, err
	}
	var payload struct {
		Proofs []exclusionmap.ExclusionProof `json:"proofs"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, err
	}
	proofs := make(map[string]exclusionmap.ExclusionProof, len(payload.Proofs))
	crowding := make(map[string]exclusionmap.CrowdingStress, len(payload.Proofs))
	for _, proof := range payload.Proofs {
		if err := exclusionmap.ValidateProof(proof); err != nil {
			return nil, nil, fmt.Errorf("exclusion proof %s: %w", proof.EventID, err)
		}
		key := proofKey(proof.EventID)
		proofs[key] = proof
		crowding[key] = proof.CrowdingStress
	}
	return proofs, crowding, nil
}

func proofKey(id string) string {
	return id
}

func readProofJSONL[T any](path string, newT func() T) ([]T, error) {
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

func (b ProofBundle) entryProof(eventID string) (g10entryproof.EntryProof, bool) {
	if ep, ok := b.EntryProofs[eventID]; ok {
		return ep, true
	}
	return g10entryproof.EntryProof{}, false
}

func (b ProofBundle) executionPolicy(eventID string) (g10executionpolicy.FrozenPolicy, bool) {
	if fp, ok := b.ExecutionPolicies[eventID]; ok {
		if fp.TriggerID != "" && fp.TriggerID != eventID {
			return g10executionpolicy.FrozenPolicy{}, false
		}
		return fp, true
	}
	return g10executionpolicy.FrozenPolicy{}, false
}

// verifyExecutionPolicyHash checks playbook-bound hash integrity for one eventID.
// Hash lookup is integrity-only — never substitutes another trigger's policy.
func (b ProofBundle) verifyExecutionPolicyHash(eventID, hash string) bool {
	if hash == "" {
		return true
	}
	fp, ok := b.ExecutionPolicies[eventID]
	if !ok {
		return false
	}
	return fp.ExecutionPolicyHash == hash
}

func (b ProofBundle) exitProof(eventID string) (g10exitproof.Proof, bool) {
	if xp, ok := b.ExitProofs[eventID]; ok {
		return xp, true
	}
	return g10exitproof.Proof{}, false
}

func (b ProofBundle) crowdingStress(eventID string) (exclusionmap.CrowdingStress, bool) {
	if cs, ok := b.ExclusionCrowding[eventID]; ok {
		return cs, true
	}
	return exclusionmap.CrowdingStress{}, false
}

func (b ProofBundle) exclusionProof(eventID string) (exclusionmap.ExclusionProof, bool) {
	if ep, ok := b.ExclusionProofs[eventID]; ok {
		return ep, true
	}
	return exclusionmap.ExclusionProof{}, false
}
