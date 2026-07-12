package personalvalidation

import (
	"fmt"

	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalplaybook"
)

// ValidateExclusionFromProof verifies triple-axis metric proofs — not narrative strings alone.
func ValidateExclusionFromProof(binding personalplaybook.ProofBinding, proof exclusionmap.ExclusionProof) error {
	if binding.ExclusionProofHash == "" {
		return fmt.Errorf("personalvalidation: missing exclusion_proof_hash")
	}
	if proof.ExclusionProofHash != binding.ExclusionProofHash {
		return fmt.Errorf("personalvalidation: exclusion proof hash mismatch")
	}
	if err := exclusionmap.ValidateTripleAxisAccepted(proof); err != nil {
		return fmt.Errorf("personalvalidation: exclusion metric proof: %w", err)
	}
	wantInst, err := exclusionmap.HashAxis(proof.Institution)
	if err != nil {
		return err
	}
	wantRetail, err := exclusionmap.HashAxis(proof.Retail)
	if err != nil {
		return err
	}
	wantBot, err := exclusionmap.HashAxis(proof.Bot)
	if err != nil {
		return err
	}
	if binding.InstitutionAxisHash != "" && binding.InstitutionAxisHash != wantInst {
		return fmt.Errorf("personalvalidation: institution axis hash mismatch")
	}
	if binding.RetailAxisHash != "" && binding.RetailAxisHash != wantRetail {
		return fmt.Errorf("personalvalidation: retail axis hash mismatch")
	}
	if binding.BotAxisHash != "" && binding.BotAxisHash != wantBot {
		return fmt.Errorf("personalvalidation: bot axis hash mismatch")
	}
	return nil
}
