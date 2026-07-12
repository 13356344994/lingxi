package personalplaybook

import (
	"errors"
	"fmt"

	"lingxi/internal/routerg10/exclusionmap"
)

// BindExclusionAxisHashes seals institution/retail/bot axis proof hashes from narrative evidence.
func BindExclusionAxisHashes(narrative ExclusionNarrative) (inst, retail, bot string, err error) {
	if narrative.InstitutionExcludeReason == "" || narrative.RetailExcludeReason == "" || narrative.BotExcludeReason == "" {
		return "", "", "", errors.New("personalplaybook: triple exclusion narrative incomplete")
	}
	inst, err = exclusionmap.HashAxisEvidence(exclusionmap.AxisInstitution, narrative.InstitutionExcludeReason)
	if err != nil {
		return "", "", "", fmt.Errorf("personalplaybook: institution axis: %w", err)
	}
	retail, err = exclusionmap.HashAxisEvidence(exclusionmap.AxisRetail, narrative.RetailExcludeReason)
	if err != nil {
		return "", "", "", fmt.Errorf("personalplaybook: retail axis: %w", err)
	}
	bot, err = exclusionmap.HashAxisEvidence(exclusionmap.AxisBot, narrative.BotExcludeReason)
	if err != nil {
		return "", "", "", fmt.Errorf("personalplaybook: bot axis: %w", err)
	}
	return inst, retail, bot, nil
}

// ValidateExclusionAccepted checks sealed three-axis proof hashes — not narrative strings alone.
func ValidateExclusionAccepted(binding ProofBinding, narrative ExclusionNarrative) error {
	if binding.ExclusionProofHash == "" {
		return errors.New("personalplaybook: missing exclusion_proof_hash")
	}
	if binding.InstitutionAxisHash == "" || binding.RetailAxisHash == "" || binding.BotAxisHash == "" {
		return errors.New("personalplaybook: missing triple axis proof hashes")
	}
	wantInst, wantRetail, wantBot, err := BindExclusionAxisHashes(narrative)
	if err != nil {
		return err
	}
	if binding.InstitutionAxisHash != wantInst {
		return errors.New("personalplaybook: institution axis hash mismatch")
	}
	if binding.RetailAxisHash != wantRetail {
		return errors.New("personalplaybook: retail axis hash mismatch")
	}
	if binding.BotAxisHash != wantBot {
		return errors.New("personalplaybook: bot axis hash mismatch")
	}
	return nil
}
