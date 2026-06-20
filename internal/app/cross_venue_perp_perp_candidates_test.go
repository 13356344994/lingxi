package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func TestAppendCrossVenuePerpPerpCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cvOpp := CrossVenueOpportunity{
		Symbol:              "H-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		SpreadAPR:           13.58,
		HighFundingRate:     0.00625,
		LowFundingRate:      0.0000125,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding.Add(-time.Hour),
		TradableNotional:    150,
		NetEV:               1.5,
		SpreadEV:            2.0,
	}
	opps := []UnifiedOpportunity{
		rotationAlignPerpPerpCapital(UnifiedOpportunityFromCrossVenue(execCfg, cvOpp, nil, 0, 0), execCfg),
	}

	AppendCrossVenuePerpPerpCandidates(dir, "pp-cycle-test", execCfg, opps)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var rec UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.StrategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("strategy_type = %q, want %q", rec.StrategyType, UnifiedStrategyPerpPerp)
	}
	if rec.CycleID != "pp-cycle-test" {
		t.Fatalf("cycle_id = %q", rec.CycleID)
	}
	if rec.Symbol != "H-USDT" {
		t.Fatalf("symbol = %q", rec.Symbol)
	}
	if !rec.WouldExecute {
		t.Fatalf("would_execute = false, want true (no reject reasons)")
	}
}
