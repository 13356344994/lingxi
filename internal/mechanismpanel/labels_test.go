package mechanismpanel

import (
	"math"
	"testing"
	"time"
)

func almostEqual(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

// TestAbsorbFutureRejectsAtOrBeforeDecisionTime 是"未来标签与入场特征物理分离"的运行时闸门
// 的直接测试:即使调用方(编程错误地)把决策时刻及之前的数据递给 absorbFuture,状态必须
// 保持不变——分离不是靠调用约定,是靠这个函数体内的时间检查强制的。
func TestAbsorbFutureRejectsAtOrBeforeDecisionTime(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabel("ep-1", "BTC-USDT", decision, 59900, []time.Duration{5 * time.Second})

	pl.absorbFuture(decision, 12345)                     // 恰好等于 decisionTime,必须被拒绝
	pl.absorbFuture(decision.Add(-1*time.Second), 99999) // 早于 decisionTime,必须被拒绝

	if len(pl.future) != 0 {
		t.Fatalf("absorbFuture must reject t<=decisionTime, got future=%+v", pl.future)
	}
	if pl.results[0].Observed {
		t.Fatalf("horizon must remain unobserved when no valid future data was absorbed")
	}
}

func TestPendingLabelHorizonFreezeAndRunningExtremes(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	decisionMid := 59900.0
	pl := newPendingLabel("ep-1", "BTC-USDT", decision, decisionMid, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second})

	type sample struct {
		offset time.Duration
		mid    float64
	}
	samples := []sample{
		{2 * time.Second, 59850},
		{6 * time.Second, 59860},
		{10 * time.Second, 59870},
		{17 * time.Second, 59900},
		{27 * time.Second, 59950},
	}
	for _, s := range samples {
		pl.absorbFuture(decision.Add(s.offset), s.mid)
	}

	if !pl.isDone() {
		t.Fatalf("expected pending label to be done after covering all horizons")
	}
	label := pl.finalize()
	if !label.Complete {
		t.Fatalf("expected Complete=true, got horizons=%+v", label.Horizons)
	}
	if len(label.Horizons) != 3 {
		t.Fatalf("expected 3 horizons, got %d", len(label.Horizons))
	}
	if len(label.FuturePricePath) != 5 {
		t.Fatalf("expected 5 future price points, got %d", len(label.FuturePricePath))
	}
	for _, p := range label.FuturePricePath {
		if p.OffsetMs <= 0 {
			t.Fatalf("future price path offset must be strictly positive (post-decision), got %d", p.OffsetMs)
		}
	}

	ret := func(mid float64) float64 { return (mid/decisionMid - 1) * 10000 }

	h0, h1, h2 := label.Horizons[0], label.Horizons[1], label.Horizons[2]
	if h0.HorizonMs != 5000 || !h0.Observed {
		t.Fatalf("h0 unexpected: %+v", h0)
	}
	if !almostEqual(h0.ForwardRetBps, ret(59860), 1e-6) {
		t.Errorf("h0.ForwardRetBps = %v, want %v (nearest sample at/after 5s = t=6s)", h0.ForwardRetBps, ret(59860))
	}
	if !almostEqual(h0.MaxAdverseBps, ret(59850), 1e-6) || !almostEqual(h0.MaxFavorableBps, ret(59850), 1e-6) {
		t.Errorf("h0 excursion unexpected: %+v (want both = ret(59850)=%v, single sample within window)", h0, ret(59850))
	}

	if h1.HorizonMs != 10000 || !h1.Observed {
		t.Fatalf("h1 unexpected: %+v", h1)
	}
	if !almostEqual(h1.ForwardRetBps, ret(59870), 1e-6) {
		t.Errorf("h1.ForwardRetBps = %v, want %v", h1.ForwardRetBps, ret(59870))
	}
	if !almostEqual(h1.MaxAdverseBps, ret(59850), 1e-6) {
		t.Errorf("h1.MaxAdverseBps = %v, want %v", h1.MaxAdverseBps, ret(59850))
	}
	if !almostEqual(h1.MaxFavorableBps, ret(59870), 1e-6) {
		t.Errorf("h1.MaxFavorableBps = %v, want %v", h1.MaxFavorableBps, ret(59870))
	}

	if h2.HorizonMs != 20000 || !h2.Observed {
		t.Fatalf("h2 unexpected: %+v", h2)
	}
	if !almostEqual(h2.ForwardRetBps, ret(59950), 1e-6) {
		t.Errorf("h2.ForwardRetBps = %v, want %v", h2.ForwardRetBps, ret(59950))
	}
	if !almostEqual(h2.MaxAdverseBps, ret(59850), 1e-6) {
		t.Errorf("h2.MaxAdverseBps = %v, want %v", h2.MaxAdverseBps, ret(59850))
	}
	if !almostEqual(h2.MaxFavorableBps, ret(59900), 1e-6) {
		t.Errorf("h2.MaxFavorableBps = %v, want %v (max seen within <=20s window before freeze, i.e. t=17s sample)", h2.MaxFavorableBps, ret(59900))
	}
}

func TestPendingLabelIncompleteWhenDataRunsOutBeforeHorizon(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabel("ep-2", "ETH-USDT", decision, 3000, []time.Duration{60 * time.Second})
	pl.absorbFuture(decision.Add(5*time.Second), 3010)
	pl.absorbFuture(decision.Add(70*time.Second), 3050) // exceeds the only horizon before it was ever satisfied at exactly 60s... still freezes since offset>=60s

	if !pl.isDone() {
		t.Fatalf("expected done once offset exceeds max horizon")
	}
	label := pl.finalize()
	if !label.Complete {
		t.Fatalf("70s sample should have frozen the 60s horizon (offset>=horizon), expected Complete=true")
	}
}

func TestPendingLabelIncompleteWhenStreamEndsInsideHorizon(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabel("ep-3", "ETH-USDT", decision, 3000, []time.Duration{60 * time.Second})
	pl.absorbFuture(decision.Add(5*time.Second), 3010) // never reaches 60s horizon

	if pl.isDone() {
		t.Fatalf("must not be done while horizon window still open and no data beyond it yet")
	}
	label := pl.finalize() // simulate forced finalize at true stream end
	if label.Complete {
		t.Fatalf("expected Complete=false when insufficient future data was available")
	}
	if label.Horizons[0].Observed {
		t.Fatalf("horizon must stay Observed=false without a sample at/after it")
	}
}

func TestPendingLabelRejectsStaleHorizonPrice(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabel("ep-stale", "ETH-USDT", decision, 3000, []time.Duration{60 * time.Second})
	pl.absorbFuture(decision.Add(71*time.Second), 3050)

	label := pl.finalize()
	if label.Complete {
		t.Fatalf("stale horizon sample must not produce a complete label")
	}
	if label.Horizons[0].Observed {
		t.Fatalf("sample 11s after horizon exceeds 10s staleness and must remain unobserved")
	}
}

func TestPendingLabelFinalizeCarriesPhaseMetadata(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabelForPhase("raw-1#relay", "raw-1", PhaseRelay, "BTC-USDT", decision, 100, []time.Duration{1 * time.Second})
	pl.absorbFuture(decision.Add(1500*time.Millisecond), 101)

	label := pl.finalize()
	if label.EpisodeID != "raw-1#relay" {
		t.Fatalf("EpisodeID = %q, want raw-1#relay", label.EpisodeID)
	}
	if label.RawEpisodeID != "raw-1" {
		t.Fatalf("RawEpisodeID = %q, want raw-1", label.RawEpisodeID)
	}
	if label.Phase != PhaseRelay {
		t.Fatalf("Phase = %q, want relay", label.Phase)
	}
}
