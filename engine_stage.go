package main

import (
	"fmt"
	"strings"
	"time"
)

type EngineStage string

const (
	StageReady              EngineStage = "ready"
	StageScan               EngineStage = "scan"
	StageScanEmpty          EngineStage = "scan_empty"
	StageMonitor            EngineStage = "monitor"
	StageMonitorInvalidated EngineStage = "monitor_invalidated"
	StageOpen               EngineStage = "open"
	StageOpenEmpty          EngineStage = "open_empty"
	StageClose              EngineStage = "close"
	StageCooldown           EngineStage = "cooldown"
	StageCompleted          EngineStage = "completed"
	StageStopped            EngineStage = "stopped"
	StageFailed             EngineStage = "failed"
)

type EngineStageTransition struct {
	From         EngineStage `json:"from"`
	To           EngineStage `json:"to"`
	Reason       string      `json:"reason"`
	Transitioned string      `json:"transitioned_at"`
}

var allowedEngineStageTransitions = map[EngineStage]map[EngineStage]struct{}{
	StageReady: {
		StageScan:     {},
		StageCooldown: {},
	},
	StageScan: {
		StageScanEmpty: {},
		StageMonitor:   {},
		StageCooldown:  {},
	},
	StageScanEmpty: {
		StageReady:     {},
		StageScan:      {},
		StageCooldown:  {},
		StageCompleted: {},
	},
	StageMonitor: {
		StageMonitorInvalidated: {},
		StageOpen:               {},
		StageCooldown:           {},
	},
	StageMonitorInvalidated: {
		StageReady:     {},
		StageScan:      {},
		StageCooldown:  {},
		StageCompleted: {},
	},
	StageOpen: {
		StageOpenEmpty: {},
		StageClose:     {},
		StageCooldown:  {},
	},
	StageOpenEmpty: {
		StageReady:     {},
		StageScan:      {},
		StageCooldown:  {},
		StageCompleted: {},
	},
	StageClose: {
		StageCompleted: {},
		StageCooldown:  {},
	},
	StageCooldown: {
		StageReady: {},
		StageScan:  {},
	},
	StageCompleted: {
		StageReady: {},
		StageScan:  {},
	},
}

func NormalizeEngineStage(stage EngineStage) EngineStage {
	return EngineStage(strings.TrimSpace(string(stage)))
}

func ValidateEngineStageTransition(from EngineStage, to EngineStage) error {
	from = NormalizeEngineStage(from)
	to = NormalizeEngineStage(to)
	switch {
	case to == "":
		return nil
	case from == "" || from == to:
		return nil
	case to == StageStopped || to == StageFailed:
		return nil
	}
	if next, ok := allowedEngineStageTransitions[from]; ok {
		if _, allowed := next[to]; allowed {
			return nil
		}
	}
	return fmt.Errorf("非法状态迁移: %s -> %s", from, to)
}

func NewEngineStageTransition(from EngineStage, to EngineStage, reason string) (EngineStageTransition, error) {
	if err := ValidateEngineStageTransition(from, to); err != nil {
		return EngineStageTransition{}, err
	}
	to = NormalizeEngineStage(to)
	if to == "" {
		to = NormalizeEngineStage(from)
	}
	from = NormalizeEngineStage(from)
	return EngineStageTransition{
		From:         from,
		To:           to,
		Reason:       strings.TrimSpace(reason),
		Transitioned: time.Now().UTC().Format(time.RFC3339),
	}, nil
}
