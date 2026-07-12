package entryproof

import (
	"fmt"
	"strings"

	"lingxi/internal/datatruth"
)

// ArtifactBinding carries verifiable evidence source hashes; grades must bind one.
type ArtifactBinding struct {
	WALArtifactHash        string `json:"wal_artifact_hash,omitempty"`
	MakerArtifactHash      string `json:"maker_artifact_hash,omitempty"`
	SettlementArtifactHash string `json:"settlement_artifact_hash,omitempty"`
	ShadowSourceHash       string `json:"shadow_source_hash,omitempty"`
	DepthArtifactHash      string `json:"depth_artifact_hash,omitempty"`
}

// VerifiedEvidence is the artifact-bound grade resolution result.
type VerifiedEvidence struct {
	Grade              string
	ArtifactHash       string
	ArtifactKind       string
	ExecutableDepth    float64
	CertificationOK    bool
	BlockReason        string
}

// ResolveVerifiedEvidence derives grade and depth from artifact hashes only.
// Self-declared evidence_grade / fill / depth fields in TriggerRecord are ignored.
func ResolveVerifiedEvidence(tr TriggerRecord) (VerifiedEvidence, error) {
	bind := tr.ArtifactBinding
	if bind.WALArtifactHash != "" || bind.SettlementArtifactHash != "" {
		hash, kind, err := requireArtifactHash(bind.WALArtifactHash, bind.SettlementArtifactHash, "wal", "settlement")
		if err != nil {
			return VerifiedEvidence{}, err
		}
		if !tr.RealFill && !tr.MakerFilled && bind.SettlementArtifactHash == "" {
			return VerifiedEvidence{}, fmt.Errorf("entryproof: grade A requires fill verified by settlement or WAL")
		}
		depth, err := verifiedDepth(tr, hash)
		if err != nil {
			return VerifiedEvidence{}, err
		}
		return VerifiedEvidence{
			Grade:           GradeRealFillOrWAL,
			ArtifactHash:    hash,
			ArtifactKind:    kind,
			ExecutableDepth: depth,
			CertificationOK: true,
		}, nil
	}
	if bind.MakerArtifactHash != "" {
		hash := strings.TrimSpace(bind.MakerArtifactHash)
		if err := validateArtifactHash(hash); err != nil {
			return VerifiedEvidence{}, err
		}
		depth, err := verifiedDepth(tr, hash)
		if err != nil {
			return VerifiedEvidence{}, err
		}
		return VerifiedEvidence{
			Grade:           GradeMakerAttemptAckCancel,
			ArtifactHash:    hash,
			ArtifactKind:    "maker",
			ExecutableDepth: depth,
			CertificationOK: true,
		}, nil
	}
	if bind.ShadowSourceHash != "" {
		hash := strings.TrimSpace(bind.ShadowSourceHash)
		if err := validateArtifactHash(hash); err != nil {
			return VerifiedEvidence{}, err
		}
		if strings.TrimSpace(tr.ShadowSourceID) == "" {
			return VerifiedEvidence{}, fmt.Errorf("entryproof: grade C requires shadow_source_id")
		}
		depth, err := verifiedDepth(tr, hash)
		if err != nil {
			return VerifiedEvidence{}, err
		}
		return VerifiedEvidence{
			Grade:           GradeExecutableShadowOrTrade,
			ArtifactHash:    hash,
			ArtifactKind:    "shadow",
			ExecutableDepth: depth,
			CertificationOK: false,
			BlockReason:     EntryUnprovenStatus,
		}, nil
	}
	// Grade D: public conservative boundary — discovery only, never certifies.
	if tr.ExecutableDepthUSDT <= 0 {
		return VerifiedEvidence{}, fmt.Errorf("entryproof: grade D requires conservative depth bound")
	}
	return VerifiedEvidence{
		Grade:           GradePublicConservativeBoundary,
		ArtifactHash:    "",
		ArtifactKind:    "public_boundary",
		ExecutableDepth: tr.ExecutableDepthUSDT,
		CertificationOK: false,
		BlockReason:     EntryUnprovenStatus,
	}, nil
}

func requireArtifactHash(primary, fallback string, kinds ...string) (hash, kind string, err error) {
	if h := strings.TrimSpace(primary); h != "" {
		if err := validateArtifactHash(h); err != nil {
			return "", "", err
		}
		return h, kinds[0], nil
	}
	if h := strings.TrimSpace(fallback); h != "" {
		if err := validateArtifactHash(h); err != nil {
			return "", "", err
		}
		kind = kinds[0]
		if len(kinds) > 1 {
			kind = kinds[1]
		}
		return h, kind, nil
	}
	return "", "", fmt.Errorf("entryproof: missing %s artifact hash", strings.Join(kinds, "/"))
}

func validateArtifactHash(hash string) error {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return fmt.Errorf("entryproof: empty artifact hash")
	}
	if len(hash) < 16 {
		return fmt.Errorf("entryproof: artifact hash too short")
	}
	return nil
}

func verifiedDepth(tr TriggerRecord, artifactHash string) (float64, error) {
	if tr.ExecutableDepthUSDT <= 0 {
		return 0, fmt.Errorf("entryproof: executable depth required")
	}
	if bind := tr.ArtifactBinding; bind.DepthArtifactHash != "" {
		if !depthArtifactConsistent(bind, artifactHash) {
			return 0, fmt.Errorf("entryproof: depth artifact hash mismatch")
		}
	}
	return tr.ExecutableDepthUSDT, nil
}

func depthArtifactConsistent(bind ArtifactBinding, gradeArtifact string) bool {
	depth := strings.TrimSpace(bind.DepthArtifactHash)
	if depth == "" {
		return true
	}
	if depth == strings.TrimSpace(gradeArtifact) {
		return true
	}
	for _, h := range []string{bind.WALArtifactHash, bind.SettlementArtifactHash, bind.MakerArtifactHash, bind.ShadowSourceHash} {
		if depth == strings.TrimSpace(h) {
			return true
		}
	}
	return false
}

// HashArtifactBinding canonicalizes artifact binding for manifest cross-check.
func HashArtifactBinding(b ArtifactBinding) (string, error) {
	hash, _, err := datatruth.HashJSON(b)
	return hash, err
}
