package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestAppendUnifiedCandidateLogsWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	cfg := testCrossVenueExecutorConfig()
	cvCfg := cfg.CrossVenueExecutor
	cfg.NetworkConfig.RecordDir = dir
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	opp := makeTestOpenOpportunity(cvCfg, now, "BTC-USDT", bybit.VenueName, okx.VenueName, 0.001, 0.0002, 100, 0.8, 0.7, 0.75, 0.85)
	opp.SnapshotID = "cvsnap-log"
	exec := &CrossVenueExecutor{recordDir: dir}

	traces := []crossVenueUnifiedCandidateTrace{
		{
			Opp:           opp,
			RejectReasons: []string{"EV<0: breakevenHours=30h > max 24h"},
			WouldExecute:  false,
			NotionalUSDT:  100,
		},
		{
			Opp:             opp,
			RejectReasons:   nil,
			WouldExecute:    true,
			NotionalUSDT:    80,
			HasGateNotional: true,
			Gate: crossVenueGateResult{
				OK:           true,
				NotionalUSDT: 80,
				HighMid:      100000,
				LowMid:       100000,
			},
		},
	}
	exec.appendUnifiedCandidateLogs("cvcycle-test", cvCfg, traces)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}

	var rejected UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &rejected); err != nil {
		t.Fatalf("unmarshal rejected: %v", err)
	}
	if rejected.CycleID != "cvcycle-test" || rejected.StrategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("rejected record header = %+v", rejected)
	}
	if rejected.Symbol != "BTC-USDT" || len(rejected.Venues) != 2 {
		t.Fatalf("rejected symbol/venues = %s %+v", rejected.Symbol, rejected.Venues)
	}
	if rejected.WouldExecute {
		t.Fatal("expected rejected candidate would_execute=false")
	}
	if len(rejected.RejectReasons) != 1 {
		t.Fatalf("reject_reasons = %#v", rejected.RejectReasons)
	}
	if rejected.NotionalUSDT != 100 {
		t.Fatalf("notional_usdt = %.2f", rejected.NotionalUSDT)
	}

	var executed UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[1]), &executed); err != nil {
		t.Fatalf("unmarshal executed: %v", err)
	}
	if !executed.WouldExecute || executed.NotionalUSDT != 80 {
		t.Fatalf("executed record = would_execute=%v notional=%.2f", executed.WouldExecute, executed.NotionalUSDT)
	}
	if executed.CapitalRequiredByVenue[bybit.VenueName] <= 0 || executed.CapitalRequiredByVenue[okx.VenueName] <= 0 {
		t.Fatalf("capital_required_by_venue = %+v", executed.CapitalRequiredByVenue)
	}
}

func TestAppendUnifiedCandidateLogsSwallowsWriteErrors(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cvCfg := cfg.CrossVenueExecutor
	now := time.Now().UTC()
	opp := makeTestOpenOpportunity(cvCfg, now, "ETH-USDT", bybit.VenueName, okx.VenueName, 0.001, 0.0002, 50, 0.5, 0.5, 0.5, 0.5)
	exec := &CrossVenueExecutor{recordDir: "/proc/self/fd/0"} // not a directory; append should fail silently
	exec.appendUnifiedCandidateLogs("cvcycle-fail", cvCfg, []crossVenueUnifiedCandidateTrace{{Opp: opp}})
}

func TestUnifiedCandidateLogRecordJSONShape(t *testing.T) {
	rec := UnifiedCandidateLogRecord{
		CycleID:                "cvcycle-sample",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "SOL-USDT",
		Venues:                 []string{"bybit", "okx"},
		ExpectedNetEVUSDT:      0.18,
		CapitalRequiredByVenue: map[string]float64{"bybit": 37.5, "okx": 37.5},
		CapitalEfficiencyScore: 0.0024,
		RejectReasons:          []string{"net_ev<=0"},
		WouldExecute:           false,
		NotionalUSDT:           100,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	required := []string{
		"cycle_id", "strategy_type", "symbol", "venues", "expected_net_ev_usdt",
		"capital_required_by_venue", "capital_efficiency_score", "reject_reasons",
		"would_execute", "notional_usdt",
	}
	line := string(data)
	for _, key := range required {
		if !strings.Contains(line, `"`+key+`"`) {
			t.Fatalf("missing json key %q in %s", key, line)
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(line))
	if !scanner.Scan() {
		t.Fatal("expected single json line")
	}
}
