package app

import (
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync"
	"testing"
)

// TestFinalizeGracefulShutdownIdempotentAndEmits 钉死:优雅停机收尾只执行一次,并发清晰停机事件 + 落盘状态。
func TestFinalizeGracefulShutdownIdempotent(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var events []DomainEvent

	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.dailyRiskStatePath = filepath.Join(dir, "daily_risk.json")
	engine.openHedgeStatePath = filepath.Join(dir, "open_hedges.json")
	engine.domainEventHook = func(ev DomainEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	engine.finalizeGracefulShutdown(errors.New("SIGTERM"))
	engine.finalizeGracefulShutdown(errors.New("二次调用应被忽略"))

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("优雅停机事件应恰好一次, got %d", len(events))
	}
	if events[0].Type != EventTypeGracefulShutdown {
		t.Fatalf("事件类型应为 graceful_shutdown, got %s", events[0].Type)
	}
}
