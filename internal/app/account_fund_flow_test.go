package app

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

type stubFundFlowProvider struct {
	net float64
	err error
}

func (s *stubFundFlowProvider) FetchNetExternalFundFlowUSDT(context.Context, time.Time) (float64, error) {
	if s == nil {
		return 0, venue.ErrUnsupported
	}
	return s.net, s.err
}

func TestApplyAccountFundFlowAdjustment_RealSubtractsFromDailyEquityDelta(t *testing.T) {
	st := &accountRiskEquityState{}
	obs := testAccountEquityObservation(385.55, -49.45, map[string]float64{"okx": 385.55})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		if name == "okx" {
			return &stubFundFlowProvider{net: -49.45}
		}
		return nil
	}
	buf := &strings.Builder{}
	logger := log.New(buf, "", 0)
	source, _ := applyAccountFundFlowAdjustment(
		context.Background(),
		st,
		obs,
		lookup,
		time.Now().UTC().Add(-time.Hour),
		obs.TotalUSDT,
		obs.RawDailyDelta,
		0,
		accountRiskPositionSummary{},
		0,
		5,
		logger,
	)
	if source != "real" {
		t.Fatalf("source=%q want real", source)
	}
	if st.DailyFundFlowAdjustUSDT != -49.45 {
		t.Fatalf("adjust=%v want -49.45", st.DailyFundFlowAdjustUSDT)
	}
	riskDelta := money.SumUSDT(obs.RawDailyDelta, -st.DailyFundFlowAdjustUSDT)
	if riskDelta != 0 {
		t.Fatalf("risk delta=%v want 0", riskDelta)
	}
	if !strings.Contains(buf.String(), "充提流水源=real cumulative=-49.4500 (首次确认)") {
		t.Fatalf("expected one-shot real source log, got %q", buf.String())
	}
}

func TestApplyAccountFundFlowAdjustment_RealZeroFlowLogsOneShot(t *testing.T) {
	st := &accountRiskEquityState{}
	obs := testAccountEquityObservation(100, 0, map[string]float64{"okx": 100})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		if name == "okx" {
			return &stubFundFlowProvider{net: 0}
		}
		return nil
	}
	buf := &strings.Builder{}
	logger := log.New(buf, "", 0)
	source, step := applyAccountFundFlowAdjustment(
		context.Background(),
		st,
		obs,
		lookup,
		time.Now().UTC().Add(-time.Hour),
		obs.TotalUSDT,
		obs.RawDailyDelta,
		0,
		accountRiskPositionSummary{},
		0,
		5,
		logger,
	)
	if source != "real" || step != 0 {
		t.Fatalf("source=%q step=%v want real/0", source, step)
	}
	if !strings.Contains(buf.String(), "充提流水源=real cumulative=0.0000 (首次确认)") {
		t.Fatalf("expected one-shot real zero-flow log, got %q", buf.String())
	}
}

func TestApplyAccountFundFlowAdjustment_HeuristicLogsOneShot(t *testing.T) {
	st := &accountRiskEquityState{
		HasLastTrusted:       true,
		LastTrustedTotalUSDT: 435,
		LastPositionCount:    0,
	}
	obs := testAccountEquityObservation(385.55, -49.45, map[string]float64{"okx": 385.55})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		if name == "okx" {
			return &stubFundFlowProvider{err: venue.ErrUnsupported}
		}
		return nil
	}
	buf := &strings.Builder{}
	logger := log.New(buf, "", 0)
	source, step := applyAccountFundFlowAdjustment(
		context.Background(),
		st,
		obs,
		lookup,
		time.Now().UTC().Add(-time.Hour),
		obs.TotalUSDT,
		obs.RawDailyDelta,
		0,
		accountRiskPositionSummary{},
		0,
		5,
		logger,
	)
	if source != "heuristic" {
		t.Fatalf("source=%q want heuristic", source)
	}
	if step != -49.45 {
		t.Fatalf("step=%v want -49.45", step)
	}
	if !strings.Contains(buf.String(), "充提流水源=heuristic cumulative=-49.4500 (首次确认)") {
		t.Fatalf("expected one-shot heuristic log, got %q", buf.String())
	}
}

func TestApplyAccountFundFlowAdjustment_FallbackWhenUnsupported(t *testing.T) {
	st := &accountRiskEquityState{
		HasLastTrusted:       true,
		LastTrustedTotalUSDT: 435,
		LastPositionCount:    0,
	}
	obs := testAccountEquityObservation(385.55, -49.45, map[string]float64{"okx": 385.55})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		if name == "okx" {
			return &stubFundFlowProvider{err: venue.ErrUnsupported}
		}
		return nil
	}
	source, step := applyAccountFundFlowAdjustment(
		context.Background(),
		st,
		obs,
		lookup,
		time.Now().UTC().Add(-time.Hour),
		obs.TotalUSDT,
		obs.RawDailyDelta,
		0,
		accountRiskPositionSummary{},
		0,
		5,
		nil,
	)
	if source != "heuristic" {
		t.Fatalf("source=%q want heuristic", source)
	}
	if step != -49.45 {
		t.Fatalf("step=%v want -49.45", step)
	}
}

func TestAccountRiskWatcherWithdrawalWithRealFundFlowNoHalt(t *testing.T) {
	var haltCount atomic.Int32
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 40
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 385.55, 435, 49.45 }
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 435
	w.equityState.LastTrustedByVenue = map[string]float64{"okx": 435}
	w.equityState.LastPositionCount = 0
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(385.55, -49.45, map[string]float64{"okx": 385.55})
	}
	w.fundFlowLookup = func(name string) venue.ExternalFundFlowProvider {
		if name == "okx" {
			return &stubFundFlowProvider{net: -49.45}
		}
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 {
		t.Fatalf("withdrawal must not halt, got halt=%d", haltCount.Load())
	}
	snap := w.buildSnapshot(w.nowFn())
	if snap.DailyEquityDeltaForRiskUSDT != 0 {
		t.Fatalf("risk delta=%v want 0 after real withdrawal adjust", snap.DailyEquityDeltaForRiskUSDT)
	}
}

func TestFetchAccountRealFundFlowUSDT_ErrUnsupportedReturnsFalse(t *testing.T) {
	obs := testAccountEquityObservation(100, 0, map[string]float64{"okx": 100})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		return &stubFundFlowProvider{err: venue.ErrUnsupported}
	}
	_, ok := fetchAccountRealFundFlowUSDT(context.Background(), obs, lookup, time.Now().UTC())
	if ok {
		t.Fatal("ErrUnsupported should not count as real fund-flow ok")
	}
}

func TestFetchAccountRealFundFlowUSDT_OtherErrorReturnsFalse(t *testing.T) {
	obs := testAccountEquityObservation(100, 0, map[string]float64{"okx": 100})
	lookup := func(name string) venue.ExternalFundFlowProvider {
		return &stubFundFlowProvider{err: errors.New("api down")}
	}
	_, ok := fetchAccountRealFundFlowUSDT(context.Background(), obs, lookup, time.Now().UTC())
	if ok {
		t.Fatal("API error should fall back to heuristic")
	}
}
