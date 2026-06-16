package app

import "strings"

const (
	crossVenueSwitchSequenceCloseThenOpen = "close_then_open"
	crossVenueSwitchSequenceOpenThenClose = "open_then_close"
	crossVenueSwitchSequenceStagedSwitch  = "staged_switch"
)

func crossVenueConfiguredSwitchSequence(cfg CrossVenueExecutorConfig) string {
	switch strings.ToLower(strings.TrimSpace(cfg.SwitchSequence)) {
	case crossVenueSwitchSequenceOpenThenClose:
		return crossVenueSwitchSequenceOpenThenClose
	case crossVenueSwitchSequenceStagedSwitch:
		return crossVenueSwitchSequenceStagedSwitch
	default:
		return crossVenueSwitchSequenceCloseThenOpen
	}
}

func crossVenueChooseSwitchSequence(cfg CrossVenueExecutorConfig, current *CrossVenuePosition, next CrossVenueOpportunity) string {
	seq := crossVenueConfiguredSwitchSequence(cfg)
	if seq == crossVenueSwitchSequenceOpenThenClose || seq == crossVenueSwitchSequenceStagedSwitch {
		// 只有在市场质量、恢复质量、执行质量都足够高时才允许开放进攻型序列；
		// 否则强制回退为 close_then_open，保持文档要求的保守默认。
		if current == nil || current.RecoveryQualityScore < 0.85 || next.RecoveryQualityScore < 0.85 || next.ExecutionScore < 0.85 || next.LiquidityScore < 0.80 {
			return crossVenueSwitchSequenceCloseThenOpen
		}
	}
	return seq
}

func crossVenueSwitchesAllowedPerCycle(cfg CrossVenueExecutorConfig) int {
	if cfg.MaxSwitchesPerCycle > 0 {
		return cfg.MaxSwitchesPerCycle
	}
	return 1
}

func crossVenueSwitchSequenceNeedsTargetFirst(seq string) bool {
	switch strings.ToLower(strings.TrimSpace(seq)) {
	case crossVenueSwitchSequenceOpenThenClose, crossVenueSwitchSequenceStagedSwitch:
		return true
	default:
		return false
	}
}

func crossVenueTransitionHeadroomSlots(intents []crossVenueSwitchIntent) int {
	for _, intent := range intents {
		if !crossVenueSwitchSequenceNeedsTargetFirst(intent.SwitchSequence) {
			continue
		}
		switch strings.TrimSpace(intent.PartialSwitchPhase) {
		case "", "pending", "open_new", "open_new_first_leg", "open_new_retry", "open_new_second_leg":
			return 1
		}
	}
	return 0
}

func crossVenueIsStagedSwitch(seq string) bool {
	return strings.EqualFold(strings.TrimSpace(seq), crossVenueSwitchSequenceStagedSwitch)
}

func crossVenueIsStagedWaitPhase(phase string) bool {
	switch strings.TrimSpace(phase) {
	case "staged_close_old", "staged_open_new_second_leg":
		return true
	default:
		return false
	}
}

func crossVenueStagedSwitchReadyForSecondLeg(intent crossVenueSwitchIntent) bool {
	if !crossVenueIsStagedSwitch(intent.SwitchSequence) {
		return false
	}
	switch strings.TrimSpace(intent.PartialSwitchPhase) {
	case "open_new", "open_new_second_leg", "closed_old", "consumed":
		return true
	default:
		return false
	}
}

func crossVenueSwitchTargetOpenCompleted(pos CrossVenuePosition) bool {
	return strings.TrimSpace(pos.PartialSwitchPhase) == "consumed" || pos.Status == CrossVenueStatusOpen
}
