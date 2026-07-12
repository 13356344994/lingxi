package reverseevent

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

const (
	LedgerStatusDuplicate   = "duplicate"
	LedgerStatusInvalid     = "invalid"
	LedgerStatusUnexplained = "unexplained"
	LedgerStatusNonOverlap  = "skipped_nonoverlap"
)

// CanonicalEventCardHash hashes one EventCard identity before sealing.
func CanonicalEventCardHash(card FrozenEventCard) (string, error) {
	payload := struct {
		ProfileID             string              `json:"profile_id"`
		EventFamily           string              `json:"event_family"`
		Horizon               string              `json:"horizon"`
		Direction             string              `json:"direction"`
		DelayBars             int                 `json:"delay_bars"`
		ShockCluster          string              `json:"shock_cluster"`
		TriggerConditions     TriggerSpec         `json:"trigger_conditions"`
		DirectionRule         string              `json:"direction_rule"`
		MaxWaitTime           string              `json:"max_wait_time"`
		DynamicDeadline       DynamicDeadlineSpec `json:"dynamic_deadline"`
		InvalidatorConditions []string            `json:"invalidator_conditions"`
		CapacityBand          string              `json:"capacity_band"`
		CapitalTiers          []int               `json:"capital_tiers"`
		DataRequirements      []string            `json:"data_requirements"`
		EventCompletion       string              `json:"event_completion"`
		PayerStatus           string              `json:"payer_status"`
		DiscoveryMode         string              `json:"discovery_mode"`
		MapManifestHash       string              `json:"map_manifest_hash"`
		SourceWinnerIDsHash   string              `json:"source_winner_ids_hash"`
	}{
		ProfileID:             card.ProfileID,
		EventFamily:           card.EventFamily,
		Horizon:               card.Horizon,
		Direction:             card.Direction,
		DelayBars:             card.DelayBars,
		ShockCluster:          card.ShockCluster,
		TriggerConditions:     card.TriggerConditions,
		DirectionRule:         card.DirectionRule,
		MaxWaitTime:           card.MaxWaitTime,
		DynamicDeadline:       card.DynamicDeadline,
		InvalidatorConditions: append([]string(nil), card.InvalidatorConditions...),
		CapacityBand:          card.CapacityBand,
		CapitalTiers:          append([]int(nil), card.CapitalTiers...),
		DataRequirements:      append([]string(nil), card.DataRequirements...),
		EventCompletion:       card.EventCompletion,
		PayerStatus:           card.PayerStatus,
		DiscoveryMode:         card.DiscoveryMode,
		MapManifestHash:       card.MapManifestHash,
		SourceWinnerIDsHash:   card.SourceWinnerIDsHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashEventCards hashes the ordered card bundle for manifest binding.
func HashEventCards(cards []FrozenEventCard) (string, error) {
	hash, _, err := datatruth.HashJSON(cards)
	return hash, err
}

// ValidateEventCard checks card invariants and required fields.
func ValidateEventCard(card FrozenEventCard) error {
	if card.SchemaVersion != SchemaVersionCard {
		return errors.New("card schema mismatch")
	}
	if card.Generation != protocol.Generation || card.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("card generation/protocol mismatch")
	}
	if card.Status != protocol.UnexplainedCandidateStatus {
		return fmt.Errorf("card status must be %s", protocol.UnexplainedCandidateStatus)
	}
	if card.ValidationStage != ValidationStagePending {
		return fmt.Errorf("card validation_stage must be %s", ValidationStagePending)
	}
	if card.TradeAllowed || card.ExamEligible {
		return errors.New("unexplained cards must not allow trade or exam")
	}
	if containsPassStatus(card.ValidationStage) || containsPassStatus(card.Status) {
		return errors.New("reverse discovery must not emit pass status")
	}
	for _, field := range protocol.EventCardRequiredFields {
		if err := requireCardField(card, field); err != nil {
			return err
		}
	}
	if card.EventCompletion == "" || card.PayerStatus == "" {
		return errors.New("card missing event_completion or payer_status")
	}
	if card.EventCardHash == "" {
		return errors.New("card missing event_card_hash")
	}
	want, err := CanonicalEventCardHash(card)
	if err != nil {
		return err
	}
	if card.EventCardHash != want {
		return errors.New("card hash mismatch")
	}
	if card.ExamActions != protocol.ExamActions {
		return errors.New("exam_actions must be zero")
	}
	return nil
}

func requireCardField(card FrozenEventCard, field string) error {
	switch field {
	case "trigger_conditions":
		if len(card.TriggerConditions.Conditions) == 0 {
			return errors.New("missing trigger_conditions")
		}
	case "direction_rule":
		if card.DirectionRule == "" {
			return errors.New("missing direction_rule")
		}
	case "max_wait_time":
		if card.MaxWaitTime == "" {
			return errors.New("missing max_wait_time")
		}
	case "dynamic_deadline":
		if card.DynamicDeadline.Horizon == "" {
			return errors.New("missing dynamic_deadline")
		}
	case "invalidator_conditions":
		if len(card.InvalidatorConditions) == 0 {
			return errors.New("missing invalidator_conditions")
		}
	case "capacity_band":
		if card.CapacityBand == "" {
			return errors.New("missing capacity_band")
		}
	case "capital_tiers":
		if len(card.CapitalTiers) == 0 {
			return errors.New("missing capital_tiers")
		}
	case "data_requirements":
		if len(card.DataRequirements) == 0 {
			return errors.New("missing data_requirements")
		}
	default:
		return fmt.Errorf("unknown required field %q", field)
	}
	return nil
}

func containsPassStatus(status string) bool {
	return status == protocol.ValidationPassStatus ||
		status == protocol.WFPassStatus ||
		status == protocol.MechanismUnexplainedEventPassStatus ||
		status == protocol.ExamBoundaryResearchPassStatus
}

// BuildEventCard reverse-derives one card from a winner group and frozen thresholds.
func BuildEventCard(group WinnerGroup, th eventmap.FrozenThresholds, ordinal int) (FrozenEventCard, error) {
	if len(group.Winners) == 0 {
		return FrozenEventCard{}, errors.New("empty winner group")
	}
	ids := winnerIDs(group.Winners)
	sourceHash, err := HashWinnerIDs(ids)
	if err != nil {
		return FrozenEventCard{}, err
	}
	capitalTiers := uniqueCapitalTiers(group)
	primaryFamily, err := ProfilePrimaryFamily(group.ProfileID)
	if err != nil {
		return FrozenEventCard{}, err
	}
	trigger := reverseTrigger(group.CellKey.EventFamily, th)
	card := FrozenEventCard{
		SchemaVersion:         SchemaVersionCard,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		ProfileID:             group.ProfileID,
		Ordinal:               ordinal,
		EventFamily:           group.CellKey.EventFamily,
		Horizon:               group.CellKey.Horizon,
		Direction:             group.CellKey.Direction,
		DelayBars:             group.CellKey.DelayBars,
		ShockCluster:             group.ShockCluster,
		TriggerConditions:     trigger,
		DirectionRule:         fmt.Sprintf("fixed:%s", group.CellKey.Direction),
		MaxWaitTime:           group.CellKey.Horizon,
		DynamicDeadline: DynamicDeadlineSpec{
			Horizon:           group.CellKey.Horizon,
			FallbackOnly:      protocol.DeadlineIsFallbackOnly,
			StopErrorRequired: protocol.StopErrorRequired,
		},
		InvalidatorConditions: append([]string(nil), protocol.StopErrorExitTriggers...),
		CapacityBand:          capacityBandFor(group.CellKey.CapitalTier),
		CapitalTiers:          capitalTiers,
		DataRequirements:      []string{primaryFamily + "/ohlcv", "btc_reference"},
		EventCompletion:       EventCompletionStopError,
		PayerStatus:           PayerStatusUnexplained,
		Status:                protocol.UnexplainedCandidateStatus,
		ValidationStage:       ValidationStagePending,
		TradeAllowed:          false,
		ExamEligible:          false,
		SourceWinnerCount:     len(group.Winners),
		SourceWinnerIDsHash:   sourceHash,
		MapManifestHash:       group.ManifestHash,
		DiscoveryMode:         protocol.DiscoveryMode,
		ExamActions:           protocol.ExamActions,
	}
	hash, err := CanonicalEventCardHash(card)
	if err != nil {
		return FrozenEventCard{}, err
	}
	card.EventCardHash = hash
	return card, nil
}

func reverseTrigger(family string, th eventmap.FrozenThresholds) TriggerSpec {
	spec := TriggerSpec{EventFamily: family}
	switch family {
	case eventmap.FamilyExtremeContinuation:
		spec.Conditions = []TriggerCondition{
			{Field: "abs_return_bps", Op: "gte", Value: th.ExtremeReturnAbsBPS},
			{Field: "rms_bps", Op: "gte", Value: th.ExtremeRMSBPS},
			{Field: "return_sign", Op: "eq_prior_sign", Note: "continuation"},
		}
	case eventmap.FamilyExtremeReversal:
		spec.Conditions = []TriggerCondition{
			{Field: "abs_return_bps", Op: "gte", Value: th.ExtremeReturnAbsBPS},
			{Field: "rms_bps", Op: "gte", Value: th.ExtremeRMSBPS},
			{Field: "return_sign", Op: "ne_prior_sign", Note: "reversal"},
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

func capacityBandFor(capitalTier int) string {
	return fmt.Sprintf("tier_%d_usdt", capitalTier)
}

func uniqueCapitalTiers(group WinnerGroup) []int {
	seen := map[int]bool{}
	var tiers []int
	for _, winner := range group.Winners {
		if seen[winner.CapitalTier] {
			continue
		}
		seen[winner.CapitalTier] = true
		tiers = append(tiers, winner.CapitalTier)
	}
	sort.Ints(tiers)
	if len(tiers) == 0 {
		tiers = []int{group.CellKey.CapitalTier}
	}
	return tiers
}

func winnerIDs(winners []eventmap.WinnerEntry) []string {
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

// NonOverlapCooldownBars returns profile-scaled event window span in bars.
func NonOverlapCooldownBars(group WinnerGroup) (int, error) {
	spec, err := eventmap.ProfileSpecFor(group.ProfileID)
	if err != nil {
		return 0, err
	}
	return eventmap.CooldownBarsForEvent(spec, group.CellKey.Horizon, group.CellKey.DelayBars), nil
}

// NonOverlapViolations reports symbol/family/date collisions inside one card group.
func NonOverlapViolations(group WinnerGroup, cooldownBars int, barDuration time.Duration) []string {
	if cooldownBars < 1 {
		cooldownBars = 1
	}
	if barDuration <= 0 {
		barDuration = time.Hour
	}
	winners := append([]eventmap.WinnerEntry(nil), group.Winners...)
	sort.Slice(winners, func(i, j int) bool {
		if winners[i].Symbol != winners[j].Symbol {
			return winners[i].Symbol < winners[j].Symbol
		}
		return winners[i].DecisionTime.Before(winners[j].DecisionTime)
	})
	type slot struct {
		end time.Time
	}
	last := map[string]slot{}
	var violations []string
	cooldown := time.Duration(cooldownBars) * barDuration
	for _, winner := range winners {
		key := winner.Symbol + "|" + winner.EventFamily
		if prev, ok := last[key]; ok && winner.DecisionTime.Before(prev.end) {
			violations = append(violations, winner.EventID)
		}
		last[key] = slot{end: winner.DecisionTime.Add(cooldown)}
	}
	return violations
}
