package main

import (
	"testing"
	"time"
)

func TestBuildUnifiedDashboardEventsDedupesLegacyBridgeAgainstDomain(t *testing.T) {
	domainEvents := []DomainEvent{{
		Type:       EventTypeRecoveryEscalated,
		Level:      EventLevelWarning,
		OccurredAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		RunID:      "run-1",
		CycleID:    "cycle-1",
		Protocol:   "recovery.exposure_rescue",
		Stage:      LifecycleStageRecovering,
		Successful: false,
		Reason:     "timeout recovered",
	}}
	executionIncidents := []ExecutionIncidentRecord{{
		RunID:         "run-1",
		CycleID:       "cycle-1",
		RecordedAt:    "2026-06-01T12:00:00Z",
		Category:      RiskCategoryRecovery,
		Severity:      RiskSeverityWarning,
		Phase:         "recovery_close",
		Action:        "recover",
		IncidentCount: 1,
		IncidentLimit: 3,
		Detail:        "timeout recovered",
	}}

	got := buildUnifiedDashboardEvents(domainEvents, executionIncidents, 10)
	if len(got) != 1 {
		t.Fatalf("expected deduped unified event list, got %+v", got)
	}
	if got[0].Source != "domain" || got[0].Type != string(EventTypeRecoveryEscalated) {
		t.Fatalf("unexpected unified event: %+v", got[0])
	}
}

func TestBuildUnifiedDashboardEventsKeepsLegacyExecutionWhenNoDomainMappingExists(t *testing.T) {
	got := buildUnifiedDashboardEvents(nil, []ExecutionIncidentRecord{{
		RunID:         "run-2",
		CycleID:       "cycle-2",
		RecordedAt:    "2026-06-01T12:01:00Z",
		Category:      RiskCategoryExecution,
		Severity:      RiskSeverityWarning,
		Phase:         "partial_fill_watch",
		Action:        "observe",
		IncidentCount: 2,
		IncidentLimit: 3,
		Detail:        "maker partial fill still pending",
	}}, 10)
	if len(got) != 1 {
		t.Fatalf("expected one legacy unified event, got %+v", got)
	}
	if got[0].Source != "legacy_execution" || got[0].Type != "legacy_execution" {
		t.Fatalf("unexpected legacy unified event: %+v", got[0])
	}
}
