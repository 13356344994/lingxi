package app

import "time"

// EventType 用于统一最小结构化事件的类型。
type EventType string

const (
	EventTypeFuseTripped         EventType = "fuse_tripped"
	EventTypeSymbolCooldownSet   EventType = "symbol_cooldown_set"
	EventTypeOrderUncertain      EventType = "order_uncertain"
	EventTypeRecoveryEscalated   EventType = "recovery_escalated"
	EventTypeRecoveryStarted     EventType = "recovery_started"
	EventTypeOpenPrepared        EventType = "open_prepared"
	EventTypeOpenExecuted        EventType = "open_executed"
	EventTypeOpenValidated       EventType = "open_validated"
	EventTypeCloseExecuted       EventType = "close_executed"
	EventTypeStageTransition     EventType = "stage_transition"
	EventTypeEngineGearChanged   EventType = "engine_gear_changed"
	EventTypeRiskDowngrade       EventType = "risk_downgrade"
	EventTypePendingFundingStuck EventType = "pending_funding_stuck"
	EventTypeGracefulShutdown    EventType = "graceful_shutdown"
	EventTypeEngineStarted       EventType = "engine_started"
	EventTypeEngineCrashed       EventType = "engine_crashed"
)

// EventLevel 描述结构化事件的观测级别。
type EventLevel string

const (
	EventLevelInfo     EventLevel = "info"
	EventLevelWarning  EventLevel = "warning"
	EventLevelCritical EventLevel = "critical"
)

// DomainEvent 是未来事件流的最小公共结构。
type DomainEvent struct {
	TraceRecordMetadata
	Type       EventType
	Level      EventLevel
	OccurredAt time.Time
	RunID      string
	CycleID    string
	Protocol   string
	Stage      LifecycleStage
	Successful bool
	Reason     string
}

// DomainEventSink 描述结构化事件的最小落点接口。
type DomainEventSink interface {
	AppendDomainEvents(events []DomainEvent) error
}
