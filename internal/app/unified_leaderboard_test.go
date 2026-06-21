package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestBuildUnifiedLeaderboardHidesStructuralRejectsByDefault(t *testing.T) {
	cycle := "cvcycle-landscape"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategyCrossVenuePerpSpot, Symbol: "LOW-USDT", Venues: []string{"bybit", "binance"},
			RejectReasons: []string{"not_borrowable"}, FundingAPR: 0.12, RawFundingRate: 0.0003, FundingIntervalHours: 8},
		{CycleID: cycle, StrategyType: UnifiedStrategyCrossVenuePerpSpot, Symbol: "HIGH-USDT", Venues: []string{"bybit", "binance"},
			RejectReasons: []string{"no_spot_market"}, FundingAPR: 0.85, RawFundingRate: 0.002, FundingIntervalHours: 8},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "MID-USDT", Venues: []string{"bybit", "okx"},
			WouldExecute: true, SpreadAPR: 0.40, ExpectedNetEVUSDT: 0.01},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if len(resp.Opportunities) != 1 {
		t.Fatalf("opportunities = %d, want only executable/displayable rows: %+v", len(resp.Opportunities), resp.Opportunities)
	}
	if resp.Opportunities[0].Symbol != "MID-USDT" || !resp.Opportunities[0].WouldExecute {
		t.Fatalf("executable should pin top: %+v", resp.Opportunities[0])
	}
	if resp.TotalCandidates != 3 || resp.DisplayedCandidates != 1 {
		t.Fatalf("counts total=%d displayed=%d", resp.TotalCandidates, resp.DisplayedCandidates)
	}
	if len(resp.RejectReasonsTop) == 0 {
		t.Fatal("structural rejects should still contribute to reject statistics")
	}
}

func TestDedupeUnifiedCandidatesLatest(t *testing.T) {
	records := []UnifiedCandidateLogRecord{
		{CycleID: "c1", StrategyType: UnifiedStrategyCrossVenueSpotPerp, Symbol: "BTC-USDT", FundingAPR: 0.1},
		{CycleID: "c1", StrategyType: UnifiedStrategyCrossVenueSpotPerp, Symbol: "BTC-USDT", FundingAPR: 0.5},
		{CycleID: "c1", StrategyType: UnifiedStrategyCrossVenueSpotPerp, Symbol: "ETH-USDT", FundingAPR: 0.2},
	}
	got := dedupeUnifiedCandidatesLatest(records)
	if len(got) != 2 {
		t.Fatalf("deduped = %d, want 2", len(got))
	}
	if got[0].FundingAPR != 0.5 {
		t.Fatalf("BTC should keep latest row: %.2f", got[0].FundingAPR)
	}
}

func TestDedupeUnifiedCandidatesDifferentVenuesRetained(t *testing.T) {
	records := []UnifiedCandidateLogRecord{
		{CycleID: "c1", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT", Venues: []string{"bybit", "okx"},
			SpreadAPR: 0.30, WouldExecute: true},
		{CycleID: "c1", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT", Venues: []string{"bybit", "binance"},
			SpreadAPR: 0.55, WouldExecute: false, RejectReasons: []string{"execution<0.50"}},
	}
	got := dedupeUnifiedCandidatesLatest(records)
	if len(got) != 2 {
		t.Fatalf("deduped = %d, want 2 (different venue pairs)", len(got))
	}
	venues := map[string]bool{}
	for _, rec := range got {
		key := strings.Join(rec.Venues, "+")
		venues[key] = true
	}
	if !venues["bybit+okx"] || !venues["bybit+binance"] {
		t.Fatalf("expected both venue pairs retained: %+v", got)
	}

	resp := BuildUnifiedLeaderboard(records, 0)
	if resp.TotalCandidates != 2 {
		t.Fatalf("total_candidates = %d, want 2", resp.TotalCandidates)
	}
}

func TestLatestUnifiedCycleIDParsesEmbeddedTime(t *testing.T) {
	olderSnap := "cvsnap-20260621T100000.000000000"
	newerCycle := "cvcycle-20260621T110000.000000000"
	records := []UnifiedCandidateLogRecord{
		{CycleID: olderSnap, Symbol: "OLD-USDT"},
		{CycleID: newerCycle, Symbol: "NEW-USDT"},
	}
	got := latestUnifiedCycleID(records)
	if got != newerCycle {
		t.Fatalf("latest cycle = %q, want %q (cvcycle newer than cvsnap)", got, newerCycle)
	}

	resp := BuildUnifiedLeaderboard(records, 0)
	if resp.CycleID != newerCycle {
		t.Fatalf("BuildUnifiedLeaderboard cycle_id = %q, want %q", resp.CycleID, newerCycle)
	}
	if resp.CycleScannedAt == "" || resp.CycleAgeSeconds < 0 {
		t.Fatalf("expected cycle age metadata: scanned_at=%q age=%.0f", resp.CycleScannedAt, resp.CycleAgeSeconds)
	}
}

func TestParseUnifiedCycleIDTime(t *testing.T) {
	want := time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC)
	got, ok := parseUnifiedCycleIDTime("cvcycle-20260621T110000.000000000")
	if !ok {
		t.Fatal("expected parse ok")
	}
	if !got.Equal(want) {
		t.Fatalf("parsed = %v, want %v", got, want)
	}
	if _, ok := parseUnifiedCycleIDTime("cvcycle-bad"); ok {
		t.Fatal("expected bad cycle id to fail parse")
	}
}

func TestUnifiedScannerWouldExecuteLegacyFallback(t *testing.T) {
	rec := UnifiedCandidateLogRecord{WouldExecute: true}
	if !unifiedScannerWouldExecute(rec) {
		t.Fatal("legacy would_execute should count as scanner pass")
	}
	rec2 := UnifiedCandidateLogRecord{ScannerWouldExecute: true}
	if !unifiedScannerWouldExecute(rec2) {
		t.Fatal("explicit scanner_would_execute should pass")
	}
}

func TestUnifiedLandscapeSortKey(t *testing.T) {
	if unifiedLandscapeSortKey(UnifiedCandidateLogRecord{SpreadAPR: 0.25}) != 0.25 {
		t.Fatal("spread APR should win")
	}
	if unifiedLandscapeSortKey(UnifiedCandidateLogRecord{FundingAPR: 0.18}) != 0.18 {
		t.Fatal("funding APR fallback")
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
	if len(resp.Opportunities) != 3 {
		t.Fatalf("opportunities = %d, want displayable rows only: %+v", len(resp.Opportunities), resp.Opportunities)
	}

	got := make([]string, len(resp.Opportunities))
	for i, o := range resp.Opportunities {
		got[i] = o.Symbol
	}
	want := []string{"GOOD-USDT", "EXEC-USDT", "NEAR-USDT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rank order = %v, want %v", got, want)
	}
	if resp.Opportunities[0].Symbol != "GOOD-USDT" {
		t.Fatal("top row should rank by capital-aware net EV among rotation-pass rows")
	}
	nearIdx := -1
	for i, o := range resp.Opportunities {
		if o.Symbol == "NEAR-USDT" {
			nearIdx = i
			if o.ExpectedNetEVUSDT >= 0 {
				t.Fatalf("NEAR near-miss should have negative EV: %+v", o)
			}
		}
	}
	if nearIdx < 0 {
		t.Fatal("expected NEAR-USDT in ranking")
	}
}

func TestUnifiedCandidateTierHelpers(t *testing.T) {
	t.Skip("tier helpers retained for diagnostics; leaderboard now sorts by funding landscape")
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
	// 轮动闸可开优先,再按 capital-aware(此处回退扫描)净 EV 降序:BBB(0.20) 在 AAA(0.05) 前。
	if resp.Opportunities[0].Symbol != "BBB-USDT" || resp.Opportunities[0].Rank != 1 {
		t.Fatalf("top opportunity = %+v", resp.Opportunities[0])
	}
	if resp.Opportunities[1].Symbol != "AAA-USDT" {
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

func TestBuildUnifiedLeaderboardPerStrategyGrouping(t *testing.T) {
	cycle := "cvsnap-per-strategy"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "AAA-USDT", FundingAPR: 0.20},
		{CycleID: cycle, StrategyType: UnifiedStrategySingleVenueSpotPerp, Symbol: "BBB-USDT", FundingAPR: 0.80},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "HHH-USDT", Venues: []string{"binance", "bybit"},
			SpreadAPR: 13.69, WouldExecute: true, ExpectedNetEVUSDT: 0.5},
	}
	resp := BuildUnifiedLeaderboard(records, 0)

	// 总榜:全策略统计 3 条,默认只展示可评估/可执行候选。
	if resp.TotalCandidates != 3 || len(resp.Opportunities) != 1 {
		t.Fatalf("total candidates=%d opportunities=%d, want 3/1", resp.TotalCandidates, len(resp.Opportunities))
	}

	// 分策略:固定 5 个分组、固定顺序(即使无候选也保留)。
	if len(resp.Strategies) != 5 {
		t.Fatalf("strategies = %d, want 5", len(resp.Strategies))
	}
	wantOrder := []string{
		UnifiedStrategySingleVenueSpotPerp,
		UnifiedStrategyCrossVenueSpotPerp,
		UnifiedStrategySingleVenuePerpSpot,
		UnifiedStrategyCrossVenuePerpSpot,
		UnifiedStrategyPerpPerp,
	}
	for i, st := range wantOrder {
		if resp.Strategies[i].StrategyType != st {
			t.Fatalf("strategies[%d] = %q, want %q", i, resp.Strategies[i].StrategyType, st)
		}
	}

	// svsp 组:2 条纯费率景观行,保留 total 但默认不展示。
	svsp := resp.Strategies[0]
	if svsp.TotalCandidates != 2 || svsp.DisplayedCandidates != 0 || len(svsp.Opportunities) != 0 {
		t.Fatalf("svsp group = %+v", svsp)
	}

	// perp_perp 组:1 条可执行,组内 would_execute 计数正确。
	pp := resp.Strategies[4]
	if pp.TotalCandidates != 1 || pp.WouldExecuteCount != 1 {
		t.Fatalf("perp_perp group = %+v", pp)
	}

	// 空策略组(cvsp)仍保留且为空。
	cvsp := resp.Strategies[1]
	if cvsp.TotalCandidates != 0 || len(cvsp.Opportunities) != 0 {
		t.Fatalf("empty cvsp group should be present and empty: %+v", cvsp)
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

func TestUnifiedNearestFundingMinutes(t *testing.T) {
	now := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Minute).Format(time.RFC3339)
	later := now.Add(4 * time.Hour).Format(time.RFC3339)
	same := now.Add(2 * time.Hour).Format(time.RFC3339)

	rec := UnifiedCandidateLogRecord{
		HighNextFundingTime: later,
		LowNextFundingTime:  soon,
	}
	got := unifiedNearestFundingMinutes(rec, now)
	if got < 29 || got > 31 {
		t.Fatalf("nearest mins = %.1f, want ~30 (low leg)", got)
	}

	recSame := UnifiedCandidateLogRecord{
		HighNextFundingTime: same,
		LowNextFundingTime:  same,
	}
	gotSame := unifiedNearestFundingMinutes(recSame, now)
	if gotSame < 119 || gotSame > 121 {
		t.Fatalf("same-leg mins = %.1f, want ~120", gotSame)
	}

	recFallback := UnifiedCandidateLogRecord{
		NextFundingTime: soon,
	}
	gotFallback := unifiedNearestFundingMinutes(recFallback, now)
	if gotFallback < 29 || gotFallback > 31 {
		t.Fatalf("fallback mins = %.1f, want ~30", gotFallback)
	}
}

func TestLeaderboardEntryNearestFundingAndRotation(t *testing.T) {
	now := time.Date(2026, 6, 20, 14, 0, 0, 0, time.UTC)
	soon := now.Add(45 * time.Minute).Format(time.RFC3339)
	later := now.Add(3 * time.Hour).Format(time.RFC3339)
	rec := UnifiedCandidateLogRecord{
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC-USDT",
		Venues:                 []string{"okx", "bybit"},
		HighNextFundingTime:    later,
		LowNextFundingTime:     soon,
		WouldExecute:           true,
		RotationWouldExecute:   false,
		HighFundingRate:        0.0001,
		LowFundingRate:         -0.00005,
		CapitalEfficiencyScore: 0.003,
		CapitalRequiredByVenue: map[string]float64{"okx": 30, "bybit": 25},
	}
	entry := leaderboardEntryFromRecord(rec, 1, 0.5, now)
	if entry.MinutesUntilFunding < 44 || entry.MinutesUntilFunding > 46 {
		t.Fatalf("minutes_until_funding = %.1f, want ~45", entry.MinutesUntilFunding)
	}
	if !entry.WouldExecute || entry.RotationWouldExecute {
		t.Fatalf("execute flags = would=%v rotation=%v", entry.WouldExecute, entry.RotationWouldExecute)
	}
	if entry.HighFundingRate != 0.0001 || entry.LowFundingRate != -0.00005 {
		t.Fatalf("leg rates = high %.6f low %.6f", entry.HighFundingRate, entry.LowFundingRate)
	}
	if !strings.Contains(entry.RankReason, "距结算 45 min") {
		t.Fatalf("rank_reason should use nearest leg: %q", entry.RankReason)
	}
}

func TestUnifiedNearestFundingMinutesExpiredHighLegRolls(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 36, 0, 0, time.UTC)
	expiredHigh := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	futureLow := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	rec := UnifiedCandidateLogRecord{
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "H-USDT",
		Venues:                 []string{"bybit", "binance"},
		FundingIntervalHours:   1,
		HighNextFundingTime:    expiredHigh,
		LowNextFundingTime:     futureLow,
		CapitalEfficiencyScore: 0.002,
	}
	got := unifiedNearestFundingMinutes(rec, now)
	if got < 23 || got > 25 {
		t.Fatalf("nearest mins = %.1f, want ~24 (rolled 1h high leg), not ~85 from low leg", got)
	}
	entry := leaderboardEntryFromRecord(rec, 1, 0.5, now)
	if entry.MinutesUntilFunding < 23 || entry.MinutesUntilFunding > 25 {
		t.Fatalf("entry minutes_until_funding = %.1f, want ~24", entry.MinutesUntilFunding)
	}
}

func TestBuildUnifiedLeaderboardSortsByCapitalAwareNetEVNotAPR(t *testing.T) {
	cycle := "cvcycle-cap-ev"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "HIGH-APR-USDT", Venues: []string{"bybit", "okx"},
			SpreadAPR: 5.0, ExpectedNetEVUSDT: 0.50, NotionalUSDT: 90,
			CapitalAwareNetEVUSDT: -0.08, CapitalAwareNotionalUSDT: 22, RotationWouldExecute: false},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "LOW-APR-USDT", Venues: []string{"bybit", "binance"},
			SpreadAPR: 0.3, ExpectedNetEVUSDT: 0.12, NotionalUSDT: 80,
			CapitalAwareNetEVUSDT: 0.04, CapitalAwareNotionalUSDT: 20, RotationWouldExecute: true},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if len(resp.Opportunities) != 2 {
		t.Fatalf("opportunities = %d", len(resp.Opportunities))
	}
	if resp.Opportunities[0].Symbol != "LOW-APR-USDT" {
		t.Fatalf("rank1 = %s, want LOW-APR-USDT (rotation pass + positive cap EV)", resp.Opportunities[0].Symbol)
	}
	if resp.Opportunities[1].Symbol != "HIGH-APR-USDT" {
		t.Fatalf("rank2 = %s, want HIGH-APR-USDT", resp.Opportunities[1].Symbol)
	}
}

func TestBuildUnifiedLeaderboardPositiveScannerNegativeCapitalAwareRanksLower(t *testing.T) {
	cycle := "cvcycle-scanner-vs-cap"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "TRAP-USDT", Venues: []string{"bybit", "okx"},
			WouldExecute: true, SpreadAPR: 13.0, ExpectedNetEVUSDT: 0.35, NotionalUSDT: 95,
			CapitalAwareNetEVUSDT: -0.02, CapitalAwareNotionalUSDT: 22, RotationWouldExecute: false},
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "REAL-USDT", Venues: []string{"bybit", "binance"},
			WouldExecute: true, SpreadAPR: 0.8, ExpectedNetEVUSDT: 0.10, NotionalUSDT: 88,
			CapitalAwareNetEVUSDT: 0.06, CapitalAwareNotionalUSDT: 24, RotationWouldExecute: true},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if resp.Opportunities[0].Symbol != "REAL-USDT" || !resp.Opportunities[0].RotationWouldExecute {
		t.Fatalf("top = %+v, want REAL-USDT rotation pass", resp.Opportunities[0])
	}
	trap := resp.Opportunities[1]
	if trap.Symbol != "TRAP-USDT" || trap.RotationWouldExecute {
		t.Fatalf("trap row = %+v, want rotation_would_execute=false", trap)
	}
	if trap.ScannerEstimateOnly {
		t.Fatalf("trap should have capital-aware fields populated: %+v", trap)
	}
	if trap.ExpectedNetEVUSDT <= 0 || trap.CapitalAwareNetEVUSDT >= 0 {
		t.Fatalf("trap scanner EV should be positive while cap EV negative: %+v", trap)
	}
}

func TestBuildUnifiedLeaderboardOldRecordFallbackGraceful(t *testing.T) {
	cycle := "cvcycle-legacy"
	records := []UnifiedCandidateLogRecord{
		{CycleID: cycle, StrategyType: UnifiedStrategyPerpPerp, Symbol: "LEGACY-USDT", Venues: []string{"bybit", "okx"},
			WouldExecute: true, SpreadAPR: 2.0, ExpectedNetEVUSDT: 0.15, NotionalUSDT: 75},
	}
	resp := BuildUnifiedLeaderboard(records, 0)
	if len(resp.Opportunities) != 1 {
		t.Fatalf("opportunities = %d", len(resp.Opportunities))
	}
	entry := resp.Opportunities[0]
	if !entry.ScannerEstimateOnly {
		t.Fatalf("legacy row should be scanner_estimate_only: %+v", entry)
	}
	if entry.CapitalAwareNotionalUSDT != 0 || entry.CapitalAwareNetEVUSDT != 0 {
		t.Fatalf("legacy row should not fabricate capital-aware fields: %+v", entry)
	}
	if entry.ExpectedNetEVUSDT != 0.15 || entry.NotionalUSDT != 75 {
		t.Fatalf("legacy scanner fields = ev %.4f notional %.2f", entry.ExpectedNetEVUSDT, entry.NotionalUSDT)
	}
}
