package app

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	svsp := NewSingleVenueSpotPerpExecutor(execCfg, nil, t.TempDir(), nil)
	_ = svsp.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: "binance", Status: SVSPStatusOpen,
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1, EntryPrice: 100},
		NotionalUSDT: 100, OpenedAt: time.Now().UTC(), IdemPrefix: "test",
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
}
