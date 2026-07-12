package exclusionmap

import (
	"errors"
	"fmt"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// BuildProof seals one evaluation into an ExclusionProof artifact.
func BuildProof(item EventWorkItem, result EvaluationResult, constitutionHash string) (ExclusionProof, error) {
	proof := ExclusionProof{
		SchemaVersion:    SchemaVersion,
		Generation:       Generation,
		ProtocolHash:     g10protocolv2.RouterProtocolHash,
		ConstitutionHash: constitutionHash,
		EventID:          item.Event.EventID,
		Symbol:           item.Event.Symbol,
		ProfileID:        item.ProfileID,
		EventFamily:      item.Event.EventFamily,
		Horizon:          result.Horizon,
		Direction:        result.Direction,
		DelayBars:        result.DelayBars,
		DecisionTime:     item.Event.DecisionTime,
		ShockCluster:     item.ShockCluster,
		Institution:      result.Institution,
		Retail:           result.Retail,
		Bot:              result.Bot,
		CrowdingStress:   result.Crowding,
		ForbiddenHits:    append([]string(nil), result.ForbiddenHits...),
		Accepted:         result.Accepted,
		AcceptReason:     result.AcceptReason,
		RejectReasons:    append([]string(nil), result.RejectReasons...),
		Provenance:       result.Provenance,
	}
	if proof.Provenance.ConstitutionHash == "" {
		proof.Provenance.ConstitutionHash = constitutionHash
	}
	hash, err := hashProof(proof)
	if err != nil {
		return ExclusionProof{}, err
	}
	proof.ExclusionProofHash = hash
	return proof, nil
}

func hashProof(proof ExclusionProof) (string, error) {
	payload := proof
	payload.ExclusionProofHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateProof checks one proof self-hash.
func ValidateProof(proof ExclusionProof) error {
	want, err := hashProof(proof)
	if err != nil {
		return err
	}
	if proof.ExclusionProofHash == "" || proof.ExclusionProofHash != want {
		return errors.New("exclusionmap: exclusion proof hash mismatch (tamper/drift)")
	}
	return nil
}

// HashAxis seals one axis exclusion artifact (metric bindings + primary evidence).
func HashAxis(axis AxisExclusion) (string, error) {
	hash, _, err := datatruth.HashJSON(axis)
	return hash, err
}

// HashAxisEvidence seals a minimal axis proof from frozen primary evidence text.
func HashAxisEvidence(axis, primaryEvidence string) (string, error) {
	payload := struct {
		Axis            string `json:"axis"`
		PrimaryEvidence string `json:"primary_evidence"`
	}{
		Axis:            axis,
		PrimaryEvidence: primaryEvidence,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateTripleAxisAccepted verifies sealed three-axis acceptance — not narrative strings alone.
func ValidateTripleAxisAccepted(proof ExclusionProof) error {
	if err := ValidateProof(proof); err != nil {
		return err
	}
	if !proof.Accepted {
		return errors.New("exclusionmap: exclusion proof not accepted")
	}
	if !tripleAxisComplete(proof.Institution, proof.Retail, proof.Bot) {
		return errors.New("exclusionmap: triple axis incomplete")
	}
	for _, axis := range []AxisExclusion{proof.Institution, proof.Retail, proof.Bot} {
		if axis.PrimaryEvidence == "" {
			return fmt.Errorf("exclusionmap: axis %q missing primary evidence", axis.Axis)
		}
		if _, err := HashAxis(axis); err != nil {
			return fmt.Errorf("exclusionmap: axis %q hash: %w", axis.Axis, err)
		}
	}
	return nil
}

// HashExclusionMap seals the triple exclusion map artifact.
func HashExclusionMap(m TripleExclusionMap) (string, error) {
	payload := m
	payload.MapHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildExclusionMap seals accepted proofs into one map artifact.
func BuildExclusionMap(cfg RunConfig, constitutionHash string, proofs []ExclusionProof) (TripleExclusionMap, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return TripleExclusionMap{}, err
	}
	m := TripleExclusionMap{
		SchemaVersion:    SchemaVersion,
		Generation:       Generation,
		ProtocolHash:     g10protocolv2.RouterProtocolHash,
		ConstitutionHash: constitutionHash,
		BuiltAt:          time.Now().UTC(),
		ConfigHash:       cfgHash,
		Proofs:           append([]ExclusionProof(nil), proofs...),
	}
	hash, err := HashExclusionMap(m)
	if err != nil {
		return TripleExclusionMap{}, err
	}
	m.MapHash = hash
	return m, nil
}

// LoadExclusionMap reads triple_exclusion_map.json with hash verification.
func LoadExclusionMap(path string) (TripleExclusionMap, error) {
	m, err := loadJSONFile(path, func() TripleExclusionMap { return TripleExclusionMap{} })
	if err != nil {
		return TripleExclusionMap{}, err
	}
	want, err := HashExclusionMap(m)
	if err != nil {
		return TripleExclusionMap{}, err
	}
	if m.MapHash == "" || m.MapHash != want {
		return TripleExclusionMap{}, fmt.Errorf("exclusionmap: map hash mismatch")
	}
	return m, nil
}

func loadJSONFile[T any](path string, newT func() T) (T, error) {
	var zero T
	raw, err := readFile(path)
	if err != nil {
		return zero, err
	}
	out := newT()
	if err := unmarshalJSON(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}
