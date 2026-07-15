package pathvalidation

// Verdict extends research/firstbattle/data_driven_router_v3/protocol.json's
// frozen verdicts list with two labels this validation stage needs and the
// frozen list does not anticipate: UNREPLAYABLE (a policy artifact with no
// safe, non-fabricated way to determine per-row membership) and
// INACTIVITY_FAILURE (a path that cleared every quantitative gate in
// validation but is essentially dormant in the leaf_wf confirmation window).
type Verdict string

const (
	VerdictValidatedPath     Verdict = "VALIDATED_PATH"
	VerdictPosthocOnly       Verdict = "POSTHOC_ONLY"
	VerdictUnstable          Verdict = "UNSTABLE"
	VerdictUnexecutable      Verdict = "UNEXECUTABLE"
	VerdictDataBlind         Verdict = "DATA_BLIND"
	VerdictNoEdge            Verdict = "NO_EDGE"
	VerdictDegenerate        Verdict = "DEGENERATE"
	VerdictNoValidatedPath   Verdict = "NO_VALIDATED_PATH"
	VerdictUnreplayable      Verdict = "UNREPLAYABLE"
	VerdictInactivityFailure Verdict = "INACTIVITY_FAILURE"
)

// HardFailureReason enumerates the specific hard failures this owner
// distinguishes from ordinary soft drawdown. A single bad fold/month/day
// never appears here: only ruin, unknown/unexecutable cost, or a genuinely
// insufficient replayable sample do.
type HardFailureReason string

const (
	HardFailureRuin             HardFailureReason = "RUIN_DETECTED"
	HardFailureUnexecutable     HardFailureReason = "UNEXECUTABLE_PREPARE_OR_COST"
	HardFailureInsufficientEvid HardFailureReason = "INSUFFICIENT_SUPPORT"
)

// PathVerdict is the complete, itemized decision for one candidate path
// (one owner's one policy leaf) after validation-partition gating and, if
// applicable, one leaf_wf confirmation replay.
type PathVerdict struct {
	Owner                 string              `json:"owner"`
	LeafID                string              `json:"leaf_id"`
	Predicate             string              `json:"predicate"`
	Intent                string              `json:"intent"`
	ReplayStatus          string              `json:"replay_status"`
	Verdict               Verdict             `json:"verdict"`
	ValidationVerdict     Verdict             `json:"validation_verdict"`
	HardFailures          []HardFailureReason `json:"hard_failures,omitempty"`
	SoftEvidenceNotes     []string            `json:"soft_evidence_notes,omitempty"`
	Validation            GateReport          `json:"validation"`
	ValidationPValue      float64             `json:"validation_p_value"`
	ValidationSignificant bool                `json:"validation_significant"`
	WFAttempted           bool                `json:"wf_attempted"`
	WFReport              GateReport          `json:"wf_report"`
	WFPassed              bool                `json:"wf_passed"`
	InactivityFailure     bool                `json:"inactivity_failure"`
	FidelityNote          string              `json:"fidelity_note"`
}

// DecideValidationVerdict turns one leaf's validation-partition gate report
// (plus its BH-FDR significance decision, computed across the whole
// validation family by the caller) into a verdict. It never downgrades a
// path because of a single bad fold, month, or day: every threshold below is
// the frozen multi-sample protocol gate, which already tolerates a minority
// of negative folds/months/blocks.
func DecideValidationVerdict(report GateReport, significant bool) (Verdict, []HardFailureReason, []string) {
	var hard []HardFailureReason
	var soft []string

	if report.RuinCount > 0 {
		hard = append(hard, HardFailureRuin)
	}
	if report.UnknownCostCount > 0 {
		hard = append(hard, HardFailureUnexecutable)
	}
	if !report.MeetsMinimumSupport {
		hard = append(hard, HardFailureInsufficientEvid)
	}
	if len(hard) > 0 {
		if contains(hard, HardFailureRuin) || contains(hard, HardFailureUnexecutable) {
			return VerdictUnexecutable, hard, soft
		}
		return VerdictNoEdge, hard, soft
	}

	if !report.MeetsBaseNetPositive {
		return VerdictNoEdge, hard, soft
	}
	if !significant {
		soft = append(soft, "sign-test p-value did not clear the BH-FDR validation-family threshold")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsSymbolFolds {
		soft = append(soft, "fewer than the required positive symbol-hash folds")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsMonthLOO {
		soft = append(soft, "calendar month leave-one-out positive fraction below threshold")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsRegimeBlocks {
		soft = append(soft, "BTC regime-block support or positivity below threshold")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsAllDelaysPositive {
		soft = append(soft, "not every delay in 0..3 kept a positive net account return")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsCostStress {
		soft = append(soft, "1.5x cost stress net reward was not positive")
		return VerdictUnstable, hard, soft
	}
	if !report.MeetsTopTwoDay {
		soft = append(soft, "top-two-day positive contribution fraction exceeded 0.35")
		return VerdictPosthocOnly, hard, soft
	}
	if !report.MeetsSingleSymbol {
		soft = append(soft, "single-symbol positive contribution fraction exceeded 0.35")
		return VerdictPosthocOnly, hard, soft
	}
	return VerdictValidatedPath, hard, soft
}

func contains(reasons []HardFailureReason, target HardFailureReason) bool {
	for _, r := range reasons {
		if r == target {
			return true
		}
	}
	return false
}
