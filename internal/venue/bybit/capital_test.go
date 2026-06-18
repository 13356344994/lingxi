package bybit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestPlanAllocationBybit(t *testing.T) {
	a := &Adapter{}
	caps := a.Capabilities()
	plan := a.PlanAllocation(100, caps)
	if plan.TotalTradingUSDT != 100 {
		t.Errorf("TotalTradingUSDT 应回填 100, got %v", plan.TotalTradingUSDT)
	}
	if plan.Targets[WalletUnified] != 100 {
		t.Errorf("统一账户目标应为 100, got %v", plan.Targets[WalletUnified])
	}
	if plan.RemainderWallet != WalletFunding {
		t.Errorf("余量钱包应为 funding, got %v", plan.RemainderWallet)
	}
	if !caps.UnifiedWallet {
		t.Error("bybit should declare UnifiedWallet=true")
	}
	// 负数夹到 0
	if p := a.PlanAllocation(-5, caps); p.TotalTradingUSDT != 0 {
		t.Errorf("负总额应夹到 0, got %v", p.TotalTradingUSDT)
	}
}

// capitalFake 起一个能同时应答钱包查询(UNIFIED/FUND)与 inter-transfer 的 mock,并记录划转请求体。
func capitalFake(t *testing.T, unifiedUSDT, fundUSDT string) (*Adapter, *[]map[string]any) {
	t.Helper()
	var transfers []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathWalletBalance:
			_, _ = io.WriteString(w, `{"retCode":0,"retMsg":"OK","result":{"list":[{"accountType":"UNIFIED","coin":[`+
				`{"coin":"USDT","walletBalance":"`+unifiedUSDT+`","locked":"0","availableToWithdraw":"`+unifiedUSDT+`","usdValue":"`+unifiedUSDT+`"}]}]}}`)
		case pathAssetCoinsBalance:
			_, _ = io.WriteString(w, `{"retCode":0,"retMsg":"OK","result":{"balance":[`+
				`{"coin":"USDT","walletBalance":"`+fundUSDT+`","transferBalance":"`+fundUSDT+`"}]}}`)
		case pathInterTransfer:
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			transfers = append(transfers, m)
			_, _ = io.WriteString(w, `{"retCode":0,"retMsg":"OK","result":{"transferId":"`+m["transferId"].(string)+`","status":"SUCCESS"}}`)
		default:
			http.Error(w, "no route", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, &transfers
}

func TestExecuteRebalanceFundsUnifiedFromFunding(t *testing.T) {
	a, transfers := capitalFake(t, "0", "149.6")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletFunding || got[0].To != WalletUnified || got[0].AssetUSDT != 100 {
		t.Fatalf("应一笔 funding→unified 100, got %+v", got)
	}
	if len(*transfers) != 1 {
		t.Fatalf("应发出 1 笔划转, got %d", len(*transfers))
	}
	tr := (*transfers)[0]
	if tr["coin"] != "USDT" || tr["fromAccountType"] != "FUND" || tr["toAccountType"] != "UNIFIED" || tr["amount"] != "100" {
		t.Errorf("划转体不符: %+v", tr)
	}
	if _, ok := tr["transferId"].(string); !ok || tr["transferId"] == "" {
		t.Errorf("缺 transferId(幂等键): %+v", tr)
	}
}

func TestExecuteRebalanceReturnsExcessToFunding(t *testing.T) {
	a, transfers := capitalFake(t, "120", "0")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletUnified || got[0].To != WalletFunding {
		t.Fatalf("应一笔 unified→funding, got %+v", got)
	}
	if got[0].AssetUSDT != 20 {
		t.Errorf("应回划 20, got %v", got[0].AssetUSDT)
	}
	if (*transfers)[0]["fromAccountType"] != "UNIFIED" || (*transfers)[0]["amount"] != "20" {
		t.Errorf("回划体不符: %+v", (*transfers)[0])
	}
}

func TestExecuteRebalanceNoopWhenInPlace(t *testing.T) {
	a, transfers := capitalFake(t, "100", "49.6")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 0 || len(*transfers) != 0 {
		t.Fatalf("已就位应零划转, got=%+v transfers=%d", got, len(*transfers))
	}
}

func TestExecuteRebalanceRejectsOverTotal(t *testing.T) {
	a, _ := capitalFake(t, "0", "149.6")
	plan := a.PlanAllocation(200, a.Capabilities()) // 目标 200 > 总额 149.6
	if _, err := a.ExecuteRebalance(context.Background(), plan); err == nil {
		t.Fatal("目标超过可调总额应报错")
	}
}

func TestExecuteRebalanceCapsByFundingAvailable(t *testing.T) {
	// 统一 0、资金 80,目标 100 > 总额 80 → 报错(不会划超过可用)。
	a, _ := capitalFake(t, "0", "80")
	plan := a.PlanAllocation(100, a.Capabilities())
	if _, err := a.ExecuteRebalance(context.Background(), plan); err == nil {
		t.Fatal("目标超过总额应报错")
	}
}

func TestTransferUSDTSignedPOSTHeaders(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"transferId": "T1", "status": "SUCCESS"}})
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	a.t.nowFn = func() time.Time { return time.UnixMilli(1700000000000).UTC() }
	tr, err := a.transferUSDT(context.Background(), WalletFunding, WalletUnified, 50)
	if err != nil {
		t.Fatalf("transferUSDT: %v", err)
	}
	if tr.VenueRef != "inter-transfer:T1" {
		t.Errorf("VenueRef 不符: %s", tr.VenueRef)
	}
	if hdr.Get("X-BAPI-API-KEY") != "K" || hdr.Get("X-BAPI-SIGN") == "" || hdr.Get("Content-Type") != "application/json" {
		t.Errorf("签名 POST 头缺失: %v", hdr)
	}
}

func TestBybitAccountTypeMapping(t *testing.T) {
	if v, _ := bybitAccountType(WalletUnified); v != "UNIFIED" {
		t.Errorf("unified→UNIFIED, got %s", v)
	}
	if v, _ := bybitAccountType(WalletFunding); v != "FUND" {
		t.Errorf("funding→FUND, got %s", v)
	}
	if _, err := bybitAccountType(venue.Wallet("spot")); err == nil {
		t.Error("未知钱包应报错")
	}
}
