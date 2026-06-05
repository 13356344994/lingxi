package app

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync"
	"testing"
)

func newShutdownTestEngine(t *testing.T, hook func(DomainEvent)) *FundingRateSniperEngine {
	t.Helper()
	dir := t.TempDir()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.dailyRiskStatePath = filepath.Join(dir, "daily_risk.json")
	engine.openHedgeStatePath = filepath.Join(dir, "open_hedges.json")
	engine.domainEventHook = hook
	return engine
}

// TestFinalizeGracefulShutdownIdempotent 钉死:计划停机(ctx 取消)收尾只执行一次,并发 graceful_shutdown 事件。
func TestFinalizeGracefulShutdownIdempotent(t *testing.T) {
	var mu sync.Mutex
	var events []DomainEvent
	engine := newShutdownTestEngine(t, func(ev DomainEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	engine.finalizeGracefulShutdown(context.Canceled)
	engine.finalizeGracefulShutdown(errors.New("二次调用应被忽略"))

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("停机事件应恰好一次, got %d", len(events))
	}
	if events[0].Type != EventTypeGracefulShutdown || events[0].Level != EventLevelWarning {
		t.Fatalf("计划停机事件类型/级别应为 graceful_shutdown/warning, got %s/%s", events[0].Type, events[0].Level)
	}
}

// TestFinalizeGracefulShutdownCrashEmitsCritical 钉死:致命错误退出 → engine_crashed/critical 事件(便于区分崩溃)。
func TestFinalizeGracefulShutdownCrashEmitsCritical(t *testing.T) {
	var mu sync.Mutex
	var events []DomainEvent
	engine := newShutdownTestEngine(t, func(ev DomainEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	engine.finalizeGracefulShutdown(fatalEngineError{message: "单周期巨亏熔断"})

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("崩溃事件应恰好一次, got %d", len(events))
	}
	if events[0].Type != EventTypeEngineCrashed || events[0].Level != EventLevelCritical {
		t.Fatalf("异常退出事件类型/级别应为 engine_crashed/critical, got %s/%s", events[0].Type, events[0].Level)
	}
}
