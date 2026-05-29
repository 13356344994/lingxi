package main

import (
	"strings"
	"time"
)

const (
	RiskCategoryExecution      = "execution"
	RiskCategoryRecovery       = "recovery"
	RiskCategoryPlatform       = "platform"
	RiskCategoryOrderUncertain = "order_uncertain"
	RiskCategorySafety         = "safety"
	RiskSeverityInfo           = "info"
	RiskSeverityWarning        = "warning"
	RiskSeverityCritical       = "critical"
)

type RiskEvent struct {
	TraceRecordMetadata
	RunID         string
	CycleID       string
	RecordedAt    string
	Level         string
	Category      string
	Severity      string
	Phase         string
	Symbol        string
	Action        string
	IncidentCount int
	IncidentLimit int
	Fatal         bool
	Detail        string
}

func newRiskEvent(level string, category string, severity string, phase string, symbol string, action string, incidentCount int, incidentLimit int, fatal bool, detail string) RiskEvent {
	return RiskEvent{
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		Level:         nonEmpty(level, "action"),
		Category:      nonEmpty(category, RiskCategoryExecution),
		Severity:      nonEmpty(severity, RiskSeverityInfo),
		Phase:         nonEmpty(phase, "unknown"),
		Symbol:        nonEmpty(symbol, "-"),
		Action:        nonEmpty(action, "action"),
		IncidentCount: incidentCount,
		IncidentLimit: incidentLimit,
		Fatal:         fatal,
		Detail:        detail,
	}
}

func (e RiskEvent) ExecutionRecord() ExecutionIncidentRecord {
	return ExecutionIncidentRecord{
		TraceRecordMetadata: e.TraceRecordMetadata.normalized(),
		RunID:               e.RunID,
		CycleID:             e.CycleID,
		RecordedAt:          e.RecordedAt,
		Level:               e.Level,
		Category:            e.Category,
		Severity:            e.Severity,
		Phase:               e.Phase,
		Symbol:              e.Symbol,
		Action:              e.Action,
		IncidentCount:       e.IncidentCount,
		IncidentLimit:       e.IncidentLimit,
		Fatal:               e.Fatal,
		Detail:              e.Detail,
	}
}

func (e RiskEvent) WithTrace(runID string, cycleID string) RiskEvent {
	e.RunID = strings.TrimSpace(runID)
	e.CycleID = strings.TrimSpace(cycleID)
	return e
}

func (e RiskEvent) WithMetadata(metadata TraceRecordMetadata) RiskEvent {
	e.TraceRecordMetadata = metadata.normalized()
	return e
}
