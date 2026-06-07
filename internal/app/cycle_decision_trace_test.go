package app

import (
	"strings"
	"sync/atomic"
	"testing"
)

func TestSummarizeScanNearMissPicksClosestBelowGate(t *testing.T) {
	obs := []ScanObservationRecord{
		{Symbol: "AAAUSDT", EstimatedRate: 0.0060, MinRateGate: 0.0055, Passed: true},
		// 两个未过闸且费率低于闸：BBB 差 0.0010，CCC 差 0.0003（更接近）。
		{Symbol: "BBBUSDT", Direction: "positive", EstimatedRate: 0.0045, MinRateGate: 0.0055, Passed: false, RejectReason: "正向费率不足"},
		{Symbol: "CCCUSDT", Direction: "positive", EstimatedRate: 0.0052, MinRateGate: 0.0055, Passed: false, RejectReason: "正向费率不足"},
		// 费率其实够了但被其他规则拒（基差），不算近失。
		{Symbol: "DDDUSDT", EstimatedRate: 0.0080, MinRateGate: 0.0055, Passed: false, RejectReason: "基差率过大"},
	}
	got := summarizeScanNearMiss(obs)
	if got.Scanned != 4 {
		t.Fatalf("Scanned = %d, want 4", got.Scanned)
	}
	if got.PassedScan != 1 {
		t.Fatalf("PassedScan = %d, want 1", got.PassedScan)
	}
	if got.NearMiss == nil {
		t.Fatal("NearMiss = nil, want CCCUSDT")
	}
	if got.NearMiss.Symbol != "CCCUSDT" {
		t.Fatalf("NearMiss.Symbol = %s, want CCCUSDT (smallest gap below gate)", got.NearMiss.Symbol)
	}
	if d := got.NearMiss.Gap - 0.0003; d > 1e-9 || d < -1e-9 {
		t.Fatalf("NearMiss.Gap = %.6f, want 0.0003", got.NearMiss.Gap)
	}
}

func TestSummarizeScanNearMissNoneBelowGate(t *testing.T) {
	obs := []ScanObservationRecord{
		{Symbol: "AAAUSDT", EstimatedRate: 0.0090, MinRateGate: 0.0055, Passed: false, RejectReason: "基差率过大"},
	}
	got := summarizeScanNearMiss(obs)
	if got.NearMiss != nil {
		t.Fatalf("NearMiss = %+v, want nil (rate above gate → not a floor near-miss)", got.NearMiss)
	}
}

func TestClassifyRejectReason(t *testing.T) {
	cases := map[string]string{
		"资金费率数据过期":                      "数据过期",
		"资金费率跳跃度过大 (0.02 >= 0.015)":     "费率跳变过大",
		"基差率过大 (0.003 > 0.0015)":        "基差过大",
		"正向费率不足 (0.004 < 0.0055)":       "费率不足",
		"正向费率过高 (0.04 > 0.03)":          "费率过高",
		"净收益率低于作废阈值 (0.0001 <= 0.0005)": "净利率低于阈值",
		"连续观察阶段发现盘口深度不足，coverage=0.5":   "盘口深度/覆盖不足",
		"":          "未标注",
		"某种从未见过的原因": "其他",
	}
	for in, want := range cases {
		if got := classifyRejectReason(in); got != want {
			t.Errorf("classifyRejectReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTallyInvalidReasonsSkipsValid(t *testing.T) {
	mk := func(valid int32, reason string) *MonitoredTarget {
		tgt := &MonitoredTarget{InvalidReason: reason}
		atomic.StoreInt32(&tgt.IsValid, valid)
		return tgt
	}
	targets := []*MonitoredTarget{
		mk(1, ""), // 有效 → 跳过
		mk(0, "正向费率不足 (0.004 < 0.0055)"),       // 费率不足
		mk(0, "净收益率低于作废阈值 (0.0001 <= 0.0005)"), // 净利率低于阈值
		mk(0, "净收益率低于作废阈值 (0.0002 <= 0.0005)"), // 净利率低于阈值
		nil, // nil 安全跳过
	}
	tally := tallyInvalidReasons(targets)
	if tally["费率不足"] != 1 {
		t.Errorf("费率不足 = %d, want 1", tally["费率不足"])
	}
	if tally["净利率低于阈值"] != 2 {
		t.Errorf("净利率低于阈值 = %d, want 2", tally["净利率低于阈值"])
	}
	if _, ok := tally["未标注"]; ok {
		t.Error("有效目标(空 reason)不应进入 tally")
	}
}

func TestFormatCycleDecisionFunnelZeroOpenWithNearMiss(t *testing.T) {
	f := cycleDecisionFunnel{
		CycleID: "run-1/cycle-7",
		Scan: scanFunnelSummary{
			Scanned:    120,
			PassedScan: 0,
			NearMiss: &scanNearMiss{
				Symbol: "CCCUSDT", Direction: "positive",
				EstimatedRate: 0.0052, MinRateGate: 0.0055, Gap: 0.0003,
				RejectReason: "正向费率不足",
			},
		},
		AfterCooldown: 0,
	}
	out := formatCycleDecisionFunnel(f)
	if !strings.Contains(out, "海选评估=120") {
		t.Errorf("缺少海选评估计数: %s", out)
	}
	if !strings.Contains(out, "开仓=0") {
		t.Errorf("缺少开仓计数: %s", out)
	}
	if !strings.Contains(out, "CCCUSDT") || !strings.Contains(out, "0.005200") {
		t.Errorf("缺少近失候选提示: %s", out)
	}
	if !strings.Contains(out, "MinEstimatedFundingRate") {
		t.Errorf("应提示可调的入场闸参数名: %s", out)
	}
}

func TestFormatCycleDecisionFunnelOpenedOmitsNearMiss(t *testing.T) {
	f := cycleDecisionFunnel{
		CycleID:        "run-1/cycle-8",
		Scan:           scanFunnelSummary{Scanned: 80, PassedScan: 3},
		AfterCooldown:  3,
		MonitorValid:   2,
		BorrowAdmitted: 2,
		Selected:       2,
		Opened:         1,
	}
	out := formatCycleDecisionFunnel(f)
	if !strings.Contains(out, "开仓=1") {
		t.Errorf("缺少开仓计数: %s", out)
	}
	if strings.Contains(out, "本轮 0 开仓") {
		t.Errorf("已开仓不应出现 0 开仓提示: %s", out)
	}
}

func TestFormatReasonTallyStableOrder(t *testing.T) {
	// 计数降序；同计数按名字升序，保证输出确定（便于日志比对）。
	// 同计数按字符串升序：丙(U+4E19) < 乙(U+4E59) < 甲(U+7532)。
	tally := map[string]int{"乙": 2, "甲": 2, "丙": 5}
	got := formatReasonTally(tally)
	want := "丙×5, 乙×2, 甲×2"
	if got != want {
		t.Fatalf("formatReasonTally = %q, want %q", got, want)
	}
}
