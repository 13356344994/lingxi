package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUnifiedCandidatesJSONL(t *testing.T, path string, records []UnifiedCandidateLogRecord) {
	t.Helper()
	var b strings.Builder
	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReadUnifiedCandidatesJSONLSkipsBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unified_candidates.jsonl")
	content := `{"cycle_id":"c1","strategy_type":"perp_perp","symbol":"BTC-USDT","venues":["bybit","okx"],"expected_net_ev_usdt":0.1,"capital_efficiency_score":0.001,"would_execute":true}
{broken json line}
{"cycle_id":"c1","strategy_type":"perp_perp","symbol":"ETH-USDT","venues":["bybit","okx"],"expected_net_ev_usdt":0.2,"capital_efficiency_score":0.002,"would_execute":false,"reject_reasons":["net_ev<=0"]}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	records, err := readUnifiedCandidatesJSONL(path)
	if err != nil {
		t.Fatalf("readUnifiedCandidatesJSONL() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
}

func TestReadUnifiedCandidatesJSONLMissingFile(t *testing.T) {
	records, err := readUnifiedCandidatesJSONL(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("readUnifiedCandidatesJSONL() error = %v", err)
	}
	if records != nil {
		t.Fatalf("records = %#v, want nil", records)
	}
}

func TestBuildUnifiedLeaderboardTieredRanking(t *testing.T) {
	cycle := "cvsnap-tier-test"
	records := []UnifiedCandidateLogRecord{
		// Tier 3 degenerate — would sort above negative EV without tiering.
		{CycleID: cycle, StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "ZZZ-USDT",
			RejectReasons: []string{"no_spot_market"}},
		{CycleID: cycle, StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "AAA-USDT",
			RejectReasons: []string{"not_borrowable"}},
		// Tier 2 evaluated near-miss with negative EV.
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "NEAR-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: -0.15, CapitalEfficiencyScore: -0.002, WouldExecute: false,
			RejectReasons: []string{"EV<0"}, CapitalRequiredByVenue: map[string]float64{"bybit": 45, "okx": 45}},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "GOOD-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.08, CapitalEfficiencyScore: 0.004, WouldExecute: false,
			RejectReasons: []string{"execution<0.50"}, CapitalRequiredByVenue: map[string]float64{"bybit": 20, "okx": 20}, NotionalUSDT: 50},
		// Tier 1 executable.
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "EXEC-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.05, CapitalEfficiencyScore: 0.001, WouldExecute: true,
			CapitalRequiredByVenue: map[string]float64{"bybit": 10, "okx": 10}, NotionalUSDT: 30},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if len(resp.Opportunities) != 5 {
		t.Fatalf("opportunities = %d, want 5", len(resp.Opportunities))
	}

	got := make([]string, len(resp.Opportunities))
	for i, o := range resp.Opportunities {
		got[i] = o.Symbol
	}
	want := []string{"EXEC-USDT", "GOOD-USDT", "NEAR-USDT", "AAA-USDT", "ZZZ-USDT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rank order = %v, want %v", got, want)
	}
	if !resp.Opportunities[0].WouldExecute {
		t.Fatal("top row should be executable")
	}
	if resp.Opportunities[2].ExpectedNetEVUSDT >= 0 {
		t.Fatalf("tier-2 negative near-miss = %+v", resp.Opportunities[2])
	}
	if resp.Opportunities[3].ExpectedNetEVUSDT != 0 || resp.Opportunities[4].ExpectedNetEVUSDT != 0 {
		t.Fatalf("degenerate rows should have zero EV: %+v, %+v", resp.Opportunities[3], resp.Opportunities[4])
	}
}

func TestUnifiedCandidateTierHelpers(t *testing.T) {
	if unifiedCandidateTier(UnifiedCandidateLogRecord{WouldExecute: true}) != 1 {
		t.Fatal("executable should be tier 1")
	}
	eval := UnifiedCandidateLogRecord{
		ExpectedNetEVUSDT:      -0.1,
		CapitalRequiredByVenue: map[string]float64{"bybit": 10},
	}
	if unifiedCandidateTier(eval) != 2 {
		t.Fatal("evaluated near-miss should be tier 2")
	}
	degen := UnifiedCandidateLogRecord{RejectReasons: []string{"no_spot_market"}}
	if unifiedCandidateTier(degen) != 3 {
		t.Fatal("degenerate reject should be tier 3")
	}
	if unifiedCandidateEvaluated(degen) {
		t.Fatal("degenerate should not be evaluated")
	}
	if !unifiedCandidateEvaluated(UnifiedCandidateLogRecord{NotionalUSDT: 1}) {
		t.Fatal("notional>0 should count as evaluated")
	}
}

func TestBuildUnifiedLeaderboardSortsByPrimaryKey(t *testing.T) {
	cycle := "cvcycle-test"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "AAA-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.05, CapitalEfficiencyScore: 0.001, WouldExecute: true,
			CapitalRequiredByVenue: map[string]float64{"bybit": 10, "okx": 10}},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "BBB-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.20, CapitalEfficiencyScore: 0.005, WouldExecute: false,
			RejectReasons: []string{"execution<0.50"}, CapitalRequiredByVenue: map[string]float64{"bybit": 20, "okx": 20}},
		{CycleID: "cvcycle-old", StrategyType: UnifiedStrategyPerpPerp, Symbol: "OLD-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 9.99, CapitalEfficiencyScore: 9.99, WouldExecute: true},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if resp.CycleID != cycle {
		t.Fatalf("cycle_id = %q, want %q", resp.CycleID, cycle)
	}
	if resp.TotalCandidates != 2 {
		t.Fatalf("total_candidates = %d, want 2", resp.TotalCandidates)
	}
	if len(resp.Opportunities) != 2 {
		t.Fatalf("opportunities = %d, want 2", len(resp.Opportunities))
	}
	// 分层排序:可执行(would_execute)候选优先于"已评估近失"候选,即使后者主排序键更高。
	// AAA 可执行(tier1)→ 排名第 1;BBB 仅近失(tier2)→ 排名第 2。
	if resp.Opportunities[0].Symbol != "AAA-USDT" || resp.Opportunities[0].Rank != 1 {
		t.Fatalf("top opportunity = %+v", resp.Opportunities[0])
	}
	if !resp.Opportunities[0].WouldExecute {
		t.Fatalf("top opportunity should be executable: %+v", resp.Opportunities[0])
	}
	if resp.Opportunities[0].PrimarySortKey != 0.001 {
		t.Fatalf("primary_sort_key = %.6f", resp.Opportunities[0].PrimarySortKey)
	}
	if resp.Opportunities[1].Symbol != "BBB-USDT" {
		t.Fatalf("second opportunity = %+v", resp.Opportunities[1])
	}
	if resp.Opportunities[0].RankReason == "" {
		t.Fatal("expected rank_reason")
	}
}

func TestBuildUnifiedLeaderboardRejectAndCapitalStats(t *testing.T) {
	cycle := "cvcycle-stats"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, Symbol: "BTC-USDT", Venues: []string{"bybit", "okx"}, WouldExecute: false,
			RejectReasons: []string{"高费所永续可用 USDT 5.00 < 所需 30.00", "execution<0.50"}},
		{CycleID: cycle, Symbol: "ETH-USDT", Venues: []string{"bybit", "okx"}, WouldExecute: false,
			RejectReasons: []string{"低费所永续可用 USDT 1.00 < 所需 20.00"}},
		{CycleID: cycle, Symbol: "SOL-USDT", Venues: []string{"bybit", "okx"}, WouldExecute: false,
			RejectReasons: []string{"execution<0.50"}},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if len(resp.RejectReasonsTop) == 0 {
		t.Fatal("expected reject_reasons_top")
	}
	if resp.RejectReasonsTop[0].Reason != "execution<0.50" || resp.RejectReasonsTop[0].Count != 2 {
		t.Fatalf("reject_reasons_top[0] = %+v", resp.RejectReasonsTop[0])
	}
	if len(resp.VenueCapitalInsufficient) != 2 {
		t.Fatalf("venue_capital_insufficient_top = %+v", resp.VenueCapitalInsufficient)
	}
}

func TestLoadUnifiedLeaderboardIntegration(t *testing.T) {
	dir := t.TempDir()
	path := UnifiedCandidatesLogPath(dir)
	writeUnifiedCandidatesJSONL(t, path, []UnifiedCandidateLogRecord{
		{CycleID: "cvcycle-a", Symbol: "BTC-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.1, CapitalEfficiencyScore: 0.002, WouldExecute: true},
	})
	resp, err := LoadUnifiedLeaderboard(path, 10)
	if err != nil {
		t.Fatalf("LoadUnifiedLeaderboard() error = %v", err)
	}
	if resp.SourcePath != path || len(resp.Opportunities) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDashboardUnifiedLeaderboardAPI(t *testing.T) {
	dir := t.TempDir()
	path := UnifiedCandidatesLogPath(dir)
	writeUnifiedCandidatesJSONL(t, path, []UnifiedCandidateLogRecord{
		{CycleID: "cvcycle-api", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT", Venues: []string{"bybit", "okx"},
			ExpectedNetEVUSDT: 0.12, CapitalEfficiencyScore: 0.0016, WouldExecute: true,
			CapitalRequiredByVenue: map[string]float64{"bybit": 15, "okx": 15}},
	})
	exec := &CrossVenueExecutor{recordDir: dir}
	s := &DashboardServer{crossVenueExecutor: exec}

	req := httptest.NewRequest(http.MethodGet, "/api/unified/leaderboard", nil)
	rec := httptest.NewRecorder()
	s.handleUnifiedLeaderboardAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp UnifiedLeaderboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CycleID != "cvcycle-api" || len(resp.Opportunities) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestUnifiedPrimarySortKeyFallback(t *testing.T) {
	rec := UnifiedCandidateLogRecord{
		ExpectedNetEVUSDT:      0.10,
		CapitalEfficiencyScore: 0,
		CapitalRequiredByVenue: map[string]float64{"bybit": 25, "okx": 25},
	}
	got := unifiedPrimarySortKey(rec)
	want := 0.10 / 50.0
	if got != want {
		t.Fatalf("primary sort key = %.6f, want %.6f", got, want)
	}
}
