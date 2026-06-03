package app

import (
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"
)

// TestTraceIDsNoReentrantPlatformRiskLock 锁定回归契约：traceIDs() 必须可以在「调用方已持有
// platformRiskMu」时被安全调用。历史 bug 中 traceIDs() 自己又去 Lock platformRiskMu，导致引擎在
// 首个 ready->scan 状态迁移写日志时同协程自死锁（Go 互斥锁不可重入），整轮卡死、面板仍在线但引擎哑火。
// 若回归，本用例会在持锁后调用 traceIDs() 处永久阻塞，由 go test 超时捕获。
func TestTraceIDsNoReentrantPlatformRiskLock(t *testing.T) {
	e := &FundingRateSniperEngine{}
	e.setTraceLogIDs("run-1", "cycle-1")

	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()

	runID, cycleID := e.traceIDs()
	if runID != "run-1" || cycleID != "cycle-1" {
		t.Fatalf("traceIDs 返回异常: runID=%q cycleID=%q", runID, cycleID)
	}
}

// TestRuntimeLogWriterEmitWhileRiskLocked 复现真实链路：实盘引擎的日志写入器（runtimeLogWriter）
// 以 engine.traceIDs 作为 traceIDProvider。当引擎在持有 platformRiskMu 时写日志（状态迁移等），
// 写入器会回调 traceIDs。本用例在持锁状态下走一遍完整日志写入，确保不再自死锁。
func TestRuntimeLogWriterEmitWhileRiskLocked(t *testing.T) {
	store, err := NewRuntimeLogStore(filepath.Join(t.TempDir(), runtimeEventLogJSONLName))
	if err != nil {
		t.Fatalf("创建 RuntimeLogStore 失败: %v", err)
	}

	e := &FundingRateSniperEngine{}
	e.setTraceLogIDs("run-2", "")
	logger := log.New(newRuntimeLogWriter(store, "test", io.Discard, e.traceIDs, e.recordMetadata), "", 0)

	done := make(chan struct{})
	go func() {
		e.platformRiskMu.Lock()
		defer e.platformRiskMu.Unlock()
		logger.Printf("emit while holding platformRiskMu")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("持有 platformRiskMu 时写日志超时——traceIDs 可能再次回锁 platformRiskMu 造成自死锁")
	}
}
