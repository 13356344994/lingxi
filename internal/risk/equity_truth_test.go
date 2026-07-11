package risk

import (
	"testing"
	"time"
)

var equityTruthTestTime = time.Date(2026, 7, 11, 6, 0, 0, 0, time.UTC)

func TestEquityTruthLedger_FlowFirstThenEquity(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1",
		Venue:         "okx",
		RawEquity:     MicroUSDTFromFloat64(100),
		ExchangeTime:  equityTruthTestTime,
	})

	view := ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "flow-1", Venue: "okx", Amount: MicroUSDTFromFloat64(-22.35),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	if got, want := view.TradingEquity.Float64(), 100.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("flow-first trading_equity=%.8f want %.8f", got, want)
	}
	if got, want := view.PendingOutflowReserve.Float64(), 22.35; diffAbs(got, want) > 1e-6 {
		t.Fatalf("flow-first pending_outflow=%.8f want %.8f", got, want)
	}
	if view.DrawdownFromFrontier != 0 {
		t.Fatalf("flow-first drawdown should stay 0, got %.8f", view.DrawdownFromFrontier.Float64())
	}

	view = ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-2",
		Venue:         "okx",
		RawEquity:     MicroUSDTFromFloat64(77.65),
		ExchangeTime:  equityTruthTestTime.Add(2 * time.Minute),
	})
	if got, want := view.TradingEquity.Float64(), 100.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("post-match trading_equity=%.8f want %.8f", got, want)
	}
	if view.PendingFlowCount != 0 || view.PendingEquityCount != 0 || view.HasPending {
		t.Fatalf("expected fully matched pending state, got flows=%d equity=%d has_pending=%v",
			view.PendingFlowCount, view.PendingEquityCount, view.HasPending)
	}
	if got, want := view.SolvencyEquity.Float64(), 77.65; diffAbs(got, want) > 1e-6 {
		t.Fatalf("post-match solvency_equity=%.8f want %.8f", got, want)
	}
}

func TestEquityTruthLedger_EquityFirstThenFlow(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	view := ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-2", Venue: "okx", RawEquity: MicroUSDTFromFloat64(80),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	if view.PendingEquityCount != 1 || view.PendingFlowCount != 0 || !view.HasPending {
		t.Fatalf("equity-first pending mismatch: %+v", view)
	}
	if got, want := view.DrawdownFromFrontier.Float64(), 20.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("equity-first drawdown=%.8f want %.8f", got, want)
	}

	view = ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "flow-1", Venue: "okx", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Minute),
	})
	if view.PendingEquityCount != 0 || view.PendingFlowCount != 0 || view.HasPending {
		t.Fatalf("expected pending cleared after backfill flow, got %+v", view)
	}
	if got, want := view.TradingEquity.Float64(), 100.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("equity-first backfilled trading_equity=%.8f want %.8f", got, want)
	}
	if got := view.DrawdownFromFrontier; got != 0 {
		t.Fatalf("equity-first backfilled drawdown should be 0, got %.8f", got.Float64())
	}
}

func TestEquityTruthLedger_RealLossPreservedWithOutflow(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "flow-1", Venue: "okx", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	view := ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-2", Venue: "okx", RawEquity: MicroUSDTFromFloat64(75),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Minute),
	})
	if got, want := view.TradingEquity.Float64(), 95.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("trading_equity=%.8f want %.8f", got, want)
	}
	if got, want := view.DrawdownFromFrontier.Float64(), 5.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("drawdown=%.8f want %.8f", got, want)
	}
}

func TestEquityTruthLedger_CleanFrontierOnlyUpdatesWhenClean(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "flow-in-1", Venue: "okx", Amount: MicroUSDTFromFloat64(10),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	view := ledger.View()
	if got, want := view.HighWaterCleanFrontier.Float64(), 100.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("dirty frontier moved unexpectedly: %.8f", got)
	}

	view = ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-2", Venue: "okx", RawEquity: MicroUSDTFromFloat64(110),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Minute),
	})
	if view.HasPending {
		t.Fatalf("flow should be matched after equity update, got pending view=%+v", view)
	}
	if got, want := view.HighWaterCleanFrontier.Float64(), 100.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("post-match frontier should stay 100 without trading pnl, got %.8f", got)
	}

	view = ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-3", Venue: "okx", RawEquity: MicroUSDTFromFloat64(112),
		ExchangeTime: equityTruthTestTime.Add(3 * time.Minute),
	})
	if got, want := view.TradingEquity.Float64(), 102.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("expected +2 trading pnl, got %.8f", got)
	}
	if got, want := view.HighWaterCleanFrontier.Float64(), 102.0; diffAbs(got, want) > 1e-6 {
		t.Fatalf("clean frontier should move with real pnl, got %.8f", got)
	}
}

func TestEquityTruthLedger_OrdinaryPnLDoesNotMatchLateWithdrawal(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	view := ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-pnl", Venue: "okx", RawEquity: MicroUSDTFromFloat64(98),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	if view.HasPending || view.PendingEquityCount != 0 {
		t.Fatalf("small ordinary PnL must not become pending: %+v", view)
	}
	if got := view.DrawdownFromFrontier.Float64(); diffAbs(got, 2) > 1e-6 {
		t.Fatalf("ordinary PnL drawdown=%.8f want 2", got)
	}

	view = ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "late-withdrawal", Venue: "okx", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Hour),
	})
	if view.ConfirmedFlowTotal != 0 {
		t.Fatalf("late withdrawal incorrectly matched old PnL: confirmed=%.8f", view.ConfirmedFlowTotal.Float64())
	}
	if view.PendingFlowCount != 1 || !view.HasPending {
		t.Fatalf("late withdrawal should be a new pending flow: %+v", view)
	}
}

func TestEquityTruthLedger_ExpiredLargeTradingLossStopsPendingProtection(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	view := ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "large-loss", Venue: "okx", RawEquity: MicroUSDTFromFloat64(70),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})
	if !view.HasPending || view.PendingEquityCount != 1 {
		t.Fatalf("large fresh delta should receive bounded async protection: %+v", view)
	}

	view = ledger.ExpirePending(equityTruthTestTime.Add(time.Minute + EquityTruthMatchWindow + time.Second))
	if view.HasPending || view.PendingEquityCount != 0 {
		t.Fatalf("expired loss must leave pending protection: %+v", view)
	}
	if got := view.DrawdownFromFrontier.Float64(); diffAbs(got, 30) > 1e-6 {
		t.Fatalf("expired real loss must remain in drawdown, got %.8f", got)
	}

	view = ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "much-later-withdrawal", Venue: "okx", Amount: MicroUSDTFromFloat64(-30),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Hour),
	})
	if view.ConfirmedFlowTotal != 0 {
		t.Fatalf("expired real loss was matched by later withdrawal: %.8f", view.ConfirmedFlowTotal.Float64())
	}
}

func TestEquityTruthLedger_RestoreKeepsEventDeduplication(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	flow := FundFlowEvent{
		EventID: "flow-1", Venue: "okx", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	}
	ledger.ApplyFundFlow(flow)
	obs := EquityObservation{
		ObservationID: "obs-2", Venue: "okx", RawEquity: MicroUSDTFromFloat64(80),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Minute),
	}
	view := ledger.ApplyEquityObservation(obs)
	if got := view.ConfirmedFlowTotal.Float64(); diffAbs(got, -20) > 1e-6 {
		t.Fatalf("pre-restart confirmed flow=%.8f", got)
	}

	restarted := NewEquityTruthLedger("okx")
	restarted.Restore(ledger.Snapshot())
	restarted.ApplyFundFlow(flow)
	view = restarted.ApplyEquityObservation(obs)
	if got := view.ConfirmedFlowTotal.Float64(); diffAbs(got, -20) > 1e-6 {
		t.Fatalf("duplicate events double-counted after restore: %.8f", got)
	}
	if view.PendingFlowCount != 0 || view.PendingEquityCount != 0 {
		t.Fatalf("duplicates created pending state after restore: %+v", view)
	}
}

func TestEquityTruthLedger_DoesNotMatchAcrossVenuesOrOutsideWindow(t *testing.T) {
	ledger := NewEquityTruthLedger("okx")
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-1", Venue: "okx", RawEquity: MicroUSDTFromFloat64(100),
		ExchangeTime: equityTruthTestTime,
	})
	ledger.ApplyEquityObservation(EquityObservation{
		ObservationID: "obs-loss", Venue: "okx", RawEquity: MicroUSDTFromFloat64(80),
		ExchangeTime: equityTruthTestTime.Add(time.Minute),
	})

	view := ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "wrong-venue", Venue: "bybit", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Minute),
	})
	if view.ConfirmedFlowTotal != 0 || view.PendingEquityCount != 1 {
		t.Fatalf("cross-venue event changed OKX ledger: %+v", view)
	}

	view = ledger.ApplyFundFlow(FundFlowEvent{
		EventID: "too-late", Venue: "okx", Amount: MicroUSDTFromFloat64(-20),
		ExchangeTime: equityTruthTestTime.Add(2 * time.Hour),
	})
	if view.ConfirmedFlowTotal != 0 {
		t.Fatalf("out-of-window event incorrectly matched: %.8f", view.ConfirmedFlowTotal.Float64())
	}
	if view.PendingEquityCount != 0 || view.PendingFlowCount != 1 {
		t.Fatalf("old equity should expire and late flow remain pending: %+v", view)
	}
}

func diffAbs(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
