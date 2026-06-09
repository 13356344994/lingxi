package app

import (
	"fmt"
	"strings"
	"time"
)

type exposureLockState struct {
	Phase    string
	Mode     string
	RunID    string
	CycleID  string
	Reason   string
	LockedAt time.Time
}

func (e *FundingRateSniperEngine) lockSymbolForExposure(task exposureRescueTask) {
	if e == nil || strings.TrimSpace(task.Symbol) == "" {
		return
	}
	e.platformRiskMu.Lock()
	if e.exposureLocks == nil {
		e.exposureLocks = make(map[string]exposureLockState)
	}
	e.exposureLocks[task.Symbol] = exposureLockState{
		Phase:    strings.TrimSpace(task.Phase),
		Mode:     string(task.Mode),
		RunID:    strings.TrimSpace(e.runID),
		CycleID:  strings.TrimSpace(e.cycleIDSnapshot()),
		Reason:   fmt.Sprintf("phase=%s mode=%s pair=%s", task.Phase, task.Mode, task.Pair),
		LockedAt: time.Now().UTC(),
	}
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) releaseExposureLock(symbol string) {
	if e == nil || strings.TrimSpace(symbol) == "" {
		return
	}
	e.platformRiskMu.Lock()
	delete(e.exposureLocks, symbol)
	e.platformRiskMu.Unlock()
}

func (e *FundingRateSniperEngine) exposureLockState(symbol string) (exposureLockState, bool) {
	if e == nil || strings.TrimSpace(symbol) == "" {
		return exposureLockState{}, false
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	state, ok := e.exposureLocks[symbol]
	return state, ok
}

func (e *FundingRateSniperEngine) ensureSymbolNotLockedByExposure(symbol string, action string) error {
	if e == nil || strings.TrimSpace(symbol) == "" {
		return nil
	}
	state, ok := e.exposureLockState(symbol)
	if !ok {
		return nil
	}
	return fmt.Errorf(
		"symbol=%s 已被 ExposureManager 锁定，禁止执行 %s: phase=%s mode=%s locked_at=%s",
		symbol,
		nonEmpty(strings.TrimSpace(action), "operation"),
		nonEmpty(state.Phase, "unknown"),
		nonEmpty(state.Mode, "unknown"),
		state.LockedAt.UTC().Format(time.RFC3339),
	)
}
