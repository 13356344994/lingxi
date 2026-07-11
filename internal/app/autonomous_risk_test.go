package app

import (
	"context"
	"errors"
	"log"
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/risk"
)

func TestUpdateAutonomousHighWater_FlowFirstThenEquity(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	st := autonomousRiskState{
		Phase: autonomyPhaseActive, Venue: "okx",
		DayStartCST: beijingCSTDayStart(now), LastDailyFlowUSDT: 0,
	}
	snap := accountRiskSnapshot{
		VenueEquities:            map[string]float64{"okx": 120},
		DailyFundFlowByVenueUSDT: map[string]float64{"okx": 0},
	}
	if !updateAutonomousHighWater(&st, snap, now) {
		t.Fatal("应更新")
	}
	if diff := math.Abs(st.HighWaterUSDT - 120); diff > 1e-6 || st.DrawdownUSDT != 0 {
		t.Fatalf("初始化错误: %+v", st)
	}
	// 2026-07-11 事故回归:先出现资金流(提现)后才到账户权益变化。
	snap.VenueEquities["okx"] = 120
	snap.DailyFundFlowByVenueUSDT["okx"] = -22.35
	updateAutonomousHighWater(&st, snap, now.Add(time.Minute))
	if !st.HasPendingAdjustments || st.PendingFlowCount == 0 {
		t.Fatalf("先资金流时应进入 pending 保护: %+v", st)
	}
	if diff := math.Abs(st.TradingEquityUSDT - 120); diff > 1e-6 || st.DrawdownUSDT != 0 {
		t.Fatalf("pending 阶段不应产生交易回撤: %+v", st)
	}
	wantSolvency := 120 - 22.35
	if diff := math.Abs(st.SolvencyEquityUSDT - wantSolvency); diff > 1e-6 {
		t.Fatalf("pending 转出应立刻占用偿付准备金 got=%.6f want=%.6f", st.SolvencyEquityUSDT, wantSolvency)
	}
	// 权益随后落账后，pending 清空，交易权益高水位与回撤保持干净。
	snap.VenueEquities["okx"] = 97.65
	updateAutonomousHighWater(&st, snap, now.Add(2*time.Minute))
	if st.HasPendingAdjustments || st.PendingFlowCount != 0 {
		t.Fatalf("权益到账后应完成匹配: %+v", st)
	}
	if diff := math.Abs(st.TradingEquityUSDT - 120); diff > 1e-6 || st.DrawdownUSDT != 0 {
		t.Fatalf("先流后权益不应伪造回撤: %+v", st)
	}
}

func TestHandleAutonomousRisk_FlowFirstDoesNotTripFlatten(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	w := &accountRiskWatcher{
		recordDir: t.TempDir(), logger: log.New(&strings.Builder{}, "", 0), nowFn: func() time.Time { return now },
	}
	halted := 0
	w.haltFn = func(string) error { halted++; return nil }
	w.notifyFn = func(string, string) {}

	base := accountRiskSnapshot{
		EquityDataTrusted:        true,
		VenueEquities:            map[string]float64{"okx": 100},
		DailyFundFlowByVenueUSDT: map[string]float64{"okx": 0},
	}
	if w.handleAutonomousRisk(base, now) {
		t.Fatal("初始化不应阻塞 active")
	}
	base.DailyFundFlowByVenueUSDT["okx"] = -22.35
	if w.handleAutonomousRisk(base, now.Add(time.Minute)) {
		t.Fatal("先资金流阶段不应触发 flatten 阻塞")
	}
	base.VenueEquities["okx"] = 77.65
	if w.handleAutonomousRisk(base, now.Add(2*time.Minute)) {
		t.Fatal("到账后仍不应触发 flatten 阻塞")
	}
	st, ok := loadAutonomousRisk(w.recordDir)
	if !ok {
		t.Fatal("应存在 autonomous 风险状态")
	}
	if st.Phase != autonomyPhaseActive {
		t.Fatalf("先流后权益不应触发总止错, phase=%s state=%+v", st.Phase, st)
	}
	if halted != 0 {
		t.Fatalf("先流后权益不应触发 halt, got=%d", halted)
	}
}

func TestAutonomousRiskBreachFlattensAndLearns(t *testing.T) {
	now := time.Now().UTC()
	w := &accountRiskWatcher{
		recordDir: t.TempDir(), logger: log.New(&strings.Builder{}, "", 0), nowFn: func() time.Time { return now },
	}
	halted, flattened := 0, 0
	w.haltFn = func(string) error { halted++; return nil }
	w.flattenFn = func(context.Context) error { flattened++; return nil }
	w.notifyFn = func(string, string) {}
	base := accountRiskSnapshot{
		EquityDataTrusted:        true,
		VenueEquities:            map[string]float64{"okx": 100},
		DailyFundFlowByVenueUSDT: map[string]float64{"okx": 0},
	}
	if w.handleAutonomousRisk(base, now) {
		t.Fatal("初始化不应阻塞 active")
	}
	base.VenueEquities["okx"] = 69.9
	if w.handleAutonomousRisk(base, now.Add(time.Minute)) {
		t.Fatal("大额权益变化在资金流匹配窗内应先受 pending 保护")
	}
	if !w.handleAutonomousRisk(base, now.Add(time.Minute+risk.EquityTruthMatchWindow+time.Second)) {
		t.Fatal("匹配窗过期后真实30%回撤应阻塞")
	}
	st, ok := loadAutonomousRisk(w.recordDir)
	if !ok || st.Phase != autonomyPhaseLearning || st.Generation != 2 {
		t.Fatalf("状态错误: %+v ok=%v", st, ok)
	}
	if halted == 0 || flattened != 1 {
		t.Fatalf("动作错误 halt=%d flatten=%d", halted, flattened)
	}
}

func TestAutonomyGenerationGate(t *testing.T) {
	dir := t.TempDir()
	st := autonomousRiskState{
		SchemaVersion: 1, Phase: autonomyPhaseProbe, Venue: "okx",
		RequireGenerationCert: true, AllowedSources: []string{"new_source"},
	}
	if err := saveAutonomousRisk(dir, st); err != nil {
		t.Fatal(err)
	}
	if !autonomySourceAllowed(dir, "new_source") || autonomySourceAllowed(dir, "old_source") {
		t.Fatal("generation allowlist 失效")
	}
	st.Phase = autonomyPhaseShadow
	if err := saveAutonomousRisk(dir, st); err != nil {
		t.Fatal(err)
	}
	if autonomySourceAllowed(dir, "new_source") {
		t.Fatal("shadow阶段不得开真钱")
	}
}

func TestAutonomousFlattenRetries(t *testing.T) {
	now := time.Now().UTC()
	w := &accountRiskWatcher{recordDir: t.TempDir(), logger: log.New(&strings.Builder{}, "", 0), nowFn: func() time.Time { return now }}
	w.haltFn = func(string) error { return nil }
	attempt := 0
	w.flattenFn = func(context.Context) error {
		attempt++
		if attempt == 1 {
			return errors.New("temporary")
		}
		return nil
	}
	w.notifyFn = func(string, string) {}
	base := accountRiskSnapshot{EquityDataTrusted: true, VenueEquities: map[string]float64{"okx": 100},
		DailyFundFlowByVenueUSDT: map[string]float64{"okx": 0}}
	w.handleAutonomousRisk(base, now)
	base.VenueEquities["okx"] = 60
	w.handleAutonomousRisk(base, now.Add(time.Minute))
	w.handleAutonomousRisk(base, now.Add(time.Minute+risk.EquityTruthMatchWindow+time.Second))
	st, _ := loadAutonomousRisk(w.recordDir)
	if st.Phase != autonomyPhaseFlatten || st.FlattenAttempts != 1 {
		t.Fatalf("首次失败应保持flattening: %+v", st)
	}
	w.handleAutonomousRisk(base, now.Add(2*time.Minute+risk.EquityTruthMatchWindow+time.Second))
	st, _ = loadAutonomousRisk(w.recordDir)
	if st.Phase != autonomyPhaseLearning || st.FlattenAttempts != 2 {
		t.Fatalf("重试成功应进入relearning: %+v", st)
	}
}
