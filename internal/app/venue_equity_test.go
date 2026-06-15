package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
)

type equityTestVenue struct {
	name         string
	balances     venue.Balances
	positions    []venue.Position
	balancesErr  error
	positionsErr error
	balanceCalls atomic.Int32
}

func (f *equityTestVenue) Name() string { return f.name }

func (f *equityTestVenue) Capabilities() venue.Capabilities { return venue.Capabilities{} }
func (f *equityTestVenue) ToVenue(venue.Symbol, venue.Market) (string, error) {
	return "", nil
}
func (f *equityTestVenue) ToCanonical(string, venue.Market) (venue.Symbol, error) { return "", nil }
func (f *equityTestVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return nil, nil
}
func (f *equityTestVenue) FetchFundingSnapshot(context.Context) ([]venue.FundingSnapshot, error) {
	return nil, nil
}
func (f *equityTestVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{}, nil
}
func (f *equityTestVenue) FetchDepth(context.Context, venue.Market, venue.Symbol, int) (venue.Depth, error) {
	return venue.Depth{}, nil
}
func (f *equityTestVenue) FetchOpenInterest(context.Context, venue.Symbol) (float64, error) {
	return 0, nil
}
func (f *equityTestVenue) FetchRecentQuoteVolume(context.Context, venue.Market, venue.Symbol, time.Duration) (float64, error) {
	return 0, nil
}
func (f *equityTestVenue) ServerTime(context.Context) (time.Time, error) { return time.Time{}, nil }

func (f *equityTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	f.balanceCalls.Add(1)
	if f.balancesErr != nil {
		return venue.Balances{}, f.balancesErr
	}
	return f.balances, nil
}

func (f *equityTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	if f.positionsErr != nil {
		return nil, f.positionsErr
	}
	return f.positions, nil
}

func (f *equityTestVenue) FetchTradingCostProfile(context.Context, []venue.Symbol) (venue.CostProfile, error) {
	return venue.CostProfile{}, nil
}
func (f *equityTestVenue) MaxBorrowable(context.Context, string) (float64, error) {
	return 0, venue.ErrUnsupported
}
func (f *equityTestVenue) FetchBorrowQuote(context.Context, string) (venue.BorrowQuote, error) {
	return venue.BorrowQuote{}, venue.ErrUnsupported
}
func (f *equityTestVenue) FetchLeverageBrackets(context.Context, venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, nil
}
func (f *equityTestVenue) VerifyAPIKeyPosture(context.Context) (venue.KeyPosture, error) {
	return venue.KeyPosture{}, nil
}
func (f *equityTestVenue) PlaceOrder(context.Context, venue.OrderRequest) (venue.OrderResult, error) {
	return venue.OrderResult{}, nil
}
func (f *equityTestVenue) PlaceBatch(context.Context, []venue.OrderRequest) ([]venue.OrderResult, error) {
	return nil, nil
}
func (f *equityTestVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}
func (f *equityTestVenue) QueryOrder(context.Context, venue.Market, venue.Symbol, string) (venue.OrderResult, error) {
	return venue.OrderResult{}, nil
}
func (f *equityTestVenue) SetLeverage(context.Context, venue.Symbol, int) error { return nil }
func (f *equityTestVenue) SetMarginMode(context.Context, venue.Symbol, venue.MarginMode) error {
	return nil
}
func (f *equityTestVenue) BorrowRepay(context.Context, string, float64, venue.BorrowDir) error {
	return venue.ErrUnsupported
}
func (f *equityTestVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return nil, nil
}
func (f *equityTestVenue) FetchUserTrades(context.Context, venue.Market, venue.Symbol, time.Time) ([]venue.Trade, error) {
	return nil, nil
}
func (f *equityTestVenue) StreamBookTicker(context.Context, []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, nil
}
func (f *equityTestVenue) StreamDepth(context.Context, []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, nil
}
func (f *equityTestVenue) StreamUserData(context.Context) (<-chan venue.UserEvent, error) {
	return nil, nil
}
func (f *equityTestVenue) Classify(error) (string, string) { return "request_error", "minor" }
func (f *equityTestVenue) ReserveOrderBudget(int) bool     { return true }
func (f *equityTestVenue) ReleaseOrderBudget(int)          {}
func (f *equityTestVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return venue.ErrUnsupported
}
func (f *equityTestVenue) CaptureMovableSnapshot(context.Context) ([]venue.WalletSnapshot, float64, error) {
	return nil, 0, venue.ErrUnsupported
}
func (f *equityTestVenue) PlanAllocation(float64, venue.Capabilities) venue.CapitalAllocationPlan {
	return venue.CapitalAllocationPlan{}
}
func (f *equityTestVenue) ExecuteRebalance(context.Context, venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	return nil, venue.ErrUnsupported
}

var _ venue.Venue = (*equityTestVenue)(nil)

func TestEquityUSDTFromBalancesAndPositions(t *testing.T) {
	bal := venue.Balances{ByAsset: map[string]venue.Balance{
		"USDT": {Asset: "USDT", Free: 80, Locked: 20},
		"BTC":  {Asset: "BTC", WalletUSD: 50},
	}}
	positions := []venue.Position{
		{UnrealizedPnL: 5},
		{UnrealizedPnL: -2},
	}
	got := equityUSDTFromBalancesAndPositions(bal, positions)
	want := 80.0 + 20.0 + 50.0 + 5.0 - 2.0
	if got != want {
		t.Fatalf("equity = %.4f, want %.4f", got, want)
	}
}

func TestBuildVenuesFinancialSnapshotEquityUSDT(t *testing.T) {
	good := &equityTestVenue{
		name: "okx",
		balances: venue.Balances{ByAsset: map[string]venue.Balance{
			"USDT": {Asset: "USDT", Free: 80, Locked: 20},
			"BTC":  {Asset: "BTC", WalletUSD: 50},
		}},
		positions: []venue.Position{{UnrealizedPnL: 5}},
	}
	bad := &equityTestVenue{
		name:        "gate",
		balancesErr: errors.New("rate limited"),
	}

	exec := &CrossVenueExecutor{
		venues: map[string]venue.Venue{
			"okx":  good,
			"gate": bad,
		},
	}

	orch := NewOrchestrator()
	orch.Register(&venueRuntime{name: "okx"})
	orch.Register(&venueRuntime{name: "gate"})

	s := &DashboardServer{orchestrator: orch, crossVenueExecutor: exec}
	byVenue, total, _ := s.buildVenuesFinancialSnapshot(time.Now())

	if eq := byVenue["okx"].EquityUSDT; eq != 155 {
		t.Fatalf("okx equity_usdt = %.4f, want 155", eq)
	}
	if byVenue["gate"].EquityUSDT != 0 {
		t.Fatalf("gate equity_usdt 失败时应为 0, got %.4f", byVenue["gate"].EquityUSDT)
	}
	if byVenue["gate"].CapitalError == "" {
		t.Fatal("gate 拉取失败应标 capital_error")
	}
	if total.EquityUSDT != 155 {
		t.Fatalf("total equity_usdt = %.4f, want 155(仅累加成功所)", total.EquityUSDT)
	}
}

func TestFetchVenueEquityUSDTUsesCache(t *testing.T) {
	fake := &equityTestVenue{
		name: "binance",
		balances: venue.Balances{ByAsset: map[string]venue.Balance{
			"USDT": {Asset: "USDT", Free: 100},
		}},
	}
	exec := &CrossVenueExecutor{venues: map[string]venue.Venue{"binance": fake}}
	rt := &venueRuntime{name: "binance"}
	s := &DashboardServer{crossVenueExecutor: exec}

	eq1, err := s.fetchVenueEquityUSDT(context.Background(), rt)
	if err != nil || eq1 != 100 {
		t.Fatalf("首次拉取: eq=%.4f err=%v", eq1, err)
	}
	eq2, err := s.fetchVenueEquityUSDT(context.Background(), rt)
	if err != nil || eq2 != 100 {
		t.Fatalf("缓存命中: eq=%.4f err=%v", eq2, err)
	}
	if got := fake.balanceCalls.Load(); got != 1 {
		t.Fatalf("缓存期内 FetchBalances 应只调用 1 次, got %d", got)
	}
}
