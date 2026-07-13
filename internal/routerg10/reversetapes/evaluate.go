package reversetapes

import (
	"errors"
	"strings"

	g10constitution "lingxi/internal/routerg10/constitution"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/announcements"
)

var errLookahead = errors.New("lookahead")

// Evaluate reconstructs one reverse tape from bound proofs.
func Evaluate(in EvaluationInput, usedShocks map[string]bool) EvaluationResult {
	result := EvaluationResult{RejectReasons: []string{}}

	if !in.Exclusion.Accepted {
		result.RejectReasons = append(result.RejectReasons, RejectExclusionNotAccepted)
		return result
	}
	if in.Announcements.Incomplete {
		result.RejectReasons = append(result.RejectReasons, RejectIncompletePIT)
		return result
	}
	if err := checkAnnouncementLookahead(in); err != nil {
		result.RejectReasons = append(result.RejectReasons, RejectAnnouncementLookahead)
		return result
	}
	if usedShocks != nil && in.Exclusion.ShockCluster != "" {
		key := shockOverlapKey(in.Exclusion.Symbol, in.Exclusion.ShockCluster)
		if usedShocks[key] {
			result.RejectReasons = append(result.RejectReasons, RejectShockOverlap)
			return result
		}
	}
	if err := detectActionHindsight(in); err != nil {
		result.RejectReasons = append(result.RejectReasons, RejectActionHindsight)
		return result
	}
	if err := detectFillOnlySurvivorship(in); err != nil {
		result.RejectReasons = append(result.RejectReasons, RejectFillOnlySurvivorship)
		return result
	}
	if err := verifyPrecommittedExecutionPolicy(in); err != nil {
		result.RejectReasons = append(result.RejectReasons, RejectProofHashMismatch)
		return result
	}

	direction := normalizeDirection(in.Exclusion.Direction)
	frozenAction := in.Execution.SelectedAction
	discoveryOnly := !in.Entry.CertificationOK || !in.Exit.CertificationOK

	if !in.Entry.CertificationOK {
		result.RejectReasons = append(result.RejectReasons, RejectEntryUnproven)
	}
	if !in.Exit.CertificationOK {
		result.RejectReasons = append(result.RejectReasons, RejectExitUnproven)
	}
	if frozenAction == g10executionpolicy.ActionSkip && !discoveryOnly {
		result.RejectReasons = append(result.RejectReasons, RejectSkipFrozenAction)
		return result
	}
	if !canEnter(in) {
		result.RejectReasons = append(result.RejectReasons, RejectCannotEnter)
		if !discoveryOnly {
			return result
		}
	}
	if !canExit(in) && !discoveryOnly {
		result.RejectReasons = append(result.RejectReasons, RejectCannotExit)
		return result
	}

	realizedFill, fillReason, profit := computeRealizedPath(in, frozenAction)
	if !realizedFill {
		profit = 0
	}
	if !realizedFill && in.Execution.MakerQuadrant == g10executionpolicy.QuadrantUnfilledSubsequentlyProfitable && !discoveryOnly {
		result.RejectReasons = append(result.RejectReasons, RejectPaperWinnerNoFill)
		return result
	}

	label := in.OutcomeLabel
	label.LabelOnly = true
	if !realizedFill && label.SubsequentlyProfitable {
		label.MissedProfitableMove = true
	}
	label.MakerFilled = realizedFill
	label.ToxicFillStatus = in.Execution.ToxicFillStatus

	capacity := computeCapacity(in)
	drawdown := computeMaxDrawdown(in, profit, realizedFill)
	terminalLoss := computeTerminalLoss(in.CapitalUSDT, profit)

	visibleIDs := visibleAnnouncementIDs(in)

	tape := ExecutableProfitTape{
		SchemaVersion:      SchemaVersionTape,
		Generation:         Generation,
		ProtocolHash:       g10protocolv2.RouterProtocolHash,
		EventID:            in.Exclusion.EventID,
		Symbol:             in.Exclusion.Symbol,
		ProfileID:          in.Exclusion.ProfileID,
		EventFamily:        in.Exclusion.EventFamily,
		PersonalTapeFamily: ClassifyPersonalTapeFamily(in.Exclusion),
		Horizon:            in.Exclusion.Horizon,
		DecisionTime:       in.Exclusion.DecisionTime.UTC(),
		ShockCluster:       in.Exclusion.ShockCluster,
		ProofBinding: ProofBinding{
			EventID:              in.Exclusion.EventID,
			ExclusionProofHash:   in.Exclusion.ExclusionProofHash,
			EntryProofHash:       in.Entry.EntryProofHash,
			ExecutionPolicyHash:  in.Execution.ExecutionPolicyHash,
			ExitProofHash:        in.Exit.ExitProofHash,
			AnnouncementTapeHash: in.Announcements.TapeHash,
			ConstitutionHash:     in.ConstitutionHash,
		},
		DecisionBefore: DecisionBeforeState{
			Direction:              direction,
			DelayBars:              in.Exclusion.DelayBars,
			FrozenExecutionAction:  frozenAction,
			DecisionVisibleInfo:    in.Entry.DecisionVisibleInfo,
			DynamicMinNotionalUSDT: in.Entry.DynamicMinNotional,
			ExecutableDepthUSDT:    in.Entry.ExecutableDepth,
			VisibleAnnouncementIDs: visibleIDs,
			InstitutionExclude:     in.Exclusion.Institution.PrimaryEvidence,
			RetailExclude:          in.Exclusion.Retail.PrimaryEvidence,
			BotExclude:             in.Exclusion.Bot.PrimaryEvidence,
			MakerQuadrant:          in.Execution.MakerQuadrant,
			NoChaseAfterMiss:       in.Execution.NoChaseAfterMiss,
			NoLongLivedMaker:       in.Execution.NoLongLivedMaker,
		},
		OutcomeLabel:       label,
		AbsoluteProfitUSDT: profit,
		MaxCapacityUSDT:    capacity,
		MaxDrawdownUSDT:    drawdown,
		TerminalLossUSDT:   terminalLoss,
		RealizedFill:       realizedFill,
		FillOrMissReason:   fillReason,
		DiscoveryMode:      DiscoveryMode,
	}

	if discoveryOnly {
		tape.Status = TapeStatusDiscoveryOnly
		result.Status = TapeStatusDiscoveryOnly
		result.Accepted = true
		result.RejectReasons = uniqueRejectReasons(result.RejectReasons)
	} else if frozenAction == g10executionpolicy.ActionSkip {
		result.RejectReasons = append(result.RejectReasons, RejectSkipFrozenAction)
		return result
	} else {
		tape.Status = TapeStatusExecutableCertified
		result.Status = TapeStatusExecutableCertified
		result.Accepted = true
	}

	hash, err := hashTape(tape)
	if err != nil {
		result.Accepted = false
		result.RejectReasons = append(result.RejectReasons, "HASH_ERROR")
		return result
	}
	tape.TapeHash = hash
	result.Tape = tape
	return result
}

func canEnter(in EvaluationInput) bool {
	if in.Entry.DynamicMinNotional <= 0 || in.Entry.ExecutableDepth <= 0 {
		return false
	}
	if in.Entry.ExecutableDepth < in.Entry.DynamicMinNotional {
		return false
	}
	return true
}

func canExit(in EvaluationInput) bool {
	return in.Exit.ExitDepthSufficient && in.Exit.CanExitBeforeDelistOrMaint && in.Exit.PartialCloseFeasible
}

func computeRealizedPath(in EvaluationInput, action string) (filled bool, reason string, profit float64) {
	switch action {
	case g10executionpolicy.ActionBoundedMaker:
		filled = in.Execution.MakerQuadrant == g10executionpolicy.QuadrantFilledProfitable ||
			in.Execution.MakerQuadrant == g10executionpolicy.QuadrantFilledUnprofitable
		if !filled {
			if in.Execution.MakerQuadrant == g10executionpolicy.QuadrantUnfilledSubsequentlyProfitable {
				return false, MissedProfitableMoveStatus, 0
			}
			return false, "MAKER_UNFILLED", 0
		}
		for _, a := range in.Execution.ActionsCompared {
			if a.Action == g10executionpolicy.ActionBoundedMaker {
				profit = a.ExpectedNetUSDT
				break
			}
		}
		return true, "MAKER_FILLED", profit
	case g10executionpolicy.ActionConditionalMarketableLimit:
		var allowed bool
		for _, a := range in.Execution.ActionsCompared {
			if a.Action != g10executionpolicy.ActionConditionalMarketableLimit {
				continue
			}
			if !a.Allowed {
				return false, "CONDITIONAL_LIMIT_BLOCKED", 0
			}
			allowed = true
			break
		}
		if !allowed {
			return false, "CONDITIONAL_LIMIT_BLOCKED", 0
		}
		if !cmlFillEvidence(in) {
			return false, "CONDITIONAL_LIMIT_NO_FILL_EVIDENCE", 0
		}
		for _, a := range in.Execution.ActionsCompared {
			if a.Action == g10executionpolicy.ActionConditionalMarketableLimit {
				return true, "CONDITIONAL_LIMIT_FILLED", a.ExpectedNetUSDT
			}
		}
		return false, "CONDITIONAL_LIMIT_BLOCKED", 0
	default:
		return false, RejectSkipFrozenAction, 0
	}
}

func computeCapacity(in EvaluationInput) float64 {
	cap := in.Entry.ExecutableDepth * in.Entry.ParticipationRate
	if cap <= 0 {
		cap = in.Entry.ExecutableDepth
	}
	if cap < in.Entry.DynamicMinNotional {
		return 0
	}
	return cap
}

func computeMaxDrawdown(_ EvaluationInput, profit float64, filled bool) float64 {
	if !filled || profit >= 0 {
		return 0
	}
	return -profit
}

func computeTerminalLoss(capital, profit float64) float64 {
	if profit >= 0 {
		return 0
	}
	loss := -profit
	maxEvent := capital * g10constitution.MaxEventTerminalLossFraction
	if loss > maxEvent {
		return maxEvent
	}
	return loss
}

func bpsToUSDT(bps, capital float64) float64 {
	return capital * bps / 10000
}

func normalizeDirection(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "long", "buy":
		return DirectionLong
	case "short", "sell":
		return DirectionShort
	default:
		return strings.ToUpper(d)
	}
}

func checkAnnouncementLookahead(in EvaluationInput) error {
	decision := in.Exclusion.DecisionTime
	for _, rec := range in.Announcements.Items {
		if rec.Symbol != "" && !strings.Contains(in.Exclusion.Symbol, stripSuffix(rec.Symbol)) {
			continue
		}
		if !announcements.VisibleAt(rec, decision) && (rec.FirstSeenAt.After(decision) || rec.PublishedAt.After(decision)) {
			return errLookahead
		}
	}
	return nil
}

func visibleAnnouncementIDs(in EvaluationInput) []string {
	decision := in.Exclusion.DecisionTime
	var ids []string
	for _, rec := range in.Announcements.Items {
		if announcements.VisibleAt(rec, decision) {
			ids = append(ids, rec.ID)
		}
	}
	return ids
}

func stripSuffix(s string) string {
	return strings.TrimSuffix(strings.TrimSuffix(s, "-SWAP"), "-USDT")
}

// detectActionHindsight rejects if outcome-informed action would differ from frozen selection.
func detectActionHindsight(in EvaluationInput) error {
	if !in.Execution.FreezeBeforeOutcomeKnown {
		return errLookahead
	}
	hindsight := g10executionpolicy.ActionSkip
	best := 0.0
	for _, a := range in.Execution.ActionsCompared {
		ev := a.ExpectedNetUSDT
		if !a.Allowed {
			continue
		}
		if a.Action == g10executionpolicy.ActionBoundedMaker && in.Execution.MakerGate.ForbidMaker {
			continue
		}
		if ev > best {
			best = ev
			hindsight = a.Action
		}
	}
	if hindsight != in.Execution.SelectedAction && best > 0 && in.Execution.MakerQuadrant == g10executionpolicy.QuadrantFilledProfitable {
		return errLookahead
	}
	return nil
}

// detectFillOnlySurvivorship rejects when positive labels exist but the maker
// cohort lacks unfilled triggers in the all-trigger denominator.
func detectFillOnlySurvivorship(in EvaluationInput) error {
	if !executionShowsPositiveFilledPath(in) {
		return nil
	}
	stats := in.Execution.MakerStats.Quadrants
	total := stats.TotalTriggers
	if in.AllTriggersDenominator > 0 && total != in.AllTriggersDenominator {
		return errLookahead
	}
	if total <= 0 {
		return errLookahead
	}
	unfilled := stats.UnfilledSubsequentlyProfitable + stats.UnfilledNotProfitable
	if unfilled == 0 {
		return errLookahead
	}
	filled := stats.FilledProfitable + stats.FilledUnprofitable
	if filled == 0 {
		return errLookahead
	}
	return nil
}

func verifyPrecommittedExecutionPolicy(in EvaluationInput) error {
	want, err := g10executionpolicy.CanonicalPolicyHash(in.Execution)
	if err != nil {
		return err
	}
	if want != in.Execution.ExecutionPolicyHash {
		return errLookahead
	}
	for _, a := range in.Execution.ActionsCompared {
		if a.Action == in.Execution.SelectedAction {
			return nil
		}
	}
	return errLookahead
}

func shockOverlapKey(symbol, cluster string) string {
	return symbol + "|" + cluster
}

// GradeDiscoveryOnly reports whether entry grade is C/D discovery-only.
func GradeDiscoveryOnly(ep g10entryproof.EntryProof) bool {
	return ep.EvidenceGrade == g10entryproof.GradeExecutableShadowOrTrade ||
		ep.EvidenceGrade == g10entryproof.GradePublicConservativeBoundary
}

func executionShowsPositiveFilledPath(in EvaluationInput) bool {
	for _, a := range in.Execution.ActionsCompared {
		if a.Allowed && a.ExpectedNetUSDT > 0 {
			return true
		}
	}
	return in.Execution.MakerQuadrant == g10executionpolicy.QuadrantFilledProfitable
}

func cmlFillEvidence(in EvaluationInput) bool {
	ep := in.Entry
	return g10entryproof.CertificationAllowed(ep.EvidenceGrade) && ep.EvidenceArtifactHash != ""
}

func uniqueRejectReasons(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range in {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
