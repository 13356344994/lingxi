package app

import (
	"path/filepath"
	"strings"
	"time"
)

const crossVenueTransitionsFileName = "transitions.jsonl"

type crossVenueTransitionRecord struct {
	DecisionCycleID string    `json:"decision_cycle_id,omitempty"`
	From            string    `json:"from,omitempty"`
	To              string    `json:"to,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	SnapshotID      string    `json:"snapshot_id,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	PositionKey     string    `json:"position_key,omitempty"`
	Symbol          string    `json:"symbol,omitempty"`
}

func (e *CrossVenueExecutor) recordCrossVenueTransition(pos CrossVenuePosition, from, to, reason string) {
	if e == nil || e.recordDir == "" {
		return
	}
	rec := crossVenueTransitionRecord{
		DecisionCycleID: strings.TrimSpace(pos.DecisionCycleID),
		From:            strings.TrimSpace(from),
		To:              strings.TrimSpace(to),
		Reason:          strings.TrimSpace(reason),
		SnapshotID:      strings.TrimSpace(pos.CurrentSnapshotID),
		Timestamp:       e.nowFn(),
		PositionKey:     crossVenueTransitionPositionKey(pos),
		Symbol:          pos.Symbol,
	}
	path := filepath.Join(crossVenueRecordDir(e.recordDir), crossVenueTransitionsFileName)
	if err := appendJSONLRecord(path, rec); err != nil && e.logger != nil {
		e.logger.Printf("[跨所执行] transition 落盘失败 symbol=%s from=%s to=%s: %v", pos.Symbol, from, to, err)
	}
}

func crossVenueTransitionPositionKey(pos CrossVenuePosition) string {
	return crossVenuePositionKey(pos)
}
