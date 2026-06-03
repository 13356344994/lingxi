package app

import (
	"sort"
	"strings"
	"time"
)

// UnifiedDashboardEvent 是 dashboard 使用的统一事件主视图。
type UnifiedDashboardEvent struct {
	Source         string `json:"source"`
	OccurredAt     string `json:"occurred_at"`
	Type           string `json:"type"`
	Level          string `json:"level"`
	Protocol       string `json:"protocol"`
	Stage          string `json:"stage"`
	Successful     bool   `json:"successful"`
	Reason         string `json:"reason"`
	RunID          string `json:"run_id"`
	CycleID        string `json:"cycle_id"`
	LegacyCategory string `json:"legacy_category"`
	LegacySeverity string `json:"legacy_severity"`
	LegacyAction   string `json:"legacy_action"`
	LegacyPhase    string `json:"legacy_phase"`
	Symbol         string `json:"symbol"`
	IncidentCount  int    `json:"incident_count"`
	IncidentLimit  int    `json:"incident_limit"`
	Fatal          bool   `json:"fatal"`
}

func buildUnifiedDashboardEvents(domainEvents []DomainEvent, executionIncidents []ExecutionIncidentRecord, limit int) []UnifiedDashboardEvent {
	events := make([]UnifiedDashboardEvent, 0, len(domainEvents)+len(executionIncidents))
	seen := make(map[string]struct{}, len(domainEvents))
	for _, event := range domainEvents {
		view := newUnifiedDashboardEventFromDomainEvent(event)
		events = append(events, view)
		seen[unifiedDashboardEventSemanticKey(view)] = struct{}{}
	}
	for _, record := range executionIncidents {
		view := newUnifiedDashboardEventFromExecutionIncident(record)
		if view.Source == "legacy_bridge" {
			if _, duplicated := seen[unifiedDashboardEventSemanticKey(view)]; duplicated {
				continue
			}
		}
		events = append(events, view)
	}
	sort.SliceStable(events, func(i, j int) bool {
		left := parseUnifiedDashboardEventTime(events[i].OccurredAt)
		right := parseUnifiedDashboardEventTime(events[j].OccurredAt)
		switch {
		case left.Equal(right):
			if events[i].Source == events[j].Source {
				return events[i].Type < events[j].Type
			}
			return events[i].Source < events[j].Source
		case left.IsZero():
			return false
		case right.IsZero():
			return true
		default:
			return left.After(right)
		}
	})
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events
}

func newUnifiedDashboardEventFromDomainEvent(event DomainEvent) UnifiedDashboardEvent {
	return UnifiedDashboardEvent{
		Source:     "domain",
		OccurredAt: formatUnifiedDashboardEventTime(event.OccurredAt),
		Type:       strings.TrimSpace(string(event.Type)),
		Level:      strings.TrimSpace(string(event.Level)),
		Protocol:   strings.TrimSpace(event.Protocol),
		Stage:      strings.TrimSpace(string(event.Stage)),
		Successful: event.Successful,
		Reason:     strings.TrimSpace(event.Reason),
		RunID:      strings.TrimSpace(event.RunID),
		CycleID:    strings.TrimSpace(event.CycleID),
	}
}

func newUnifiedDashboardEventFromExecutionIncident(record ExecutionIncidentRecord) UnifiedDashboardEvent {
	if event, ok := record.DomainEvent(); ok {
		view := newUnifiedDashboardEventFromDomainEvent(event)
		view.Source = "legacy_bridge"
		view.LegacyCategory = strings.TrimSpace(record.Category)
		view.LegacySeverity = strings.TrimSpace(record.Severity)
		view.LegacyAction = strings.TrimSpace(record.Action)
		view.LegacyPhase = strings.TrimSpace(record.Phase)
		view.Symbol = strings.TrimSpace(record.Symbol)
		view.IncidentCount = record.IncidentCount
		view.IncidentLimit = record.IncidentLimit
		view.Fatal = record.Fatal
		return view
	}
	return UnifiedDashboardEvent{
		Source:         "legacy_execution",
		OccurredAt:     strings.TrimSpace(record.RecordedAt),
		Type:           "legacy_execution",
		Level:          nonEmpty(strings.TrimSpace(record.Severity), strings.TrimSpace(record.Level)),
		Protocol:       strings.TrimSpace("legacy." + nonEmpty(record.Category, "execution")),
		Stage:          "",
		Successful:     !record.Fatal,
		Reason:         strings.TrimSpace(record.Detail),
		RunID:          strings.TrimSpace(record.RunID),
		CycleID:        strings.TrimSpace(record.CycleID),
		LegacyCategory: strings.TrimSpace(record.Category),
		LegacySeverity: strings.TrimSpace(record.Severity),
		LegacyAction:   strings.TrimSpace(record.Action),
		LegacyPhase:    strings.TrimSpace(record.Phase),
		Symbol:         strings.TrimSpace(record.Symbol),
		IncidentCount:  record.IncidentCount,
		IncidentLimit:  record.IncidentLimit,
		Fatal:          record.Fatal,
	}
}

func unifiedDashboardEventSemanticKey(event UnifiedDashboardEvent) string {
	return strings.Join([]string{
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(event.CycleID),
		strings.TrimSpace(event.Type),
		strings.TrimSpace(event.Stage),
		strings.TrimSpace(event.Reason),
		normalizeUnifiedDashboardEventTime(event.OccurredAt),
	}, "|")
}

func parseUnifiedDashboardEventTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err == nil {
		return parsed.UTC()
	}
	parsed, err = time.Parse(time.RFC3339Nano, raw)
	if err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func formatUnifiedDashboardEventTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func normalizeUnifiedDashboardEventTime(raw string) string {
	parsed := parseUnifiedDashboardEventTime(raw)
	if parsed.IsZero() {
		return strings.TrimSpace(raw)
	}
	return parsed.Format(time.RFC3339)
}
