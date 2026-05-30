package main

import (
	"strconv"
	"testing"
	"time"
)

func TestMonthlyFundingEscalatedPauseTreats202AsThrottleSignal(t *testing.T) {
	got := monthlyFundingEscalatedPause(map[string]int{"http_202": 3})
	if got != monthlyFundingPause202 {
		t.Fatalf("expected 202 pause=%s, got %s", monthlyFundingPause202, got)
	}

	got = monthlyFundingEscalatedPause(map[string]int{"http_429": 1})
	if got != monthlyFundingPause429 {
		t.Fatalf("expected 429 pause=%s, got %s", monthlyFundingPause429, got)
	}

	got = monthlyFundingEscalatedPause(map[string]int{"http_202": 3, "http_403": 1})
	if got != monthlyFundingPause403 {
		t.Fatalf("expected 403 to override 202 pause, got %s", got)
	}
}

func TestShouldAbortMonthlyFundingFetchAfterConsecutive202Batches(t *testing.T) {
	consecutive202 := 0
	consecutive429 := 0
	consecutive403 := 0
	consecutive418 := 0

	if shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 10}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect first 202-heavy batch to abort immediately")
	}
	if consecutive202 != 1 {
		t.Fatalf("expected first 202-heavy batch to increment counter, got %d", consecutive202)
	}
	if !shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 12}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("expected second consecutive 202-heavy batch to abort")
	}
}

func TestMonthlyFundingPause202StrongerThanNormalBatchPause(t *testing.T) {
	if monthlyFundingPause202 <= monthlyFundingFetchBatchPause {
		t.Fatalf("expected 202 pause to exceed normal batch pause, got 202=%s normal=%s", monthlyFundingPause202, monthlyFundingFetchBatchPause)
	}
	if monthlyFundingPause202 <= 0*time.Second {
		t.Fatalf("expected positive 202 pause, got %s", monthlyFundingPause202)
	}
}

func TestAdaptiveMonthlyFundingBatchPauseExpandsUnderPressure(t *testing.T) {
	got := adaptiveMonthlyFundingBatchPause(map[string]int{"http_202": 10}, 20)
	if got <= monthlyFundingPause202 {
		t.Fatalf("expected 202-heavy batch pause to exceed base 202 pause, got %s", got)
	}

	got429 := adaptiveMonthlyFundingBatchPause(map[string]int{"http_429": 10}, 20)
	if got429 <= got {
		t.Fatalf("expected 429-heavy batch pause to exceed 202-heavy batch pause, got 429=%s 202=%s", got429, got)
	}
}

func TestAdaptiveMonthlyFundingFetchConcurrencyDropsAndRecovers(t *testing.T) {
	got := adaptiveMonthlyFundingFetchConcurrency(20, 20, 20, map[string]int{"http_202": 12})
	if got >= 20 {
		t.Fatalf("expected 202-heavy batch to reduce concurrency, got %d", got)
	}

	got = adaptiveMonthlyFundingFetchConcurrency(20, 12, 20, map[string]int{"http_429": 10})
	if got != 6 {
		t.Fatalf("expected 429-heavy batch to halve concurrency from 12 to 6, got %d", got)
	}

	got = adaptiveMonthlyFundingFetchConcurrency(20, 6, 20, map[string]int{})
	if got != 7 {
		t.Fatalf("expected calm batch to recover concurrency by one, got %d", got)
	}
}

func TestShouldAbortMonthlyFundingFetchAfterConsecutive429Batches(t *testing.T) {
	consecutive202 := 0
	consecutive429 := 0
	consecutive403 := 0
	consecutive418 := 0

	if shouldAbortMonthlyFundingFetch(map[string]int{"http_429": 10}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect first 429-heavy batch to abort immediately")
	}
	if !shouldAbortMonthlyFundingFetch(map[string]int{"http_429": 11}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("expected second consecutive 429-heavy batch to abort")
	}
}

func TestTrimMonthlyFundingGroupItemsKeepsTop20Only(t *testing.T) {
	items := make([]FundingRateHistoryItem, 25)
	for i := range items {
		items[i] = FundingRateHistoryItem{Symbol: "SYM" + strconv.Itoa(i)}
	}

	trimmed := trimMonthlyFundingGroupItems(items, monthlyFundingTopSymbolsPerTime)
	if len(trimmed) != monthlyFundingTopSymbolsPerTime {
		t.Fatalf("expected trimmed size=%d, got %d", monthlyFundingTopSymbolsPerTime, len(trimmed))
	}
	if trimmed[0].Symbol != items[0].Symbol || trimmed[len(trimmed)-1].Symbol != items[monthlyFundingTopSymbolsPerTime-1].Symbol {
		t.Fatalf("expected trim to keep leading items, got first=%s last=%s", trimmed[0].Symbol, trimmed[len(trimmed)-1].Symbol)
	}

	underLimit := trimMonthlyFundingGroupItems(items[:12], monthlyFundingTopSymbolsPerTime)
	if len(underLimit) != 12 {
		t.Fatalf("expected under-limit slice to remain unchanged, got %d", len(underLimit))
	}
}

func TestMonthlyFundingTimelineUses8HourSteps(t *testing.T) {
	start := time.Date(2026, 5, 1, 1, 30, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)

	got := monthlyFundingTimeline(start, end)
	if len(got) != 4 {
		t.Fatalf("expected 4 funding checkpoints, got %d", len(got))
	}

	expected := []time.Time{
		time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 1, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
	}
	for i := range expected {
		if !got[i].Equal(expected[i]) {
			t.Fatalf("checkpoint[%d] expected %s, got %s", i, expected[i].Format(time.RFC3339), got[i].Format(time.RFC3339))
		}
	}
}

func TestValidateMonthlyActualFunding(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	reason, _ := runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0100,
		FundingRate:          0.0400,
	})
	if reason != "funding_rate_extreme" {
		t.Fatalf("expected funding_rate_extreme, got %s", reason)
	}

	reason, _ = runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0010,
		FundingRate:          0.0200,
	})
	if reason != "funding_jump_too_large" {
		t.Fatalf("expected funding_jump_too_large, got %s", reason)
	}

	reason, _ = runner.validateMonthlyActualFunding(monthlyCandidate{
		EstimatedFundingRate: 0.0050,
		FundingRate:          0.0060,
	})
	if reason != "" {
		t.Fatalf("expected no rejection, got %s", reason)
	}
}
