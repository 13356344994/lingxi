package main

import (
	"strings"
	"testing"
	"time"
)

func TestEvaluateMonitorNetProfit(t *testing.T) {
	t.Run("immediate invalidate", func(t *testing.T) {
		got := EvaluateMonitorNetProfit(0.0004, 0.0005, 0.0020, 3, 1)
		if got.State != "invalidated" || !got.Invalidate {
			t.Fatalf("expected invalidated decision, got %+v", got)
		}
	})

	t.Run("observe and count breaches", func(t *testing.T) {
		got := EvaluateMonitorNetProfit(0.0015, 0.0005, 0.0020, 3, 1)
		if got.State != "observing" || got.Invalidate || got.NextBreachCount != 2 {
			t.Fatalf("expected observing with breach count 2, got %+v", got)
		}
	})

	t.Run("healthy resets breach count", func(t *testing.T) {
		got := EvaluateMonitorNetProfit(0.0030, 0.0005, 0.0020, 3, 2)
		if got.State != "healthy" || got.Invalidate || got.NextBreachCount != 0 {
			t.Fatalf("expected healthy reset, got %+v", got)
		}
	})
}

func TestEvaluateExecutionCoverageRisk(t *testing.T) {
	cases := []struct {
		name     string
		coverage float64
		action   string
		severity string
	}{
		{name: "healthy", coverage: 0.99, action: "healthy", severity: "healthy"},
		{name: "observe", coverage: 0.95, action: "observe", severity: "minor"},
		{name: "symbol cooldown", coverage: 0.75, action: "symbol_cooldown", severity: "major"},
		{name: "global pause", coverage: 0.50, action: "global_pause", severity: "critical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateExecutionCoverageRisk("depth", tc.coverage)
			if got.Action != tc.action || got.Severity != tc.severity {
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
