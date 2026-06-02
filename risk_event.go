package main

import (
	"fmt"
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
	EventType     string
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
		EventType:     "",
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

func (e RiskEvent) DomainEvent(currentStage LifecycleStage) (DomainEvent, bool) {
	eventType := EventType(strings.TrimSpace(e.EventType))
	if eventType == "" {
		return DomainEvent{}, false
	}
	occurredAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(e.RecordedAt)); err == nil {
		occurredAt = parsed.UTC()
	}
	stage := riskEventLifecycleStage(eventType, currentStage)
	reason := strings.TrimSpace(e.Detail)
	if reason == "" {
		reason = strings.TrimSpace(fmt.Sprintf("category=%s action=%s", nonEmpty(e.Category, "-"), nonEmpty(e.Action, "-")))
	}
	return DomainEvent{
		TraceRecordMetadata: e.TraceRecordMetadata.normalized(),
		Type:                eventType,
		Level:               riskEventLevel(e.Severity),
		OccurredAt:          occurredAt,
		RunID:               strings.TrimSpace(e.RunID),
		CycleID:             strings.TrimSpace(e.CycleID),
		Protocol:            strings.TrimSpace("risk." + nonEmpty(e.Category, "event")),
		Stage:               stage,
		Successful:          !e.Fatal,
		Reason:              reason,
	}, true
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

func (e RiskEvent) WithEventType(eventType EventType) RiskEvent {
	e.EventType = strings.TrimSpace(string(eventType))
	return e
}

func riskEventLevel(severity string) EventLevel {
	switch strings.TrimSpace(severity) {
	case RiskSeverityCritical:
		return EventLevelCritical
	case RiskSeverityWarning:
		return EventLevelWarning
	default:
		return EventLevelInfo
	}
}

func riskEventLifecycleStage(eventType EventType, fallback LifecycleStage) LifecycleStage {
	switch eventType {
	case EventTypeFuseTripped:
		return LifecycleStageFused
	case EventTypeSymbolCooldownSet:
		return LifecycleStageCoolingDown
	case EventTypeRecoveryEscalated:
		return LifecycleStageRecovering
	case EventTypeOrderUncertain:
		if NormalizeLifecycleStage(fallback) != "" {
			return NormalizeLifecycleStage(fallback)
		}
		return LifecycleStageRecovering
	default:
		return NormalizeLifecycleStage(fallback)
	}
}
