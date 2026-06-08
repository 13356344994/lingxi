package app

import (
	"context"
	"testing"

	"lingxi/internal/venue"
)

// fakeCapMgr 是 venue.CapitalManagement 的离线 fake(确定性、不触网、不真实划转)。
// 行为对齐 Bybit:目标全进 unified、余量留 funding;ExecuteRebalance 把资金账户的钱挪到统一账户(改写内部快照)。
type fakeCapMgr struct {
	uni, fund float64
	executed  []venue.Transfer
}

func (f *fakeCapMgr) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	return []venue.WalletSnapshot{
		{Wallet: "unified", Label: "统一交易账户", FreeUSDT: f.uni},
		{Wallet: "funding", Label: "资金账户", FreeUSDT: f.fund},
	}, f.uni + f.fund, nil
}

func (f *fakeCapMgr) PlanAllocation(total float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	if total < 0 {
		total = 0
	}
	return venue.CapitalAllocationPlan{
		TotalTradingUSDT: total,
		Targets:          map[venue.Wallet]float64{"unified": total},
		RemainderWallet:  "funding",
	}
}

func (f *fakeCapMgr) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	target := plan.Targets["unified"]
	diff := target - f.uni
	if diff > 1e-9 { // funding → unified
		amt := diff
		if amt > f.fund {
			amt = f.fund
		}
		f.fund -= amt
		f.uni += amt
		tr := venue.Transfer{From: "funding", To: "unified", AssetUSDT: amt, VenueRef: "fake"}
		f.executed = append(f.executed, tr)
		return []venue.Transfer{tr}, nil
	}
	if diff < -1e-9 { // unified → funding
		amt := -diff
		f.uni -= amt
		f.fund += amt
		tr := venue.Transfer{From: "unified", To: "funding", AssetUSDT: amt, VenueRef: "fake"}
		f.executed = append(f.executed, tr)
		return []venue.Transfer{tr}, nil
	}
	return nil, nil
}

func mkVenueController(t *testing.T, name string) *RuntimeController {
	t.Helper()
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore: %v", err)
	}
	c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController: %v", err)
	}
	c.venueName = name
	return c
}

func TestIsNonBinanceVenue(t *testing.T) {
	cases := map[string]bool{"": false, "binance": false, "BINANCE": false, "bybit": true, "okx": true}
	for name, want := range cases {
		c := &RuntimeController{venueName: name}
		if got := c.isNonBinanceVenue(); got != want {
			t.Errorf("isNonBinanceVenue(%q)=%v want %v", name, got, want)
		}
	}
}

func TestVenueSnapshotToRuntimeMapping(t *testing.T) {
	snaps := []venue.WalletSnapshot{
		{Wallet: "unified", Label: "统一交易账户", FreeUSDT: 0},
		{Wallet: "funding", Label: "资金账户", FreeUSDT: 149.6},
	}
	plan := venue.CapitalAllocationPlan{
		TotalTradingUSDT: 100,
		Targets:          map[venue.Wallet]float64{"unified": 100},
		RemainderWallet:  "funding",
	}
	out := venueSnapshotToRuntime(snaps, 149.6, plan)
	if len(out.Wallets) != 2 {
		t.Fatalf("应映射 2 个钱包, got %d", len(out.Wallets))
	}
	if out.Wallets[0].Wallet != "unified" || out.Wallets[0].DesiredUSDT != 100 {
		t.Errorf("统一账户目标错误: %+v", out.Wallets[0])
	}
	// 余量钱包目标 = 总额 - 目标和 = 149.6 - 100 = 49.6
	if out.Wallets[1].Wallet != "funding" || !approxEqBybit(out.Wallets[1].DesiredUSDT, 49.6) {
		t.Errorf("资金账户余量目标错误: %+v", out.Wallets[1])
	}
	if out.FundingFreeUSDT != 149.6 {
		t.Errorf("兼容字段 FundingFreeUSDT 应回填: %v", out.FundingFreeUSDT)
	}
	if out.TotalMovableUSDT != 149.6 {
		t.Errorf("总额错误: %v", out.TotalMovableUSDT)
	}
}

func TestAdjustVenueTradingCapitalMovesFundingToUnified(t *testing.T) {
	c := mkVenueController(t, "bybit")
	fake := &fakeCapMgr{uni: 0, fund: 149.6}
	orig := venueCapitalManagerFactory
	venueCapitalManagerFactory = func(name, sp string) (venue.CapitalManagement, venue.Capabilities, error) {
		return fake, venue.Capabilities{}, nil
	}
	defer func() { venueCapitalManagerFactory = orig }()

	res, err := c.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{TargetTradingUSDT: 100, Mode: "live"})
	if err != nil {
		t.Fatalf("AdjustTradingCapital: %v", err)
	}
	if len(res.Transfers) != 1 || res.Transfers[0].From != "funding" || res.Transfers[0].To != "unified" || res.Transfers[0].AmountUSDT != 100 {
		t.Fatalf("应一笔 funding→unified 100, got %+v", res.Transfers)
	}
	// 划转后:统一 100、资金 49.6
	var uniAfter, fundAfter float64
	for _, wRow := range res.After.Wallets {
		switch wRow.Wallet {
		case "unified":
			uniAfter = wRow.FreeUSDT
		case "funding":
			fundAfter = wRow.FreeUSDT
		}
	}
	if uniAfter != 100 || !approxEqBybit(fundAfter, 49.6) {
		t.Errorf("划转后余额错误: unified=%v funding=%v", uniAfter, fundAfter)
	}
	if res.AppliedTradingUSDT != 100 {
		t.Errorf("应用目标错误: %v", res.AppliedTradingUSDT)
	}
}

func TestAdjustVenueTradingCapitalRejectsOverTotal(t *testing.T) {
	c := mkVenueController(t, "bybit")
	fake := &fakeCapMgr{uni: 0, fund: 80}
	orig := venueCapitalManagerFactory
	venueCapitalManagerFactory = func(name, sp string) (venue.CapitalManagement, venue.Capabilities, error) {
		return fake, venue.Capabilities{}, nil
	}
	defer func() { venueCapitalManagerFactory = orig }()
	if _, err := c.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{TargetTradingUSDT: 100, Mode: "live"}); err == nil {
		t.Fatal("目标超过可调总额应报错")
	}
}

func TestAdjustVenueTradingCapitalRejectsMonthly(t *testing.T) {
	c := mkVenueController(t, "bybit")
	orig := venueCapitalManagerFactory
	venueCapitalManagerFactory = func(name, sp string) (venue.CapitalManagement, venue.Capabilities, error) {
		return &fakeCapMgr{fund: 100}, venue.Capabilities{}, nil
	}
	defer func() { venueCapitalManagerFactory = orig }()
	if _, err := c.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{TargetTradingUSDT: 50, Mode: "monthly"}); err == nil {
		t.Fatal("多所暂不支持 monthly 应报错")
	}
}

func TestCurrentVenueCapitalSnapshotDispatch(t *testing.T) {
	c := mkVenueController(t, "bybit")
	orig := venueCapitalManagerFactory
	venueCapitalManagerFactory = func(name, sp string) (venue.CapitalManagement, venue.Capabilities, error) {
		return &fakeCapMgr{uni: 30, fund: 70}, venue.Capabilities{}, nil
	}
	defer func() { venueCapitalManagerFactory = orig }()
	snap, err := c.CurrentCapitalSnapshot()
	if err != nil {
		t.Fatalf("CurrentCapitalSnapshot: %v", err)
	}
	if len(snap.Wallets) != 2 {
		t.Fatalf("应返回 2 个钱包, got %d", len(snap.Wallets))
	}
	if snap.TotalMovableUSDT != 100 {
		t.Errorf("总额错误: %v", snap.TotalMovableUSDT)
	}
}
