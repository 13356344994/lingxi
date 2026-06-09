package app

import "testing"

func TestNewLifecycleTransitionFromProtocol(t *testing.T) {
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	transition, err := NewLifecycleTransitionFromProtocol(result)
	if err != nil {
		t.Fatalf("NewLifecycleTransitionFromProtocol() error = %v", err)
	}
	if transition.From != LifecycleStageMonitoring || transition.To != LifecycleStagePreOpenCheck {
		t.Fatalf("transition = %s -> %s, want %s -> %s", transition.From, transition.To, LifecycleStageMonitoring, LifecycleStagePreOpenCheck)
	}
}

func TestNewLifecycleTransitionFromProtocolRejectsInvalidTransition(t *testing.T) {
	result := ProtocolResult{
		Protocol:     "close.execute",
		Summary:      "close sequence executed",
		CurrentStage: LifecycleStageClosing,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "invalid jump",
		},
	}

	if _, err := NewLifecycleTransitionFromProtocol(result); err == nil {
		t.Fatalf("NewLifecycleTransitionFromProtocol() expected error, got nil")
	}
}

func TestLifecycleStateMachineApplyProtocolResult(t *testing.T) {
	machine := NewLifecycleStateMachine(LifecycleStageMonitoring)
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	transition, err := machine.ApplyProtocolResult(result)
	if err != nil {
		t.Fatalf("ApplyProtocolResult() error = %v", err)
	}
	if transition.To != LifecycleStagePreOpenCheck {
		t.Fatalf("ApplyProtocolResult() to = %s, want %s", transition.To, LifecycleStagePreOpenCheck)
	}
	if machine.CurrentStage() != LifecycleStagePreOpenCheck {
		t.Fatalf("CurrentStage() = %s, want %s", machine.CurrentStage(), LifecycleStagePreOpenCheck)
	}
	if len(machine.Transitions()) != 1 {
		t.Fatalf("Transitions() len = %d, want 1", len(machine.Transitions()))
	}
}

// TestApplyLifecycleProtocolResultBridgesIntermediateStage 钉死 FIX_PLAN #7:机器停在 PreOpenCheck(prepare 后),
// execute 协议声明 from=Opening(跳过 PreOpenCheck→Opening 一跳)→ 应自动桥接合法单跳后到达 Holding,而非丢弃迁移。
func TestApplyLifecycleProtocolResultBridgesIntermediateStage(t *testing.T) {
	e := &FundingRateSniperEngine{}
	e.resetLifecycleTracking(LifecycleStagePreOpenCheck, "prepared")
	result := ProtocolResult{
		Protocol:     "open.execute",
		CurrentStage: LifecycleStageOpening,
		Lifecycle:    LifecycleSuggestion{NextStage: LifecycleStageHolding, Reason: "executed"},
	}
	if err := e.applyLifecycleProtocolResult(result); err != nil {
		t.Fatalf("应桥接 PreOpenCheck→Opening 后应用 Opening→Holding: %v", err)
	}
	if got := e.lifecycleStateMachine.CurrentStage(); got != LifecycleStageHolding {
		t.Fatalf("机器应到达 Holding, got %s", got)
	}

	// 平仓同理:机器在 CloseMonitoring,close 协议声明 from=Closing → 桥接 CloseMonitoring→Closing 后到 Settling。
	e2 := &FundingRateSniperEngine{}
	e2.resetLifecycleTracking(LifecycleStageCloseMonitoring, "close monitoring")
	closeResult := ProtocolResult{
		Protocol:     "close.execute",
		CurrentStage: LifecycleStageClosing,
		Lifecycle:    LifecycleSuggestion{NextStage: LifecycleStageSettling, Reason: "closed"},
	}
	if err := e2.applyLifecycleProtocolResult(closeResult); err != nil {
		t.Fatalf("应桥接 CloseMonitoring→Closing 后应用 Closing→Settling: %v", err)
	}
	if got := e2.lifecycleStateMachine.CurrentStage(); got != LifecycleStageSettling {
		t.Fatalf("机器应到达 Settling, got %s", got)
	}
}

// TestApplyLifecycleProtocolResultRejectsNonAdjacentMismatch 钉死:非「合法单跳」的阶段不一致维持原行为(不桥接、返回错误),零回归。
func TestApplyLifecycleProtocolResultRejectsNonAdjacentMismatch(t *testing.T) {
	e := &FundingRateSniperEngine{}
	e.resetLifecycleTracking(LifecycleStageHolding, "holding")
	result := ProtocolResult{
		Protocol:     "open.prepare",
		CurrentStage: LifecycleStageMonitoring, // Holding→Monitoring 非合法单跳
		Lifecycle:    LifecycleSuggestion{NextStage: LifecycleStagePreOpenCheck, Reason: "x"},
	}
	if err := e.applyLifecycleProtocolResult(result); err == nil {
		t.Fatal("非合法单跳应维持原行为返回错误(不桥接)")
	}
	if got := e.lifecycleStateMachine.CurrentStage(); got != LifecycleStageHolding {
		t.Fatalf("拒绝后机器阶段不应改变, got %s", got)
	}
}

func TestLifecycleStateMachineRejectsStageMismatch(t *testing.T) {
	machine := NewLifecycleStateMachine(LifecycleStageHolding)
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	if _, err := machine.ApplyProtocolResult(result); err == nil {
		t.Fatalf("ApplyProtocolResult() expected stage mismatch error, got nil")
	}
}
