package accountrouter

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
)

func testBar(at int64, open, close float64) counterfactual.Bar {
	return counterfactual.Bar{TimeMS: at, Open: open, High: max(open, close) * 1.001, Low: min(open, close) * .999, Close: close, RollingQuoteVolumeUSDT: 1e9}
}

func testPolicy() modelapi.Policy {
	return modelapi.Policy{Leaves: []modelapi.CandidateLeaf{{LeafID: leafID, Predicate: "TRUE"}}}
}

func testRows() []datav3.StateRowV2 {
	eventID := ""
	for i := 0; ; i++ {
		candidate := "event-" + fmt.Sprint(i)
		if counterfactual.ContextID(candidate) == "FLAT|age=0|fraction=0" {
			eventID = candidate
			break
		}
	}
	return []datav3.StateRowV2{{SchemaVersion: datav3.StateSchemaVersionV2, EventID: eventID, Instrument: "BTC", DecisionMS: 0, Partition: "validation"}}
}

func testFunding() counterfactual.FundingSource {
	return counterfactual.FundingSource{CompleteDays: map[string]bool{"1970-01-01": true, "1970-01-02": true}}
}

func testBars(closeAt24 float64) map[string]map[int64]counterfactual.Bar {
	bars := map[int64]counterfactual.Bar{}
	for h := 0; h <= 30; h++ {
		close := 100 + float64(h)
		if h == 24 {
			close = closeAt24
		}
		bars[int64(h)*hourMS] = testBar(int64(h)*hourMS, 1000+float64(h), close)
	}
	return map[string]map[int64]counterfactual.Bar{"BTC": bars}
}

func TestPartitionRefusal(t *testing.T) {
	if _, err := loadRows(t.TempDir(), []string{"router_holdout"}); err == nil {
		t.Fatal("router_holdout must be refused before any file is opened")
	}
}

func TestFutureAfterPartitionHorizonCannotChangeMetricsHash(t *testing.T) {
	before := testBars(124)
	after := testBars(124)
	after["BTC"][30*hourMS] = testBar(30*hourMS, 1, 999999)
	a, err := replayScenario(testRows(), testPolicy(), .1, 0, 1, before, testFunding())
	if err != nil {
		t.Fatal(err)
	}
	b, err := replayScenario(testRows(), testPolicy(), .1, 0, 1, after, testFunding())
	if err != nil {
		t.Fatal(err)
	}
	rawA, _ := json.Marshal(a)
	rawB, _ := json.Marshal(b)
	if string(rawA) != string(rawB) {
		t.Fatalf("post-horizon future changed metrics/hash:\n%s\n%s", rawA, rawB)
	}
}

func TestOpenAndCloseUseCompletedBarClose(t *testing.T) {
	a := &account{Cash: 100, Positions: map[string]position{}, Peak: 100}
	entry := testBar(0, 1000, 100)
	if !openPosition(a, "BTC", 10, 100, 0, 1, entry) {
		t.Fatal("open rejected")
	}
	if a.Positions["BTC"].Entry != 100 {
		t.Fatalf("entry=%g, want completed close 100", a.Positions["BTC"].Entry)
	}
	exit := testBar(24*hourMS, 1, 110)
	before := a.Cash
	if !closePosition(a, a.Positions["BTC"], 100, 1, exit) {
		t.Fatal("close failed")
	}
	if a.Cash <= before {
		t.Fatal("close did not use profitable close price")
	}
}

func TestDelayShiftsExecutionAndExpiry(t *testing.T) {
	rows := testRows()
	bars := testBars(124)
	result, err := replayScenario(rows, testPolicy(), .1, 3, 1, bars, testFunding())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionEndMS != 27*hourMS {
		t.Fatalf("end=%d, want decision+delay+24h", result.ExecutionEndMS)
	}
	// Entry at hour 3 close=103 and expiry at hour 27 close=127.
	if result.Tiers["100"].CashflowAdjustedWealth <= 0 {
		t.Fatal("delay-shifted profitable replay did not execute")
	}
}

func TestStressCostChangesMetricsAndSelectionGate(t *testing.T) {
	base, err := replayScenario(testRows(), testPolicy(), .1, 0, 1, testBars(124), testFunding())
	if err != nil {
		t.Fatal(err)
	}
	stress, err := replayScenario(testRows(), testPolicy(), .1, 0, 1.5, testBars(124), testFunding())
	if err != nil {
		t.Fatal(err)
	}
	if !(stress.Tiers["100"].CashflowAdjustedWealth < base.Tiers["100"].CashflowAdjustedWealth) {
		t.Fatal("1.5x stress cost did not reduce wealth")
	}
}

func safeTiers(wealth float64) map[string]Metrics {
	return map[string]Metrics{
		"30":   {CashflowAdjustedWealth: wealth},
		"100":  {CashflowAdjustedWealth: wealth},
		"1000": {CashflowAdjustedWealth: wealth},
	}
}

func TestAlternateDelayStressLossDoesNotBlock(t *testing.T) {
	scenario := ScenarioResult{DelayBars: 2, CostStress: 1.5, Tiers: safeTiers(-1)}
	if !scenarioPassesFrozenGate(scenario) {
		t.Fatal("safe alternate-delay stress loss incorrectly blocked")
	}
}

func TestAnyScenarioRuinBlocks(t *testing.T) {
	tiers := safeTiers(1)
	metrics := tiers["100"]
	metrics.Ruin = true
	tiers["100"] = metrics
	scenario := ScenarioResult{DelayBars: 3, CostStress: 1.5, Tiers: tiers}
	if scenarioPassesFrozenGate(scenario) {
		t.Fatal("ruin did not block alternate-delay stress scenario")
	}
}

func TestOwnDelayStressLossBlocks(t *testing.T) {
	scenario := ScenarioResult{DelayBars: 0, CostStress: 1.5, Tiers: safeTiers(-1)}
	if scenarioPassesFrozenGate(scenario) {
		t.Fatal("own-delay cost-stress loss did not block")
	}
}

func TestOpenLeverageUsesCurrentMTMEquity(t *testing.T) {
	a := &account{Cash: 10, Positions: map[string]position{
		"OLD": {Instrument: "OLD", Notional: 100, Entry: 100, LastMark: 200},
	}}
	bar := testBar(0, 100, 100)
	if !openPosition(a, "NEW", 100, 100, 0, 1, bar) {
		t.Fatal("MTM-supported open was incorrectly tested against cash-only equity")
	}
}

func TestActiveMissingBarFailsClosed(t *testing.T) {
	a := &account{Cash: 100, Positions: map[string]position{"BTC": {Instrument: "BTC", Notional: 10, Entry: 100, LastMark: 100, ExpiryMS: 24 * hourMS}}}
	markAndExpire(a, hourMS, 100, 1, map[string]map[int64]counterfactual.Bar{"BTC": {}}, counterfactual.FundingSource{})
	if !a.Ruined || a.Unknown == 0 || len(a.Positions) != 0 {
		t.Fatal("missing active bar did not fail closed")
	}
}

func TestClusterCapExpiryFundingAndWithdrawalSurviveRuin(t *testing.T) {
	a := &account{Cash: 100, Positions: map[string]position{}, Peak: 100}
	bar := testBar(0, 100, 100)
	for i := 0; i < 4; i++ {
		if !openPosition(a, string(rune('a'+i)), 50, 100, 0, 1, bar) {
			t.Fatal("fixture open rejected before 2x cluster cap")
		}
	}
	if grossNotional(a) != 200 || grossNotional(a)+50 <= 2*100 {
		t.Fatal("2x crypto cluster cap fixture invalid")
	}
	b := &account{Cash: 160, Withdrawals: 50, Positions: map[string]position{
		"BTC": {Instrument: "BTC", Notional: 50, Entry: 100, LastMark: 100, ExpiryMS: hourMS},
	}}
	bars := map[string]map[int64]counterfactual.Bar{"BTC": {hourMS: testBar(hourMS, 100, 110)}}
	funding := counterfactual.FundingSource{Events: map[string][]counterfactual.FundingEvent{"BTC": {{TimeMS: hourMS, Rate: .01}}}}
	markAndExpire(b, hourMS, 100, 1, bars, funding)
	if len(b.Positions) != 0 {
		t.Fatal("position remained after expiry")
	}
	if b.Cash >= 165 {
		t.Fatal("actual funding was not charged")
	}
	withdrawalsBeforeRuin := b.Withdrawals
	recordRuin(b, 2*hourMS, "TEST_RUIN")
	if b.Withdrawals != withdrawalsBeforeRuin || b.Withdrawals < 50 {
		t.Fatal("prior withdrawal did not survive later ruin")
	}
}

func TestMatchingSignalWithIncompleteFutureIsSkipped(t *testing.T) {
	bars := testBars(124)
	delete(bars["BTC"], 27*hourMS)
	result, err := replayScenario(testRows(), testPolicy(), .25, 0, 1, bars, testFunding())
	if err != nil {
		t.Fatal(err)
	}
	metrics := result.Tiers["100"]
	if result.SkippedIneligibleSignal != 1 || result.EligibleSignalCount != 0 {
		t.Fatalf("skipped=%d eligible=%d, want 1/0", result.SkippedIneligibleSignal, result.EligibleSignalCount)
	}
	if metrics.Activation != 0 || metrics.Ruin || metrics.MissedCertifiedOpportunity != 0 {
		t.Fatalf("ineligible matching row affected account: %+v", metrics)
	}
}

func TestEligibleSupportMatchesPrepareFixture(t *testing.T) {
	rows := testRows()
	second := rows[0]
	second.EventID += "-other"
	second.Instrument = "ETH"
	rows = append(rows, second)
	bars := testBars(124)
	eth := map[int64]counterfactual.Bar{}
	for h := 0; h < 27; h++ {
		eth[int64(h)*hourMS] = testBar(int64(h)*hourMS, 100, 100)
	}
	bars["ETH"] = eth
	result, err := replayScenario(rows, testPolicy(), .25, 0, 1, bars, testFunding())
	if err != nil {
		t.Fatal(err)
	}
	if result.EligibleSignalCount != 1 || result.SkippedIneligibleSignal != 1 {
		t.Fatalf("support=%d skipped=%d, want PrepareRow-consistent 1/1", result.EligibleSignalCount, result.SkippedIneligibleSignal)
	}
}

func TestBatchHashOrderingStable(t *testing.T) {
	events := []string{"b", "a", "c"}
	copyEvents := append([]string(nil), events...)
	sortByHash := func(values []string) {
		for i := 0; i < len(values); i++ {
			for j := i + 1; j < len(values); j++ {
				if eventHash(values[j]) < eventHash(values[i]) {
					values[i], values[j] = values[j], values[i]
				}
			}
		}
	}
	sortByHash(events)
	sortByHash(copyEvents)
	if !reflect.DeepEqual(events, copyEvents) {
		t.Fatal("hash ordering is not deterministic")
	}
}
