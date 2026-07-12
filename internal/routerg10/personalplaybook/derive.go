package personalplaybook

import (
	"fmt"
	"sort"

	g10constitution "lingxi/internal/routerg10/constitution"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/reversetapes"
	"lingxi/internal/routerg6/eventmap"
)

type deriveState struct {
	playbookLimit int
	playbooks     []PersonalWildPlaybook
	discovery     []PersonalWildPlaybook
	ledger        []ExperimentAttempt
	registered    map[string]bool
	dedupSeen     map[string]bool
}

// Derive runs bounded personal wild playbook derivation from certified reverse tapes.
func Derive(
	seeds []TapeSeed,
	discoveryTapes []reversetapes.ExecutableProfitTape,
	playbookLimit int,
	resume *Checkpoint,
) ([]PersonalWildPlaybook, []PersonalWildPlaybook, ExperimentLedger, error) {
	state := deriveState{
		playbookLimit: playbookLimit,
		registered:    map[string]bool{},
		dedupSeen:       map[string]bool{},
	}
	if resume != nil {
		state.playbooks = append([]PersonalWildPlaybook(nil), resume.Playbooks...)
		state.discovery = append([]PersonalWildPlaybook(nil), resume.DiscoveryPlaybooks...)
		state.ledger = append([]ExperimentAttempt(nil), resume.LedgerAttempts...)
		for _, pb := range state.playbooks {
			state.registered[pb.PlaybookHash] = true
			state.dedupSeen[dedupKeyFromPlaybook(pb)] = true
		}
	}

	for _, seed := range seeds {
		if certifiedCount(state.playbooks) >= playbookLimit {
			break
		}
		if err := state.attemptCertified(seed.Tape); err != nil {
			return nil, nil, ExperimentLedger{}, err
		}
	}

	for _, tape := range discoveryTapes {
		if err := state.attemptDiscovery(tape); err != nil {
			return nil, nil, ExperimentLedger{}, err
		}
	}

	ledger, err := SealLedger(ExperimentLedger{
		SchemaVersion: SchemaVersionLedger,
		Generation:    Generation,
		ProtocolHash:  g10protocolv2.RouterProtocolHash,
		LedgerStage:   LedgerStagePersonalWild,
		DiscoveryMode: DiscoveryMode,
		Attempts:      state.ledger,
	})
	if err != nil {
		return nil, nil, ExperimentLedger{}, err
	}
	for i := range state.playbooks {
		state.playbooks[i].ExperimentHash = ledger.LedgerHash
	}
	for i := range state.discovery {
		state.discovery[i].ExperimentHash = ledger.LedgerHash
	}
	return state.playbooks, state.discovery, ledger, nil
}

func certifiedCount(playbooks []PersonalWildPlaybook) int {
	n := 0
	for _, pb := range playbooks {
		if pb.Status == StatusPersonalWildCandidate {
			n++
		}
	}
	return n
}

func (s *deriveState) attemptCertified(tape reversetapes.ExecutableProfitTape) error {
	entry := s.newEntry(tape)
	if err := validateCertifiedTape(tape); err != nil {
		entry.Status = LedgerStatusTapeInvalid
		entry.StatusReason = err.Error()
		entry.RejectReasons = []string{err.Error()}
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if key := dedupKeyFromTape(tape); s.dedupSeen[key] {
		entry.Status = LedgerStatusDeduped
		entry.StatusReason = "discovery_dedup_tracker_key"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	pb, err := BuildPlaybook(tape, len(s.playbooks)+1)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	entry.TapeHash = pb.TapeHash
	entry.PersonalTapeFamily = pb.PersonalTapeFamily
	if s.registered[pb.PlaybookHash] {
		entry.Status = LedgerStatusDuplicate
		entry.StatusReason = "duplicate_playbook_hash"
		s.ledger = append(s.ledger, entry)
		return nil
	}
	if err := ValidatePlaybook(pb); err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	s.registered[pb.PlaybookHash] = true
	s.dedupSeen[dedupKeyFromTape(tape)] = true
	if pb.Status == StatusNotPersonalEnough {
		s.playbooks = append(s.playbooks, pb)
		entry.Status = LedgerStatusNotPersonal
		entry.StatusReason = StatusNotPersonalEnough
		entry.RejectReasons = append([]string(nil), pb.NotPersonalReasons...)
	} else if certifiedCount(s.playbooks) < s.playbookLimit {
		s.playbooks = append(s.playbooks, pb)
		entry.Status = LedgerStatusCandidate
		entry.StatusReason = StatusPersonalWildCandidate
	} else {
		entry.Status = LedgerStatusDeduped
		entry.StatusReason = "playbook_limit_reached"
	}
	s.ledger = append(s.ledger, entry)
	return nil
}

func (s *deriveState) attemptDiscovery(tape reversetapes.ExecutableProfitTape) error {
	entry := s.newEntry(tape)
	pb, err := BuildDiscoveryPlaybook(tape)
	if err != nil {
		entry.Status = LedgerStatusInvalid
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	entry.TapeHash = pb.TapeHash
	entry.PersonalTapeFamily = pb.PersonalTapeFamily
	entry.Status = LedgerStatusDiscoveryOnly
	entry.StatusReason = StatusDiscoveryOnly
	if err := ValidateDiscoveryPlaybook(pb); err != nil {
		entry.Status = LedgerStatusNotCertifiable
		entry.StatusReason = err.Error()
		s.ledger = append(s.ledger, entry)
		return nil
	}
	s.discovery = append(s.discovery, pb)
	s.ledger = append(s.ledger, entry)
	return nil
}

func (s *deriveState) newEntry(tape reversetapes.ExecutableProfitTape) ExperimentAttempt {
	return ExperimentAttempt{
		AttemptOrdinal: len(s.ledger) + 1,
		EventID:        tape.EventID,
		Symbol:         tape.Symbol,
		ProfileID:      tape.ProfileID,
		Horizon:        tape.Horizon,
		LedgerStage:    LedgerStagePersonalWild,
	}
}

// BuildPlaybook materializes one personal wild playbook from a certified tape.
func BuildPlaybook(tape reversetapes.ExecutableProfitTape, ordinal int) (PersonalWildPlaybook, error) {
	notPersonal, reasons := detectNotPersonalEnough(tape)
	status := StatusPersonalWildCandidate
	if notPersonal {
		status = StatusNotPersonalEnough
	}

	pb := PersonalWildPlaybook{
		SchemaVersion: SchemaVersionPlaybook,
		Generation:    Generation,
		ProtocolHash:  g10protocolv2.RouterProtocolHash,
		TapeHash:      tape.TapeHash,
		SourceEventID: tape.EventID,
		Ordinal:       ordinal,
		ProofBinding: ProofBinding{
			ConstitutionHash:     tape.ProofBinding.ConstitutionHash,
			ExclusionProofHash:   tape.ProofBinding.ExclusionProofHash,
			EntryProofHash:       tape.ProofBinding.EntryProofHash,
			ExecutionPolicyHash:  tape.ProofBinding.ExecutionPolicyHash,
			ExitProofHash:        tape.ProofBinding.ExitProofHash,
			AnnouncementTapeHash: tape.ProofBinding.AnnouncementTapeHash,
		},
		Symbol:             tape.Symbol,
		ProfileID:          tape.ProfileID,
		EventFamily:        tape.EventFamily,
		PersonalTapeFamily: tape.PersonalTapeFamily,
		Horizon:            tape.Horizon,
		DelayBars:          tape.DecisionBefore.DelayBars,
		Direction:          tape.DecisionBefore.Direction,
		ShockCluster:       tape.ShockCluster,
		ExclusionNarrative: buildExclusionNarrative(tape),
		EntryNarrative:     buildEntryNarrative(tape),
		ExecutionNarrative: buildExecutionNarrative(tape),
		RiskNarrative:      buildRiskNarrative(tape),
		ExitNarrative:      buildExitNarrative(tape),
		StopNarrative:      buildStopNarrative(tape),
		PublicTrigger:      buildPublicTrigger(tape),
		Status:             status,
		NotPersonalReasons: reasons,
		ValidationStage:    ValidationStagePending,
		TradeAllowed:       g10protocolv2.TradeAllowedDefault,
		ExamEligible:       g10protocolv2.ExamEligible,
		Certified:          false,
		DiscoveryMode:      DiscoveryMode,
		ExamActions:        g10protocolv2.ExamActions,
	}
	instHash, retailHash, botHash, err := BindExclusionAxisHashes(pb.ExclusionNarrative)
	if err != nil && status == StatusPersonalWildCandidate {
		return PersonalWildPlaybook{}, err
	}
	if err == nil {
		pb.ProofBinding.InstitutionAxisHash = instHash
		pb.ProofBinding.RetailAxisHash = retailHash
		pb.ProofBinding.BotAxisHash = botHash
	}
	hash, err := CanonicalPlaybookHash(pb)
	if err != nil {
		return PersonalWildPlaybook{}, err
	}
	pb.PlaybookHash = hash
	return pb, nil
}

// BuildDiscoveryPlaybook records a discovery-only tape as non-certifiable playbook.
func BuildDiscoveryPlaybook(tape reversetapes.ExecutableProfitTape) (PersonalWildPlaybook, error) {
	pb, err := BuildPlaybook(tape, 0)
	if err != nil {
		return PersonalWildPlaybook{}, err
	}
	pb.Status = StatusDiscoveryOnly
	pb.Certified = false
	pb.NotPersonalReasons = nil
	hash, err := CanonicalPlaybookHash(pb)
	if err != nil {
		return PersonalWildPlaybook{}, err
	}
	pb.PlaybookHash = hash
	return pb, nil
}

func buildExclusionNarrative(tape reversetapes.ExecutableProfitTape) ExclusionNarrative {
	return ExclusionNarrative{
		InstitutionExcludeReason: tape.DecisionBefore.InstitutionExclude,
		RetailExcludeReason:      tape.DecisionBefore.RetailExclude,
		BotExcludeReason:         tape.DecisionBefore.BotExclude,
	}
}

func buildEntryNarrative(tape reversetapes.ExecutableProfitTape) EntryNarrative {
	why := fmt.Sprintf(
		"executable_depth %.2f >= dynamic_min_notional %.2f at decision; visible_info=%s",
		tape.DecisionBefore.ExecutableDepthUSDT,
		tape.DecisionBefore.DynamicMinNotionalUSDT,
		tape.DecisionBefore.DecisionVisibleInfo,
	)
	return EntryNarrative{
		WhyUserCanEnter:          why,
		DynamicMinNotionalUSDT:   tape.DecisionBefore.DynamicMinNotionalUSDT,
		ExecutableDepthUSDT:      tape.DecisionBefore.ExecutableDepthUSDT,
		DecisionVisibleInfo:      tape.DecisionBefore.DecisionVisibleInfo,
		VisibleAnnouncementCount: len(tape.DecisionBefore.VisibleAnnouncementIDs),
	}
}

func buildExecutionNarrative(tape reversetapes.ExecutableProfitTape) ExecutionNarrative {
	action := tape.DecisionBefore.FrozenExecutionAction
	allowed := allowedActionsFor(action)
	conclusion := makerSelectionConclusion(tape)
	return ExecutionNarrative{
		MakerSelectionConclusion: conclusion,
		AllowedActions:           allowed,
		FrozenExecutionAction:    action,
		MakerQuadrant:            tape.DecisionBefore.MakerQuadrant,
		NoChaseAfterMiss:         tape.DecisionBefore.NoChaseAfterMiss,
		NoLongLivedMaker:         tape.DecisionBefore.NoLongLivedMaker,
	}
}

func makerSelectionConclusion(tape reversetapes.ExecutableProfitTape) string {
	quad := tape.DecisionBefore.MakerQuadrant
	action := tape.DecisionBefore.FrozenExecutionAction
	switch {
	case action == g10executionpolicy.ActionSkip:
		return "frozen_skip: remaining_edge_insufficient_after_costs"
	case action == g10executionpolicy.ActionConditionalMarketableLimit:
		return fmt.Sprintf("conditional_marketable_limit_selected; maker_quadrant=%s", quad)
	case action == g10executionpolicy.ActionBoundedMaker:
		switch quad {
		case g10executionpolicy.QuadrantFilledProfitable:
			return "maker_allowed: filled_profitable with E(net|filled)>=0"
		case g10executionpolicy.QuadrantFilledUnprofitable:
			return "maker_filled_unprofitable: adverse_selection_detected"
		case g10executionpolicy.QuadrantUnfilledSubsequentlyProfitable:
			return "maker_unfilled_missed_profitable_move: pnl=0 no_chase"
		default:
			return "maker_unfilled_not_profitable: correct_skip"
		}
	default:
		return "execution_policy_frozen_at_decision"
	}
}

func allowedActionsFor(frozen string) []string {
	out := []string{g10executionpolicy.ActionSkip}
	for _, action := range ExecutionActionsCompared {
		if action == frozen {
			out = append(out, action)
		}
	}
	if len(out) == 1 {
		return append(out, frozen)
	}
	sort.Strings(out)
	return out
}

func buildRiskNarrative(tape reversetapes.ExecutableProfitTape) RiskNarrative {
	capital := g10protocolv2.CapitalTiersUSDT[0]
	maxSize := tape.MaxCapacityUSDT
	if maxSize <= 0 {
		maxSize = tape.DecisionBefore.ExecutableDepthUSDT
	}
	eventBudget := capital * g10constitution.MaxEventTerminalLossFraction
	accountBudget := capital * g10constitution.MaxAccountTerminalRiskFraction
	eventOcc := 0.0
	if eventBudget > 0 {
		eventOcc = tape.TerminalLossUSDT / eventBudget * 100
	}
	accountOcc := 0.0
	if accountBudget > 0 {
		accountOcc = tape.TerminalLossUSDT / accountBudget * 100
	}
	return RiskNarrative{
		MaxSizeUSDT:                     maxSize,
		MaxCapacityUSDT:                 tape.MaxCapacityUSDT,
		EventTerminalBudgetFraction:     g10constitution.MaxEventTerminalLossFraction,
		AccountTerminalBudgetFraction:   g10constitution.MaxAccountTerminalRiskFraction,
		EventTerminalRiskOccupancyPct:   eventOcc,
		AccountTerminalRiskOccupancyPct: accountOcc,
		RiskCeilingIsNotSizingTarget:    g10constitution.RiskCeilingIsNotSizingTarget,
	}
}

func buildExitNarrative(tape reversetapes.ExecutableProfitTape) ExitNarrative {
	return ExitNarrative{
		InvalidatorConditions: append([]string(nil), g10protocolv2.StopErrorExitTriggers...),
		DeadlineHorizon:       tape.Horizon,
		ExitRule:              "thesis_health_fail_or_event_complete_or_toxic_fill_exit",
		StopErrorRequired:     true,
	}
}

func buildStopNarrative(tape reversetapes.ExecutableProfitTape) StopNarrative {
	crowding := "disable_when_1_3_5_micro_competitors_collapse_edge_below_threshold"
	if tape.DecisionBefore.BotExclude == "crowding_stress_1_3_5_micro_competitors" ||
		tape.DecisionBefore.BotExclude == "queue_crowding" {
		crowding = "active: bot_crowding_stress_passed_at_decision_freeze"
	}
	capStop := fmt.Sprintf(
		"stop_when_capacity_expansion_exceeds_%.0f_usdt_or_min_lot_nonlinearity_breaks",
		tape.MaxCapacityUSDT*2,
	)
	return StopNarrative{
		CrowdingStopRule:         crowding,
		CapitalExpansionStopRule: capStop,
	}
}

func buildPublicTrigger(tape reversetapes.ExecutableProfitTape) PublicTriggerSpec {
	spec := PublicTriggerSpec{PersonalTapeFamily: tape.PersonalTapeFamily}
	switch tape.PersonalTapeFamily {
	case reversetapes.FamilyAnnouncementShock:
		spec.Conditions = []PublicTriggerCondition{
			{Field: "visible_announcement_count", Op: "gte", Value: 1, Note: "pit_announcement_visible"},
			{Field: "cross_venue_shock_cluster", Op: "eq", Note: tape.ShockCluster},
		}
	case reversetapes.FamilyMinLotNonlinearity:
		spec.Conditions = []PublicTriggerCondition{
			{Field: "dynamic_min_notional_usdt", Op: "gte", Value: tape.DecisionBefore.DynamicMinNotionalUSDT},
			{Field: "min_lot_nonlinearity", Op: "eq", Value: 1, Note: "retail_exclusion_primary"},
		}
	case reversetapes.FamilyMechanicalFlowTail:
		spec.Conditions = []PublicTriggerCondition{
			{Field: "event_family", Op: "eq", Note: tape.EventFamily},
			{Field: "mechanical_flow_exhaustion", Op: "eq", Value: 1},
		}
	case reversetapes.FamilyPrepositionedCapital:
		spec.Conditions = []PublicTriggerCondition{
			{Field: "prepositioned_capital_window", Op: "eq", Value: 1},
			{Field: "custody_authorization_limit", Op: "eq", Value: 1},
		}
	case reversetapes.FamilyRareDynamicAbandon:
		spec.Conditions = []PublicTriggerCondition{
			{Field: "event_sparsity", Op: "eq", Value: 1},
			{Field: "dynamic_threshold_shift", Op: "eq", Value: 1},
		}
	default:
		conds := []PublicTriggerCondition{}
		if len(tape.DecisionBefore.VisibleAnnouncementIDs) > 0 {
			conds = append(conds, PublicTriggerCondition{
				Field: "visible_announcement_count", Op: "gte",
				Value: float64(len(tape.DecisionBefore.VisibleAnnouncementIDs)),
			})
			if tape.ShockCluster != "" && tape.ShockCluster != "none" {
				conds = append(conds, PublicTriggerCondition{
					Field: "cross_venue_shock_cluster", Op: "eq", Note: tape.ShockCluster,
				})
			}
		}
		if tape.DecisionBefore.DynamicMinNotionalUSDT > 0 {
			ratio := tape.DecisionBefore.DynamicMinNotionalUSDT / g10protocolv2.CapitalTiersUSDT[0]
			conds = append(conds, PublicTriggerCondition{
				Field: "dynamic_min_notional_usdt", Op: "gte", Value: tape.DecisionBefore.DynamicMinNotionalUSDT,
			})
			if ratio > 0 {
				conds = append(conds, PublicTriggerCondition{
					Field: "min_lot_nonlinearity", Op: "gte", Value: ratio,
				})
			}
		}
		if len(conds) == 0 {
			conds = []PublicTriggerCondition{
				{Field: "abs_return_bps", Op: "gte", Value: 20},
				{Field: "rms_bps", Op: "gte", Value: 25},
			}
		}
		spec.Conditions = conds
	}
	return spec
}

func dedupKeyFromTape(tape reversetapes.ExecutableProfitTape) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s",
		tape.Symbol, tape.PersonalTapeFamily, tape.Horizon,
		tape.DecisionBefore.DelayBars, tape.DecisionBefore.FrozenExecutionAction,
	)
}

func dedupKeyFromPlaybook(pb PersonalWildPlaybook) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s",
		pb.Symbol, pb.PersonalTapeFamily, pb.Horizon,
		pb.DelayBars, pb.ExecutionNarrative.FrozenExecutionAction,
	)
}

func sortedKeys(m map[string][]reversetapes.ExecutableProfitTape) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// detectNotPersonalEnough flags fixed public K-line, easy replication, or only-small patterns.
func detectNotPersonalEnough(tape reversetapes.ExecutableProfitTape) (bool, []string) {
	var reasons []string
	inst := tape.DecisionBefore.InstitutionExclude
	retail := tape.DecisionBefore.RetailExclude
	bot := tape.DecisionBefore.BotExclude

	if inst == "" || retail == "" || bot == "" {
		reasons = append(reasons, ReasonMissingTripleExclude)
	}
	if tape.PersonalTapeFamily == reversetapes.FamilyUnknown {
		reasons = append(reasons, ReasonUnknownTapeFamily)
	}
	if onlySmallEvidence(inst, retail, bot) {
		reasons = append(reasons, ReasonOnlySmall)
	}
	if fixedPublicKlinePattern(tape) {
		reasons = append(reasons, ReasonFixedPublicKline)
	}
	if easyReplicationAcrossSymbols(tape) {
		reasons = append(reasons, ReasonEasyReplication)
	}
	return len(reasons) > 0, reasons
}

func onlySmallEvidence(inst, retail, bot string) bool {
	nonSmall := 0
	for _, ev := range []string{inst, retail, bot} {
		switch ev {
		case "", "small_market_size_only", "SMALL_BUT_COMPETITIVE", "PUBLIC_AND_CROWDED":
		default:
			nonSmall++
		}
	}
	return nonSmall == 0
}

func fixedPublicKlinePattern(tape reversetapes.ExecutableProfitTape) bool {
	if tape.PersonalTapeFamily != reversetapes.FamilyUnknown {
		return false
	}
	switch tape.EventFamily {
	case eventmap.FamilyExtremeContinuation, eventmap.FamilyExtremeReversal, eventmap.FamilyBTCRelative:
		return len(tape.DecisionBefore.VisibleAnnouncementIDs) == 0
	default:
		return false
	}
}

func easyReplicationAcrossSymbols(tape reversetapes.ExecutableProfitTape) bool {
	return tape.PersonalTapeFamily == reversetapes.FamilyUnknown &&
		tape.ShockCluster == "" &&
		len(tape.DecisionBefore.VisibleAnnouncementIDs) == 0
}
