package app

// fast v2.2 自适应惩罚单测:单币亏损退避(门槛递增/冷却指数升级/赢单降档)+ 弱时段提标降额。
// 这些是"替代禁开"的核心机制,失效会退回 v1 同币无限锤模式。

import (
	"io"
	"log"
	"testing"
	"time"

	"lingxi/internal/core"
)

func newFastAdaptiveTestEngine(t *testing.T) *OKXDirectionalEngine {
	t.Helper()
	cfg := core.DefaultEngineConfig().OKXDirectional
	cfg.FastLiveScoreThreshold = 0.6
	cfg.FastLossScoreBumpPerLoss = 0.05
	cfg.FastLossScoreBumpCap = 0.25
	cfg.FastWeakHoursUTC = "0-7"
	cfg.FastWeakHoursScoreBump = 0.10
	cfg.FastWeakHoursNotionalFrac = 0.5
	cfg.FastCooldownSeconds = 300
	cfg.FastLossCooldownBackoffFactor = 3
	cfg.FastLossCooldownCapSeconds = 7200
	dir := t.TempDir()
	e := &OKXDirectionalEngine{
		cfg:             cfg,
		venueName:       "test_adaptive_" + t.Name(),
		recordDir:       dir,
		settlementsPath: dir + "/settlements.jsonl",
		logger:          log.New(io.Discard, "", 0),
		nowFn:           time.Now,
	}
	return e
}

func TestFastAdaptive_LossLevelRaisesThreshold(t *testing.T) {
	e := newFastAdaptiveTestEngine(t)
	// 强时段(UTC 12:00),隔离弱时段影响
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

	th0, frac0, _ := e.fastEntryAdaptive("AAA-USDT", now)
	if th0 != 0.6 || frac0 != 1.0 {
		t.Fatalf("干净币基线应为 0.6/1.0,得 %.2f/%.2f", th0, frac0)
	}
	// 连亏 3 笔 → 门槛 0.6+0.15
	for i := 0; i < 3; i++ {
		e.fastDailyAdd(now, "AAA-USDT", -0.05)
	}
	th3, _, tags := e.fastEntryAdaptive("AAA-USDT", now)
	if th3 < 0.749 || th3 > 0.751 {
		t.Fatalf("亏3档门槛应≈0.75,得 %.3f (tags=%s)", th3, tags)
	}
	// 赢 1 笔降 1 档 → 0.70
	e.fastDailyAdd(now, "AAA-USDT", +0.05)
	th2, _, _ := e.fastEntryAdaptive("AAA-USDT", now)
	if th2 < 0.699 || th2 > 0.701 {
		t.Fatalf("赢单降档后应≈0.70,得 %.3f", th2)
	}
	// cap:亏 10 笔也不超 0.6+0.25
	for i := 0; i < 10; i++ {
		e.fastDailyAdd(now, "AAA-USDT", -0.05)
	}
	thCap, _, _ := e.fastEntryAdaptive("AAA-USDT", now)
	if thCap > 0.851 {
		t.Fatalf("门槛加成应被 cap 在 +0.25,得 %.3f", thCap)
	}
	// 其他币不受影响
	thB, _, _ := e.fastEntryAdaptive("BBB-USDT", now)
	if thB != 0.6 {
		t.Fatalf("BBB 应不受 AAA 亏损影响,得 %.3f", thB)
	}
}

func TestFastAdaptive_WeakHours(t *testing.T) {
	e := newFastAdaptiveTestEngine(t)
	weak := time.Date(2026, 7, 3, 3, 0, 0, 0, time.UTC)   // UTC 03 ∈ 0-7
	strong := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC) // UTC 15 ∉ 0-7

	thW, fracW, tags := e.fastEntryAdaptive("CCC-USDT", weak)
	if thW < 0.699 || thW > 0.701 || fracW != 0.5 {
		t.Fatalf("弱时段应 0.70/0.5,得 %.3f/%.2f (tags=%s)", thW, fracW, tags)
	}
	thS, fracS, _ := e.fastEntryAdaptive("CCC-USDT", strong)
	if thS != 0.6 || fracS != 1.0 {
		t.Fatalf("强时段应 0.6/1.0,得 %.3f/%.2f", thS, fracS)
	}
}

func TestFastParseWeakHoursUTC(t *testing.T) {
	m := fastParseWeakHoursUTC("0-3,22,23")
	for h, want := range map[int]bool{0: true, 3: true, 4: false, 22: true, 23: true, 12: false} {
		if m[h] != want {
			t.Fatalf("hour %d: got %v want %v", h, m[h], want)
		}
	}
	empty := fastParseWeakHoursUTC("")
	for h := 0; h < 24; h++ {
		if empty[h] {
			t.Fatalf("空配置不应命中任何小时")
		}
	}
}

func TestFastAdaptive_CooldownBackoff(t *testing.T) {
	e := newFastAdaptiveTestEngine(t)
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	e.nowFn = func() time.Time { return now }
	sym := "DDD-USDT"
	// 亏 3 档:冷却应为 300×3^2=2700s
	for i := 0; i < 3; i++ {
		e.fastDailyAdd(now, sym, -0.05)
	}
	e.fastSetCooldown(sym, "fast_stop_reversal", now)
	fastCooldownMu.Lock()
	until := fastCooldown[e.venueName][sym]
	fastCooldownMu.Unlock()
	got := until.Sub(now)
	if got != 2700*time.Second {
		t.Fatalf("亏3档冷却应 2700s,得 %v", got)
	}
	// cap:亏 10 档也不超 7200s
	for i := 0; i < 7; i++ {
		e.fastDailyAdd(now, sym, -0.05)
	}
	e.fastSetCooldown(sym, "fast_stop_reversal", now)
	fastCooldownMu.Lock()
	until = fastCooldown[e.venueName][sym]
	fastCooldownMu.Unlock()
	if until.Sub(now) != 7200*time.Second {
		t.Fatalf("冷却应被 cap 在 7200s,得 %v", until.Sub(now))
	}
}
