package app

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAutonomousRulesMatch(t *testing.T) {
	rules := defaultAutonomousStrategies(time.Now(), 1).Strategies[0].Rules
	if !autonomousRulesMatch(rules, autonomousMetrics{-250, -400, 2, 200}) {
		t.Fatal("冻结条件应命中")
	}
	if autonomousRulesMatch(rules, autonomousMetrics{-250, -400, 1.4, 200}) {
		t.Fatal("量能不足不应命中")
	}
}

func TestAutonomousPerf(t *testing.T) {
	vals, syms := []float64{}, []string{}
	for i := 0; i < 60; i++ {
		vals = append(vals, 20)
		syms = append(syms, "S"+string(rune('A'+i%10)))
	}
	p := autonomousPerfFrom(vals, syms, 6)
	if p.N != 60 || p.Median != 20 || p.PF != 99 || p.GoodBlocks != 6 || p.TopShare > 0.11 {
		t.Fatalf("perf=%+v", p)
	}
}

func TestAutonomousLifecycleKeepsPassingShadowSealed(t *testing.T) {
	e := newFastV3TestEngine(t)
	now := time.Now().UTC()
	e.nowFn = func() time.Time { return now }
	e.settlementsPath = filepath.Join(e.recordDir, "settlements.jsonl")
	riskDir := filepath.Dir(e.recordDir)
	risk := autonomousRiskState{
		SchemaVersion: 1, Phase: autonomyPhaseActive, Venue: "okx", Generation: 1,
		ActivatedAt: now.Add(-time.Hour), HighWaterUSDT: 100, CurrentEquityUSDT: 100, StopPct: 0.30,
	}
	if err := saveAutonomousRisk(riskDir, risk); err != nil {
		t.Fatal(err)
	}
	reg := defaultAutonomousStrategies(now.Add(-time.Hour), 1)
	if err := saveAutonomousRegistry(e.recordDir, reg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		ev := directionFactorEvent{
			RecordedAt: now.Add(-time.Duration(60-i) * time.Second), Stage: "close",
			Source: "panic_volume_fade_4h", Symbol: "S" + string(rune('A'+i%10)), Net18Bps: 25,
		}
		if err := appendJSONLRecord(filepath.Join(e.recordDir, directionFactorEventFile), ev); err != nil {
			t.Fatal(err)
		}
	}
	e.autonomousLifecycleTick()
	got := loadAutonomousRegistry(e.recordDir, now, 1)
	if got.Strategies[0].State != "shadow" {
		t.Fatalf("state=%s reason=%s", got.Strategies[0].State, got.Strategies[0].LastReason)
	}
	if e.fastProbeAllowed("panic_volume_fade_4h") {
		t.Fatal("重构期 shadow 过门也不得自动接入真钱探针")
	}
}
