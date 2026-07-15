package counterfactual

import (
	"math"
	"testing"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/accountstate"
	"lingxi/internal/datadrivenv3/modelapi"
)

func TestCapitalTierAssignmentCoversFrozenTiersDeterministically(t *testing.T) {
	seen := map[float64]bool{}
	for i := 0; i < 1000; i++ {
		event := SHA256Hex(string(rune(i + 1)))
		first := CapitalTier(event)
		if first != CapitalTier(event) {
			t.Fatal("capital tier assignment changed")
		}
		seen[first] = true
	}
	for _, tier := range []float64{30, 100, 1000} {
		if !seen[tier] {
			t.Fatalf("tier %.0f was never assigned", tier)
		}
	}
}

func TestDynamicBarCostAndCapitalTier(t *testing.T) {
	quiet := Bar{High: 100.01, Low: 99.99, RollingQuoteVolumeUSDT: 1_000_000}
	volatile := Bar{High: 110, Low: 90, RollingQuoteVolumeUSDT: 1_000_000}
	quiet30, err := OneWayCostBPS(quiet, 30)
	if err != nil {
		t.Fatal(err)
	}
	quiet1000, _ := OneWayCostBPS(quiet, 1000)
	volatile30, _ := OneWayCostBPS(volatile, 30)
	if quiet1000 <= quiet30 {
		t.Fatal("impact did not increase with capital tier")
	}
	if volatile30 <= quiet30 {
		t.Fatal("toxicity did not change with execution bar")
	}
}

func TestDelayThenHorizonUsesExecutionOrigin(t *testing.T) {
	prepared := fixturePrepared(100)
	target := accountstate.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1, DelayBars: 2}
	ending, err := runTarget(prepared, target)
	if err != nil {
		t.Fatal(err)
	}
	entryCost, _ := OneWayCostBPS(prepared.Bars[prepared.Candidate.Row.DecisionMS+2*hourMS], 100)
	exitCost, _ := OneWayCostBPS(prepared.Bars[prepared.Candidate.Row.DecisionMS+3*hourMS], 100)
	want := 100 - (entryCost+exitCost)*100/10000
	if math.Abs(ending.Cash-want) > 1e-9 {
		t.Fatalf("cash %.12f want %.12f", ending.Cash, want)
	}
}

func TestReverseChargesBothLegs(t *testing.T) {
	bar := Bar{High: 100.01, Low: 99.99, Close: 100, RollingQuoteVolumeUSDT: 1_000_000}
	a := account{Cash: 100, Position: position{Fraction: 1, Notional: 100, Entry: 100}}
	cost, _ := OneWayCostBPS(bar, 100)
	if err := transition(&a, -1, bar, 100); err != nil {
		t.Fatal(err)
	}
	want := 100 - 2*100*cost/10000
	if math.Abs(a.Cash-want) > 1e-9 {
		t.Fatalf("reverse cash %.12f want %.12f", a.Cash, want)
	}
}

func TestFundingIntervalIsStrictAfterThroughInclusive(t *testing.T) {
	events := []FundingEvent{{TimeMS: 10, Rate: .01}, {TimeMS: 20, Rate: .02}, {TimeMS: 30, Rate: .04}}
	tests := []struct {
		name           string
		after, through int64
		want           float64
	}{
		{"strictly_excludes_after", 10, 30, -6},
		{"includes_through", 19, 20, -2},
		{"excludes_after_equal_event", 20, 20, 0},
		{"before_first", 0, 9, 0},
		{"after_last", 30, 40, 0},
		{"empty_interval", 30, 20, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := fundingCashflow(events, test.after, test.through, 100)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("funding %.6f want %.6f", got, test.want)
			}
		})
	}
}

func TestWithdrawalSurvivesLaterRuin(t *testing.T) {
	a := account{Cash: 150}
	withdraw(&a, 100, 100)
	if a.Withdrawals != 50 || a.Cash != 100 {
		t.Fatalf("unexpected withdrawal state: %+v", a)
	}
	a.Position = position{Fraction: 1, Notional: 100, Entry: 100}
	if !ruined(a, 0.01) {
		t.Fatal("fixture did not ruin")
	}
	if a.Withdrawals != 50 {
		t.Fatal("past withdrawal was erased")
	}
}

func TestDeterministicSamplingAndTotalOrder(t *testing.T) {
	row := datav3.StateRowV2{EventID: "event", Partition: "discovery"}
	a, b := CandidateFor(row), CandidateFor(row)
	if a != b {
		t.Fatal("candidate assignment is not deterministic")
	}
	current := .25
	zero := accountstate.TargetIntent{TargetSignedPositionFraction: 0, TargetHorizonHours: 1}
	long := accountstate.TargetIntent{TargetSignedPositionFraction: .5, TargetHorizonHours: 1}
	if !targetBefore(zero, long, current) {
		t.Fatal("numeric total-order tie break was not applied")
	}
	if len(Targets()) != 108 {
		t.Fatalf("target count=%d", len(Targets()))
	}
}

func TestValidationShardRefusedBeforeOpen(t *testing.T) {
	_, err := loadDiscoveryShard(t.TempDir(), datav3.ShardReceiptV2{Partition: "validation", Path: "partition=validation/month=x.jsonl.gz"})
	if err == nil {
		t.Fatal("validation shard was not refused")
	}
}

func fixturePrepared(tier float64) Prepared {
	decision := int64(24 * hourMS)
	bars := map[int64]Bar{}
	for i := int64(0); i <= 30; i++ {
		timestamp := decision + i*hourMS
		bars[timestamp] = Bar{TimeMS: timestamp, Open: 100, High: 100.01, Low: 99.99, Close: 100, RollingQuoteVolumeUSDT: 1_000_000}
	}
	row := datav3.StateRowV2{EventID: "event", Instrument: "BTC-USDT-SWAP", DecisionMS: decision, Partition: "discovery"}
	return Prepared{
		Candidate: Candidate{Row: row, ContextID: "FLAT|age=0|fraction=0", TierUSDT: tier},
		Bars:      bars,
		Funding:   FundingSource{Events: map[string][]FundingEvent{}, CompleteDays: map[string]bool{"1970-01-02": true, "1970-01-03": true}},
		Account:   modelAccount(tier),
	}
}

func modelAccount(tier float64) (out modelapi.AccountContext) {
	out.CashUSDT, out.MarkedEquityUSDT, out.RetainedCapitalBaseUSDT, out.FreeEquityUSDT = tier, tier, tier, tier
	return out
}
