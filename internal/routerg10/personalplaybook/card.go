package personalplaybook

import (
	"errors"
	"fmt"
	"strings"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// ForbiddenTriggerFields must never appear in public trigger conditions.
var ForbiddenTriggerFields = []string{
	"institution_exclude_reason",
	"retail_exclude_reason",
	"bot_exclude_reason",
	"exclusion_proof_hash",
	"entry_proof_hash",
	"execution_policy_hash",
	"exit_proof_hash",
	"announcement_tape_hash",
	"constitution_hash",
	"future_outcome",
	"realized_pnl",
	"conservative_net_bps",
	"outcome",
	"winner",
	"direction",
	"delay_bars",
	"shock_cluster",
	"max_capacity_usdt",
	"absolute_profit_usdt",
}

// KnownPublicTriggerFields are allowed feature-only predicates.
var KnownPublicTriggerFields = []string{
	"visible_announcement_count",
	"cross_venue_shock_cluster",
	"dynamic_min_notional_usdt",
	"min_lot_nonlinearity",
	"event_family",
	"mechanical_flow_exhaustion",
	"prepositioned_capital_window",
	"custody_authorization_limit",
	"event_sparsity",
	"dynamic_threshold_shift",
	"personal_tape_family",
	"horizon",
}

// KnownPublicTriggerOps are allowed comparison operators.
var KnownPublicTriggerOps = []string{
	"gte", "lte", "gt", "lt", "eq",
}

// CanonicalPlaybookHash hashes one personal wild playbook identity before sealing.
func CanonicalPlaybookHash(pb PersonalWildPlaybook) (string, error) {
	payload := struct {
		TapeHash           string               `json:"tape_hash"`
		SourceEventID      string               `json:"source_event_id"`
		ProofBinding       ProofBinding         `json:"proof_binding"`
		Symbol             string               `json:"symbol"`
		ProfileID          string               `json:"profile_id"`
		EventFamily        string               `json:"event_family"`
		PersonalTapeFamily string               `json:"personal_tape_family"`
		Horizon            string               `json:"horizon"`
		DelayBars          int                  `json:"delay_bars"`
		Direction          string               `json:"direction"`
		ShockCluster       string               `json:"shock_cluster"`
		ExclusionNarrative ExclusionNarrative   `json:"exclusion_narrative"`
		EntryNarrative     EntryNarrative       `json:"entry_narrative"`
		ExecutionNarrative ExecutionNarrative   `json:"execution_narrative"`
		RiskNarrative      RiskNarrative        `json:"risk_narrative"`
		ExitNarrative      ExitNarrative        `json:"exit_narrative"`
		StopNarrative      StopNarrative        `json:"stop_narrative"`
		PublicTrigger      PublicTriggerSpec    `json:"public_trigger"`
		Status             string               `json:"status"`
		DiscoveryMode        string               `json:"discovery_mode"`
	}{
		TapeHash:           pb.TapeHash,
		SourceEventID:      pb.SourceEventID,
		ProofBinding:       pb.ProofBinding,
		Symbol:             pb.Symbol,
		ProfileID:          pb.ProfileID,
		EventFamily:        pb.EventFamily,
		PersonalTapeFamily: pb.PersonalTapeFamily,
		Horizon:            pb.Horizon,
		DelayBars:          pb.DelayBars,
		Direction:          pb.Direction,
		ShockCluster:       pb.ShockCluster,
		ExclusionNarrative: pb.ExclusionNarrative,
		EntryNarrative:     pb.EntryNarrative,
		ExecutionNarrative: pb.ExecutionNarrative,
		RiskNarrative:      pb.RiskNarrative,
		ExitNarrative:      pb.ExitNarrative,
		StopNarrative:      pb.StopNarrative,
		PublicTrigger:      pb.PublicTrigger,
		Status:             pb.Status,
		DiscoveryMode:      pb.DiscoveryMode,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashPlaybooks hashes the ordered certified playbook bundle.
func HashPlaybooks(playbooks []PersonalWildPlaybook) (string, error) {
	certified := make([]PersonalWildPlaybook, 0, len(playbooks))
	for _, pb := range playbooks {
		if pb.Status == StatusPersonalWildCandidate {
			certified = append(certified, pb)
		}
	}
	hash, _, err := datatruth.HashJSON(certified)
	return hash, err
}

// ValidatePlaybook checks certified candidate invariants and hash chain.
func ValidatePlaybook(pb PersonalWildPlaybook) error {
	if pb.SchemaVersion != SchemaVersionPlaybook {
		return errors.New("playbook schema mismatch")
	}
	if pb.Generation != Generation || pb.ProtocolHash != g10protocolv2.RouterProtocolHash {
		return errors.New("playbook generation/protocol mismatch")
	}
	if err := validateProofBinding(pb.ProofBinding); err != nil {
		return err
	}
	if pb.Status == StatusPersonalWildCandidate {
		if err := ValidateExclusionAccepted(pb.ProofBinding, pb.ExclusionNarrative); err != nil {
			return err
		}
	}
	if err := validateRequiredNarratives(pb); err != nil {
		return err
	}
	if err := validatePublicTrigger(pb.PublicTrigger); err != nil {
		return err
	}
	if pb.Status == StatusPersonalWildCandidate {
		if pb.TradeAllowed || pb.ExamEligible || pb.Certified {
			return errors.New("personal wild candidates must not trade, exam, or certify")
		}
		if pb.ValidationStage != ValidationStagePending {
			return fmt.Errorf("validation_stage must be %s", ValidationStagePending)
		}
	}
	if pb.Status == StatusNotPersonalEnough {
		if len(pb.NotPersonalReasons) == 0 {
			return errors.New("not_personal_enough requires reasons")
		}
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
	if pb.ExamActions != g10protocolv2.ExamActions {
		return errors.New("exam_actions must be zero")
	}
	if pb.DiscoveryMode != DiscoveryMode {
		return errors.New("discovery mode mismatch")
	}
	return nil
}

// ValidateDiscoveryPlaybook checks discovery-only playbooks are never certified.
func ValidateDiscoveryPlaybook(pb PersonalWildPlaybook) error {
	if pb.Status != StatusDiscoveryOnly {
		return errors.New("discovery playbook status mismatch")
	}
	if pb.Certified {
		return errors.New("discovery playbooks cannot be certified")
	}
	if err := validateProofBinding(pb.ProofBinding); err != nil {
		return err
	}
	want, err := CanonicalPlaybookHash(pb)
	if err != nil {
		return err
	}
	if pb.PlaybookHash != want {
		return errors.New("discovery playbook hash mismatch")
	}
	return nil
}

func validateProofBinding(binding ProofBinding) error {
	if binding.ConstitutionHash == "" {
		return errors.New("missing constitution_hash")
	}
	for _, field := range PlaybookHashChain[:len(PlaybookHashChain)-1] {
		switch field {
		case "ConstitutionHash":
			if binding.ConstitutionHash == "" {
				return errors.New("proof binding: constitution_hash required")
			}
		case "ExclusionProofHash":
			if binding.ExclusionProofHash == "" {
				return errors.New("proof binding: exclusion_proof_hash required")
			}
		case "EntryProofHash":
			if binding.EntryProofHash == "" {
				return errors.New("proof binding: entry_proof_hash required")
			}
		case "ExecutionPolicyHash":
			if binding.ExecutionPolicyHash == "" {
				return errors.New("proof binding: execution_policy_hash required")
			}
		case "ExitProofHash":
			if binding.ExitProofHash == "" {
				return errors.New("proof binding: exit_proof_hash required")
			}
		case "AnnouncementTapeHash":
			if binding.AnnouncementTapeHash == "" {
				return errors.New("proof binding: announcement_tape_hash required")
			}
		}
	}
	if binding.InstitutionAxisHash == "" || binding.RetailAxisHash == "" || binding.BotAxisHash == "" {
		return errors.New("proof binding: triple axis proof hashes required")
	}
	return nil
}

func validateRequiredNarratives(pb PersonalWildPlaybook) error {
	ex := pb.ExclusionNarrative
	if ex.InstitutionExcludeReason == "" || ex.RetailExcludeReason == "" || ex.BotExcludeReason == "" {
		if pb.Status == StatusPersonalWildCandidate {
			return errors.New("missing triple exclusion narrative")
		}
	}
	if pb.EntryNarrative.WhyUserCanEnter == "" {
		return errors.New("missing why_user_can_enter")
	}
	if pb.ExecutionNarrative.MakerSelectionConclusion == "" {
		return errors.New("missing maker_selection_conclusion")
	}
	if len(pb.ExecutionNarrative.AllowedActions) == 0 {
		return errors.New("missing allowed_actions")
	}
	if pb.ExecutionNarrative.FrozenExecutionAction == "" {
		return errors.New("missing frozen_execution_action")
	}
	if pb.RiskNarrative.MaxCapacityUSDT < 0 || pb.RiskNarrative.EventTerminalBudgetFraction <= 0 {
		return errors.New("missing risk narrative")
	}
	if len(pb.ExitNarrative.InvalidatorConditions) == 0 || pb.ExitNarrative.DeadlineHorizon == "" {
		return errors.New("missing exit narrative")
	}
	if pb.StopNarrative.CrowdingStopRule == "" || pb.StopNarrative.CapitalExpansionStopRule == "" {
		return errors.New("missing stop narrative")
	}
	return nil
}

func validatePublicTrigger(spec PublicTriggerSpec) error {
	if len(spec.Conditions) == 0 {
		return errors.New("missing public_trigger conditions")
	}
	for _, cond := range spec.Conditions {
		field := strings.ToLower(strings.TrimSpace(cond.Field))
		for _, forbidden := range ForbiddenTriggerFields {
			if field == forbidden {
				return fmt.Errorf("trigger field %q is forbidden (exclusion/lookahead)", cond.Field)
			}
		}
		if !knownPublicTriggerField(field) {
			return fmt.Errorf("trigger field %q is unknown (fail closed)", cond.Field)
		}
		op := strings.ToLower(strings.TrimSpace(cond.Op))
		if !knownPublicTriggerOp(op) {
			return fmt.Errorf("trigger op %q is unknown (fail closed)", cond.Op)
		}
	}
	return nil
}

func knownPublicTriggerField(field string) bool {
	for _, want := range KnownPublicTriggerFields {
		if field == want {
			return true
		}
	}
	return false
}

func knownPublicTriggerOp(op string) bool {
	for _, want := range KnownPublicTriggerOps {
		if op == want {
			return true
		}
	}
	return false
}
