package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

type walletTransferCall struct {
	from, to string
	amount   float64
}

type fakeWalletTransferVenue struct {
	fakeFundingVenue
	calls []walletTransferCall
	err   error
}

func (f *fakeWalletTransferVenue) TransferWallet(_ context.Context, from, to string, amountUSDT float64) error {
	f.calls = append(f.calls, walletTransferCall{from: from, to: to, amount: amountUSDT})
	return f.err
}

func testCapitalRouterExecLogger(buf *bytes.Buffer) *log.Logger {
	return log.New(buf, "", 0)
}

func TestExecuteCapitalRoutingLiveCallsTransfers(t *testing.T) {
	t.Parallel()
	fake := &fakeWalletTransferVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}}
	plan := CapitalRoutingPlan{
		WithinVenueTransfers: []WalletTransfer{
			{Venue: "bybit", From: WalletKindFutures, To: WalletKindSpot, AmountUSDT: 80, Reason: "test"},
			{Venue: "binance", From: WalletKindSpot, To: WalletKindFutures, AmountUSDT: 50, Reason: "test2"},
		},
	}
	binanceFake := &fakeWalletTransferVenue{fakeFundingVenue: fakeFundingVenue{name: "binance"}}
	venues := map[string]venue.Venue{
		"bybit":   fake,
		"binance": binanceFake,
	}

	executed, errs := ExecuteCapitalRouting(context.Background(), plan, venues, true, testCapitalRouterExecLogger(&bytes.Buffer{}))
	if len(errs) != 0 {
		t.Fatalf("errs = %#v, want none", errs)
	}
	if executed != 2 {
		t.Fatalf("executed = %d, want 2", executed)
	}
	if len(fake.calls) != 1 || fake.calls[0].from != "futures" || fake.calls[0].to != "spot" || fake.calls[0].amount != 80 {
		t.Fatalf("bybit calls = %+v", fake.calls)
	}
	if len(binanceFake.calls) != 1 || binanceFake.calls[0].from != "spot" || binanceFake.calls[0].to != "futures" || binanceFake.calls[0].amount != 50 {
		t.Fatalf("binance calls = %+v", binanceFake.calls)
	}
}

func TestExecuteCapitalRoutingShadowNoRealCalls(t *testing.T) {
	t.Parallel()
	fake := &fakeWalletTransferVenue{fakeFundingVenue: fakeFundingVenue{name: "bybit"}}
	plan := CapitalRoutingPlan{
		WithinVenueTransfers: []WalletTransfer{
			{Venue: "bybit", From: WalletKindFutures, To: WalletKindSpot, AmountUSDT: 80, Reason: "shadow-test"},
		},
	}
	var buf bytes.Buffer
	executed, errs := ExecuteCapitalRouting(context.Background(), plan, map[string]venue.Venue{"bybit": fake}, false, testCapitalRouterExecLogger(&buf))
	if len(errs) != 0 {
		t.Fatalf("errs = %#v, want none", errs)
	}
	if executed != 0 {
		t.Fatalf("executed = %d, want 0 in shadow mode", executed)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("shadow mode should not call TransferWallet, got %+v", fake.calls)
	}
	logged := buf.String()
	if !strings.Contains(logged, "[影子]") || !strings.Contains(logged, "bybit") || !strings.Contains(logged, "futures→spot") || !strings.Contains(logged, "80.0000") {
		t.Fatalf("shadow log = %q", logged)
	}
}

func TestExecuteCapitalRoutingCrossVenueNeverTransfers(t *testing.T) {
	t.Parallel()
	fake := &fakeWalletTransferVenue{fakeFundingVenue: fakeFundingVenue{name: "gate"}}
	plan := CapitalRoutingPlan{
		CrossVenueSuggestions: []CrossVenueRebalanceSuggestion{
			{FromVenue: "binance", ToVenue: "gate", AmountUSDT: 120, Reason: "cross-gap"},
		},
	}
	var buf bytes.Buffer
	executed, errs := ExecuteCapitalRouting(context.Background(), plan, map[string]venue.Venue{"gate": fake}, true, testCapitalRouterExecLogger(&buf))
	if len(errs) != 0 {
		t.Fatalf("errs = %#v, want none", errs)
	}
	if executed != 0 {
		t.Fatalf("executed = %d, want 0", executed)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("cross-venue suggestions must not trigger transfers, got %+v", fake.calls)
	}
	logged := buf.String()
	if !strings.Contains(logged, "[跨所建议]") || !strings.Contains(logged, "binance→gate") || !strings.Contains(logged, "不自动执行") {
		t.Fatalf("cross-venue log = %q", logged)
	}
}

func TestExecuteCapitalRoutingUnsupportedVenueRecordsError(t *testing.T) {
	t.Parallel()
	plan := CapitalRoutingPlan{
		WithinVenueTransfers: []WalletTransfer{
			{Venue: "okx", From: WalletKindSpot, To: WalletKindFutures, AmountUSDT: 10, Reason: "test"},
		},
	}
	venues := map[string]venue.Venue{
		"okx": &fakeFundingVenue{name: "okx"},
	}

	executed, errs := ExecuteCapitalRouting(context.Background(), plan, venues, true, testCapitalRouterExecLogger(&bytes.Buffer{}))
	if executed != 0 {
		t.Fatalf("executed = %d, want 0", executed)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %#v, want 1", errs)
	}
	if !strings.Contains(errs[0].Error(), "does not support wallet transfer") {
		t.Fatalf("err = %v", errs[0])
	}
}

func TestBinanceTransferWalletDirectionMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		from, to string
		wantType string
	}{
		{"spot", "futures", "MAIN_UMFUTURE"},
		{"futures", "spot", "UMFUTURE_MAIN"},
		{"spot", "margin", "MAIN_MARGIN"},
		{"margin", "spot", "MARGIN_MAIN"},
		{"margin", "futures", "MARGIN_UMFUTURE"},
		{"futures", "margin", "UMFUTURE_MARGIN"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			t.Parallel()
			var gotType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/time":
					_, _ = w.Write([]byte(fmt.Sprintf(`{"serverTime":%d}`, time.Now().UnixMilli())))
				default:
					if r.URL.Path != "/sapi/v1/asset/transfer" {
						t.Fatalf("unexpected path: %s", r.URL.Path)
					}
					gotType = r.URL.Query().Get("type")
					if r.URL.Query().Get("asset") != "USDT" {
						t.Fatalf("asset = %q, want USDT", r.URL.Query().Get("asset"))
					}
					_, _ = w.Write([]byte(`{"tranId":12345}`))
				}
			}))
			t.Cleanup(srv.Close)

			client := exchange.NewClient(exchange.ClientConfig{
				SpotBaseURL: srv.URL,
				APIKey:      "test-key",
				SecretKey:   "test-secret",
				HTTPClient:  srv.Client(),
			})
			a := binance.New(client)
			if err := a.TransferWallet(context.Background(), tc.from, tc.to, 12.5); err != nil {
				t.Fatalf("TransferWallet: %v", err)
			}
			if gotType != tc.wantType {
				t.Fatalf("transfer type = %q, want %q", gotType, tc.wantType)
			}
		})
	}
}

func TestBinanceTransferWalletUnknownDirection(t *testing.T) {
	t.Parallel()
	a := binance.New(exchange.NewClient(exchange.ClientConfig{}))
	err := a.TransferWallet(context.Background(), "funding", "spot", 10)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v, want unsupported direction error", err)
	}
}

func TestBinanceTransferWalletZeroAmountNoOp(t *testing.T) {
	t.Parallel()
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"tranId":1}`))
	}))
	t.Cleanup(srv.Close)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL: srv.URL,
		APIKey:      "k",
		SecretKey:   "s",
		HTTPClient:  srv.Client(),
	})
	a := binance.New(client)
	if err := a.TransferWallet(context.Background(), "spot", "futures", 0); err != nil {
		t.Fatalf("TransferWallet(0): %v", err)
	}
	if called {
		t.Fatal("zero amount should not hit transfer endpoint")
	}
}

func TestBinanceTransferWalletQueryParams(t *testing.T) {
	t.Parallel()
	var values url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/time" {
			_, _ = w.Write([]byte(`{"serverTime":1700000000000}`))
			return
		}
		values, _ = url.ParseQuery(r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"tranId":99}`))
	}))
	t.Cleanup(srv.Close)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL: srv.URL,
		APIKey:      "k",
		SecretKey:   "s",
		HTTPClient:  srv.Client(),
	})
	a := binance.New(client)
	if err := a.TransferWallet(context.Background(), " FUTURES ", " spot ", 7.25); err != nil {
		t.Fatalf("TransferWallet: %v", err)
	}
	if values.Get("type") != "UMFUTURE_MAIN" {
		t.Fatalf("type = %q", values.Get("type"))
	}
	if values.Get("amount") == "" {
		t.Fatalf("amount missing in query: %v", values)
	}
}

func TestExecuteCapitalRoutingCollectsErrorsWithoutStopping(t *testing.T) {
	t.Parallel()
	okVenue := &fakeWalletTransferVenue{fakeFundingVenue: fakeFundingVenue{name: "ok"}}
	failVenue := &fakeWalletTransferVenue{
		fakeFundingVenue: fakeFundingVenue{name: "fail"},
		err:              fmt.Errorf("transfer rejected"),
	}
	plan := CapitalRoutingPlan{
		WithinVenueTransfers: []WalletTransfer{
			{Venue: "ok", From: WalletKindSpot, To: WalletKindFutures, AmountUSDT: 1, Reason: "a"},
			{Venue: "fail", From: WalletKindSpot, To: WalletKindFutures, AmountUSDT: 2, Reason: "b"},
			{Venue: "missing", From: WalletKindSpot, To: WalletKindFutures, AmountUSDT: 3, Reason: "c"},
		},
	}
	venues := map[string]venue.Venue{"ok": okVenue, "fail": failVenue}
	executed, errs := ExecuteCapitalRouting(context.Background(), plan, venues, true, log.New(io.Discard, "", 0))
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
	if len(errs) != 2 {
		t.Fatalf("errs = %#v, want 2", errs)
	}
}
