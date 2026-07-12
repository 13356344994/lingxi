package entryproof

import (
	"fmt"
	"strings"

	"lingxi/internal/datatruth"
)

// BuildEntryProof freezes one entry proof from a trigger at decision time.
func BuildEntryProof(tr TriggerRecord) (EntryProof, error) {
	if strings.TrimSpace(tr.TriggerID) == "" {
		return EntryProof{}, fmt.Errorf("entryproof: trigger_id required")
	}
	if tr.DecisionTime.IsZero() {
		return EntryProof{}, fmt.Errorf("entryproof: decision_time required")
	}
	venue := strings.ToLower(strings.TrimSpace(tr.Venue))
	if venue == "" {
		venue = ExecutionVenueOKX
	}
	if venue != ExecutionVenueOKX {
		return EntryProof{}, fmt.Errorf("entryproof: venue %q forbidden; OKX only", venue)
	}
	if strings.TrimSpace(tr.Symbol) == "" {
		return EntryProof{}, fmt.Errorf("entryproof: symbol required")
	}
	if strings.TrimSpace(tr.DecisionVisibleInfo) == "" {
		return EntryProof{}, fmt.Errorf("entryproof: decision_visible_info required")
	}

	minSz := strings.TrimSpace(tr.MinSz)
	if minSz == "" {
		minSz = strings.TrimSpace(tr.LotSz)
	}
	lotSz := strings.TrimSpace(tr.LotSz)
	ctVal := strings.TrimSpace(tr.CtVal)
	ctType := normalizeCtType(tr.CtType)

	dynamicMin, resolvedCtType, err := DynamicMinNotionalUSDT(minSz, ctVal, ctType, tr.Price)
	if err != nil {
		return EntryProof{}, err
	}

	verified, err := ResolveVerifiedEvidence(tr)
	if err != nil {
		return EntryProof{}, err
	}
	grade := verified.Grade
	if !DiscoveryAllowed(grade) {
		return EntryProof{}, fmt.Errorf("entryproof: evidence grade %q not allowed", grade)
	}
	executableDepth := verified.ExecutableDepth

	cancelRule := strings.TrimSpace(tr.CancelRule)
	if cancelRule == "" {
		cancelRule = CancelRuleStateChangeImmediate
	}
	noChase := strings.TrimSpace(tr.NoChaseRule)
	if noChase == "" {
		noChase = NoChaseRuleAfterMiss
	}

	ep := EntryProof{
		SchemaVersion:       SchemaVersion,
		TriggerID:           tr.TriggerID,
		DecisionTime:        tr.DecisionTime.UTC(),
		Venue:               venue,
		DecisionVisibleInfo: tr.DecisionVisibleInfo,
		OrderType:           strings.TrimSpace(tr.OrderType),
		Price:               tr.Price,
		MinSz:               minSz,
		LotSz:               lotSz,
		CtVal:               ctVal,
		CtType:              resolvedCtType,
		DynamicMinNotional:  dynamicMin,
		LatencyMS:           tr.LatencyMS,
		ExecutableDepth:     executableDepth,
		ParticipationRate:   tr.ParticipationRate,
		PartialFill:         tr.PartialFill,
		QueueEstimate:       tr.QueueAheadUSDT,
		MaxWaitMS:           tr.MaxWaitMS,
		CancelRule:          cancelRule,
		NoChaseRule:         noChase,
		EvidenceGrade:       grade,
		EvidenceArtifactHash: verified.ArtifactHash,
		EvidenceArtifactKind: verified.ArtifactKind,
		ShadowSourceID:       tr.ShadowSourceID,
		CertificationOK:     verified.CertificationOK,
	}
	if !ep.CertificationOK {
		ep.EntryStatus = EntryUnprovenStatus
		if verified.BlockReason != "" {
			ep.EntryStatus = verified.BlockReason
		}
	}
	hash, err := CanonicalEntryProofHash(ep)
	if err != nil {
		return EntryProof{}, err
	}
	ep.EntryProofHash = hash
	return ep, nil
}

// CanonicalEntryProofHash hashes proof payload with hash field cleared.
func CanonicalEntryProofHash(ep EntryProof) (string, error) {
	payload := ep
	payload.EntryProofHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateEntryProof checks required frozen fields.
func ValidateEntryProof(ep EntryProof) error {
	if ep.SchemaVersion != SchemaVersion {
		return fmt.Errorf("entryproof: schema_version drift")
	}
	if ep.EntryProofHash == "" {
		return fmt.Errorf("entryproof: entry_proof_hash required")
	}
	want, err := CanonicalEntryProofHash(ep)
	if err != nil {
		return err
	}
	if want != ep.EntryProofHash {
		return fmt.Errorf("entryproof: entry_proof_hash mismatch")
	}
	if ep.Venue != ExecutionVenueOKX {
		return fmt.Errorf("entryproof: venue must be okx")
	}
	if ep.DynamicMinNotional <= 0 {
		return fmt.Errorf("entryproof: dynamic_min_notional required")
	}
	if ep.CtType == CtTypeInverse || ep.CtType == CtTypeUnknown {
		return fmt.Errorf("entryproof: ct_type %q fail closed", ep.CtType)
	}
	if !DiscoveryAllowed(ep.EvidenceGrade) {
		return fmt.Errorf("entryproof: invalid evidence grade %q", ep.EvidenceGrade)
	}
	if ep.CertificationOK {
		if ep.EvidenceArtifactHash == "" {
			return fmt.Errorf("entryproof: certified grade requires evidence_artifact_hash")
		}
		if !CertificationAllowed(ep.EvidenceGrade) {
			return fmt.Errorf("entryproof: grade %q cannot certify", ep.EvidenceGrade)
		}
	}
	if !ep.CertificationOK && ep.EntryStatus != EntryUnprovenStatus {
		return fmt.Errorf("entryproof: C/D grade must be ENTRY_UNPROVEN")
	}
	return nil
}

// ConfigFingerprint hashes run config for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}
