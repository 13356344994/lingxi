package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lingxi/internal/venue"
)

func TestPlanAllocationGate(t *testing.T) {
	a := &Adapter{}
	caps := a.Capabilities()
	plan := a.PlanAllocation(1000, caps)
	if plan.TotalTradingUSDT != 1000 {
		t.Errorf("TotalTradingUSDT 应回填: %v", plan.TotalTradingUSDT)
	}
	if plan.Targets[WalletFutures] != 1000 {
		t.Errorf("永续目标应为 1000, got %v", plan.Targets[WalletFutures])
	}
	if plan.RemainderWallet != WalletSpot {
		t.Errorf("余量钱包应为 spot, got %v", plan.RemainderWallet)
	}
	if !caps.SupportsCapitalRebalance || len(caps.CapitalWallets) != 2 {
		t.Errorf("能力声明不符: %+v", caps)
	}
	if !caps.UnifiedWallet {
		t.Error("gate should declare UnifiedWallet=true")
	}
}

func capitalFakeGate(t *testing.T, spotUSDT, futuresUSDT string) (*Adapter, *[]map[string]any) {
	t.Helper()
	var transfers []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathSpotAccounts:
			_, _ = io.WriteString(w, `[{"currency":"USDT","available":"`+spotUSDT+`","locked":"0"}]`)
		case pathFuturesAccounts:
			_, _ = io.WriteString(w, `{"currency":"USDT","available":"`+futuresUSDT+`","total":"`+futuresUSDT+`"}`)
		case pathWalletTransfers:
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			transfers = append(transfers, m)
			_, _ = io.WriteString(w, `{"id":"tx-1"}`)
		default:
			http.Error(w, "no route "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, &transfers
}

func TestCaptureMovableSnapshotGate(t *testing.T) {
	a, _ := capitalFakeGate(t, "80", "70")
	snaps, total, err := a.CaptureMovableSnapshot(context.Background())
	if err != nil {
		t.Fatalf("CaptureMovableSnapshot: %v", err)
	}
	if total != 150 {
		t.Errorf("总额应为 150, got %v", total)
	}
	if len(snaps) != 2 {
		t.Fatalf("应 2 个钱包, got %d", len(snaps))
	}
}

func TestExecuteRebalanceGateFundsFuturesFromSpot(t *testing.T) {
	a, transfers := capitalFakeGate(t, "149.6", "0")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletSpot || got[0].To != WalletFutures || got[0].VenueRef != "unified-wallet-noop" {
		t.Fatalf("统一账户应 no-op 成功, got %+v", got)
	}
	if len(*transfers) != 0 {
		t.Fatalf("统一账户不应调用旧 wallet/transfers, got %d calls", len(*transfers))
	}
}

func TestExecuteRebalanceGateReturnsExcessToSpot(t *testing.T) {
	a, transfers := capitalFakeGate(t, "0", "120")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletFutures || got[0].To != WalletSpot || got[0].VenueRef != "unified-wallet-noop" {
		t.Fatalf("统一账户应 no-op 成功, got %+v", got)
	}
	if len(*transfers) != 0 {
		t.Fatalf("统一账户不应调用旧 wallet/transfers, got %d calls", len(*transfers))
	}
}

func TestExecuteRebalanceGateNoopWhenInPlace(t *testing.T) {
	a, transfers := capitalFakeGate(t, "49.6", "100")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 0 || len(*transfers) != 0 {
		t.Fatalf("已就位应零划转, got=%+v transfers=%d", got, len(*transfers))
	}
}

func TestExecuteRebalanceGateSkipsTinyAmount(t *testing.T) {
	a, transfers := capitalFakeGate(t, "0.00000001", "99.999999995")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("微小差额应跳过不报错: %v", err)
	}
	if len(got) != 0 || len(*transfers) != 0 {
		t.Fatalf("微小差额应零划转, got=%+v transfers=%d", got, len(*transfers))
	}
}

func TestExecuteRebalanceGateRejectsOverTotal(t *testing.T) {
	a, _ := capitalFakeGate(t, "80", "0")
	plan := a.PlanAllocation(100, a.Capabilities())
	if _, err := a.ExecuteRebalance(context.Background(), plan); err == nil {
		t.Fatal("目标超过可调总额应报错")
	}
}

func TestTransferWallet(t *testing.T) {
	cases := []struct {
		name       string
		from, to   string
		amount     float64
		wantCall   bool
		wantFrom   string
		wantTo     string
		wantAmount string
		wantErr    string
	}{
		{name: "spot_to_futures", from: "spot", to: "futures", amount: 12.5, wantCall: false},
		{name: "futures_to_spot", from: "futures", to: "spot", amount: 7.25, wantCall: false},
		{name: "trim_and_case", from: " FUTURES ", to: " spot ", amount: 3, wantCall: false},
		{name: "zero_noop", from: "spot", to: "futures", amount: 0},
		{name: "negative_noop", from: "spot", to: "futures", amount: -1},
		{name: "unsupported_margin", from: "spot", to: "margin", amount: 10, wantErr: "unsupported"},
		{name: "unsupported_funding", from: "funding", to: "spot", amount: 10, wantErr: "unsupported"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var transfers []map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != pathWalletTransfers {
					http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				b, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				transfers = append(transfers, m)
				_, _ = io.WriteString(w, `{"id":"tx-tw-1"}`)
			}))
			t.Cleanup(srv.Close)
			a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = a.TransferWallet(context.Background(), tc.from, tc.to, tc.amount)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				if len(transfers) != 0 {
					t.Fatalf("unsupported direction should not call transfer, got %d calls", len(transfers))
				}
				return
			}
			if err != nil {
				t.Fatalf("TransferWallet: %v", err)
			}
			if !tc.wantCall {
				if len(transfers) != 0 {
					t.Fatalf("expected no transfer call, got %d", len(transfers))
				}
				return
			}
			if len(transfers) != 1 {
				t.Fatalf("expected 1 transfer call, got %d", len(transfers))
			}
			tr := transfers[0]
			if tr["from"] != tc.wantFrom || tr["to"] != tc.wantTo {
				t.Fatalf("transfer direction = %+v, want from=%q to=%q", tr, tc.wantFrom, tc.wantTo)
			}
			if tr["currency"] != "USDT" || tr["settle"] != "usdt" {
				t.Fatalf("transfer fields = %+v", tr)
			}
			if tr["amount"] != tc.wantAmount {
				t.Fatalf("amount = %v, want %q", tr["amount"], tc.wantAmount)
			}
		})
	}
}

func TestGateTransferWalletMapping(t *testing.T) {
	if v, _ := gateTransferWallet(WalletSpot); v != "spot" {
		t.Errorf("spot→spot, got %s", v)
	}
	if v, _ := gateTransferWallet(WalletFutures); v != "futures" {
		t.Errorf("futures→futures, got %s", v)
	}
	if _, err := gateTransferWallet(venue.Wallet("funding")); err == nil {
		t.Error("未知钱包应报错")
	}
}

func TestGateUnifiedTransferNoOp(t *testing.T) {
	transferCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathWalletTransfers {
			transferCalled = true
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"label":"PERPETUAL_CONTRACT_TRANSFER_USDT_EXCEPTION","message":"USDT Perpetual assets and positions have been migrated to the Unified Account. Please do not transfer USDT to the futures account."}`)
			return
		}
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr, err := a.transferUSDT(context.Background(), WalletSpot, WalletFutures, 100)
	if err != nil {
		t.Fatalf("transferUSDT unified no-op: %v", err)
	}
	if transferCalled {
		t.Fatal("统一账户 proactive no-op 不应触网")
	}
	if tr.AssetUSDT != 0 || tr.VenueRef != "unified-wallet-noop" {
		t.Fatalf("unexpected no-op transfer: %+v", tr)
	}
	if err := a.TransferWallet(context.Background(), "spot", "futures", 50); err != nil {
		t.Fatalf("TransferWallet unified no-op: %v", err)
	}
}

func TestGateUnifiedTransferNoOpErrorFallback(t *testing.T) {
	err := fmt.Errorf(`gate: label=PERPETUAL_CONTRACT_TRANSFER_USDT_EXCEPTION message="USDT Perpetual assets and positions have been migrated to the Unified Account. Please do not transfer USDT to the futures account."`)
	if !isGateUnifiedTransferNoOpError(err) {
		t.Fatalf("expected unified transfer no-op error, got %v", err)
	}
}
