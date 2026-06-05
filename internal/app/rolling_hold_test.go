package app

import "testing"

// TestEvaluateRollDecision 钉死续持判定(§16.4)的纯函数语义:门槛闸 + 安全垫闸,fail-safe 偏向平仓。
func TestEvaluateRollDecision(t *testing.T) {
	cases := []struct {
		name     string
		in       rollDecisionInput
		wantKeep bool
	}{
		{
			name: "正向carry明显盈利_续持",
			// 下期预期净资金费 0.004，边际持有成本≈0(正向 carry)，门槛/安全垫 0.0005。
			in:       rollDecisionInput{Symbol: "BTCUSDT", ExpectedNextNetFundingRate: 0.004, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0005},
			wantKeep: true,
		},
		{
			name: "贴近零边际_安全垫拦截_平仓",
			// 边际净 carry = 0.0006，门槛 0.0005 过，但未明显超过安全垫 0.0008 → 平仓。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.0006, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0008},
			wantKeep: false,
		},
		{
			name:     "低于门槛_平仓",
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.0003, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
		{
			name: "负carry借币利息吃掉资金费_平仓",
			// 下期预期净资金费 0.002，但多持一周期借币利息 0.0025 > 资金费 → 边际净 carry 为负 → 平仓。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.002, MarginalHoldCostRate: 0.0025, HoldRollThresholdRate: 0, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
		{
			name:     "负carry借币利息后仍明显盈利_续持",
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.005, MarginalHoldCostRate: 0.0025, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0005},
			wantKeep: true,
		},
		{
			name: "R7保守估计退回0_举证失败_平仓",
			// 符号冲突时保守估计为 0：净 carry = -成本 ≤ 0，不得续持(续持举证责任在证明下期仍盈利)。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0, MarginalHoldCostRate: 0.0001, HoldRollThresholdRate: 0, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateRollDecision(tc.in)
			if got.Keep != tc.wantKeep {
				t.Fatalf("Keep=%v, want %v (netHoldCarry=%.6f, reason=%s)", got.Keep, tc.wantKeep, got.NetHoldCarryRate, got.Reason)
			}
			if got.Reason == "" {
				t.Fatal("Reason 不应为空(可观测性)")
			}
		})
	}
}

// TestEvaluateRollDecisionClampsNegativeHoldCost 钉死边际成本为负被防御性夹为 0(成本不应为负，避免被错误数据放大续持)。
func TestEvaluateRollDecisionClampsNegativeHoldCost(t *testing.T) {
	got := evaluateRollDecision(rollDecisionInput{ExpectedNextNetFundingRate: 0.0003, MarginalHoldCostRate: -1.0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0})
	if got.Keep {
		t.Fatalf("负边际成本应夹为 0、不得把 0.0003 抬过门槛 0.0005: keep=%v netHoldCarry=%.6f", got.Keep, got.NetHoldCarryRate)
	}
	if got.NetHoldCarryRate != 0.0003 {
		t.Fatalf("夹 0 后净 carry 应等于 ExpectedNextNetFundingRate=0.0003, got %.6f", got.NetHoldCarryRate)
	}
}
