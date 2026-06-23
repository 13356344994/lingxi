package app

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func writeSettlementLine(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateStrategySettlementsCSTDayAndAllTime(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 19, 2, 30, 0, 0, time.UTC)
	dayStart := beijingCSTDayStart(now)

	svspPath := filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName)
	if err := os.MkdirAll(filepath.Dir(svspPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, svspPath, SingleVenueSpotPerpSettlement{
		Symbol: "BTC-USDT", Venue: "binance",
		OpenTime: dayStart.Add(-2 * time.Hour), CloseTime: dayStart.Add(time.Minute),
		NetPnL: 1.5, RealizedFunding: 0.5, OpenFees: 0.1, CloseFees: 0.1, TotalFees: 0.2,
	})
	writeSettlementLine(t, svspPath, SingleVenueSpotPerpSettlement{
		Symbol: "ETH-USDT", Venue: "binance",
		OpenTime: dayStart.Add(-time.Hour), CloseTime: dayStart.Add(-time.Minute),
		NetPnL: 99, RealizedFunding: 1, TotalFees: 0.3,
	})

	cvspPath := filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile)
	if err := os.MkdirAll(filepath.Dir(cvspPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, cvspPath, CrossVenueSpotPerpSettlement{
		Symbol: "SOL-USDT", SpotVenue: "binance", PerpVenue: "bybit",
		OpenTime: dayStart, CloseTime: dayStart.Add(30 * time.Minute),
		NetPnL: 2.0, RealizedFunding: 0.8, OpenFees: 0.2, CloseFees: 0.2, TotalFees: 0.4,
	})

	perpPath := filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName)
	if err := os.MkdirAll(filepath.Dir(perpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, perpPath, CrossVenueSettlement{
		Symbol: "AVAX-USDT", HighVenue: "okx", LowVenue: "gate",
		OpenTime: dayStart, CloseTime: dayStart.Add(time.Hour),
		FundingTruthStatus: "complete",
		NetPnL:             -0.5, RealizedFunding: 0.1, OpenFees: 0.1, CloseFees: 0.1, TotalFees: 0.2,
	})

	s := &DashboardServer{store: mustTestReportStore(t, dir)}
	today, err := TodayCSTStrategyPnL(s, now)
	if err != nil {
		t.Fatal(err)
	}
	if today.TradeCount != 3 {
		t.Fatalf("today trade_count=%d want 3", today.TradeCount)
	}
	wantToday := 3.0
	if math.Abs(today.TotalNetPnL-wantToday) > 1e-9 {
		t.Fatalf("today total=%v want %v", today.TotalNetPnL, wantToday)
	}
	if math.Abs(today.ByStrategy[UnifiedStrategySingleVenueSpotPerp]-1.5) > 1e-9 {
		t.Fatalf("by_strategy svsp=%v", today.ByStrategy)
	}
	if math.Abs(today.ByStrategy[UnifiedStrategyCrossVenueSpotPerp]-2.0) > 1e-9 {
		t.Fatalf("by_strategy cvsp=%v", today.ByStrategy)
	}

	allTime, err := AllTimeStrategyPnL(s, now)
	if err != nil {
		t.Fatal(err)
	}
	wantAll := 102.0
	if math.Abs(allTime.TotalNetPnL-wantAll) > 1e-9 {
		t.Fatalf("all_time total=%v want %v", allTime.TotalNetPnL, wantAll)
	}
}

func mustTestReportStore(t *testing.T, dir string) *ReportStore {
	t.Helper()
	store, err := NewReportStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRotationPositionViewNormalization(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	now := time.Now().UTC()
	opened := now.Add(-2 * time.Hour)

	svsp := viewFromSVSP(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: "binance", Status: SVSPStatusOpen,
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1, EntryPrice: 100},
		NotionalUSDT: 100, OpenExpectedNetEV: 1.2, EstFundingUSDT: 0.5,
		RealizedFundingUSDT: 0.12, OpenedAt: opened,
		SingleVenueSpotPerpAttribution: SingleVenueSpotPerpAttribution{OpenFeesUSDT: 0.08},
	}, execCfg, now)
	if svsp.StrategyType != UnifiedStrategySingleVenueSpotPerp || svsp.Status != "open" {
		t.Fatalf("svsp view=%+v", svsp)
	}
	if svsp.ExpectedCloseCostUSDT <= 0 || svsp.AccruedFundingUSDT != 0.12 {
		t.Fatalf("svsp costs/funding=%+v", svsp)
	}

	cvsp := viewFromCVSP(CrossVenueSpotPerpPosition{
		Symbol: "ETH-USDT", SpotVenue: "binance", PerpVenue: "bybit", Status: CVSPStatusOpen,
		SpotLeg:      CrossVenueSpotPerpLegState{Venue: "binance", Market: "spot", Side: "buy", Qty: 2, EntryPrice: 50},
		PerpLeg:      CrossVenueSpotPerpLegState{Venue: "bybit", Market: "perp", Side: "short", Qty: 2, EntryPrice: 50},
		NotionalUSDT: 100, OpenFeesUSDT: 0.1, ExpectedNetEVUSDT: 2.0, ExpectedFundingUSDT: 1.0,
		PerpLegFundingUSDT: 0.2, OpenedAt: opened,
	}, execCfg, now)
	if cvsp.StrategyType != UnifiedStrategyCrossVenueSpotPerp || len(cvsp.Venues) != 2 {
		t.Fatalf("cvsp view=%+v", cvsp)
	}

	svps := viewFromSVPS(SingleVenuePerpSpotPosition{
		Symbol: "SOL-USDT", Venue: "okx", Status: SVPSStatusClosing,
		SpotLeg:      SingleVenuePerpSpotLegState{Market: "spot", Side: "sell", Qty: 3, EntryPrice: 20},
		PerpLeg:      SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 3, EntryPrice: 20},
		NotionalUSDT: 60, OpenExpectedNetEV: 0.8, EstFundingUSDT: 0.3,
		RealizedFundingUSDT: 0.05, OpenedAt: opened, CloseReason: "rotation_switch",
		SingleVenuePerpSpotAttribution: SingleVenuePerpSpotAttribution{OpenFeesUSDT: 0.05},
	}, execCfg, now)
	if svps.Status != "closing" || svps.SwitchLineage == "" {
		t.Fatalf("svps view=%+v", svps)
	}

	cvps := viewFromCVPS(CrossVenuePerpSpotPosition{
		Symbol: "NEAR-USDT", SpotVenue: "gate", PerpVenue: "binance", Status: CVPSStatusOrphanSingleLeg,
		SpotLeg:      CrossVenuePerpSpotLegState{Venue: "gate", Market: "spot", Side: "sell", Qty: 4, EntryPrice: 10},
		PerpLeg:      CrossVenuePerpSpotLegState{Venue: "binance", Market: "perp", Side: "long", Qty: 4, EntryPrice: 10},
		NotionalUSDT: 40, OpenFeesUSDT: 0.04, ExpectedNetEVUSDT: 0.6, ExpectedFundingUSDT: 0.2,
		PerpLegFundingUSDT: 0.03, OpenedAt: opened, CloseReason: "dust_orphan dust_residual≈$0.50",
	}, execCfg, now)
	if cvps.Status != "dust-closed" {
		t.Fatalf("cvps status=%q want dust-closed", cvps.Status)
	}
	if cvps.SpotLeg.MarketType != "spot" || cvps.PerpLeg.MarketType != "perp" {
		t.Fatalf("market_type spot=%q perp=%q", cvps.SpotLeg.MarketType, cvps.PerpLeg.MarketType)
	}
}

func TestRotationLegUnrealizedPnLMath(t *testing.T) {
	tests := []struct {
		name  string
		side  string
		qty   float64
		entry float64
		cur   float64
		want  float64
	}{
		{"spot_long_up", "buy", 2, 100, 105, 10},
		{"spot_long_down", "long", 2, 100, 95, -10},
		{"perp_short_up", "short", 3, 50, 48, 6},
		{"perp_short_down", "short", 3, 50, 52, -6},
		{"spot_short_up", "sell", 1, 20, 18, 2},
		{"perp_long_up", "long", 4, 10, 11, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rotationLegUnrealizedPnLUSDT(tc.side, tc.qty, tc.entry, tc.cur)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("pnl=%v want %v", got, tc.want)
			}
		})
	}
}

func TestRotationPositionTotalPnLAndRatio(t *testing.T) {
	spotPnL := rotationLegUnrealizedPnLUSDT("buy", 1, 100, 101)    // +1
	perpPnL := rotationLegUnrealizedPnLUSDT("short", 1, 100, 99.5) // +0.5
	accrued := 0.2
	openFees := 0.08
	closeFees := 0.08
	total := rotationPositionTotalPnLUSDT(spotPnL, perpPnL, accrued, openFees, closeFees)
	wantTotal := 1.54
	if math.Abs(total-wantTotal) > 1e-9 {
		t.Fatalf("total=%v want %v", total, wantTotal)
	}
	ratioPtr := rotationPositionPnLRatio(total, 100)
	if ratioPtr == nil {
		t.Fatal("ratio nil")
	}
	if math.Abs(*ratioPtr-0.0154) > 1e-9 {
		t.Fatalf("ratio=%v want 0.0154", *ratioPtr)
	}
}

func TestSettlementViewPnLRatio(t *testing.T) {
	view := settlementViewFromRecord(strategySettlementRecord{
		NetPnL: 2.5, NotionalUSDT: 100,
	})
	if math.Abs(view.PnLRatio-0.025) > 1e-9 {
		t.Fatalf("pnl_ratio=%v want 0.025", view.PnLRatio)
	}
}

type rotationPriceTestVenue struct {
	fakeFundingVenue
	spotMid float64
	perpMid float64
}

func (v *rotationPriceTestVenue) FetchBookTicker(_ context.Context, m venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	mid := v.spotMid
	if m == venue.MarketPerp {
		mid = v.perpMid
	}
	if mid <= 0 {
		return venue.BookTicker{}, venue.ErrUnsupported
	}
	return venue.BookTicker{BidPrice: mid - 0.5, AskPrice: mid + 0.5}, nil
}

func TestEnrichOpenRotationPositionViewsWithLivePrices(t *testing.T) {
	venues := map[string]venue.Venue{
		binance.VenueName: &rotationPriceTestVenue{
			fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
			spotMid:          102,
			perpMid:          101,
		},
	}
	view := RotationPositionView{
		Symbol:                "BTC-USDT",
		Status:                "open",
		NotionalUSDT:          100,
		OpenFeesUSDT:          0.08,
		ExpectedCloseFeesUSDT: 0.08,
		AccruedFundingUSDT:    0.12,
		SpotLeg:               rotationSpotLegView(binance.VenueName, "spot", "buy", 1, 100, 0, nil),
		PerpLeg:               rotationPerpLegView(binance.VenueName, "perp", "short", 1, 100, 0, nil),
	}
	enrichOneRotationPositionWithLivePrices(context.Background(), venues, &view)
	if view.SpotLeg.PriceUnavailable || view.PerpLeg.PriceUnavailable {
		t.Fatalf("price unavailable spot=%v perp=%v", view.SpotLeg.PriceUnavailable, view.PerpLeg.PriceUnavailable)
	}
	if view.SpotLeg.CurrentPrice != 102 || view.PerpLeg.CurrentPrice != 101 {
		t.Fatalf("prices spot=%v perp=%v", view.SpotLeg.CurrentPrice, view.PerpLeg.CurrentPrice)
	}
	if view.TotalPnLUSDT == nil {
		t.Fatal("total pnl nil")
	}
	wantSpot := 2.0
	wantPerp := -1.0
	if math.Abs(*view.SpotLeg.LegUnrealizedPnLUSDT-wantSpot) > 1e-9 {
		t.Fatalf("spot leg pnl=%v want %v", *view.SpotLeg.LegUnrealizedPnLUSDT, wantSpot)
	}
	if math.Abs(*view.PerpLeg.LegUnrealizedPnLUSDT-wantPerp) > 1e-9 {
		t.Fatalf("perp leg pnl=%v want %v", *view.PerpLeg.LegUnrealizedPnLUSDT, wantPerp)
	}
	wantTotal := rotationPositionTotalPnLUSDT(wantSpot, wantPerp, 0.12, 0.08, 0.08)
	if math.Abs(*view.TotalPnLUSDT-wantTotal) > 1e-9 {
		t.Fatalf("total=%v want %v", *view.TotalPnLUSDT, wantTotal)
	}
	if view.PnLRatio == nil || math.Abs(*view.PnLRatio-wantTotal/100) > 1e-9 {
		t.Fatalf("ratio=%v want %v", view.PnLRatio, wantTotal/100)
	}
}

func TestEnrichRotationPositionFailSoftOnMissingPrice(t *testing.T) {
	view := RotationPositionView{
		Symbol:  "ETH-USDT",
		Status:  "open",
		SpotLeg: rotationSpotLegView(binance.VenueName, "spot", "buy", 1, 50, 0, nil),
		PerpLeg: rotationPerpLegView(binance.VenueName, "perp", "short", 1, 50, 0, nil),
	}
	enrichOneRotationPositionWithLivePrices(context.Background(), map[string]venue.Venue{}, &view)
	if !view.PriceUnavailable {
		t.Fatal("expected price_unavailable")
	}
	if view.SpotLeg.LegUnrealizedPnLUSDT != nil || view.PerpLeg.LegUnrealizedPnLUSDT != nil {
		t.Fatalf("leg pnl should be omitted: spot=%v perp=%v", view.SpotLeg.LegUnrealizedPnLUSDT, view.PerpLeg.LegUnrealizedPnLUSDT)
	}
	if view.TotalPnLUSDT != nil {
		t.Fatalf("total pnl should be omitted when prices missing: %v", view.TotalPnLUSDT)
	}
}

func TestEarningsAPIReturnsRotationBasedRealizedPnL(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	dayStart := beijingCSTDayStart(now)

	svspPath := filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName)
	if err := os.MkdirAll(filepath.Dir(svspPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, svspPath, SingleVenueSpotPerpSettlement{
		Symbol: "BTC-USDT", Venue: "binance",
		OpenTime: dayStart, CloseTime: dayStart.Add(time.Minute),
		NetPnL: 4.2, RealizedFunding: 1.0, TotalFees: 0.2,
	})

	store := mustTestReportStore(t, dir)
	s := &DashboardServer{store: store}
	pnl, err := TodayCSTStrategyPnL(s, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(pnl.TotalNetPnL-4.2) > 1e-9 {
		t.Fatalf("TodayCSTStrategyPnL total=%v want 4.2", pnl.TotalNetPnL)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/earnings", nil)
	rec := httptest.NewRecorder()
	s.handleEarningsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["basis"] != "settlement_ledger_realized_pnl" {
		t.Fatalf("basis=%v", resp["basis"])
	}
	if math.Abs(resp["realized_pnl_usdt"].(float64)-4.2) > 1e-9 {
		t.Fatalf("realized_pnl=%v", resp["realized_pnl_usdt"])
	}
	if _, ok := resp["account_equity_delta_usdt"]; !ok {
		t.Fatalf("missing account_equity_delta_usdt: %+v", resp)
	}
}

func TestRotationPositionsAPIHandler(t *testing.T) {
	execCfg := EngineConfig{CrossVenueExecutor: CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}}
	venues := map[string]venue.Venue{
		binance.VenueName: &rotationPriceTestVenue{
			fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
			spotMid:          100,
			perpMid:          100,
		},
	}
	svsp := NewSingleVenueSpotPerpExecutor(execCfg, venues, t.TempDir(), nil)
	_ = svsp.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: "binance", Status: SVSPStatusOpen,
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1, EntryPrice: 100},
		NotionalUSDT: 100, OpenedAt: time.Now().UTC(), IdemPrefix: "test",
		SingleVenueSpotPerpAttribution: SingleVenueSpotPerpAttribution{OpenFeesUSDT: 0.08},
	})
	s := &DashboardServer{svspExec: svsp}
	req := httptest.NewRequest(http.MethodGet, "/api/rotation/positions?status=open", nil)
	rec := httptest.NewRecorder()
	s.handleRotationPositionsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp RotationPositionsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || resp.Positions[0].Symbol != "BTC-USDT" {
		t.Fatalf("resp=%+v", resp)
	}
	pos := resp.Positions[0]
	if pos.SpotLeg.MarketType != "spot" || pos.PerpLeg.MarketType != "perp" {
		t.Fatalf("market types spot=%q perp=%q", pos.SpotLeg.MarketType, pos.PerpLeg.MarketType)
	}
	if pos.TotalPnLUSDT == nil {
		t.Fatal("expected total_pnl_usdt")
	}
}

func TestViewFromCrossVenuePerpPerp(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	now := time.Now().UTC()
	opened := now.Add(-90 * time.Minute)
	pos := CrossVenuePosition{
		Symbol: "ETH-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status:       CrossVenueStatusOpen,
		HighLeg:      CrossVenueLegState{Side: "sell", Qty: 0.5, EntryPrice: 2500},
		LowLeg:       CrossVenueLegState{Side: "buy", Qty: 0.5, EntryPrice: 2498},
		NotionalUSDT: 1250, EstFundingUSDT: 2.5, CurrentFutureEV: 1.8,
		HighLegFundingUSDT: 0.3, LowLegFundingUSDT: -0.1,
		CrossVenueAttribution: CrossVenueAttribution{OpenFeesUSDT: 0.2},
		OpenedAt:              opened,
	}
	view := viewFromCrossVenuePerpPerp(pos, execCfg, now)
	if view.StrategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("strategy_type=%q want %q", view.StrategyType, UnifiedStrategyPerpPerp)
	}
	if len(view.Venues) != 2 || view.Venues[0] != bybit.VenueName || view.Venues[1] != okx.VenueName {
		t.Fatalf("venues=%v", view.Venues)
	}
	if view.SpotLeg.MarketType != "perp" || view.PerpLeg.MarketType != "perp" {
		t.Fatalf("leg market types spot=%q perp=%q", view.SpotLeg.MarketType, view.PerpLeg.MarketType)
	}
	if view.SpotLeg.Venue != bybit.VenueName || view.PerpLeg.Venue != okx.VenueName {
		t.Fatalf("leg venues spot=%q perp=%q", view.SpotLeg.Venue, view.PerpLeg.Venue)
	}
	if view.SpotLeg.Side != "sell" || view.PerpLeg.Side != "buy" {
		t.Fatalf("leg sides spot=%q perp=%q", view.SpotLeg.Side, view.PerpLeg.Side)
	}
	if math.Abs(view.NotionalUSDT-1250) > 1e-9 {
		t.Fatalf("notional=%v want 1250", view.NotionalUSDT)
	}
	if math.Abs(view.OpenFeesUSDT-0.2) > 1e-9 || math.Abs(view.AccruedFundingUSDT-0.2) > 1e-9 {
		t.Fatalf("fees/funding=%+v", view)
	}
	if view.Status != "open" || view.OpportunityID == "" {
		t.Fatalf("status=%q opp=%q", view.Status, view.OpportunityID)
	}
}

func TestRotationExpectedNextFundingSignConvention(t *testing.T) {
	now := time.Date(2026, 6, 21, 15, 30, 0, 0, time.UTC)
	nextHigh := now.Add(30 * time.Minute)
	nextLow := time.Date(2026, 6, 21, 16, 0, 0, 0, time.UTC)

	shortPositive := rotationExpectedLegFundingCashflowUSDT("sell", 0.005, 100, 8, nextHigh, now)
	if math.Abs(shortPositive-0.5) > 1e-9 {
		t.Fatalf("short rate>0 want +0.5 got %v", shortPositive)
	}
	longNegative := rotationExpectedLegFundingCashflowUSDT("buy", -0.0126, 94.44, 8, nextLow, now)
	wantLong := 0.0126 * 94.44
	if math.Abs(longNegative-wantLong) > 1e-4 {
		t.Fatalf("long rate<0 want ~%v got %v", wantLong, longNegative)
	}

	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	pos := CrossVenuePosition{
		Symbol: "H-USDT", HighVenue: bybit.VenueName, LowVenue: binance.VenueName,
		Status:       CrossVenueStatusOpen,
		HighLeg:      CrossVenueLegState{Side: "sell", Qty: 100, EntryPrice: 1.0},
		LowLeg:       CrossVenueLegState{Side: "buy", Qty: 94.44, EntryPrice: 1.0},
		NotionalUSDT: 100, HighFundingRate: 0.005, LowFundingRate: -0.0126,
		HighNextFundingTime: nextHigh, LowNextFundingTime: nextLow,
		HighLegFundingUSDT: -0.156, LowLegFundingUSDT: 0,
		OpenedAt: now.Add(-2 * time.Hour),
	}
	view := viewFromCrossVenuePerpPerp(pos, execCfg, now)
	if math.Abs(view.RealizedAccruedFundingUSDT+0.156) > 1e-9 {
		t.Fatalf("realized_accrued=%v want -0.156", view.RealizedAccruedFundingUSDT)
	}
	wantExpected := money.SumUSDT(0.005*100, -(-0.0126)*94.44)
	if math.Abs(view.ExpectedNextFundingUSDT-wantExpected) > 1e-4 {
		t.Fatalf("expected_next=%v want %v", view.ExpectedNextFundingUSDT, wantExpected)
	}
	if view.HighFundingRate != 0.005 || view.LowFundingRate != -0.0126 {
		t.Fatalf("rates high=%v low=%v", view.HighFundingRate, view.LowFundingRate)
	}
	if view.HighNextFundingTime == nil || !view.HighNextFundingTime.Equal(nextHigh) {
		t.Fatalf("high_next=%v want %v", view.HighNextFundingTime, nextHigh)
	}
	if view.LowNextFundingTime == nil || !view.LowNextFundingTime.Equal(nextLow) {
		t.Fatalf("low_next=%v want %v", view.LowNextFundingTime, nextLow)
	}
}

func TestViewFromCrossVenuePerpPerpWindowSettlementCounts(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	highNext := now.Add(2 * time.Hour)
	lowNext := now.Add(45 * time.Minute)
	pos := CrossVenuePosition{
		Symbol: "H-USDT", HighVenue: okx.VenueName, LowVenue: bybit.VenueName,
		Status:              CrossVenueStatusOpen,
		HighLeg:             CrossVenueLegState{Side: "sell", Qty: 100, EntryPrice: 1.0},
		LowLeg:              CrossVenueLegState{Side: "buy", Qty: 100, EntryPrice: 1.0},
		NotionalUSDT:        100,
		HighFundingRate:     0.004,
		LowFundingRate:      0.0001,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: highNext,
		LowNextFundingTime:  lowNext,
		OpenedAt:            now.Add(-30 * time.Minute),
	}
	view := viewFromCrossVenuePerpPerp(pos, execCfg, now)
	if view.HighSettlementsInWindow != 0 {
		t.Fatalf("high_settlements_in_window=%d want 0 (4h leg next settlement beyond window)", view.HighSettlementsInWindow)
	}
	if view.LowSettlementsInWindow != 1 {
		t.Fatalf("low_settlements_in_window=%d want 1 (1h leg within window)", view.LowSettlementsInWindow)
	}
	if view.ComparableWindowEndTime == nil || !view.ComparableWindowEndTime.Equal(lowNext) {
		t.Fatalf("comparable_window_end_time=%v want %v", view.ComparableWindowEndTime, lowNext)
	}
	if view.HighIntervalHours != 4 || view.LowIntervalHours != 1 {
		t.Fatalf("intervals high=%d low=%d want 4/1", view.HighIntervalHours, view.LowIntervalHours)
	}
}

func TestRotationExpectedNextFundingPastOrZeroTime(t *testing.T) {
	now := time.Date(2026, 6, 21, 16, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	// 过期时间戳但无周期(interval=0):无法定位未来结算 → 0。
	if got := rotationExpectedLegFundingCashflowUSDT("sell", 0.01, 100, 0, past, now); got != 0 {
		t.Fatalf("past next funding without interval want 0 got %v", got)
	}
	if got := rotationExpectedLegFundingCashflowUSDT("buy", -0.01, 100, 8, time.Time{}, now); got != 0 {
		t.Fatalf("zero next funding want 0 got %v", got)
	}
	total := rotationExpectedNextFundingUSDT("sell", 0.01, 100, 0, past, "buy", -0.01, 100, 0, time.Time{}, now)
	if total != 0 {
		t.Fatalf("both legs stale without interval want 0 got %v", total)
	}
}

func TestRotationExpectedNextFundingAdvancesStaleTimestamp(t *testing.T) {
	now := time.Date(2026, 6, 23, 2, 4, 0, 0, time.UTC)
	// 行情快照滞后:下次结算显示 02:00(4 分钟前),但仍有周期可推进到真正的下一次。
	staleNext := time.Date(2026, 6, 23, 2, 0, 0, 0, time.UTC)
	// 2h 周期的多腿(rate<0 → 收资费),推进到 04:00,期望 +rate 反号。
	got := rotationExpectedLegFundingCashflowUSDT("buy", -0.003, 100, 2, staleNext, now)
	if math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("stale long leg should advance and pay +0.3 got %v", got)
	}
	// 1h 周期的空腿(rate<0 → 付资费),推进到 03:00。
	short := rotationExpectedLegFundingCashflowUSDT("sell", -0.0002, 100, 1, staleNext, now)
	if math.Abs(short-(-0.02)) > 1e-9 {
		t.Fatalf("stale short leg should advance to -0.02 got %v", short)
	}
	total := rotationExpectedNextFundingUSDT("sell", -0.0002, 100, 1, staleNext, "buy", -0.003, 100, 2, staleNext, now)
	if math.Abs(total-money.SumUSDT(-0.02, 0.3)) > 1e-9 {
		t.Fatalf("stale total want %v got %v", money.SumUSDT(-0.02, 0.3), total)
	}
}

func TestAdvanceFundingTimeToFuture(t *testing.T) {
	now := time.Date(2026, 6, 23, 2, 4, 0, 0, time.UTC)
	stale := time.Date(2026, 6, 23, 2, 0, 0, 0, time.UTC)
	if got := advanceFundingTimeToFuture(stale, now, 2); !got.Equal(time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("advance 2h want 04:00 got %v", got)
	}
	if got := advanceFundingTimeToFuture(stale, now, 1); !got.Equal(time.Date(2026, 6, 23, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("advance 1h want 03:00 got %v", got)
	}
	future := now.Add(time.Hour)
	if got := advanceFundingTimeToFuture(future, now, 2); !got.Equal(future) {
		t.Fatalf("future timestamp should be unchanged got %v", got)
	}
	if got := advanceFundingTimeToFuture(time.Time{}, now, 2); !got.IsZero() {
		t.Fatalf("zero timestamp should stay zero got %v", got)
	}
	if got := advanceFundingTimeToFuture(stale, now, 0); !got.Equal(stale) {
		t.Fatalf("no interval should leave stale unchanged got %v", got)
	}
}

func TestViewFromCrossVenuePerpPerpCarrySingleLegUnaffected(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	now := time.Now().UTC()
	view := viewFromSVSP(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: "binance", Status: SVSPStatusOpen,
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1, EntryPrice: 100},
		NotionalUSDT: 100, RealizedFundingUSDT: 0.12, OpenedAt: now,
		SingleVenueSpotPerpAttribution: SingleVenueSpotPerpAttribution{OpenFeesUSDT: 0.08},
	}, execCfg, now)
	if view.HighFundingRate != 0 || view.LowFundingRate != 0 {
		t.Fatalf("carry should not expose leg rates: high=%v low=%v", view.HighFundingRate, view.LowFundingRate)
	}
	if view.HighNextFundingTime != nil || view.LowNextFundingTime != nil {
		t.Fatalf("carry should not expose next funding times")
	}
	if view.RealizedAccruedFundingUSDT != 0 || view.ExpectedNextFundingUSDT != 0 {
		t.Fatalf("carry funding extras should be zero: realized=%v expected=%v", view.RealizedAccruedFundingUSDT, view.ExpectedNextFundingUSDT)
	}
}

func TestBuildRotationPositionViewsIncludesPerpPerp(t *testing.T) {
	execCfg := EngineConfig{CrossVenueExecutor: CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	opened := time.Now().UTC().Add(-time.Hour)
	cvExec := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return time.Now().UTC() },
	}
	if err := cvExec.store.saveAll([]CrossVenuePosition{{
		Symbol: "ETH-USDT", Status: CrossVenueStatusOpen,
		HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		HighLeg:      CrossVenueLegState{Side: "sell", Qty: 0.5, EntryPrice: 2500},
		LowLeg:       CrossVenueLegState{Side: "buy", Qty: 0.5, EntryPrice: 2498},
		NotionalUSDT: 1250, OpenedAt: opened,
	}}); err != nil {
		t.Fatal(err)
	}
	svsp := NewSingleVenueSpotPerpExecutor(execCfg, map[string]venue.Venue{
		binance.VenueName: &rotationPriceTestVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, spotMid: 100, perpMid: 100},
	}, t.TempDir(), nil)
	_ = svsp.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName, Status: SVSPStatusOpen,
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1, EntryPrice: 100},
		NotionalUSDT: 100, OpenedAt: opened, IdemPrefix: "test",
	})
	s := &DashboardServer{svspExec: svsp, crossVenueExecutor: cvExec}
	views := buildRotationPositionViews(s, execCfg.CrossVenueExecutor, "open", "")
	if len(views) != 2 {
		t.Fatalf("views=%d want 2: %+v", len(views), views)
	}
	byStrategy := map[string]RotationPositionView{}
	for _, v := range views {
		byStrategy[v.StrategyType] = v
	}
	if _, ok := byStrategy[UnifiedStrategySingleVenueSpotPerp]; !ok {
		t.Fatalf("missing carry: %+v", byStrategy)
	}
	pp, ok := byStrategy[UnifiedStrategyPerpPerp]
	if !ok {
		t.Fatalf("missing perp-perp: %+v", byStrategy)
	}
	if pp.Symbol != "ETH-USDT" || math.Abs(pp.NotionalUSDT-1250) > 1e-9 {
		t.Fatalf("perp-perp view=%+v", pp)
	}
	if pp.SpotLeg.MarketType != "perp" || pp.PerpLeg.MarketType != "perp" {
		t.Fatalf("perp-perp leg types spot=%q perp=%q", pp.SpotLeg.MarketType, pp.PerpLeg.MarketType)
	}
}

func TestBuildRotationPositionViewsWithoutPerpPerp(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	s := &DashboardServer{}
	views := buildRotationPositionViews(s, execCfg, "open", "")
	if views != nil {
		t.Fatalf("expected nil/empty views, got %+v", views)
	}
}

func TestRotationPositionsAPIIncludesPerpPerp(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	opened := time.Now().UTC().Add(-30 * time.Minute)
	cvExec := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return time.Now().UTC() },
	}
	if err := cvExec.store.saveAll([]CrossVenuePosition{{
		Symbol: "SOL-USDT", Status: CrossVenueStatusOpen,
		HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		HighLeg:      CrossVenueLegState{Side: "sell", Qty: 10, EntryPrice: 20},
		LowLeg:       CrossVenueLegState{Side: "buy", Qty: 10, EntryPrice: 19.9},
		NotionalUSDT: 200, OpenedAt: opened,
	}}); err != nil {
		t.Fatal(err)
	}
	s := &DashboardServer{crossVenueExecutor: cvExec}
	req := httptest.NewRequest(http.MethodGet, "/api/rotation/positions?status=open", nil)
	rec := httptest.NewRecorder()
	s.handleRotationPositionsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp RotationPositionsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Positions) != 1 {
		t.Fatalf("count=%d positions=%d", resp.Count, len(resp.Positions))
	}
	pos := resp.Positions[0]
	if pos.StrategyType != UnifiedStrategyPerpPerp || pos.Symbol != "SOL-USDT" {
		t.Fatalf("position=%+v", pos)
	}
	if len(pos.Venues) != 2 || pos.SpotLeg.MarketType != "perp" || pos.PerpLeg.MarketType != "perp" {
		t.Fatalf("venues=%v spot_type=%q perp_type=%q", pos.Venues, pos.SpotLeg.MarketType, pos.PerpLeg.MarketType)
	}
}

func TestRotationSettlementsAPIIncludesPerpPerp(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cvExec := &CrossVenueExecutor{
		cfg:       cfg,
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		recordDir: cfg.NetworkConfig.RecordDir,
		nowFn:     func() time.Time { return time.Date(2026, 6, 21, 8, 30, 0, 0, time.UTC) },
	}
	path := cvExec.settlementsLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, path, CrossVenueSettlement{
		SettlementID:       "cv-AMAT",
		PositionKey:        "AMAT-USDT|cv-AMAT",
		OpportunityID:      "perp_perp:AMAT-USDT:bybit+binance:cvsnap-test",
		Symbol:             "AMAT-USDT",
		HighVenue:          bybit.VenueName,
		LowVenue:           binance.VenueName,
		OpenTime:           time.Date(2026, 6, 21, 7, 57, 48, 0, time.UTC),
		CloseTime:          time.Date(2026, 6, 21, 8, 18, 53, 0, time.UTC),
		NotionalUSDT:       88.75,
		RealizedFunding:    0.584531,
		NetPnL:             -0.160703,
		CloseReason:        "未来净值<=0(-0.0781)",
		FundingTruthStatus: "partial",
		HighLegFunding:     0.584531,
		EstimatedFunding:   0.585981,
		OpenFees:           0.024308,
		CloseFees:          0.024525,
		TotalFees:          0.048833,
	})
	s := &DashboardServer{crossVenueExecutor: cvExec}
	req := httptest.NewRequest(http.MethodGet, "/api/rotation/settlements?limit=10", nil)
	rec := httptest.NewRecorder()
	s.handleRotationSettlementsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp RotationSettlementsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Settlements) != 1 {
		t.Fatalf("count=%d settlements=%d body=%s", resp.Count, len(resp.Settlements), rec.Body.String())
	}
	got := resp.Settlements[0]
	if got.StrategyType != UnifiedStrategyPerpPerp || got.Symbol != "AMAT-USDT" {
		t.Fatalf("settlement=%+v", got)
	}
	if got.PositionKey != "AMAT-USDT|cv-AMAT" || got.NetPnL != -0.160703 {
		t.Fatalf("unexpected settlement fields: %+v", got)
	}
}

func TestRotationSettlementsAPILimitsAfterGlobalCloseTimeSort(t *testing.T) {
	dir := t.TempDir()
	oldTime := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 6, 21, 8, 18, 53, 0, time.UTC)

	cvspPath := filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile)
	if err := os.MkdirAll(filepath.Dir(cvspPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, cvspPath, CrossVenueSpotPerpSettlement{
		SettlementID: "old-cvsp", Symbol: "BEL-USDT", SpotVenue: binance.VenueName, PerpVenue: "gate",
		OpenTime: oldTime.Add(-time.Minute), CloseTime: oldTime, NetPnL: -0.01,
	})

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cvExec := &CrossVenueExecutor{
		cfg:       cfg,
		store:     mustCrossVenueStore(t, dir),
		recordDir: dir,
		nowFn:     func() time.Time { return newTime.Add(time.Minute) },
	}
	perpPath := cvExec.settlementsLedgerPath()
	if err := os.MkdirAll(filepath.Dir(perpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSettlementLine(t, perpPath, CrossVenueSettlement{
		SettlementID: "new-perp", PositionKey: "AMAT-USDT|new-perp",
		Symbol: "AMAT-USDT", HighVenue: bybit.VenueName, LowVenue: binance.VenueName,
		OpenTime: newTime.Add(-20 * time.Minute), CloseTime: newTime, NetPnL: -0.160703,
		FundingTruthStatus: "partial",
	})

	s := &DashboardServer{store: mustTestReportStore(t, dir), crossVenueExecutor: cvExec}
	req := httptest.NewRequest(http.MethodGet, "/api/rotation/settlements?limit=1", nil)
	rec := httptest.NewRecorder()
	s.handleRotationSettlementsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp RotationSettlementsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Settlements) != 1 {
		t.Fatalf("count=%d settlements=%d body=%s", resp.Count, len(resp.Settlements), rec.Body.String())
	}
	if got := resp.Settlements[0].Symbol; got != "AMAT-USDT" {
		t.Fatalf("latest settlement symbol=%s, want AMAT-USDT; body=%s", got, rec.Body.String())
	}
}
