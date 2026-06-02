package main

import (
	"io"
	"log"
	"testing"
	"time"
)

func TestNewProtocolEvents(t *testing.T) {
	engine := &FundingRateSniperEngine{
		runID:          "run-1",
		currentCycleID: "cycle-1",
	}

	events := newProtocolEvents(engine, "open.execute", true,
		protocolEventSpec{
			EventType: EventTypeOpenExecuted,
			Level:     EventLevelInfo,
			Stage:     LifecycleStageOpening,
			Reason:    "open sequence executed",
		},
		protocolEventSpec{
			EventType: EventTypeOpenValidated,
			Level:     EventLevelInfo,
			Stage:     LifecycleStageValidatingOpen,
			Reason:    "fresh open hedges validated",
		},
	)

	if len(events) != 2 {
		t.Fatalf("newProtocolEvents() len = %d, want 2", len(events))
	}
	if events[0].RunID != "run-1" || events[0].CycleID != "cycle-1" {
		t.Fatalf("newProtocolEvents() trace ids = (%s,%s), want (run-1,cycle-1)", events[0].RunID, events[0].CycleID)
	}
	if events[0].Protocol != "open.execute" {
		t.Fatalf("newProtocolEvents() protocol = %s, want open.execute", events[0].Protocol)
	}
	if events[0].Stage != LifecycleStageOpening {
		t.Fatalf("newProtocolEvents() stage = %s, want %s", events[0].Stage, LifecycleStageOpening)
	}
	if !events[0].Successful {
		t.Fatalf("newProtocolEvents() successful = false, want true")
	}
	if events[1].Type != EventTypeOpenValidated {
		t.Fatalf("newProtocolEvents() second type = %s, want %s", events[1].Type, EventTypeOpenValidated)
	}
	if events[0].TraceRecordMetadata.StrategyVersion == "" {
		t.Fatalf("newProtocolEvents() metadata.strategy_version is empty")
	}
}

func TestEventLevelOrDefault(t *testing.T) {
	if got := eventLevelOrDefault(""); got != EventLevelInfo {
		t.Fatalf("eventLevelOrDefault(\"\") = %s, want %s", got, EventLevelInfo)
	}
	if got := eventLevelOrDefault(EventLevelCritical); got != EventLevelCritical {
		t.Fatalf("eventLevelOrDefault(critical) = %s, want %s", got, EventLevelCritical)
	}
}

type captureDomainEventSink struct {
	events []DomainEvent
}

func (s *captureDomainEventSink) AppendDomainEvents(events []DomainEvent) error {
	s.events = append(s.events, events...)
	return nil
}

func TestEmitDomainEventsUsesSink(t *testing.T) {
	sink := &captureDomainEventSink{}
	engine := &FundingRateSniperEngine{
		logger:          log.New(io.Discard, "", 0),
		domainEventSink: sink,
	}
	occurredAt := time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)
	engine.emitDomainEvents([]DomainEvent{
		{
			Type:       EventTypeCloseExecuted,
			Level:      EventLevelInfo,
			OccurredAt: occurredAt,
			Protocol:   "close.execute",
			Stage:      LifecycleStageClosing,
			Successful: true,
			Reason:     "close sequence executed",
		},
	})
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 emitted domain event, got %d", len(sink.events))
	}
	if sink.events[0].Type != EventTypeCloseExecuted || sink.events[0].Protocol != "close.execute" {
		t.Fatalf("unexpected emitted domain event: %+v", sink.events[0])
	}
}

func TestEmitRiskEventBridgesToDomainEventSink(t *testing.T) {
	sink := &captureDomainEventSink{}
	engine := &FundingRateSniperEngine{
		logger:                log.New(io.Discard, "", 0),
		domainEventSink:       sink,
		currentLifecycleStage: LifecycleStageHolding,
	}
	engine.emitRiskEvent(newRiskEvent("action", RiskCategoryPlatform, RiskSeverityWarning, "symbol_cooldown", "BTCUSDT", "set", 1, 3, false, "depth collapsed").WithEventType(EventTypeSymbolCooldownSet))
	if len(sink.events) != 1 {
		t.Fatalf("expected bridged risk domain event, got %d", len(sink.events))
	}
	if sink.events[0].Type != EventTypeSymbolCooldownSet || sink.events[0].Protocol != "risk.platform" {
		t.Fatalf("unexpected bridged risk domain event: %+v", sink.events[0])
	}
	if sink.events[0].Stage != LifecycleStageCoolingDown {
		t.Fatalf("unexpected bridged risk lifecycle stage: %+v", sink.events[0])
	}
}

func TestEmitDomainEventsInvokesHookWithoutSink(t *testing.T) {
	captured := make([]DomainEvent, 0, 1)
	engine := &FundingRateSniperEngine{
		logger:          log.New(io.Discard, "", 0),
		domainEventHook: func(event DomainEvent) { captured = append(captured, event) },
	}
	engine.emitDomainEvents([]DomainEvent{{
		Type:       EventTypeOpenPrepared,
		Level:      EventLevelInfo,
		OccurredAt: time.Date(2026, 6, 1, 12, 35, 0, 0, time.UTC),
		Protocol:   "open.prepare",
		Stage:      LifecycleStagePreOpenCheck,
		Successful: true,
		Reason:     "open sequence prepared",
	}})
	if len(captured) != 1 || captured[0].Type != EventTypeOpenPrepared {
		t.Fatalf("expected domain event hook to receive event, got %+v", captured)
	}
}
