package reversemine

import (
	"errors"
	"fmt"
	"math"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

// CanonicalCandidateHash hashes one candidate rule identity.
func CanonicalCandidateHash(c Candidate) (string, error) {
	payload := struct {
		ProfileID   string      `json:"profile_id"`
		CellKey     interface{} `json:"cell_key"`
		Conditions  []Condition `json:"conditions"`
		SearchSpace string      `json:"search_space_id"`
		Algorithm   string      `json:"derivation_algorithm_version"`
	}{
		ProfileID: c.ProfileID, CellKey: c.CellKey,
		Conditions: c.Conditions, SearchSpace: protocol.ReverseMineSearchSpaceID,
		Algorithm: protocol.DerivationAlgorithmVersion,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateCandidate checks candidate invariants.
func ValidateCandidate(c Candidate) error {
	if c.ProfileID == "" {
		return errors.New("reversemine: candidate missing profile")
	}
	if c.Status != CandidateStatus {
		return fmt.Errorf("reversemine: candidate status must be %s", CandidateStatus)
	}
	if err := protocol.ValidateReverseMineBinding(c.SearchSpaceID, c.DerivationAlgorithm); err != nil {
		return fmt.Errorf("reversemine: candidate binding: %w", err)
	}
	n := len(c.Conditions)
	if n < protocol.MinFeaturesPerRule || n > protocol.MaxFeaturesPerRule {
		return fmt.Errorf("reversemine: candidate must have %d..%d conditions", protocol.MinFeaturesPerRule, protocol.MaxFeaturesPerRule)
	}
	seen := map[string]bool{}
	for _, cond := range c.Conditions {
		if err := validateFeatureKey(cond.FeatureKey); err != nil {
			return err
		}
		if seen[cond.FeatureKey] {
			return fmt.Errorf("reversemine: duplicate feature %s", cond.FeatureKey)
		}
		seen[cond.FeatureKey] = true
		if cond.Op != OpLTE && cond.Op != OpGTE {
			return fmt.Errorf("reversemine: invalid operator %q", cond.Op)
		}
		if !isFinite(cond.Threshold) {
			return errors.New("reversemine: threshold must be finite")
		}
	}
	if c.CanonicalHash == "" {
		return errors.New("reversemine: candidate missing canonical hash")
	}
	want, err := CanonicalCandidateHash(c)
	if err != nil {
		return err
	}
	if c.CanonicalHash != want {
		return errors.New("reversemine: candidate canonical hash mismatch")
	}
	phenomenonID, err := CandidatePhenomenonID(c)
	if err != nil {
		return err
	}
	if c.PhenomenonID == "" || c.PhenomenonID != phenomenonID {
		return errors.New("reversemine: candidate phenomenon id mismatch")
	}
	return nil
}

// Fires reports whether candidate matches a validation event using only
// decision-before features and exact cell dimensions.
func (c Candidate) Fires(event ValidationEvent) (bool, bool) {
	if event.ProfileID != c.ProfileID {
		return false, false
	}
	if event.CellKey != c.CellKey {
		return false, false
	}
	if len(c.Conditions) == 0 {
		return false, false
	}
	hasFeature := false
	for _, cond := range c.Conditions {
		if IsForbiddenFeatureKey(cond.FeatureKey) {
			return false, false
		}
		v, ok := event.Features[cond.FeatureKey]
		if !ok || !isFinite(v) {
			return false, false
		}
		hasFeature = true
		switch cond.Op {
		case OpLTE:
			if v > cond.Threshold {
				return false, hasFeature
			}
		case OpGTE:
			if v < cond.Threshold {
				return false, hasFeature
			}
		default:
			return false, hasFeature
		}
	}
	return true, hasFeature
}

// RejectForbiddenEventFeatures returns an error if event carries forbidden inputs.
func RejectForbiddenEventFeatures(features map[string]float64) error {
	for key := range features {
		if IsForbiddenFeatureKey(key) {
			return fmt.Errorf("reversemine: forbidden validation feature %q", key)
		}
	}
	return nil
}

// HashCandidates commits the ordered candidate list.
func HashCandidates(candidates []Candidate) (string, error) {
	hash, _, err := datatruth.HashJSON(candidates)
	return hash, err
}

func buildCandidate(
	ordinal int,
	cell CellWorkItem,
	conditions []Condition,
	sourceCount int,
	sourceHash string,
) (Candidate, error) {
	c := Candidate{
		Ordinal:             ordinal,
		ProfileID:           cell.ProfileID,
		CellKey:             cell.Key,
		Conditions:          append([]Condition(nil), conditions...),
		Status:              CandidateStatus,
		SourceWinnerCount:   sourceCount,
		SourceWinnerIDsHash: sourceHash,
		MapManifestHash:     cell.ManifestHash,
		WinnerSeedMode:      WinnerSeedMode,
		SearchSpaceID:       protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm: protocol.DerivationAlgorithmVersion,
	}
	phenomenonID, err := CandidatePhenomenonID(c)
	if err != nil {
		return Candidate{}, err
	}
	c.PhenomenonID = phenomenonID
	hash, err := CanonicalCandidateHash(c)
	if err != nil {
		return Candidate{}, err
	}
	c.CanonicalHash = hash
	return c, nil
}

// CandidatePhenomenonID applies the protocol identity, intentionally ignoring
// capital tier, liquidity bucket, and symbol class.
func CandidatePhenomenonID(c Candidate) (string, error) {
	conditions := make([]protocol.PhenomenonCondition, len(c.Conditions))
	for i, condition := range c.Conditions {
		conditions[i] = protocol.PhenomenonCondition{
			FeatureKey: condition.FeatureKey,
			Op:         string(condition.Op),
			Threshold:  condition.Threshold,
			Quantile:   condition.Quantile,
		}
	}
	return protocol.PhenomenonID(protocol.PhenomenonIdentity{
		ProfileID:       c.ProfileID,
		SkyCoarseMarket: c.CellKey.SkyCoarse,
		Horizon:         c.CellKey.Horizon,
		Direction:       c.CellKey.Direction,
		Conditions:      conditions,
	})
}

func validateConditionsFinite(conditions []Condition) bool {
	for _, cond := range conditions {
		if !isFinite(cond.Threshold) {
			return false
		}
	}
	return len(conditions) > 0 && conditions[0].Op != ""
}

func quantileAllowed(q float64) bool {
	for _, allowed := range protocol.ThresholdQuantiles {
		if math.Abs(allowed-q) < 1e-12 {
			return true
		}
	}
	return false
}
