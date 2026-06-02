package main

import (
	"strings"
	"testing"
	"time"
)


func TestEvaluateExecutionCoverageRisk(t *testing.T) {
	cases := []struct {
		name     string
		coverage float64
		action   string
		severity string
	}{
		{name: "healthy", coverage: 0.99, action: "observe", severity: "healthy"},
		{name: "observe", coverage: 0.95, action: "observe", severity: "minor"},
		{name: "symbol_cooldown", coverage: 0.75, action: "tighten", severity: "major"},
		{name: "global_pause", coverage: 0.50, action: "pause_cycle", severity: "critical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateExecutionCoverageRisk("depth", tc.coverage)
			if string(got.Action) != string(tc.action) || got.Severity != tc.severity {
				t.Fatalf("unexpected decision: %+v", got)
			}
		})
	}
}

func TestParseRuntimeFlexibleDuration(t *testing.T) {
	got, err := parseRuntimeFlexibleDuration("1500ms", 0, "确认超时", 100*time.Millisecond, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1500*time.Millisecond {
		t.Fatalf("unexpected duration: %s", got)
	}

	got, err = parseRuntimeFlexibleDuration("", 5*time.Second, "确认超时", time.Second, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}
	if got != 5*time.Second {
		t.Fatalf("expected fallback duration, got %s", got)
	}

	_, err = parseRuntimeFlexibleDuration("50ms", 0, "确认超时", 100*time.Millisecond, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "确认超时必须在") {
		t.Fatalf("expected range error, got %v", err)
	}
}

func TestRiskEventExecutionRecord(t *testing.T) {
	event := newRiskEvent("incident", RiskCategoryPlatform, RiskSeverityCritical, string(StageCooldown), "BTCUSDT", "trip", 2, 3, true, "platform rejected")
	record := event.ExecutionRecord()
	if record.Category != RiskCategoryPlatform || record.Severity != RiskSeverityCritical {
		t.Fatalf("unexpected record classification: %+v", record)
	}
	if record.Phase != string(StageCooldown) || !record.Fatal {
		t.Fatalf("unexpected record payload: %+v", record)
	}
}

func TestRiskEventDomainEvent(t *testing.T) {
	event := newRiskEvent("action", RiskCategoryPlatform, RiskSeverityCritical, "platform_fuse", "BTCUSDT", "trip", 2, 3, true, "platform rejected").
		WithTrace("run-1", "cycle-1").
		WithMetadata(TraceRecordMetadata{StrategyVersion: "funding-sniper-v1"}).
		WithEventType(EventTypeFuseTripped)
	event.RecordedAt = "2026-06-01T12:00:00Z"

	domainEvent, ok := event.DomainEvent(LifecycleStageMonitoring)
	if !ok {
		t.Fatal("expected risk event to convert to domain event")
	}
	if domainEvent.Type != EventTypeFuseTripped || domainEvent.Protocol != "risk.platform" {
		t.Fatalf("unexpected domain event identity: %+v", domainEvent)
	}
	if domainEvent.Stage != LifecycleStageFused {
		t.Fatalf("unexpected mapped lifecycle stage: %+v", domainEvent)
	}
	if domainEvent.Successful {
		t.Fatalf("expected fatal risk event to map to unsuccessful domain event: %+v", domainEvent)
	}
	if domainEvent.RunID != "run-1" || domainEvent.CycleID != "cycle-1" {
		t.Fatalf("unexpected trace ids: %+v", domainEvent)
	}
}
