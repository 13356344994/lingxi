package app

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

type rebalancePartialVenue struct {
	crossVenueTestVenue
}

func (v *rebalancePartialVenue) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if req.ReduceOnly && len(v.positions) > 0 {
		cur := absQty(v.positions[0].PositionAmt)
		reduced := math.Max(cur-req.Qty, 0)
		sign := 1.0
		if v.positions[0].PositionAmt < 0 {
			sign = -1
		}
		v.positions[0].PositionAmt = sign * reduced
		return venue.OrderResult{
			Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
			Status: "filled", ExecutedQty: req.Qty, AvgPrice: req.Price,
		}, nil
	}
	return v.crossVenueTestVenue.PlaceOrder(ctx, req)
}

type imbalancedLowFillVenue struct {
	crossVenueTestVenue
	lowFillRatio float64
}

func (v *imbalancedLowFillVenue) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	res, err := v.crossVenueTestVenue.PlaceOrder(ctx, req)
	if err != nil {
		return res, err
	}
	if req.Side == venue.SideBuy && !req.ReduceOnly && v.lowFillRatio > 0 && v.lowFillRatio < 1 {
		filled := req.Qty * v.lowFillRatio
		res.ExecutedQty = filled
		v.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: filled, MarkPrice: req.Price, Leverage: 2}}
	}
	return res, err
}

func TestRebalanceHedgeOrderErrorDoesNotUpdateLegQty(t *testing.T) {
	now := time.Now()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -700, MarkPrice: 100, Leverage: 2}}
	high.placeAlwaysErr = errors.New("simulated rebalance reject")
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 440, MarkPrice: 100, Leverage: 2}}

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	sink := &recordingAlertSink{}
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
		alerts: &AlertDispatcher{sinks: []AlertSink{sink}, minRank: severityRank(EventLevelInfo), timeout: time.Second, now: time.Now, lastSent: map[string]time.Time{}},
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "rebal-fail",
		HighLeg:    CrossVenueLegState{Qty: 500},
		LowLeg:     CrossVenueLegState{Qty: 500},
	}
	e.rebalanceHedge(context.Background(), pos, -700, 440, true)

	stored, err := e.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("positions=%d want 1 persisted", len(stored))
	}
	got := stored[0]
	if got.HighLeg.Qty != 500 || got.LowLeg.Qty != 500 {
		t.Fatalf("ledger must not fake balance on rebalance error, got high=%v low=%v", got.HighLeg.Qty, got.LowLeg.Qty)
	}
	if got.RebalanceFailStreak != 1 {
		t.Fatalf("RebalanceFailStreak=%d want 1", got.RebalanceFailStreak)
	}
	titles := sink.titles()
	if len(titles) == 0 || !strings.Contains(titles[len(titles)-1], "再平衡") {
		t.Fatalf("expected rebalance critical alert, got %v", titles)
	}
}

func TestRebalanceHedgeSuccessSyncsFromExchange(t *testing.T) {
	now := time.Now()
	high := &rebalancePartialVenue{crossVenueTestVenue: *newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)}
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -700, MarkPrice: 100, Leverage: 2}}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 440, MarkPrice: 100, Leverage: 2}}

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "rebal-ok",
		HighLeg:    CrossVenueLegState{Qty: 700},
		LowLeg:     CrossVenueLegState{Qty: 440},
	}
	e.rebalanceHedge(context.Background(), pos, -700, 440, true)

	stored, err := e.store.load()
	if err != nil {
		t.Fatal(err)
	}
	got := stored[0]
	if got.HighLeg.Qty != 440 || got.LowLeg.Qty != 440 {
		t.Fatalf("want exchange-synced 440/440, got high=%v low=%v", got.HighLeg.Qty, got.LowLeg.Qty)
	}
	if got.RebalanceFailStreak != 0 {
		t.Fatalf("RebalanceFailStreak=%d want 0 after success", got.RebalanceFailStreak)
	}
}

func TestRebalanceHedgeDeltaQuantizedNonZero(t *testing.T) {
	now := time.Now()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	high.instruments = []venue.Instrument{{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 100, ContractSize: 1}}
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -700, MarkPrice: 100, Leverage: 2}}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 650, MarkPrice: 100, Leverage: 2}}

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "rebal-qty0",
		HighLeg:    CrossVenueLegState{Qty: 700},
		LowLeg:     CrossVenueLegState{Qty: 650},
	}
	e.rebalanceHedge(context.Background(), pos, -700, 650, true)

	stored, err := e.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("positions=%d want 1 persisted", len(stored))
	}
	got := stored[0]
	if got.HighLeg.Qty != 700 {
		t.Fatalf("ledger high qty must stay 700 when delta quantizes to 0, got %v", got.HighLeg.Qty)
	}
	if got.RebalanceFailStreak != 1 {
		t.Fatalf("RebalanceFailStreak=%d want 1", got.RebalanceFailStreak)
	}
}

func newCrossVenueOpenTestExecutorWithVenues(t *testing.T, venues map[string]venue.Venue, sink *recordingAlertSink) *CrossVenueExecutor {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         venues,
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         testCrossVenueLogger(),
		nowFn:          func() time.Time { return now },
		symbolCooldown: map[string]time.Time{},
	}
	if sink != nil {
		e.alerts = &AlertDispatcher{
			sinks:    []AlertSink{sink},
			minRank:  severityRank(EventLevelInfo),
			timeout:  time.Second,
			now:      time.Now,
			lastSent: map[string]time.Time{},
		}
	}
	return e
}

func TestExecuteCrossVenueOpenImbalancedFillsConverge(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := &rebalancePartialVenue{crossVenueTestVenue: *newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)}
	high.instruments = []venue.Instrument{{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1, MinNotional: 5}}
	low := &imbalancedLowFillVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now),
		lowFillRatio:        440.0 / 700.0,
	}
	low.instruments = []venue.Instrument{{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1, MinNotional: 5}}

	e := newCrossVenueOpenTestExecutorWithVenues(t, map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}, nil)
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 70000, HighMid: 100, LowMid: 100, HighInstr: high.instruments[0], LowInstr: low.instruments[0]},
		Qty:        700,
		RefPrice:   100,
		IdemPrefix: "open-imbalance-converge",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if pos.Status != CrossVenueStatusOpen {
		t.Fatalf("status=%q want open", pos.Status)
	}
	if dev := qtyDeviation(pos.HighLeg.Qty, pos.LowLeg.Qty); dev > crossVenueHedgeDeviationThreshold {
		t.Fatalf("promoted imbalanced open high=%v low=%v dev=%v", pos.HighLeg.Qty, pos.LowLeg.Qty, dev)
	}
	if pos.HighLeg.Qty != 440 || pos.LowLeg.Qty != 440 {
		t.Fatalf("want converged 440/440, got high=%v low=%v", pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
}

func TestExecuteCrossVenueOpenImbalancedFillsRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := &rebalancePartialVenue{crossVenueTestVenue: *newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)}
	high.instruments = []venue.Instrument{{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1, MinNotional: 5}}
	high.placeAlwaysErr = errors.New("converge reject")
	low := &imbalancedLowFillVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now),
		lowFillRatio:        440.0 / 700.0,
	}
	low.instruments = []venue.Instrument{{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1, MinNotional: 5}}
	high.clearOnReduceOnly = true
	low.clearOnReduceOnly = true

	e := newCrossVenueOpenTestExecutorWithVenues(t, map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}, nil)
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 70000, HighMid: 100, LowMid: 100, HighInstr: high.instruments[0], LowInstr: low.instruments[0]},
		Qty:        700,
		RefPrice:   100,
		IdemPrefix: "open-imbalance-rollback",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err == nil {
		t.Fatal("expected rollback when convergence fails")
	}
	if pos.Status == CrossVenueStatusOpen {
		t.Fatalf("must not promote to Open on failed convergence, status=%q", pos.Status)
	}
}
