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

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
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
	now := time.Date(2026, 6, 15, 2, 30, 0, 0, time.UTC)
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
		NetPnL: -0.5, RealizedFunding: 0.1, OpenFees: 0.1, CloseFees: 0.1, TotalFees: 0.2,
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
