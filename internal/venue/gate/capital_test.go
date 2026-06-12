package gate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if len(got) != 1 || got[0].From != WalletSpot || got[0].To != WalletFutures || got[0].AssetUSDT != 100 {
		t.Fatalf("应一笔 spot→futures 100, got %+v", got)
	}
	if len(*transfers) != 1 {
		t.Fatalf("应发出 1 笔划转, got %d", len(*transfers))
	}
	tr := (*transfers)[0]
	if tr["currency"] != "USDT" || tr["from"] != "spot" || tr["to"] != "futures" || tr["settle"] != "usdt" {
		t.Errorf("划转体字段不符: %+v", tr)
	}
	if tr["amount"] != "100" {
		t.Errorf("amount 应为 \"100\", got %v", tr["amount"])
	}
}

func TestExecuteRebalanceGateReturnsExcessToSpot(t *testing.T) {
	a, transfers := capitalFakeGate(t, "0", "120")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletFutures || got[0].To != WalletSpot {
		t.Fatalf("应一笔 futures→spot, got %+v", got)
	}
	if got[0].AssetUSDT != 20 {
		t.Errorf("应回划 20, got %v", got[0].AssetUSDT)
	}
	tr := (*transfers)[0]
	if tr["from"] != "futures" || tr["to"] != "spot" || tr["amount"] != "20" {
		t.Errorf("回划体不符: %+v", tr)
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
