package okx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"lingxi/internal/venue"
)

func TestPlanAllocationOKX(t *testing.T) {
	a := &Adapter{}
	caps := a.Capabilities()
	plan := a.PlanAllocation(100, caps)
	if plan.TotalTradingUSDT != 100 {
		t.Errorf("TotalTradingUSDT 应回填 100, got %v", plan.TotalTradingUSDT)
	}
	if plan.Targets[WalletTrading] != 100 {
		t.Errorf("交易账户目标应为 100, got %v", plan.Targets[WalletTrading])
	}
	if plan.RemainderWallet != WalletFunding {
		t.Errorf("余量钱包应为 funding, got %v", plan.RemainderWallet)
	}
	if !caps.SupportsCapitalRebalance || len(caps.CapitalWallets) != 2 {
		t.Errorf("能力声明不符: %+v", caps)
	}
	if !caps.UnifiedWallet {
		t.Error("okx should declare UnifiedWallet=true")
	}
}

func capitalFakeOKX(t *testing.T, tradingUSDT, fundingUSDT string) (*Adapter, *[]map[string]any) {
	t.Helper()
	var transfers []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathBalance:
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"details":[`+
				`{"ccy":"USDT","availBal":"`+tradingUSDT+`","cashBal":"`+tradingUSDT+`"}]}]}`)
		case pathAssetBalances:
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[`+
				`{"ccy":"USDT","availBal":"`+fundingUSDT+`","bal":"`+fundingUSDT+`"}]}`)
		case pathAssetTransfer:
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			transfers = append(transfers, m)
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"transId":"T1"}]}`)
		default:
			http.Error(w, "no route "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{
		Name: VenueName, APIKey: "K", APISecret: "S", Passphrase: "P",
		Extra: map[string]string{"base_url": srv.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, &transfers
}

func TestCaptureMovableSnapshotOKX(t *testing.T) {
	a, _ := capitalFakeOKX(t, "30.5", "119.1")
	snaps, total, err := a.CaptureMovableSnapshot(context.Background())
	if err != nil {
		t.Fatalf("CaptureMovableSnapshot: %v", err)
	}
	if total != 149.6 {
		t.Errorf("总额应为 149.6, got %v", total)
	}
	if len(snaps) != 2 {
		t.Fatalf("应 2 个钱包, got %d", len(snaps))
	}
	var trading, funding float64
	for _, s := range snaps {
		switch s.Wallet {
		case WalletTrading:
			trading = s.FreeUSDT
		case WalletFunding:
			funding = s.FreeUSDT
		}
	}
	if trading != 30.5 || funding != 119.1 {
		t.Errorf("快照不符: trading=%v funding=%v", trading, funding)
	}
}

func TestExecuteRebalanceOKXFundsTradingFromFunding(t *testing.T) {
	a, transfers := capitalFakeOKX(t, "0", "149.6")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletFunding || got[0].To != WalletTrading || got[0].AssetUSDT != 100 {
		t.Fatalf("应一笔 funding→trading 100, got %+v", got)
	}
	if len(*transfers) != 1 {
		t.Fatalf("应发出 1 笔划转, got %d", len(*transfers))
	}
	tr := (*transfers)[0]
	if tr["ccy"] != "USDT" || tr["from"] != "6" || tr["to"] != "18" || tr["type"] != "0" {
		t.Errorf("划转体字段不符: %+v", tr)
	}
	if tr["amt"] != "100" {
		t.Errorf("amt 应为非科学计数法 \"100\", got %v", tr["amt"])
	}
	if cid, ok := tr["clientId"].(string); !ok || cid == "" {
		t.Errorf("缺 clientId(幂等键): %+v", tr)
	}
}

func TestExecuteRebalanceOKXReturnsExcessToFunding(t *testing.T) {
	a, transfers := capitalFakeOKX(t, "120", "0")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 1 || got[0].From != WalletTrading || got[0].To != WalletFunding {
		t.Fatalf("应一笔 trading→funding, got %+v", got)
	}
	if got[0].AssetUSDT != 20 {
		t.Errorf("应回划 20, got %v", got[0].AssetUSDT)
	}
	tr := (*transfers)[0]
	if tr["from"] != "18" || tr["to"] != "6" || tr["amt"] != "20" {
		t.Errorf("回划体不符: %+v", tr)
	}
}

func TestExecuteRebalanceOKXNoopWhenInPlace(t *testing.T) {
	a, transfers := capitalFakeOKX(t, "100", "49.6")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("ExecuteRebalance: %v", err)
	}
	if len(got) != 0 || len(*transfers) != 0 {
		t.Fatalf("已就位应零划转, got=%+v transfers=%d", got, len(*transfers))
	}
}

func TestExecuteRebalanceOKXSkipsTinyAmount(t *testing.T) {
	a, transfers := capitalFakeOKX(t, "99.999999995", "0")
	plan := a.PlanAllocation(100, a.Capabilities())
	got, err := a.ExecuteRebalance(context.Background(), plan)
	if err != nil {
		t.Fatalf("微小差额应跳过不报错: %v", err)
	}
	if len(got) != 0 || len(*transfers) != 0 {
		t.Fatalf("微小差额应零划转, got=%+v transfers=%d", got, len(*transfers))
	}
}

func TestExecuteRebalanceOKXRejectsOverTotal(t *testing.T) {
	a, _ := capitalFakeOKX(t, "0", "80")
	plan := a.PlanAllocation(100, a.Capabilities())
	if _, err := a.ExecuteRebalance(context.Background(), plan); err == nil {
		t.Fatal("目标超过可调总额应报错")
	}
}

func TestOKXTransferAcctMapping(t *testing.T) {
	if v, _ := okxTransferAcct(WalletFunding); v != "6" {
		t.Errorf("funding→6, got %s", v)
	}
	if v, _ := okxTransferAcct(WalletTrading); v != "18" {
		t.Errorf("trading→18, got %s", v)
	}
	if _, err := okxTransferAcct(venue.Wallet("spot")); err == nil {
		t.Error("未知钱包应报错")
	}
}

func TestFormatTransferUSDTNoScientific(t *testing.T) {
	if s := formatTransferUSDT(0.00000001); s != "0.00000001" {
		t.Errorf("formatTransferUSDT 应固定小数, got %q", s)
	}
	if s := formatTransferUSDT(100); s != "100" {
		t.Errorf("formatTransferUSDT(100)=%q", s)
	}
}
