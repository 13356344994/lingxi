package personalbrain

import (
	"time"

	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/entryproof"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// EvaluateEnterGate runs the first per-bar gate: CAN_ENTER / CANNOT_ENTER / MISSED_CORRECTLY.
func EvaluateEnterGate(
	ev MatchedPersonalEvent,
	in BarInput,
	sol SolvencyDecision,
	hasOpenPosition bool,
	toxicBlocked bool,
) EnterGateResult {
	var reasons []string

	if ev.EventID == "" {
		return EnterGateResult{Status: EnterCannotEnter, Reasons: []string{RejectPlaybookNoMatch}}
	}

	if in.ExecutionSim.MakerTouchedNotFilled && !in.ExecutionSim.MakerFilled {
		if in.ExecutionSim.SubsequentlyProfitable {
			return EnterGateResult{
				Status:  EnterMissedCorrectly,
				Reasons: []string{MissedProfitableMoveStatus, RejectNoChaseAfterMiss},
			}
		}
		return EnterGateResult{Status: EnterMissedCorrectly, Reasons: []string{"MAKER_TIMEOUT_CANCEL"}}
	}

	if !ev.Proofs.ExclusionAccepted {
		reasons = append(reasons, "EXCLUSION_NOT_ACCEPTED")
	}
	if !ev.Proofs.EntryCertified {
		reasons = append(reasons, RejectEntryUnproven)
	}
	if !entryproof.CertificationAllowed(ev.Proofs.EntryGrade) {
		reasons = append(reasons, EntryUnprovenStatus)
	}
	if !ev.Proofs.ExitCertified {
		reasons = append(reasons, RejectExitUnproven)
	}
	if !ev.Proofs.CrowdingPassed {
		reasons = append(reasons, RejectCrowdingStop)
	}
	if capitalExpansionStop(in, ev) {
		reasons = append(reasons, RejectCapitalExpansionStop)
	}
	if ev.ThesisHealth == ThesisInvalidated {
		reasons = append(reasons, RejectThesisInvalid)
	}
	if in.Features.OOD {
		reasons = append(reasons, "OOD_BLOCK")
	}
	if SolvencyBlocksNewOpen(sol) && !hasOpenPosition {
		reasons = append(reasons, RejectSolvencyForced)
	}
	if toxicBlocked && !hasOpenPosition {
		reasons = append(reasons, RejectToxicTopUpForbidden)
	}

	if len(reasons) > 0 {
		return EnterGateResult{Status: EnterCannotEnter, Reasons: uniqueStrings(reasons)}
	}
	return EnterGateResult{Status: EnterCanEnter}
}

func capitalExpansionStop(in BarInput, ev MatchedPersonalEvent) bool {
	if in.CapitalTierUSDT <= 0 {
		return false
	}
	tier := in.CapitalTierUSDT
	if tier > ev.Playbook.RiskNarrative.MaxSizeUSDT && ev.Playbook.RiskNarrative.MaxSizeUSDT > 0 {
		return true
	}
	if ev.Playbook.StopNarrative.CapitalExpansionStopRule == "exceeds_certified_tier" && tier > 200 {
		return true
	}
	return false
}

// VisibleAnnouncementSnapshot returns PIT-visible announcements at decision time.
func VisibleAnnouncementSnapshot(all []announcements.Announcement, decisionTime time.Time, tapeHash string) AnnouncementVisibility {
	visible := announcements.VisibleAnnouncements(all, decisionTime)
	ids := make([]string, 0, len(visible))
	for _, a := range visible {
		ids = append(ids, a.ID)
	}
	return AnnouncementVisibility{
		VisibleCount: len(visible),
		VisibleIDs:   ids,
		TapeHash:     tapeHash,
	}
}

// CrowdingStressLabel formats crowding stress for trace binding.
func CrowdingStressLabel(ev MatchedPersonalEvent) string {
	if !ev.Proofs.CrowdingPassed {
		return "crowding_failed"
	}
	if len(ev.Proofs.CrowdingStress.Levels) == 0 {
		return "crowding_ok"
	}
	for _, lvl := range ev.Proofs.CrowdingStress.Levels {
		if lvl.QueueCrowdingDetected {
			return "queue_crowding_detected"
		}
	}
	return "crowding_ok"
}

// DefaultEntryGrade returns grade from proofs or conservative D.
func DefaultEntryGrade(grade string) string {
	if grade != "" {
		return grade
	}
	return g10protocolv2.EntryProofDiscoveryAllowedGrades[len(g10protocolv2.EntryProofDiscoveryAllowedGrades)-1]
}
