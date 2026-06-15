package app

import (
	"context"
	"errors"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
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

func TestVenueEquityFromBalancesAndPositions(t *testing.T) {
	bal := venue.Balances{ByAsset: map[string]venue.Balance{
		"USDT": {Asset: "USDT", Free: 80, Locked: 20},
		"BTC":  {Asset: "BTC", WalletUSD: 50},
	}}
	positions := []venue.Position{
		{UnrealizedPnL: 5},
		{UnrealizedPnL: -2},
	}
	snap := venueEquityFromBalancesAndPositions(bal, positions)
	if snap.WalletUSDT != 150 {
		t.Fatalf("wallet equity = %.4f, want 150(不含浮盈亏)", snap.WalletUSDT)
	}
	if snap.UnrealizedPnLUSDT != 3 {
		t.Fatalf("unrealized_pnl = %.4f, want 3", snap.UnrealizedPnLUSDT)
	}
}

func TestTodayReturnRate(t *testing.T) {
	if got := todayReturnRate(0.35, 100); math.Abs(got-0.0035) > 1e-9 {
		t.Fatalf("todayReturnRate = %.8f, want 0.0035", got)
	}
	if got := todayReturnRate(1, 0); got != 0 {
		t.Fatalf("equity=0 时应返回 0, got %.8f", got)
	}
	if got := todayReturnRate(1, -10); got != 0 {
		t.Fatalf("equity<0 时应返回 0, got %.8f", got)
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

	if eq := byVenue["okx"].EquityUSDT; eq != 150 {
		t.Fatalf("okx equity_usdt = %.4f, want 150(钱包本金,不含浮盈亏)", eq)
	}
	if upnl := byVenue["okx"].UnrealizedPnLUSDT; upnl != 5 {
		t.Fatalf("okx unrealized_pnl_usdt = %.4f, want 5", upnl)
	}
	if byVenue["gate"].EquityUSDT != 0 {
		t.Fatalf("gate equity_usdt 失败时应为 0, got %.4f", byVenue["gate"].EquityUSDT)
	}
	if byVenue["gate"].CapitalError == "" {
		t.Fatal("gate 拉取失败应标 capital_error")
	}
	if total.EquityUSDT != 150 {
		t.Fatalf("total equity_usdt = %.4f, want 150(仅累加成功所)", total.EquityUSDT)
	}
	if total.UnrealizedPnLUSDT != 5 {
		t.Fatalf("total unrealized_pnl_usdt = %.4f, want 5", total.UnrealizedPnLUSDT)
	}
}

func TestBuildVenuesFinancialSnapshotReturnRateZeroEquity(t *testing.T) {
	bad := &equityTestVenue{
		name:        "gate",
		balancesErr: errors.New("rate limited"),
	}
	exec := &CrossVenueExecutor{venues: map[string]venue.Venue{"gate": bad}}
	orch := NewOrchestrator()
	orch.Register(&venueRuntime{name: "gate"})
	s := &DashboardServer{orchestrator: orch, crossVenueExecutor: exec}
	byVenue, total, _ := s.buildVenuesFinancialSnapshot(time.Now())
	if byVenue["gate"].TodayReturnRate != 0 {
		t.Fatalf("equity=0 时 today_return_rate 应为 0, got %.8f", byVenue["gate"].TodayReturnRate)
	}
	if total.TodayReturnRate != 0 {
		t.Fatalf("total equity=0 时 today_return_rate 应为 0, got %.8f", total.TodayReturnRate)
	}
}

func TestFetchVenueEquitySnapshotUsesCache(t *testing.T) {
	fake := &equityTestVenue{
		name: "binance",
		balances: venue.Balances{ByAsset: map[string]venue.Balance{
			"USDT": {Asset: "USDT", Free: 100},
		}},
		positions: []venue.Position{{UnrealizedPnL: -10}},
	}
	exec := &CrossVenueExecutor{venues: map[string]venue.Venue{"binance": fake}}
	rt := &venueRuntime{name: "binance"}
	s := &DashboardServer{crossVenueExecutor: exec}

	snap1, err := s.fetchVenueEquitySnapshot(context.Background(), rt)
	if err != nil || snap1.WalletUSDT != 100 || snap1.UnrealizedPnLUSDT != -10 {
		t.Fatalf("首次拉取: snap=%+v err=%v", snap1, err)
	}
	snap2, err := s.fetchVenueEquitySnapshot(context.Background(), rt)
	if err != nil || snap2.WalletUSDT != 100 || snap2.UnrealizedPnLUSDT != -10 {
		t.Fatalf("缓存命中: snap=%+v err=%v", snap2, err)
	}
	if got := fake.balanceCalls.Load(); got != 1 {
		t.Fatalf("缓存期内 FetchBalances 应只调用 1 次, got %d", got)
	}
}

// TestBinanceLiveWalletEquity 对真实 Binance 账户验证钱包口径 equity 不含浮盈亏。
// 运行:BINANCE_LIVE=1 go test ./internal/app -run TestBinanceLiveWalletEquity -v
func TestBinanceLiveWalletEquity(t *testing.T) {
	if os.Getenv("BINANCE_LIVE") != "1" {
		t.Skip("设置 BINANCE_LIVE=1 运行 Binance 钱包口径活体验证")
	}
	secretPath := os.Getenv("BINANCE_SECRET_PATH")
	if secretPath == "" {
		secretPath = "../../secret.yaml"
	}
	sec, err := LoadVenueSecretConfig(secretPath, binance.VenueName)
	if err != nil {
		t.Fatalf("加载 Binance 凭证失败: %v", err)
	}
	client, err := NewAuthenticatedBinanceClient(sec, NetworkConfig{HTTPTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("构造 Binance client 失败: %v", err)
	}
	adp := binance.New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	bal, err := adp.FetchBalances(ctx)
	if err != nil {
		t.Fatalf("FetchBalances 失败: %v", err)
	}
	positions, err := adp.FetchPositions(ctx)
	if err != nil {
		t.Fatalf("FetchPositions 失败: %v", err)
	}
	snap := venueEquityFromBalancesAndPositions(bal, positions)
	oldEquity := walletUSDTFromBalances(bal)
	for _, p := range positions {
		oldEquity = oldEquity + p.UnrealizedPnL // 旧口径对照
	}
	t.Logf("[Binance] wallet_usdt(equity_usdt)=%.4f unrealized_pnl=%.4f old_equity_with_upnl=%.4f positions=%d",
		snap.WalletUSDT, snap.UnrealizedPnLUSDT, oldEquity, len(positions))
	if snap.WalletUSDT <= 0 {
		t.Errorf("Binance wallet_usdt 应 > 0, got %.4f", snap.WalletUSDT)
	}
	if math.Abs(snap.WalletUSDT-(oldEquity-snap.UnrealizedPnLUSDT)) > 1e-6 {
		t.Errorf("wallet 口径应等于 old_equity - unrealized, got wallet=%.4f diff=%.4f",
			snap.WalletUSDT, snap.WalletUSDT-(oldEquity-snap.UnrealizedPnLUSDT))
	}
}

// TestAllVenuesLiveWalletEquity 活体验证四所钱包口径 equity 与 total(不含浮盈亏)。
// 运行:VENUE_EQUITY_VERIFY=1 go test ./internal/app -run TestAllVenuesLiveWalletEquity -v
func TestAllVenuesLiveWalletEquity(t *testing.T) {
	if os.Getenv("VENUE_EQUITY_VERIFY") != "1" {
		t.Skip("设置 VENUE_EQUITY_VERIFY=1 运行四所钱包口径活体验证")
	}
	secretPath := os.Getenv("BINANCE_SECRET_PATH")
	if secretPath == "" {
		secretPath = "../../secret.yaml"
	}
	cfg := EngineConfig{NetworkConfig: NetworkConfig{HTTPTimeout: 15 * time.Second}}
	venueNames := []string{"binance", "bybit", "gate", "okx"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var totalWallet, totalUpnl float64
	for _, name := range venueNames {
		adp, err := buildCrossVenueReadOnlyAdapter(name, cfg, secretPath)
		if err != nil {
			t.Logf("[%s] skip: %v", name, err)
			continue
		}
		account, ok := adp.(venue.AccountData)
		if !ok {
			t.Logf("[%s] skip: 非 AccountData", name)
			continue
		}
		bal, err := account.FetchBalances(ctx)
		if err != nil {
			t.Logf("[%s] FetchBalances err: %v", name, err)
			continue
		}
		positions, err := account.FetchPositions(ctx)
		if err != nil {
			t.Logf("[%s] FetchPositions err: %v", name, err)
			continue
		}
		snap := venueEquityFromBalancesAndPositions(bal, positions)
		t.Logf("[%s] wallet(equity_usdt)=%.4f unrealized_pnl=%.4f positions=%d",
			name, snap.WalletUSDT, snap.UnrealizedPnLUSDT, len(positions))
		totalWallet += snap.WalletUSDT
		totalUpnl += snap.UnrealizedPnLUSDT
	}
	t.Logf("[TOTAL] wallet(equity_usdt)=%.4f unrealized_pnl=%.4f", totalWallet, totalUpnl)
}
