package app

import "time"

// ProtocolResult 是开仓/平仓/恢复协议共享的最小返回契约。
type ProtocolResult struct {
	Protocol     string
	Summary      string
	Alerts       []string
	CurrentStage LifecycleStage
	Lifecycle    LifecycleSuggestion
	Risk         RiskDecision
	Events       []DomainEvent
	Successful   bool
}

type OpenProtocolPreparationResult struct {
	ProtocolResult
	Preparation openExecutionPreparation
}

type OpenProtocolExecutionResult struct {
	ProtocolResult
	Hedges         []OpenHedge
	BeforeSnapshot CycleAccountSnapshot
}

type CloseProtocolExecutionResult struct {
	ProtocolResult
	Settlement CycleSettlement
}

type RecoveryProtocolResult struct {
	ProtocolResult
	Action recoveryActionResult
}

type protocolEventSpec struct {
	EventType EventType
	Level     EventLevel
	Stage     LifecycleStage
	Reason    string
}

func newProtocolEvents(engine *FundingRateSniperEngine, protocol string, successful bool, specs ...protocolEventSpec) []DomainEvent {
	if engine == nil || len(specs) == 0 {
		return nil
	}
	events := make([]DomainEvent, 0, len(specs))
	occurredAt := time.Now().UTC()
	metadata := engine.recordMetadata().normalized()
	for _, spec := range specs {
		if spec.EventType == "" {
			continue
		}
		events = append(events, DomainEvent{
			TraceRecordMetadata: metadata,
			Type:                spec.EventType,
			Level:               eventLevelOrDefault(spec.Level),
			OccurredAt:          occurredAt,
			RunID:               nonEmpty(engine.runID),
			CycleID:             nonEmpty(engine.cycleIDSnapshot()),
			Protocol:            nonEmpty(protocol),
			Stage:               spec.Stage,
			Successful:          successful,
			Reason:              nonEmpty(spec.Reason),
		})
	}
	return events
}

func eventLevelOrDefault(level EventLevel) EventLevel {
	if level == "" {
		return EventLevelInfo
	}
	return level
}
