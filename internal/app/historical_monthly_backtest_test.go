package app

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"lingxi/internal/exchange"
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

	if shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 20}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect first 202-heavy batch to abort immediately")
	}
	if consecutive202 != 1 {
		t.Fatalf("expected first 202-heavy batch to increment counter, got %d", consecutive202)
	}
	if shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 20}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect second consecutive 202-only batch to abort")
	}
	if shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 20}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("did not expect third all-202 batch to abort yet")
	}
	if !shouldAbortMonthlyFundingFetch(map[string]int{"http_202": 20}, 20, &consecutive202, &consecutive429, &consecutive403, &consecutive418) {
		t.Fatal("expected fourth consecutive all-202 batch to abort")
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
	if got := adaptiveMonthlyFundingBatchPause(map[string]int{}, 20); got != 0 {
		t.Fatalf("expected calm batch pause=0, got %s", got)
	}
	if got := adaptiveMonthlyFundingBatchPause(map[string]int{"deadline_exceeded": 14}, 20); got != 0 {
		t.Fatalf("expected timeout-only batch pause=0, got %s", got)
	}
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

func TestCoarseScreenMonthlySymbolsUsesPremiumIndexSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/premiumIndex" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","markPrice":"101","indexPrice":"100","lastFundingRate":"0.0040","interestRate":"0.0001","nextFundingTime":0,"time":` + strconv.FormatInt(now.UnixMilli(), 10) + `},
			{"symbol":"ETHUSDT","markPrice":"100.5","indexPrice":"100","lastFundingRate":"0.0030","interestRate":"0.0001","nextFundingTime":0,"time":` + strconv.FormatInt(now.UnixMilli(), 10) + `},
			{"symbol":"XRPUSDT","markPrice":"99","indexPrice":"100","lastFundingRate":"-0.0020","interestRate":"0.0001","nextFundingTime":0,"time":` + strconv.FormatInt(now.UnixMilli(), 10) + `},
			{"symbol":"DOGEUSDT","markPrice":"100.2","indexPrice":"100","lastFundingRate":"0.0010","interestRate":"0.0001","nextFundingTime":0,"time":` + strconv.FormatInt(now.UnixMilli(), 10) + `}
		]`))
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.CoarseSymbolLimit = 2
	cfg.HistoricalConfig.FundingGroupTopSymbolsPerTime = 2
	cfg.HistoricalConfig.FundingDirectionMode = historicalFundingModePositive
	runner := NewBacktestRunner(nil, nil, cfg)
	if gotLimit := runner.monthlyCoarseSymbolLimit(4); gotLimit != 2 {
		t.Fatalf("expected coarse limit=2, got %d", gotLimit)
	}
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL:    "https://spot.local",
		FuturesBaseURL: server.URL,
		HTTPClient:     server.Client(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	items, err := client.FetchPremiumIndex(ctx)
	if err != nil {
		t.Fatalf("fetchPremiumIndex() error = %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected premiumIndex size=4, got %d", len(items))
	}

	got, err := runner.coarseScreenMonthlySymbols(ctx, client, []string{"XRPUSDT", "BTCUSDT", "ETHUSDT", "DOGEUSDT"}, now)
	if err != nil {
		t.Fatalf("coarseScreenMonthlySymbols() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected coarse result size=2, got %d (%v)", len(got), got)
	}
	if got[0] != "BTCUSDT" || got[1] != "ETHUSDT" {
		t.Fatalf("unexpected coarse ranking: %v", got)
	}
}

func TestCoarseScreenMonthlySymbolsFallsBackWhenPremiumIndexFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.HistoricalConfig.CoarseSymbolLimit = 2
	cfg.HistoricalConfig.FundingGroupTopSymbolsPerTime = 2
	runner := NewBacktestRunner(nil, nil, cfg)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL:    "https://spot.local",
		FuturesBaseURL: server.URL,
		HTTPClient:     server.Client(),
	})
	symbols := []string{"BTCUSDT", "ETHUSDT", "XRPUSDT"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := runner.coarseScreenMonthlySymbols(ctx, client, symbols, time.Now().UTC())
	if err != nil {
		t.Fatalf("coarseScreenMonthlySymbols() error = %v", err)
	}
	if len(got) != len(symbols) {
		t.Fatalf("expected fallback to keep all symbols, got %v", got)
	}
	for idx := range symbols {
		if got[idx] != symbols[idx] {
			t.Fatalf("expected fallback order %v, got %v", symbols, got)
		}
	}
}

func TestMergeSparseMonthlyEventGroupsMergesIntoNearbyDenseGroup(t *testing.T) {
	base := time.Date(2026, 5, 29, 18, 0, 0, 0, time.UTC)
	groups := []monthlyEventGroup{
		{FundingTime: base, RawCount: 1, Items: []FundingRateHistoryItem{{Symbol: "A", FundingTime: base.UnixMilli()}}},
		{FundingTime: base.Add(time.Hour), RawCount: 1, Items: []FundingRateHistoryItem{{Symbol: "B", FundingTime: base.Add(time.Hour).UnixMilli()}}},
		{FundingTime: base.Add(2 * time.Hour), RawCount: 3, Items: []FundingRateHistoryItem{
			{Symbol: "C", FundingTime: base.Add(2 * time.Hour).UnixMilli()},
			{Symbol: "D", FundingTime: base.Add(2 * time.Hour).UnixMilli()},
			{Symbol: "E", FundingTime: base.Add(2 * time.Hour).UnixMilli()},
		}},
	}

	merged := mergeSparseMonthlyEventGroups(groups, 3, 3*time.Hour, "both")
	if len(merged) != 1 {
		t.Fatalf("expected one merged group, got %d", len(merged))
	}
	if !merged[0].FundingTime.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("expected merged group to anchor to dense funding time, got %s", merged[0].FundingTime.Format(time.RFC3339))
	}
	if merged[0].RawCount != 5 || len(merged[0].Items) != 5 {
		t.Fatalf("unexpected merged group size: raw=%d items=%d", merged[0].RawCount, len(merged[0].Items))
	}
}

func TestTrimMonthlyFundingGroupItemsKeepsTopNOnly(t *testing.T) {
	items := make([]FundingRateHistoryItem, monthlyFundingTopSymbolsPerTime+12)
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

// TestHistoricalFundingPriorityScoreIgnoresRealizedRate 钉死 H1：候选打分绝不能回退到结算后的真实费率，
// 否则等于用未来已知结果反推选币（前视偏差）。
func TestHistoricalFundingPriorityScoreIgnoresRealizedRate(t *testing.T) {
	realizedOnly := FundingRateHistoryItem{Symbol: "AAA", FundingRate: "0.05"}
	if score := historicalFundingPriorityScore(realizedOnly, "positive_only"); score != 0 {
		t.Fatalf("只有已实现费率的候选必须打 0 分(杜绝前视)，实际=%.6f", score)
	}
	estimated := FundingRateHistoryItem{Symbol: "BBB", EstimatedFundingRate: "0.01", FundingRate: "0.05"}
	if score := historicalFundingPriorityScore(estimated, "positive_only"); score <= 0 {
		t.Fatalf("有点位预估的候选应按预估打分，实际=%.6f", score)
	}
}

// TestSortFundingItemsBySymbolIsDeterministicNotLookAhead 钉死 H1：点位预估之前的排序按字典序、与结算结果无关，
// 已实现费率最高的 symbol 不应因此被排到前面。
func TestSortFundingItemsBySymbolIsDeterministicNotLookAhead(t *testing.T) {
	items := []FundingRateHistoryItem{
		{Symbol: "ZZZ", FundingRate: "0.09"},
		{Symbol: "AAA", FundingRate: "0.01"},
		{Symbol: "MMM", FundingRate: "0.05"},
	}
	sortFundingItemsBySymbol(items)
	if items[0].Symbol != "AAA" || items[1].Symbol != "MMM" || items[2].Symbol != "ZZZ" {
		t.Fatalf("期望确定性字典序，实际=%s,%s,%s", items[0].Symbol, items[1].Symbol, items[2].Symbol)
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

func TestMonthlyKlineCacheFundingScheduleSupportsNon8HourCadence(t *testing.T) {
	cache := newMonthlyKlineCache()
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cache.storeFundingHistory("BTCUSDT", []FundingRateHistoryItem{
		{Symbol: "BTCUSDT", FundingRate: "0.0010", FundingTime: base.UnixMilli()},
		{Symbol: "BTCUSDT", FundingRate: "0.0015", FundingTime: base.Add(time.Hour).UnixMilli()},
		{Symbol: "BTCUSDT", FundingRate: "0.0020", FundingTime: base.Add(5 * time.Hour).UnixMilli()},
	})

	schedule, err := cache.getFundingSchedule(context.Background(), nil, "BTCUSDT", base, 3)
	if err != nil {
		t.Fatalf("getFundingSchedule() error = %v", err)
	}
	if len(schedule) != 3 {
		t.Fatalf("expected 3 funding settlements, got %d", len(schedule))
	}
	if got := time.UnixMilli(schedule[1].FundingTime).UTC().Sub(base); got != time.Hour {
		t.Fatalf("expected second settlement after 1h, got %s", got)
	}
	if got := time.UnixMilli(schedule[2].FundingTime).UTC().Sub(base); got != 5*time.Hour {
		t.Fatalf("expected third settlement after 5h, got %s", got)
	}
}

func TestMonthlyKlineCacheFundingScheduleAllowsAvailableTail(t *testing.T) {
	cache := newMonthlyKlineCache()
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cache.storeFundingHistory("BTCUSDT", []FundingRateHistoryItem{
		{Symbol: "BTCUSDT", FundingRate: "0.0010", FundingTime: base.UnixMilli()},
		{Symbol: "BTCUSDT", FundingRate: "0.0015", FundingTime: base.Add(8 * time.Hour).UnixMilli()},
	})

	schedule, err := cache.getFundingSchedule(context.Background(), nil, "BTCUSDT", base.Add(8*time.Hour), 2)
	if err != nil {
		t.Fatalf("getFundingSchedule() error = %v", err)
	}
	if len(schedule) != 1 {
		t.Fatalf("expected tail schedule to degrade to 1 settlement, got %d", len(schedule))
	}
	if schedule[0].FundingTime != base.Add(8*time.Hour).UnixMilli() {
		t.Fatalf("unexpected tail funding time: %d", schedule[0].FundingTime)
	}
}

func TestAlignHistoricalEstimatedFundingRateRejectsDirectionConflict(t *testing.T) {
	if got := alignHistoricalEstimatedFundingRate(0.0030, -0.0010); got != 0 {
		t.Fatalf("expected conflicting directions to collapse to 0, got %.6f", got)
	}
	if got := alignHistoricalEstimatedFundingRate(0.0030, 0.0010); got != 0.0030 {
		t.Fatalf("expected aligned directions to remain unchanged, got %.6f", got)
	}
	if got := alignHistoricalEstimatedFundingRate(0, -0.0010); got != 0 {
		t.Fatalf("expected zero estimate to stay zero, got %.6f", got)
	}
}

func TestMonthlySafetyBufferDownweightsProjectedExitCost(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	got := runner.monthlySafetyBuffer(0.20, 0.20, 0.10)
	if math.Abs(got-0.07) > 1e-9 {
		t.Fatalf("expected weighted safety buffer 0.07, got %.6f", got)
	}
	if floor := runner.monthlySafetyBuffer(0, 0, 0); math.Abs(floor-0.001) > 1e-9 {
		t.Fatalf("expected floor safety buffer 0.001, got %.6f", floor)
	}
}

func TestEstimateMonthlyPreselectScorePenalizesPoorLiquidity(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}
	cache := newMonthlyKlineCache()
	entryTime := time.Date(2026, 5, 1, 7, 59, 30, 0, time.UTC)
	fundingTime := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

	cache.spot["before:GOODUSDT:"+strconv.FormatInt(entryTime.UnixMilli(), 10)] = MarketKline{
		Open: 1, High: 1.001, Low: 0.999, Close: 1, QuoteAssetVolume: 100000,
	}
	cache.futures["before:GOODUSDT:"+strconv.FormatInt(entryTime.UnixMilli(), 10)] = MarketKline{
		Open: 1, High: 1.001, Low: 0.999, Close: 1, QuoteAssetVolume: 100000,
	}
	cache.spot["before:BADUSDT:"+strconv.FormatInt(entryTime.UnixMilli(), 10)] = MarketKline{
		Open: 1, High: 1.001, Low: 0.999, Close: 1, QuoteAssetVolume: 20,
	}
	cache.futures["before:BADUSDT:"+strconv.FormatInt(entryTime.UnixMilli(), 10)] = MarketKline{
		Open: 1, High: 1.001, Low: 0.999, Close: 1, QuoteAssetVolume: 20,
	}

	good := runner.estimateMonthlyPreselectScore(context.Background(), nil, cache, "GOODUSDT", fundingTime, entryTime, 0.01, 1)
	bad := runner.estimateMonthlyPreselectScore(context.Background(), nil, cache, "BADUSDT", fundingTime, entryTime, 0.01, 1)
	if good <= bad {
		t.Fatalf("expected liquid candidate to outrank illiquid candidate, got good=%.6f bad=%.6f", good, bad)
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

func TestValidateMonthlyActualFundingRejectsDirectionReversal(t *testing.T) {
	runner := &BacktestRunner{cfg: DefaultEngineConfig()}

	reason, _ := runner.validateMonthlyActualFunding(monthlyCandidate{
		Direction:            fundingCarryDirectionNegative,
		EstimatedFundingRate: -0.0060,
		FundingRate:          0.0010,
	})
	if reason != "funding_direction_reversed" {
		t.Fatalf("expected funding_direction_reversed, got %s", reason)
	}
}

func TestConservativeHistoricalFundingRateUsesHaircutAndAgreement(t *testing.T) {
	if got := conservativeHistoricalFundingRate(0.0010, 0.0010); got != 0.0002 {
		t.Fatalf("expected confirmed equal sources to haircut to 0.0002, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0.0010, 0); got != 0.0001 {
		t.Fatalf("expected single source haircut to 0.0001, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0.0010, 0.0002); got != 0.00002 {
		t.Fatalf("expected low agreement estimate 0.00002, got %.6f", got)
	}
}

func TestConservativeHistoricalFundingRateSupportsNegativeCarry(t *testing.T) {
	if got := conservativeHistoricalFundingRate(-0.0010, -0.0010); got != -0.0002 {
		t.Fatalf("expected equal negative sources to haircut to -0.0002, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(-0.0010, 0.0010); got != 0 {
		t.Fatalf("expected conflicting signs to collapse to 0, got %.6f", got)
	}
	if got := conservativeHistoricalFundingRate(0, -0.0010); got != -0.0001 {
		t.Fatalf("expected single negative source haircut to -0.0001, got %.6f", got)
	}
}

func TestHistoricalFundingDirectionHelpersSupportNegativeCarry(t *testing.T) {
	income := historicalFundingIncome(fundingCarryDirectionNegative, 2, 100, -0.01, 1)
	if income != 2 {
		t.Fatalf("expected negative carry funding income 2, got %.6f", income)
	}
	spotPnL := fundingCarryDirectionNegative.spotGrossPnL(2, 100, 90)
	if spotPnL != 20 {
		t.Fatalf("expected negative carry spot pnl 20, got %.6f", spotPnL)
	}
	futuresPnL := fundingCarryDirectionNegative.futuresGrossPnL(2, 100, 110)
	if futuresPnL != 20 {
		t.Fatalf("expected negative carry futures pnl 20, got %.6f", futuresPnL)
	}
}

func TestHistoricalBorrowInterestHoursAndCostForNegativeCarry(t *testing.T) {
	entryTime := time.Date(2026, 5, 1, 23, 55, 0, 0, time.UTC)
	exitTime := time.Date(2026, 5, 2, 0, 5, 0, 0, time.UTC)

	hours := historicalBorrowInterestHours(fundingCarryDirectionNegative, entryTime, exitTime, 1)
	if hours != 1 {
		t.Fatalf("expected billed hours 1, got %d", hours)
	}
	hours = historicalBorrowInterestHours(fundingCarryDirectionNegative, entryTime, exitTime.Add(8*time.Hour), 1)
	if hours != 9 {
		t.Fatalf("expected billed hours 9, got %d", hours)
	}
	cost := historicalBorrowInterestCost(fundingCarryDirectionNegative, 100, 0.001, 3)
	if math.Abs(cost-0.3) > 1e-9 {
		t.Fatalf("expected borrow interest cost 0.3, got %.6f", cost)
	}
	if got := historicalBorrowInterestCost(fundingCarryDirectionPositive, 100, 0.001, 3); got != 0 {
		t.Fatalf("expected positive carry borrow interest cost 0, got %.6f", got)
	}
}

func TestHistoricalSpotImpactMultiplierUsesNegativeCarryOverrides(t *testing.T) {
	hcfg := historicalConfig{
		NegativeCarrySpotEntryImpactMultiplier: 2.5,
		NegativeCarrySpotExitImpactMultiplier:  3.0,
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionPositive, true); got != 1 {
		t.Fatalf("expected positive carry entry multiplier 1, got %.2f", got)
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionNegative, true); got != 2.5 {
		t.Fatalf("expected negative carry entry multiplier 2.5, got %.2f", got)
	}
	if got := historicalSpotImpactMultiplier(hcfg, fundingCarryDirectionNegative, false); got != 3.0 {
		t.Fatalf("expected negative carry exit multiplier 3.0, got %.2f", got)
	}
}
