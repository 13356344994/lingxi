package main

import (
	"fmt"
	"strings"
	"time"
)

var allowedLifecycleTransitions = map[LifecycleStage]map[LifecycleStage]struct{}{
	LifecycleStageReady: {
		LifecycleStageScanning:    {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
	},
	LifecycleStageScanning: {
		LifecycleStageMonitoring:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
		LifecycleStageCompleted:   {},
	},
	LifecycleStageMonitoring: {
		LifecycleStagePreOpenCheck: {},
		LifecycleStageCoolingDown:  {},
		LifecycleStagePaused:       {},
		LifecycleStageFused:        {},
		LifecycleStageCompleted:    {},
	},
	LifecycleStagePreOpenCheck: {
		LifecycleStageOpening:     {},
		LifecycleStageRecovering:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
	},
	LifecycleStageOpening: {
		LifecycleStageValidatingOpen: {},
		LifecycleStageHolding:        {},
		LifecycleStageRecovering:     {},
		LifecycleStageCoolingDown:    {},
		LifecycleStagePaused:         {},
		LifecycleStageFused:          {},
	},
	LifecycleStageValidatingOpen: {
		LifecycleStageHolding:     {},
		LifecycleStageRecovering:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
	},
	LifecycleStageHolding: {
		LifecycleStageCloseMonitoring: {},
		LifecycleStageClosing:         {},
		LifecycleStageRecovering:      {},
		LifecycleStageCoolingDown:     {},
		LifecycleStagePaused:          {},
		LifecycleStageFused:           {},
		LifecycleStageCompleted:       {},
	},
	LifecycleStageCloseMonitoring: {
		LifecycleStageClosing:     {},
		LifecycleStageRecovering:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
		LifecycleStageCompleted:   {},
	},
	LifecycleStageClosing: {
		LifecycleStageSettling:    {},
		LifecycleStageRecovering:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
	},
	LifecycleStageSettling: {
		LifecycleStageCompleted:   {},
		LifecycleStageRecovering:  {},
		LifecycleStageCoolingDown: {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
	},
	LifecycleStageRecovering: {
		LifecycleStageRecovering:     {},
		LifecycleStageHolding:        {},
		LifecycleStageCloseMonitoring: {},
		LifecycleStageClosing:        {},
		LifecycleStageCoolingDown:    {},
		LifecycleStagePaused:         {},
		LifecycleStageFused:          {},
		LifecycleStageCompleted:      {},
	},
	LifecycleStageCoolingDown: {
		LifecycleStageReady:       {},
		LifecycleStageScanning:    {},
		LifecycleStagePaused:      {},
		LifecycleStageFused:       {},
		LifecycleStageCompleted:   {},
	},
	LifecycleStagePaused: {
		LifecycleStageReady:       {},
		LifecycleStageScanning:    {},
		LifecycleStageCoolingDown: {},
		LifecycleStageFused:       {},
		LifecycleStageCompleted:   {},
	},
	LifecycleStageFused: {
		LifecycleStagePaused:      {},
		LifecycleStageCoolingDown: {},
		LifecycleStageCompleted:   {},
	},
	LifecycleStageCompleted: {
		LifecycleStageReady:     {},
		LifecycleStageScanning:  {},
		LifecycleStageCompleted: {},
	},
}

func NormalizeLifecycleStage(stage LifecycleStage) LifecycleStage {
	return LifecycleStage(strings.TrimSpace(string(stage)))
}

func ValidateLifecycleTransition(from LifecycleStage, to LifecycleStage) error {
	from = NormalizeLifecycleStage(from)
	to = NormalizeLifecycleStage(to)
	switch {
	case from == "" && to == "":
		return nil
	case to == "" || from == to:
		return nil
	case from == "":
		return nil
	}
	if next, ok := allowedLifecycleTransitions[from]; ok {
		if _, allowed := next[to]; allowed {
			return nil
		}
	}
	return fmt.Errorf("非法生命周期迁移: %s -> %s", from, to)
}

func NewLifecycleTransition(from LifecycleStage, to LifecycleStage, reason string) (LifecycleTransition, error) {
	if err := ValidateLifecycleTransition(from, to); err != nil {
		return LifecycleTransition{}, err
	}
	from = NormalizeLifecycleStage(from)
	to = NormalizeLifecycleStage(to)
	if to == "" {
		to = from
	}
	return LifecycleTransition{
		From:         from,
		To:           to,
		Reason:       strings.TrimSpace(reason),
		Transitioned: time.Now().UTC(),
	}, nil
}

func NewLifecycleTransitionFromProtocol(result ProtocolResult) (LifecycleTransition, error) {
	from := NormalizeLifecycleStage(result.CurrentStage)
	to := NormalizeLifecycleStage(result.Lifecycle.NextStage)
	reason := strings.TrimSpace(nonEmpty(result.Lifecycle.Reason, result.Summary))
	if from == "" {
		return LifecycleTransition{}, fmt.Errorf("protocol %s 缺少 current stage", strings.TrimSpace(result.Protocol))
	}
	return NewLifecycleTransition(from, to, reason)
}

type LifecycleStateMachine struct {
	current     LifecycleStage
	transitions []LifecycleTransition
}

func NewLifecycleStateMachine(initial LifecycleStage) *LifecycleStateMachine {
	return &LifecycleStateMachine{current: NormalizeLifecycleStage(initial)}
}

func (m *LifecycleStateMachine) CurrentStage() LifecycleStage {
	if m == nil {
		return ""
	}
	return NormalizeLifecycleStage(m.current)
}

func (m *LifecycleStateMachine) Transitions() []LifecycleTransition {
	if m == nil || len(m.transitions) == 0 {
		return nil
	}
	out := make([]LifecycleTransition, len(m.transitions))
	copy(out, m.transitions)
	return out
}

func (m *LifecycleStateMachine) ApplyProtocolResult(result ProtocolResult) (LifecycleTransition, error) {
	if m == nil {
		return LifecycleTransition{}, fmt.Errorf("lifecycle state machine 未初始化")
	}
	transition, err := NewLifecycleTransitionFromProtocol(result)
	if err != nil {
		return LifecycleTransition{}, err
	}
	if current := NormalizeLifecycleStage(m.current); current != "" && current != NormalizeLifecycleStage(transition.From) {
		return LifecycleTransition{}, fmt.Errorf("生命周期状态不一致: machine=%s protocol=%s", current, transition.From)
	}
	m.current = NormalizeLifecycleStage(transition.To)
	m.transitions = append(m.transitions, transition)
	return transition, nil
}

func (e *FundingRateSniperEngine) resetLifecycleTracking(initial LifecycleStage, reason string) {
	if e == nil {
		return
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	initial = NormalizeLifecycleStage(initial)
	e.lifecycleStateMachine = NewLifecycleStateMachine(initial)
	e.currentLifecycleStage = initial
	e.currentLifecycleReason = strings.TrimSpace(reason)
	e.currentLifecycleTransitionedAt = time.Now().UTC().Format(time.RFC3339)
	e.lastLifecycleTransitionFrom = ""
	e.lastLifecycleTransitionTo = initial
}

func (e *FundingRateSniperEngine) advanceLifecycleStage(next LifecycleStage, reason string) error {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	next = NormalizeLifecycleStage(next)
	reason = strings.TrimSpace(reason)
	if e.lifecycleStateMachine == nil {
		e.lifecycleStateMachine = NewLifecycleStateMachine(next)
		e.currentLifecycleStage = next
		e.currentLifecycleReason = reason
		e.currentLifecycleTransitionedAt = time.Now().UTC().Format(time.RFC3339)
		e.lastLifecycleTransitionFrom = ""
		e.lastLifecycleTransitionTo = next
		return nil
	}
	from := NormalizeLifecycleStage(e.lifecycleStateMachine.CurrentStage())
	transition, err := NewLifecycleTransition(from, next, reason)
	if err != nil {
		return err
	}
	e.lifecycleStateMachine.current = NormalizeLifecycleStage(transition.To)
	e.lifecycleStateMachine.transitions = append(e.lifecycleStateMachine.transitions, transition)
	e.currentLifecycleStage = transition.To
	e.currentLifecycleReason = transition.Reason
	e.currentLifecycleTransitionedAt = transition.Transitioned.Format(time.RFC3339)
	e.lastLifecycleTransitionFrom = transition.From
	e.lastLifecycleTransitionTo = transition.To
	return nil
}

func (e *FundingRateSniperEngine) applyLifecycleProtocolResult(result ProtocolResult) error {
	if e == nil {
		return nil
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	currentStage := NormalizeLifecycleStage(result.CurrentStage)
	if e.lifecycleStateMachine == nil {
		e.lifecycleStateMachine = NewLifecycleStateMachine(currentStage)
		e.currentLifecycleStage = currentStage
	}
	transition, err := e.lifecycleStateMachine.ApplyProtocolResult(result)
	if err != nil {
		return err
	}
	e.currentLifecycleStage = transition.To
	e.currentLifecycleReason = strings.TrimSpace(nonEmpty(transition.Reason, result.Summary))
	e.currentLifecycleTransitionedAt = transition.Transitioned.Format(time.RFC3339)
	e.lastLifecycleTransitionFrom = transition.From
	e.lastLifecycleTransitionTo = transition.To
	return nil
}
