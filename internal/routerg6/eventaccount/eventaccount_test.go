package eventaccount_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg6/eventaccount"
	"lingxi/internal/routerg6/sizing"
)

func candidate(id, symbol, dir string, bar int, edge, stress float64) eventaccount.EventCandidate {
	return eventaccount.EventCandidate{
		EventID:           id,
		Symbol:            symbol,
		Direction:         dir,
		EventFamily:       "ohlcv_extreme_continuation",
		DecisionBarIndex:  bar,
		WindowEndBarIndex: bar + 10,
		ConservativeEdgeUSDT: edge,
		StressLossUSDT:       stress,
		ReferenceNotional:    100,
		MarginTimeSec:        3600,
		DeadlineBarIndex:     bar + 20,
	}
}

func flatPrice(_ string, _ int) (float64, bool) { return 100, true }

func TestConcurrencyCap100U(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	admit := eventaccount.QueueAdmission{MaxConcurrent: sizing.MaxConcurrent(100)}
	var cands []eventaccount.EventCandidate
	for i := 0; i < 4; i++ {
		cands = append(cands, candidate(
			fmt.Sprintf("e%d", i),
			fmt.Sprintf("SYM%d-USDT-SWAP", i),
			"long", 1, 10-float64(i), 1,
		))
	}
	accepted, rejected := admit.Select(acct, cands)
	if len(accepted) != 2 {
		t.Fatalf("accepted=%d want 2", len(accepted))
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected=%d want 2", len(rejected))
	}
}

func TestThreeTiersConcurrentAdmission(t *testing.T) {
	for _, tc := range []struct {
		equity float64
		want   int
	}{
		{30, 1}, {100, 2}, {1000, 3},
	} {
		acct := eventaccount.NewAccount(tc.equity)
		var cands []eventaccount.EventCandidate
		for i := 0; i < tc.want+2; i++ {
			cands = append(cands, candidate(
				fmt.Sprintf("id%d", i),
				fmt.Sprintf("X%d", i),
				"long", 1, 5, 0.5,
			))
		}
		accepted, _ := eventaccount.QueueAdmission{MaxConcurrent: sizing.MaxConcurrent(tc.equity)}.Select(acct, cands)
		if len(accepted) != tc.want {
			t.Fatalf("equity %v accepted=%d want %d", tc.equity, len(accepted), tc.want)
		}
	}
}

func TestOverlapSameEventRejected(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("same", "BTC-USDT-SWAP", "long", 1, 5, 1)
	acct.Slots[c.EventID] = &eventaccount.EventSlot{Candidate: c}
	if reason, ok := eventaccount.OverlapReject(acct, c); !ok || reason != eventaccount.RejectSameEvent {
		t.Fatalf("overlap reject = %q ok=%v", reason, ok)
	}
}

func TestOverlapSameSymbolWindowRejected(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	open := candidate("e1", "ETH-USDT-SWAP", "long", 5, 5, 1)
	open.WindowEndBarIndex = 15
	acct.Slots[open.EventID] = &eventaccount.EventSlot{Candidate: open}
	overlap := candidate("e2", "ETH-USDT-SWAP", "short", 10, 4, 1)
	overlap.WindowEndBarIndex = 20
	if reason, ok := eventaccount.OverlapReject(acct, overlap); !ok || reason != eventaccount.RejectSymbolOverlap {
		t.Fatalf("symbol overlap = %q ok=%v", reason, ok)
	}
}

func TestStopErrorDynamicExit(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("exit", "BTC-USDT-SWAP", "long", 1, 5, 1)
	c.CompleteAtBar = 3
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 101}, ScenarioDelay: 0},
		{BarIndex: 3, DecisionTime: time.Unix(300, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 102}, ScenarioDelay: 0},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, flatPrice)
	slot := acct.Slots["exit"]
	if slot == nil || !slot.Closed || slot.ExitReason != eventaccount.ExitEventComplete {
		t.Fatalf("want event_complete exit got %+v", slot)
	}
	if acct.ExitCounts[eventaccount.ExitEventComplete] != 1 {
		t.Fatal("exit count missing")
	}
}

func TestPendingDelayExecutesLater(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("delay", "BTC-USDT-SWAP", "long", 1, 5, 1)
	c.ScenarioDelay = 2
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 3, DecisionTime: time.Unix(300, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 4, DecisionTime: time.Unix(400, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, flatPrice)
	if _, ok := acct.Core.Positions["BTC-USDT-SWAP"]; !ok {
		t.Fatal("delayed open must execute at bar 1+1+2=4")
	}
}

func TestFlushMaturedUsesCurrentBarIndex(t *testing.T) {
	queue := &eventaccount.PendingQueue{}
	entry := eventaccount.PendingEntry{
		Candidate:    candidate("flush", "BTC-USDT-SWAP", "long", 1, 5, 1),
		ExecuteIndex: 4,
	}
	queue.Enqueue(entry)
	if matured := queue.FlushMatured(3); len(matured) != 0 {
		t.Fatalf("bar 3 must not flush executeIndex=4, got %d", len(matured))
	}
	matured := queue.FlushMatured(4)
	if len(matured) != 1 {
		t.Fatalf("bar 4 must flush executeIndex=4, got %d", len(matured))
	}
}

func TestPendingExecutesAtChangingEntryPrice(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("price", "BTC-USDT-SWAP", "long", 1, 5, 1)
	c.ScenarioDelay = 2
	prices := map[int]float64{1: 100, 2: 100, 3: 100, 4: 120}
	entryPrice := func(_ string, idx int) (float64, bool) {
		p, ok := prices[idx]
		return p, ok
	}
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 3, DecisionTime: time.Unix(300, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 2},
		{BarIndex: 4, DecisionTime: time.Unix(400, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 120}, ScenarioDelay: 2},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, entryPrice)
	pos, ok := acct.Core.Positions["BTC-USDT-SWAP"]
	if !ok || pos.Side == actionspace.SideFlat || math.Abs(pos.BasisPrice-120) > 1e-6 {
		t.Fatalf("must execute at bar-4 open=120, got %+v ok=%v", pos, ok)
	}
}

func TestLeverageBreachBlocksNewOpen(t *testing.T) {
	acct := eventaccount.NewAccount(30)
	if err := acct.Core.ApplyExecution("BTC-USDT-SWAP", actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 30}, actionspace.Action{
		Verb: actionspace.VerbOpenLong, SizeFraction: 1, TargetHorizon: "event",
	}, 100, 0, 0); err != nil {
		t.Fatal(err)
	}
	acct.Core.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 50})
	if !acct.Core.LeverageBreach {
		t.Fatal("mark-to-market loss must trip leverage breach")
	}
	if acct.AllowsNewEvent() {
		t.Fatal("must block new opens after leverage breach")
	}
}

func TestFundingAppliedBetweenBars(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("fund", "BTC-USDT-SWAP", "long", 1, 5, 1)
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(0, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 2, DecisionTime: time.Unix(3600, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 3, DecisionTime: time.Unix(7200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
	}
	bundle := portfoliosim.FundingBundle{
		BySymbol: map[string][]profitmap.FundingPoint{
			"BTC-USDT-SWAP": {{Time: time.Unix(5400, 0), Rate: 0.001}},
		},
	}
	eventaccount.ReplayScenario(acct, bars, bundle, flatPrice)
	if acct.Core.FundingPaidUSDT <= 0 {
		t.Fatal("funding must be deducted while position is open")
	}
}

func TestMinOrderSkippedInQueue(t *testing.T) {
	acct := eventaccount.NewAccount(30)
	c := candidate("min", "BTC-USDT-SWAP", "long", 1, 5, 50)
	c.MinNotionalUSDT = 100
	c.LotNotionalUSDT = 10
	_, rejected := eventaccount.QueueAdmission{MaxConcurrent: 1}.Select(acct, []eventaccount.EventCandidate{c})
	if len(rejected) != 1 {
		t.Fatalf("want min-order rejection got %d", len(rejected))
	}
}

func TestInvalidatorExitBeforeDeadline(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("inv", "BTC-USDT-SWAP", "long", 1, 5, 1)
	c.InvalidatorAtBar = 2
	c.DeadlineBarIndex = 10
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, flatPrice)
	slot := acct.Slots["inv"]
	if slot == nil || slot.ExitReason != eventaccount.ExitInvalidator {
		t.Fatalf("want invalidator exit got %+v", slot)
	}
}

func TestEffectiveLeverageAfterMTM(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("lev", "BTC-USDT-SWAP", "long", 1, 5, 1)
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, flatPrice)
	acct.Core.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 200})
	lev := acct.Core.EffectiveLeverage()
	if lev <= 0 || math.IsInf(lev, 0) {
		t.Fatalf("unexpected leverage %v", lev)
	}
}

func TestSolvencyForcedTrim(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	c := candidate("sol", "BTC-USDT-SWAP", "long", 1, 5, 1)
	bars := []eventaccount.BarReplayInput{
		{BarIndex: 1, DecisionTime: time.Unix(100, 0), Candidates: []eventaccount.EventCandidate{c}, Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
		{BarIndex: 2, DecisionTime: time.Unix(200, 0), Prices: map[string]float64{"BTC-USDT-SWAP": 100}, ScenarioDelay: 0},
	}
	eventaccount.ReplayScenario(acct, bars, portfoliosim.FundingBundle{}, flatPrice)
	acct.Core.CashUSDT = -1
	acct.Core.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 100})
	order, breached := eventaccount.SolvencyTrim(acct)
	if !breached {
		t.Fatal("solvency trim must trigger on negative cash with open slot")
	}
	if len(order) == 0 {
		t.Fatal("expected reduce order")
	}
}
