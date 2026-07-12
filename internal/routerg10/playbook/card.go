package playbook

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/protocol"
)

const (
	LedgerStatusDuplicate   = "duplicate"
	LedgerStatusInvalid     = "invalid"
	LedgerStatusCandidate   = "personal_wild_candidate"
	LedgerStatusNonOverlap  = "skipped_nonoverlap"
	LedgerStatusDeduped     = "skipped_dedup"
)

// ForbiddenTriggerFields must never appear in reverse-derived trigger_conditions.
var ForbiddenTriggerFields = []string{
	"future_outcome",
	"realized_pnl",
	"post_event_labels",
	"gross_bps",
	"conservative_net_bps",
	"fee_net_bps",
	"outcome",
	"realized_return",
	"user_tier_conservative_net_bps",
	"winner",
	"winner_label",
	"direction",
	"delay_bars",
	"horizon",
	"capital_tier",
	"eligible_capital_tiers",
	"max_capacity_usdt",
	"shock_cluster",
}

// KnownTriggerFields are the only feature-only predicates allowed at playbook build time.
var KnownTriggerFields = []string{
	"abs_return_bps",
	"rms_bps",
	"return_sign",
	"session_boundary",
	"avg_quote_volume",
	"volume_spike",
	"btc_relative_abs_bps",
	"event_family",
}

// KnownTriggerOps are the only comparison operators allowed for trigger predicates.
var KnownTriggerOps = []string{
	"gte",
	"lte",
	"gt",
	"lt",
	"eq",
	"eq_prior_sign",
	"ne_prior_sign",
}

// CanonicalPlaybookHash hashes one Event Playbook identity before sealing.
func CanonicalPlaybookHash(pb FrozenEventPlaybook) (string, error) {
	payload := struct {
		ProfileID                     string              `json:"profile_id"`
		EventFamily                   string              `json:"event_family"`
		Horizon                       string              `json:"horizon"`
		DelayBars                     int                 `json:"delay_bars"`
		ShockCluster                  string              `json:"shock_cluster"`
		TriggerConditions             TriggerSpec         `json:"trigger_conditions"`
		LongBranch                    DirectionBranch     `json:"long_branch"`
		ShortBranch                   DirectionBranch     `json:"short_branch"`
		FlatBranch                    DirectionBranch     `json:"flat_branch"`
		MaxWaitTime                   string              `json:"max_wait_time"`
		DynamicDeadline               DynamicDeadlineSpec `json:"dynamic_deadline"`
		InvalidatorConditions         []string            `json:"invalidator_conditions"`
		CapacityBand                  string              `json:"capacity_band"`
		EligibleCapitalTiers          []string            `json:"eligible_capital_tiers"`
		PersonalExecutionRequirements []string            `json:"personal_execution_requirements"`
		StopConditions                []string            `json:"stop_conditions"`
		DiscoveryMode                 string              `json:"discovery_mode"`
		MapManifestHash               string              `json:"map_manifest_hash"`
		SourceWinnerIDsHash           string              `json:"source_winner_ids_hash"`
	}{
		ProfileID:                     pb.ProfileID,
		EventFamily:                   pb.EventFamily,
		Horizon:                       pb.Horizon,
		DelayBars:                     pb.DelayBars,
		ShockCluster:                  pb.ShockCluster,
		TriggerConditions:             pb.TriggerConditions,
		LongBranch:                    pb.LongBranch,
		ShortBranch:                   pb.ShortBranch,
		FlatBranch:                    pb.FlatBranch,
		MaxWaitTime:                   pb.MaxWaitTime,
		DynamicDeadline:               pb.DynamicDeadline,
		InvalidatorConditions:         append([]string(nil), pb.InvalidatorConditions...),
		CapacityBand:                  pb.CapacityBand,
		EligibleCapitalTiers:          append([]string(nil), pb.EligibleCapitalTiers...),
		PersonalExecutionRequirements: append([]string(nil), pb.PersonalExecutionRequirements...),
		StopConditions:                append([]string(nil), pb.StopConditions...),
		DiscoveryMode:                 pb.DiscoveryMode,
		MapManifestHash:               pb.MapManifestHash,
		SourceWinnerIDsHash:           pb.SourceWinnerIDsHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashPlaybooks hashes the ordered playbook bundle for manifest binding.
func HashPlaybooks(playbooks []FrozenEventPlaybook) (string, error) {
	hash, _, err := datatruth.HashJSON(playbooks)
	return hash, err
}

// ValidatePlaybook checks playbook invariants and required fields.
func ValidatePlaybook(pb FrozenEventPlaybook) error {
	if pb.SchemaVersion != SchemaVersionPlaybook {
		return errors.New("playbook schema mismatch")
	}
	if pb.Generation != protocol.Generation || pb.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("playbook generation/protocol mismatch")
	}
	if pb.Status != protocol.PersonalWildCandidateStatus {
		return fmt.Errorf("playbook status must be %s", protocol.PersonalWildCandidateStatus)
	}
	if pb.ValidationStage != ValidationStagePending {
		return fmt.Errorf("playbook validation_stage must be %s", ValidationStagePending)
	}
	if pb.TradeAllowed || pb.ExamEligible {
		return errors.New("personal wild candidates must not allow trade or exam")
	}
	if containsPassStatus(pb.ValidationStage) || containsPassStatus(pb.Status) {
		return errors.New("reverse discovery must not emit pass status")
	}
	for _, field := range protocol.PlaybookRequiredFields {
		if err := requirePlaybookField(pb, field); err != nil {
			return err
		}
	}
	if err := validateTriggerConditions(pb.TriggerConditions); err != nil {
		return err
	}
	if err := validateBranches(pb); err != nil {
		return err
	}
	if pb.EventCompletion == "" || pb.PayerStatus == "" {
		return errors.New("playbook missing event_completion or payer_status")
	}
	if pb.PlaybookHash == "" {
		return errors.New("playbook missing playbook_hash")
	}
	want, err := CanonicalPlaybookHash(pb)
	if err != nil {
		return err
	}
	if pb.PlaybookHash != want {
		return errors.New("playbook hash mismatch")
	}
	if pb.ExamActions != protocol.ExamActions {
		return errors.New("exam_actions must be zero")
	}
	return nil
}

func requirePlaybookField(pb FrozenEventPlaybook, field string) error {
	switch field {
	case "trigger_conditions":
		if len(pb.TriggerConditions.Conditions) == 0 {
			return errors.New("missing trigger_conditions")
		}
	case "long_branch":
		if pb.LongBranch.Action == "" || pb.LongBranch.Thesis == "" {
			return errors.New("missing long_branch")
		}
	case "short_branch":
		if pb.ShortBranch.Action == "" || pb.ShortBranch.Thesis == "" {
			return errors.New("missing short_branch")
		}
	case "flat_branch":
		if pb.FlatBranch.Action == "" || pb.FlatBranch.Thesis == "" {
			return errors.New("missing flat_branch")
		}
	case "max_wait_time":
		if pb.MaxWaitTime == "" {
			return errors.New("missing max_wait_time")
		}
	case "dynamic_deadline":
		if pb.DynamicDeadline.Horizon == "" {
			return errors.New("missing dynamic_deadline")
		}
	case "invalidator_conditions":
		if len(pb.InvalidatorConditions) == 0 {
			return errors.New("missing invalidator_conditions")
		}
	case "capacity_band":
		if pb.CapacityBand == "" {
			return errors.New("missing capacity_band")
		}
	case "eligible_capital_tiers":
		if len(pb.EligibleCapitalTiers) == 0 {
			return errors.New("missing eligible_capital_tiers")
		}
	case "personal_execution_requirements":
		if len(pb.PersonalExecutionRequirements) == 0 {
			return errors.New("missing personal_execution_requirements")
		}
	case "stop_conditions":
		if len(pb.StopConditions) == 0 {
			return errors.New("missing stop_conditions")
		}
	default:
		return fmt.Errorf("unknown required field %q", field)
	}
	return nil
}

func validateTriggerConditions(spec TriggerSpec) error {
	if err := validateTriggerNoLookahead(spec); err != nil {
		return err
	}
	for _, cond := range spec.Conditions {
		field := strings.ToLower(strings.TrimSpace(cond.Field))
		if !knownTriggerField(field) {
			return fmt.Errorf("trigger field %q is unknown (fail closed)", cond.Field)
		}
		op := strings.ToLower(strings.TrimSpace(cond.Op))
		if !knownTriggerOp(op) {
			return fmt.Errorf("trigger op %q is unknown (fail closed)", cond.Op)
		}
		switch field {
		case "return_sign":
			if op != "eq_prior_sign" && op != "ne_prior_sign" {
				return fmt.Errorf("trigger op %q invalid for return_sign", cond.Op)
			}
		case "event_family":
			if strings.TrimSpace(cond.Note) == "" {
				return fmt.Errorf("trigger event_family requires note")
			}
		default:
			if op == "eq_prior_sign" || op == "ne_prior_sign" {
				return fmt.Errorf("trigger op %q invalid for field %q", cond.Op, cond.Field)
			}
		}
	}
	return nil
}

func knownTriggerField(field string) bool {
	for _, want := range KnownTriggerFields {
		if field == want {
			return true
		}
	}
	return false
}

func knownTriggerOp(op string) bool {
	for _, want := range KnownTriggerOps {
		if op == want {
			return true
		}
	}
	return false
}

func validateTriggerNoLookahead(spec TriggerSpec) error {
	for _, cond := range spec.Conditions {
		field := strings.ToLower(strings.TrimSpace(cond.Field))
		for _, forbidden := range ForbiddenTriggerFields {
			if field == forbidden {
				return fmt.Errorf("trigger field %q is forbidden (lookahead)", cond.Field)
			}
		}
	}
	return nil
}

func validateBranches(pb FrozenEventPlaybook) error {
	actions := map[string]bool{
		pb.LongBranch.Action:  true,
		pb.ShortBranch.Action: true,
		pb.FlatBranch.Action:  true,
	}
	if !actions["LONG"] || !actions["SHORT"] || !actions["NO_TRADE"] {
		return errors.New("playbook must expose LONG, SHORT, and NO_TRADE branches")
	}
	return nil
}

func containsPassStatus(status string) bool {
	return status == protocol.ValidationPassStatus ||
		status == protocol.WFPassStatus ||
		status == protocol.MechanismUnexplainedPersonalEdgeStatus ||
		status == protocol.ExamBoundaryResearchPassStatus
}

// BuildPlaybook reverse-derives one playbook from a personal residual winner group.
func BuildPlaybook(group ResidualWinnerGroup, th eventmap.FrozenThresholds, ordinal int) (FrozenEventPlaybook, error) {
	if len(group.Winners) == 0 {
		return FrozenEventPlaybook{}, errors.New("empty winner group")
	}
	ids := winnerIDs(group.Winners)
	sourceHash, err := HashWinnerIDs(ids)
	if err != nil {
		return FrozenEventPlaybook{}, err
	}
	eligible := mergeEligibleTiers(group.Winners)
	capacityBand := capacityBandFor(group.Winners)
	trigger := reverseTrigger(group.CellKey.EventFamily, th)
	long, short, flat := directionBranches(group.CellKey.EventFamily)
	pb := FrozenEventPlaybook{
		SchemaVersion:         SchemaVersionPlaybook,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		ProfileID:             group.ProfileID,
		Ordinal:               ordinal,
		EventFamily:           group.CellKey.EventFamily,
		Horizon:               group.CellKey.Horizon,
		DelayBars:             group.CellKey.DelayBars,
		ShockCluster:          group.ShockCluster,
		TriggerConditions:     trigger,
		LongBranch:            long,
		ShortBranch:           short,
		FlatBranch:            flat,
		MaxWaitTime:           group.CellKey.Horizon,
		DynamicDeadline: DynamicDeadlineSpec{
			Horizon:           group.CellKey.Horizon,
			FallbackOnly:      protocol.DeadlineIsFallbackOnly,
			StopErrorRequired: protocol.StopErrorRequired,
		},
		InvalidatorConditions: append([]string(nil), protocol.StopErrorExitTriggers...),
		CapacityBand:          capacityBand,
		EligibleCapitalTiers:  eligible,
		PersonalExecutionRequirements: personalExecutionRequirements(group.ProfileID),
		StopConditions: append([]string(nil),
			"event_complete",
			"payer_disappeared",
			"invalidator",
			"ood",
			"deadline_fallback",
		),
		EventCompletion:     EventCompletionStopError,
		PayerStatus:         PayerStatusUnexplained,
		Status:              protocol.PersonalWildCandidateStatus,
		ValidationStage:     ValidationStagePending,
		TradeAllowed:        false,
		ExamEligible:        false,
		SourceWinnerCount:   len(group.Winners),
		SourceWinnerIDsHash: sourceHash,
		MapManifestHash:     group.ManifestHash,
		DiscoveryMode:         protocol.DiscoveryMode,
		ExamActions:           protocol.ExamActions,
	}
	hash, err := CanonicalPlaybookHash(pb)
	if err != nil {
		return FrozenEventPlaybook{}, err
	}
	pb.PlaybookHash = hash
	return pb, nil
}

func reverseTrigger(family string, th eventmap.FrozenThresholds) TriggerSpec {
	spec := TriggerSpec{EventFamily: family}
	switch family {
	case eventmap.FamilyExtremeContinuation:
		spec.Conditions = []TriggerCondition{
			{Field: "abs_return_bps", Op: "gte", Value: th.ExtremeReturnAbsBPS},
			{Field: "rms_bps", Op: "gte", Value: th.ExtremeRMSBPS},
			{Field: "return_sign", Op: "eq_prior_sign", Note: "continuation_setup"},
		}
	case eventmap.FamilyExtremeReversal:
		spec.Conditions = []TriggerCondition{
			{Field: "abs_return_bps", Op: "gte", Value: th.ExtremeReturnAbsBPS},
			{Field: "rms_bps", Op: "gte", Value: th.ExtremeRMSBPS},
			{Field: "return_sign", Op: "ne_prior_sign", Note: "reversal_setup"},
		}
	case eventmap.FamilySessionLiquidity:
		spec.Conditions = []TriggerCondition{
			{Field: "session_boundary", Op: "eq", Value: 1},
			{Field: "avg_quote_volume", Op: "lte", Value: th.LowLiquidityQuote},
			{Field: "volume_spike", Op: "gte", Value: th.VolumeSpikeQuote},
		}
	case eventmap.FamilyBTCRelative:
		spec.Conditions = []TriggerCondition{
			{Field: "btc_relative_abs_bps", Op: "gte", Value: th.BTCRelativeAbsBPS},
		}
	default:
		spec.Conditions = []TriggerCondition{
			{Field: "event_family", Op: "eq", Note: family},
		}
	}
	return spec
}

func directionBranches(family string) (DirectionBranch, DirectionBranch, DirectionBranch) {
	switch family {
	case eventmap.FamilyExtremeContinuation:
		return DirectionBranch{
				Action: "LONG", Thesis: "breakout_continuation_success",
				EntryRule: "trigger_fires_and_follow_through_confirmed",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "SHORT", Thesis: "failed_breakout_fade",
				EntryRule: "trigger_fires_but_follow_through_fails",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "NO_TRADE", Thesis: "ambiguous_breakout",
				EntryRule: "trigger_fires_without_clear_follow_through_or_failure",
				ExitRule:  "max_wait_elapsed",
			}
	case eventmap.FamilyExtremeReversal:
		return DirectionBranch{
				Action: "SHORT", Thesis: "exhaustion_reversal_down",
				EntryRule: "extreme_up_trigger_with_reversal_signature",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "LONG", Thesis: "exhaustion_reversal_up",
				EntryRule: "extreme_down_trigger_with_reversal_signature",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "NO_TRADE", Thesis: "exhaustion_not_confirmed",
				EntryRule: "trigger_without_clear_exhaustion_path",
				ExitRule:  "max_wait_elapsed",
			}
	case eventmap.FamilySessionLiquidity:
		return DirectionBranch{
				Action: "LONG", Thesis: "session_liquidity_recovery_long",
				EntryRule: "low_liquidity_boundary_with_recovery_spike",
				ExitRule:  "liquidity_normalized_or_deadline",
			},
			DirectionBranch{
				Action: "SHORT", Thesis: "session_liquidity_fade_short",
				EntryRule: "recovery_spike_fails_and_flow_reverses",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "NO_TRADE", Thesis: "session_noise",
				EntryRule: "boundary_without_actionable_recovery",
				ExitRule:  "max_wait_elapsed",
			}
	default:
		return DirectionBranch{
				Action: "LONG", Thesis: "dislocation_revert_long",
				EntryRule: "relative_dislocation_extreme_with_mean_revert_setup",
				ExitRule:  "dislocation_closed_or_deadline",
			},
			DirectionBranch{
				Action: "SHORT", Thesis: "dislocation_extend_short",
				EntryRule: "dislocation_extreme_with_continuation_setup",
				ExitRule:  "event_complete_or_invalidator",
			},
			DirectionBranch{
				Action: "NO_TRADE", Thesis: "dislocation_ambiguous",
				EntryRule: "trigger_without_clear_revert_or_extend",
				ExitRule:  "max_wait_elapsed",
			}
	}
}

func personalExecutionRequirements(profileID string) []string {
	return []string{
		"fingerprint_available_or_block_open",
		"delay_bars_in_" + fmt.Sprint(protocol.DelayBars),
		"min_notional_and_lot_feasible",
		"personal_conservative_expected_net_ge_zero",
		"profile:" + profileID,
	}
}

func capacityBandFor(winners []ResidualWinnerEntry) string {
	maxCap := 0.0
	for _, w := range winners {
		if w.MaxCapacityUSDT > maxCap {
			maxCap = w.MaxCapacityUSDT
		}
	}
	if maxCap <= 0 {
		return "small_cap_residual_unbounded"
	}
	return fmt.Sprintf("0_%d_usdt", int(maxCap))
}

func mergeEligibleTiers(winners []ResidualWinnerEntry) []string {
	seen := map[string]bool{}
	var tiers []string
	for _, winner := range winners {
		for _, tier := range winner.EligibleCapitalTiers {
			if tier == "" || seen[tier] {
				continue
			}
			seen[tier] = true
			tiers = append(tiers, tier)
		}
	}
	sort.Strings(tiers)
	if len(tiers) == 0 {
		return []string{protocol.ActualEquityTierLabel}
	}
	return tiers
}

func winnerIDs(winners []ResidualWinnerEntry) []string {
	ids := make([]string, 0, len(winners))
	for _, winner := range winners {
		ids = append(ids, winner.EventID)
	}
	sort.Strings(ids)
	return ids
}

// HashWinnerIDs hashes sorted winner event ids.
func HashWinnerIDs(ids []string) (string, error) {
	hash, _, err := datatruth.HashJSON(ids)
	return hash, err
}

// DedupTrackerKey freezes discovery dedup key = symbol|family|horizon|delay_bars.
func DedupTrackerKey(symbol, family, horizon string, delayBars int) string {
	return fmt.Sprintf("%s|%s|%s|%d", symbol, family, horizon, delayBars)
}

// NonOverlapCooldownBars returns profile-scaled event window span in bars.
func NonOverlapCooldownBars(group ResidualWinnerGroup) (int, error) {
	spec, err := eventmap.ProfileSpecFor(group.ProfileID)
	if err != nil {
		return 0, err
	}
	return eventmap.CooldownBarsForEvent(spec, group.CellKey.Horizon, group.CellKey.DelayBars), nil
}
