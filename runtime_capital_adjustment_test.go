package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestAdjustTradingCapitalUSDTSweepsExcessToFunding(t *testing.T) {
	spot := 40.0
	futures := 80.0
	margin := 30.0
	funding := 50.0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"` + formatFloat(spot) + `","locked":"0"}]}`))
		case "/fapi/v3/account":
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"` + formatFloat(futures) + `","availableBalance":"` + formatFloat(futures) + `"}],"positions":[]}`))
		case "/sapi/v1/margin/account":
			_, _ = w.Write([]byte(`{"created":true,"borrowEnabled":true,"tradeEnabled":true,"transferInEnabled":true,"transferOutEnabled":true,"accountType":"MARGIN_1","TotalCollateralValueInUSDT":"0","totalNetAssetOfBtc":"0","totalLiabilityOfBtc":"0","userAssets":[{"asset":"USDT","borrowed":"0","free":"` + formatFloat(margin) + `","interest":"0","locked":"0","netAsset":"` + formatFloat(margin) + `"}]}`))
		case "/sapi/v1/asset/get-funding-asset":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected funding asset method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[{"asset":"USDT","free":"` + formatFloat(funding) + `","locked":"0","freeze":"0","withdrawing":"0"}]`))
		case "/sapi/v1/asset/transfer":
			values, _ := url.ParseQuery(r.URL.RawQuery)
			amount := parseFloat(values.Get("amount"))
			switch values.Get("type") {
			case "UMFUTURE_MAIN":
				futures -= amount
				spot += amount
			case "MARGIN_MAIN":
				margin -= amount
				spot += amount
			case "MAIN_FUNDING":
				spot -= amount
				funding += amount
			default:
				t.Fatalf("unexpected transfer type: %s", values.Get("type"))
			}
			_, _ = w.Write([]byte(`{"tranId":12345}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.StrategyConfig.LiveFundingAsset = "USDT"

	result, err := engine.AdjustTradingCapitalUSDT(context.Background(), 100)
	if err != nil {
		t.Fatalf("AdjustTradingCapitalUSDT() error = %v", err)
	}
	if got, want := len(result.Transfers), 3; got != want {
		t.Fatalf("expected %d transfers, got %d (%+v)", want, got, result.Transfers)
	}
	if got := result.After.SpotFreeUSDT; got != 25 {
		t.Fatalf("expected spot 25, got %.2f", got)
	}
	if got := result.After.CrossMarginFreeUSDT; got != 25 {
		t.Fatalf("expected cross margin 25, got %.2f", got)
	}
	if got := result.After.FuturesAvailableUSDT; got != 50 {
		t.Fatalf("expected futures 50, got %.2f", got)
	}
	if got := result.After.FundingFreeUSDT; got != 100 {
		t.Fatalf("expected funding 100, got %.2f", got)
	}
}

func TestAdjustTradingCapitalUSDTUsesFundingToTopUpTradingBuckets(t *testing.T) {
	spot := 10.0
	futures := 10.0
	margin := 0.0
	funding := 180.0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"` + formatFloat(spot) + `","locked":"0"}]}`))
		case "/fapi/v3/account":
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"` + formatFloat(futures) + `","availableBalance":"` + formatFloat(futures) + `"}],"positions":[]}`))
		case "/sapi/v1/margin/account":
			_, _ = w.Write([]byte(`{"created":true,"borrowEnabled":true,"tradeEnabled":true,"transferInEnabled":true,"transferOutEnabled":true,"accountType":"MARGIN_1","TotalCollateralValueInUSDT":"0","totalNetAssetOfBtc":"0","totalLiabilityOfBtc":"0","userAssets":[{"asset":"USDT","borrowed":"0","free":"` + formatFloat(margin) + `","interest":"0","locked":"0","netAsset":"` + formatFloat(margin) + `"}]}`))
		case "/sapi/v1/asset/get-funding-asset":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected funding asset method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[{"asset":"USDT","free":"` + formatFloat(funding) + `","locked":"0","freeze":"0","withdrawing":"0"}]`))
		case "/sapi/v1/asset/transfer":
			values, _ := url.ParseQuery(r.URL.RawQuery)
			amount := parseFloat(values.Get("amount"))
			switch values.Get("type") {
			case "FUNDING_MAIN":
				funding -= amount
				spot += amount
			case "MAIN_UMFUTURE":
				spot -= amount
				futures += amount
			case "MAIN_MARGIN":
				spot -= amount
				margin += amount
			default:
				t.Fatalf("unexpected transfer type: %s", values.Get("type"))
			}
			_, _ = w.Write([]byte(`{"tranId":12345}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.StrategyConfig.LiveFundingAsset = "USDT"

	result, err := engine.AdjustTradingCapitalUSDT(context.Background(), 100)
	if err != nil {
		t.Fatalf("AdjustTradingCapitalUSDT() error = %v", err)
	}
	if got := result.After.SpotFreeUSDT; got != 25 {
		t.Fatalf("expected spot 25, got %.2f", got)
	}
	if got := result.After.CrossMarginFreeUSDT; got != 25 {
		t.Fatalf("expected cross margin 25, got %.2f", got)
	}
	if got := result.After.FuturesAvailableUSDT; got != 50 {
		t.Fatalf("expected futures 50, got %.2f", got)
	}
	if got := result.After.FundingFreeUSDT; got != 100 {
		t.Fatalf("expected funding 100, got %.2f", got)
	}
}

func TestAdjustTradingCapitalUSDTRejectsInsufficientLiveBalance(t *testing.T) {
	spot := 10.0
	futures := 20.0
	margin := 5.0
	funding := 15.0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"` + formatFloat(spot) + `","locked":"0"}]}`))
		case "/fapi/v3/account":
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"` + formatFloat(futures) + `","availableBalance":"` + formatFloat(futures) + `"}],"positions":[]}`))
		case "/sapi/v1/margin/account":
			_, _ = w.Write([]byte(`{"created":true,"borrowEnabled":true,"tradeEnabled":true,"transferInEnabled":true,"transferOutEnabled":true,"accountType":"MARGIN_1","TotalCollateralValueInUSDT":"0","totalNetAssetOfBtc":"0","totalLiabilityOfBtc":"0","userAssets":[{"asset":"USDT","borrowed":"0","free":"` + formatFloat(margin) + `","interest":"0","locked":"0","netAsset":"` + formatFloat(margin) + `"}]}`))
		case "/sapi/v1/asset/get-funding-asset":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected funding asset method: %s", r.Method)
			}
			_, _ = w.Write([]byte(`[{"asset":"USDT","free":"` + formatFloat(funding) + `","locked":"0","freeze":"0","withdrawing":"0"}]`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.StrategyConfig.LiveFundingAsset = "USDT"

	if _, err := engine.AdjustTradingCapitalUSDT(context.Background(), 80); err == nil {
		t.Fatal("expected insufficient live balance error")
	}
}

func TestRuntimeControllerAdjustTradingCapitalMonthlySkipsRealTransfers(t *testing.T) {
	controller := &RuntimeController{
		cfg:     DefaultEngineConfig(),
		rootCtx: context.Background(),
	}

	result, err := controller.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{
		Mode:              string(RuntimeModeMonthly),
		TargetTradingUSDT: 120,
	})
	if err != nil {
		t.Fatalf("AdjustTradingCapital(monthly) error = %v", err)
	}
	if got := result.AppliedTradingUSDT; got != 120 {
		t.Fatalf("expected applied trading capital 120, got %.2f", got)
	}
	if got := result.Config.MaxCycleOpenNotionalUSDT; got != 120 {
		t.Fatalf("expected max cycle open notional 120, got %.2f", got)
	}
	if got := result.Config.PerSymbolQuoteUSDT; got != 60 {
		t.Fatalf("expected per-symbol quote 60, got %.2f", got)
	}
	if len(result.Transfers) != 0 {
		t.Fatalf("expected no real transfers in monthly mode, got %+v", result.Transfers)
	}
}

func TestCurrentCapitalSnapshotWithoutSecretPathStillReturnsConfiguredTargets(t *testing.T) {
	controller := &RuntimeController{
		cfg:     DefaultEngineConfig(),
		rootCtx: context.Background(),
	}
	applyTradingCapitalTargetToConfig(&controller.cfg, 120)

	snapshot, err := controller.CurrentCapitalSnapshot()
	if err != nil {
		t.Fatalf("CurrentCapitalSnapshot() error = %v", err)
	}
	if got := snapshot.DesiredSpotUSDT; got != 30 {
		t.Fatalf("expected desired spot 30, got %.2f", got)
	}
	if got := snapshot.DesiredCrossMarginUSDT; got != 30 {
		t.Fatalf("expected desired cross margin 30, got %.2f", got)
	}
	if got := snapshot.DesiredFuturesUSDT; got != 60 {
		t.Fatalf("expected desired futures 60, got %.2f", got)
	}
	if got := snapshot.DesiredFundingUSDT; got != 0 {
		t.Fatalf("expected desired funding 0, got %.2f", got)
	}
}

func TestCurrentCapitalSnapshotUsesRecentCache(t *testing.T) {
	controller := &RuntimeController{
		cfg:     DefaultEngineConfig(),
		rootCtx: context.Background(),
		logger:  log.New(io.Discard, "", 0),
	}
	controller.status.SecretPath = "/tmp/invalid-secret.yaml"
	cached := RuntimeCapitalSnapshot{
		SpotFreeUSDT:         12,
		FuturesAvailableUSDT: 34,
		TotalMovableUSDT:     46,
	}
	controller.cachedCapitalSnapshot = cached
	controller.cachedCapitalSnapshotAt = time.Now()
	controller.cachedCapitalSnapshotSecretKey = controller.status.SecretPath + "|" + fmt.Sprintf("%.2f", configuredTradingCapitalUSDT(controller.cfg))

	snapshot, err := controller.CurrentCapitalSnapshot()
	if err != nil {
		t.Fatalf("CurrentCapitalSnapshot() error = %v", err)
	}
	if snapshot != cached {
		t.Fatalf("expected cached capital snapshot, got %+v", snapshot)
	}
}

func TestApplyTradingCapitalTargetToConfig(t *testing.T) {
	cfg := DefaultEngineConfig()
	applyTradingCapitalTargetToConfig(&cfg, 120)
	if got := cfg.RiskConfig.MaxCycleOpenNotionalUSDT; got != 120 {
		t.Fatalf("expected cycle open notional 120, got %.2f", got)
	}
	if got := cfg.StrategyConfig.PerSymbolQuoteUSDT; got != 60 {
		t.Fatalf("expected per-symbol quote 60, got %.2f", got)
	}
	if got := cfg.PerSymbolQuoteUSDT; got != 60 {
		t.Fatalf("expected flat per-symbol quote 60, got %.2f", got)
	}
	if got := configuredTradingCapitalUSDT(cfg); got != 120 {
		t.Fatalf("expected configured trading capital 120, got %.2f", got)
	}
}
