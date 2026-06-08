package app

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBenchmarkDailyReturnsAligned(t *testing.T) {
	closes := map[string]float64{
		"2026-06-01": 100,
		"2026-06-02": 110, // +10%
		"2026-06-03": 99,  // -10%
	}
	days := []string{"2026-06-02", "2026-06-03"}
	got := benchmarkDailyReturns(closes, days)
	if len(got) != 2 {
		t.Fatalf("expected 2 returns, got %d (%v)", len(got), got)
	}
	if math.Abs(got[0]-0.10) > 1e-9 {
		t.Fatalf("day0 return = %v, want 0.10", got[0])
	}
	if math.Abs(got[1]-(-0.10)) > 1e-9 {
		t.Fatalf("day1 return = %v, want -0.10", got[1])
	}
}

func TestBenchmarkDailyReturnsMissingPriorDayReturnsNil(t *testing.T) {
	// 缺首日的前一日收盘 → 无法对齐完整基准 → nil(不静默补 0)。
	closes := map[string]float64{
		"2026-06-02": 110,
		"2026-06-03": 99,
	}
	days := []string{"2026-06-02", "2026-06-03"}
	if got := benchmarkDailyReturns(closes, days); got != nil {
		t.Fatalf("expected nil when prior-day close missing, got %v", got)
	}
}

func TestParseDailyCloses(t *testing.T) {
	// openTime(ms) 为 UTC 日 00:00;close 为第 5 列。
	rows := [][]any{
		{float64(1748736000000), "100.0", "120", "90", "105.5", "1000"}, // 2025-06-01
		{float64(1748822400000), "105.5", "130", "100", "118.0", "2000"},
	}
	got := parseDailyCloses(rows)
	if len(got) != 2 {
		t.Fatalf("expected 2 days, got %d (%v)", len(got), got)
	}
	if c, ok := got["2025-06-01"]; !ok || math.Abs(c-105.5) > 1e-9 {
		t.Fatalf("2025-06-01 close = %v ok=%v, want 105.5", c, ok)
	}
}

func TestFetchBTCDailyCloses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "BTCUSDT" || r.URL.Query().Get("interval") != "1d" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		rows := [][]any{
			{float64(1748736000000), "100", "0", "0", "100", "0"},
			{float64(1748822400000), "100", "0", "0", "110", "0"},
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()

	closes, err := fetchBTCDailyCloses(context.Background(), server.Client(), server.URL, "2025-06-02", "2025-06-02")
	if err != nil {
		t.Fatalf("fetchBTCDailyCloses error = %v", err)
	}
	if c := closes["2025-06-02"]; math.Abs(c-110) > 1e-9 {
		t.Fatalf("2025-06-02 close = %v, want 110", c)
	}
}
