package app

import (
	"io"
	"log"
	"testing"

	"lingxi/internal/lifecycle"
)

// TestGuardHedgeOrderIntent 验证下单前置门按对冲生命周期 SSOT 状态放行/拦截下单意图：
// 持仓中可平仓、干净终态禁止重复平仓、持仓中禁止再开腿。
func TestGuardHedgeOrderIntent(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}

	active := &OpenHedge{}
	active.markPendingValidation("opened")
	active.markActive("validated")
	if err := engine.guardHedgeOrderIntent(active, lifecycle.IntentCloseLeg); err != nil {
		t.Fatalf("active hedge should permit close: %v", err)
	}
	if err := engine.guardHedgeOrderIntent(active, lifecycle.IntentOpenLeg); err == nil {
		t.Fatal("active hedge must reject a new open leg")
	}

	closed := &OpenHedge{}
	closed.markPendingValidation("opened")
	closed.markActive("validated")
	closed.markClosing("closing")
	closed.completeCloseLifecycle(false, "done")
	if closed.LifecycleState != OpenHedgeLifecycleClosed {
		t.Fatalf("expected clean closed state, got %q", closed.LifecycleState)
	}
	if err := engine.guardHedgeOrderIntent(closed, lifecycle.IntentCloseLeg); err == nil {
		t.Fatal("clean-closed hedge must reject a repeat close (double-close protection)")
	}

	// nil 对冲视为空闲态，允许开仓。
	if err := engine.guardHedgeOrderIntent(nil, lifecycle.IntentOpenLeg); err != nil {
		t.Fatalf("nil hedge (idle) should permit open: %v", err)
	}
}

// TestIllegalTransitionFlagged 验证非法迁移被 SSOT 状态机标记（仍应用、但不静默）。
func TestIllegalTransitionFlagged(t *testing.T) {
	h := &OpenHedge{}
	h.markPendingValidation("opened")
	h.markActive("validated")
	h.markClosing("closing")
	h.completeCloseLifecycle(false, "done") // -> closed (terminal)
	h.markActive("illegal revive")          // closed -> active 非法
	if !h.LifecycleIllegalTransition {
		t.Fatal("expected illegal transition (closed -> active) to be flagged")
	}
}
