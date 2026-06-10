package venue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubVenue 是一个最小 Venue 实现,只用于验证接口可被满足 + 注册表往返。
type stubVenue struct{ name string }

func (s stubVenue) Name() string                                               { return s.name }
func (s stubVenue) Capabilities() Capabilities                                 { return Capabilities{SupportsNegativeCarry: true} }
func (s stubVenue) ToVenue(c Symbol, m Market) (string, error)                 { return string(c), nil }
func (s stubVenue) ToCanonical(raw string, m Market) (Symbol, error)           { return Symbol(raw), nil }
func (s stubVenue) FetchInstruments(ctx context.Context) ([]Instrument, error) { return nil, nil }
func (s stubVenue) FetchFundingSnapshot(ctx context.Context) ([]FundingSnapshot, error) {
	return nil, nil
}
func (s stubVenue) FetchBookTicker(ctx context.Context, m Market, sym Symbol) (BookTicker, error) {
	return BookTicker{}, nil
}
func (s stubVenue) FetchDepth(ctx context.Context, m Market, sym Symbol, limit int) (Depth, error) {
	return Depth{}, nil
}
func (s stubVenue) FetchOpenInterest(ctx context.Context, sym Symbol) (float64, error) { return 0, nil }
func (s stubVenue) FetchRecentQuoteVolume(ctx context.Context, m Market, sym Symbol, w time.Duration) (float64, error) {
	return 0, nil
}
func (s stubVenue) ServerTime(ctx context.Context) (time.Time, error)      { return time.Time{}, nil }
func (s stubVenue) FetchBalances(ctx context.Context) (Balances, error)    { return Balances{}, nil }
func (s stubVenue) FetchPositions(ctx context.Context) ([]Position, error) { return nil, nil }
func (s stubVenue) FetchTradingCostProfile(ctx context.Context, syms []Symbol) (CostProfile, error) {
	return CostProfile{}, nil
}
func (s stubVenue) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return 0, ErrUnsupported
}
func (s stubVenue) FetchLeverageBrackets(ctx context.Context, sym Symbol) ([]LeverageBracket, error) {
	return nil, nil
}
func (s stubVenue) VerifyAPIKeyPosture(ctx context.Context) (KeyPosture, error) {
	return KeyPosture{}, nil
}
func (s stubVenue) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error) {
	return OrderResult{}, nil
}
func (s stubVenue) PlaceBatch(ctx context.Context, reqs []OrderRequest) ([]OrderResult, error) {
	return nil, nil
}
func (s stubVenue) CancelOrder(ctx context.Context, m Market, sym Symbol, id string) error {
	return nil
}
func (s stubVenue) QueryOrder(ctx context.Context, m Market, sym Symbol, id string) (OrderResult, error) {
	return OrderResult{}, nil
}
func (s stubVenue) SetLeverage(ctx context.Context, sym Symbol, lev int) error           { return nil }
func (s stubVenue) SetMarginMode(ctx context.Context, sym Symbol, mode MarginMode) error { return nil }
func (s stubVenue) BorrowRepay(ctx context.Context, asset string, amt float64, dir BorrowDir) error {
	return ErrUnsupported
}
func (s stubVenue) FetchRealizedFunding(ctx context.Context, sym Symbol, since time.Time) ([]FundingIncome, error) {
	return nil, nil
}
func (s stubVenue) FetchUserTrades(ctx context.Context, m Market, sym Symbol, since time.Time) ([]Trade, error) {
	return nil, nil
}
func (s stubVenue) StreamBookTicker(ctx context.Context, syms []Symbol) (<-chan BookTicker, error) {
	return nil, nil
}
func (s stubVenue) StreamDepth(ctx context.Context, syms []Symbol) (<-chan Depth, error) {
	return nil, nil
}
func (s stubVenue) StreamUserData(ctx context.Context) (<-chan UserEvent, error) { return nil, nil }
func (s stubVenue) Classify(err error) (string, string)                          { return "request_error", "minor" }
func (s stubVenue) ReserveOrderBudget(units int) bool                            { return true }
func (s stubVenue) ReleaseOrderBudget(units int)                                 {}
func (s stubVenue) ArmDeadMansSwitch(ctx context.Context, c time.Duration) error {
	return ErrUnsupported
}
func (s stubVenue) CaptureMovableSnapshot(ctx context.Context) ([]WalletSnapshot, float64, error) {
	return nil, 0, nil
}
func (s stubVenue) PlanAllocation(total float64, caps Capabilities) CapitalAllocationPlan {
	return CapitalAllocationPlan{TotalTradingUSDT: total}
}
func (s stubVenue) ExecuteRebalance(ctx context.Context, plan CapitalAllocationPlan) ([]Transfer, error) {
	return nil, nil
}

// 编译期断言:stubVenue 满足 Venue 接口。
var _ Venue = stubVenue{}

func TestRegistryBuildAndRegistered(t *testing.T) {
	r := NewRegistry()
	r.Register("binance", func(cfg Config) (Venue, error) { return stubVenue{name: cfg.Name}, nil })

	v, err := r.Build(Config{Name: "binance"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if v.Name() != "binance" {
		t.Fatalf("Name() = %q, want binance", v.Name())
	}
	if !v.Capabilities().SupportsNegativeCarry {
		t.Fatal("expected SupportsNegativeCarry=true from stub")
	}
	if got := r.Registered(); len(got) != 1 || got[0] != "binance" {
		t.Fatalf("Registered() = %v, want [binance]", got)
	}
}

func TestRegistryBuildUnknown(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Build(Config{Name: "ftx"}); err == nil {
		t.Fatal("expected error for unregistered venue")
	}
}

func TestVenueErrorUnwrap(t *testing.T) {
	root := errors.New("boom")
	ve := NewVenueError("binance", "request_timeout", "minor", "-1007", true, root)
	if !errors.Is(ve, root) {
		t.Fatal("expected errors.Is to find wrapped cause")
	}
	if ve.Category != "request_timeout" || !ve.Retryable {
		t.Fatalf("unexpected venue error fields: %+v", ve)
	}
}
