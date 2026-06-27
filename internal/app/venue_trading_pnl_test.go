package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyVenueTradingPnLFromFundFlow_PerVenueReal(t *testing.T) {
	dir := t.TempDir()
	st := &accountRiskEquityState{
		DayStartCST:             time.Now().UTC(),
		DailyFundFlowSource:     "real",
		DailyFundFlowByVenueUSDT: map[string]float64{"okx": -10, "gate": 5},
	}
	if err := saveAccountRiskEquityState(dir, st, time.Now().UTC()); err != nil {
		t.Fatalf("save state: %v", err)
	}

	byVenue := map[string]VenueFinancialSnapshot{
		"okx":  {TodayEarningUSDT: 20},
		"gate": {TodayEarningUSDT: -3},
	}
	total := VenuesFinancialTotal{TodayEarningUSDT: 17}
	applyVenueTradingPnLFromFundFlow(byVenue, &total, dir)

	if !byVenue["okx"].TodayFundFlowKnown || byVenue["okx"].TodayFundFlowUSDT != -10 {
		t.Fatalf("okx fund flow=%+v", byVenue["okx"])
	}
	if byVenue["okx"].TodayTradingPnLUSDT != 30 {
		t.Fatalf("okx trading pnl=%v want 30", byVenue["okx"].TodayTradingPnLUSDT)
	}
	if byVenue["gate"].TodayTradingPnLUSDT != -8 {
		t.Fatalf("gate trading pnl=%v want -8", byVenue["gate"].TodayTradingPnLUSDT)
	}
	if !total.TodayFundFlowKnown || total.TodayFundFlowUSDT != -5 {
		t.Fatalf("total fund flow=%+v", total)
	}
	if total.TodayTradingPnLUSDT != 22 {
		t.Fatalf("total trading pnl=%v want 22", total.TodayTradingPnLUSDT)
	}
}

func TestApplyVenueTradingPnLFromFundFlow_HeuristicFallback(t *testing.T) {
	dir := t.TempDir()
	st := &accountRiskEquityState{
		DayStartCST:             time.Now().UTC(),
		DailyFundFlowSource:     "heuristic",
		DailyFundFlowAdjustUSDT: -49.45,
	}
	if err := saveAccountRiskEquityState(dir, st, time.Now().UTC()); err != nil {
		t.Fatalf("save state: %v", err)
	}

	byVenue := map[string]VenueFinancialSnapshot{"okx": {TodayEarningUSDT: -49.45}}
	total := VenuesFinancialTotal{TodayEarningUSDT: -49.45}
	applyVenueTradingPnLFromFundFlow(byVenue, &total, dir)

	if byVenue["okx"].TodayFundFlowKnown {
		t.Fatal("heuristic path must not mark per-venue fund flow known")
	}
	if byVenue["okx"].TodayTradingPnLUSDT != -49.45 {
		t.Fatalf("unknown fund flow should fall back to earning, got %v", byVenue["okx"].TodayTradingPnLUSDT)
	}
	if total.TodayFundFlowKnown || total.TodayTradingPnLUSDT != -49.45 {
		t.Fatalf("total=%+v", total)
	}
}

func TestApplyVenueTradingPnLFromFundFlow_AccountLevelFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, accountRiskEquityStateFileName)
	payload := []byte(`{"schema_version":1,"daily_fund_flow_source":"real","daily_fund_flow_adjust_usdt":-20}`)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	byVenue := map[string]VenueFinancialSnapshot{"okx": {TodayEarningUSDT: 5}}
	total := VenuesFinancialTotal{TodayEarningUSDT: 5}
	applyVenueTradingPnLFromFundFlow(byVenue, &total, dir)

	if byVenue["okx"].TodayFundFlowKnown {
		t.Fatal("missing per-venue map should leave venue unknown")
	}
	if !total.TodayFundFlowKnown || total.TodayFundFlowUSDT != -20 {
		t.Fatalf("total fund flow=%+v", total)
	}
	if total.TodayTradingPnLUSDT != 25 {
		t.Fatalf("total trading pnl=%v want 25", total.TodayTradingPnLUSDT)
	}
}
