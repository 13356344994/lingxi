package entryproof

import (
	"strings"

	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// ResolveEvidenceGrade is deprecated for proof building; use ResolveVerifiedEvidence.
// Kept for backward-compatible diagnostics only — never trusts self-declared grade.
func ResolveEvidenceGrade(tr TriggerRecord) string {
	verified, err := ResolveVerifiedEvidence(tr)
	if err != nil {
		return ""
	}
	return verified.Grade
}

// CertificationAllowed reports whether grade may certify entry eligibility.
func CertificationAllowed(grade string) bool {
	grade = strings.ToUpper(strings.TrimSpace(grade))
	for _, allowed := range g10protocolv2.EntryProofCertificationAllowedGrades {
		if grade == allowed {
			return true
		}
	}
	return false
}

// DiscoveryAllowed reports whether grade may enter discovery funnel.
func DiscoveryAllowed(grade string) bool {
	grade = strings.ToUpper(strings.TrimSpace(grade))
	for _, allowed := range g10protocolv2.EntryProofDiscoveryAllowedGrades {
		if grade == allowed {
			return true
		}
	}
	return false
}

// MakerTouchIsNotFill enforces maker touch price does not count as fill.
func MakerTouchIsNotFill(tr TriggerRecord) bool {
	if tr.MakerFilled {
		return false
	}
	return tr.MakerTouched
}
