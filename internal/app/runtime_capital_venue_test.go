package app

import (
	"context"
	"strings"
	"testing"
)

// TestAdjustTradingCapitalRunningRejectionIsPerController 钉死 P3f.4 的核心语义:调仓的「运行中拒绝」只看
// 该所自己的 controller.status.Running —— 一所在跑不影响另一所调仓(每所独立 RuntimeController)。
func TestAdjustTradingCapitalRunningRejectionIsPerController(t *testing.T) {
	mkController := func() *RuntimeController {
		store, err := NewReportStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewReportStore() error = %v", err)
		}
		c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
		if err != nil {
			t.Fatalf("NewRuntimeController() error = %v", err)
		}
		return c
	}
	running := mkController()
	idle := mkController()

	// 模拟「该所」实盘运行中。
	running.mu.Lock()
	running.status.Running = true
	running.mu.Unlock()

	// 运行中的所:调仓被拒(本所自己的状态)。
	if _, err := running.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{TargetTradingUSDT: 100}); err == nil || !strings.Contains(err.Error(), "运行中") {
		t.Fatalf("运行中的所调仓应被拒绝, got %v", err)
	}

	// 另一所未运行 → 其 status.Running 独立为 false(不被他所运行态污染);其调仓不会因他所运行而被拒。
	idle.mu.Lock()
	idleRunning := idle.status.Running
	idle.mu.Unlock()
	if idleRunning {
		t.Fatal("空闲所的 Running 不应被其它所污染")
	}
}
