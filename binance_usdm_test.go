package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewBinanceClientWithNetworkConfigUsesTransportTuning(t *testing.T) {
	client, err := NewBinanceClientWithNetworkConfig(NetworkConfig{
		HTTPTimeout:           8 * time.Second,
		MaxIdleConns:          111,
		MaxIdleConnsPerHost:   37,
		IdleConnTimeout:       55 * time.Second,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		ExpectContinueTimeout: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBinanceClientWithNetworkConfig() error = %v", err)
	}

	if client.httpClient.Timeout != 8*time.Second {
		t.Fatalf("unexpected http timeout: %s", client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.MaxIdleConns != 111 {
		t.Fatalf("unexpected max idle conns: %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 37 {
		t.Fatalf("unexpected max idle conns per host: %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 55*time.Second {
		t.Fatalf("unexpected idle conn timeout: %s", transport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != 2*time.Second {
		t.Fatalf("unexpected tls handshake timeout: %s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 4*time.Second {
		t.Fatalf("unexpected response header timeout: %s", transport.ResponseHeaderTimeout)
	}
	if transport.ExpectContinueTimeout != 1500*time.Millisecond {
		t.Fatalf("unexpected expect continue timeout: %s", transport.ExpectContinueTimeout)
	}
}

func TestBinanceClientPublicGETAPIKeyWhitelist(t *testing.T) {
	client := NewBinanceClient(time.Second)
	client.apiKey = "demo-key"
	client.applyNetworkConfig(NetworkConfig{
		PublicGETUseAPIKey:       true,
		PublicGETAPIKeyWhitelist: "premiumIndex,openInterest",
	})

	if !client.shouldAttachAPIKeyToPublicGET("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=BTCUSDT") {
		t.Fatal("expected premiumIndex request to match API key whitelist")
	}
	if client.shouldAttachAPIKeyToPublicGET("https://api.binance.com/api/v3/ticker/bookTicker?symbol=BTCUSDT") {
		t.Fatal("did not expect bookTicker request to match API key whitelist")
	}
}

func TestBinanceClientGetJSONAttachesAPIKeyForWhitelistedPublicGET(t *testing.T) {
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-MBX-APIKEY")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT"}`))
	}))
	defer server.Close()

	client := NewBinanceClient(time.Second)
	client.spotBaseURL = server.URL
	client.apiKey = "demo-key"
	client.applyNetworkConfig(NetworkConfig{
		PublicGETUseAPIKey:       true,
		PublicGETAPIKeyWhitelist: "exchangeInfo",
	})

	var payload map[string]any
	err := client.getJSON(context.Background(), server.URL+"/api/v3/exchangeInfo", &payload)
	if err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if gotAPIKey != "demo-key" {
		t.Fatalf("expected public GET to attach api key, got %q", gotAPIKey)
	}
}

func TestBinanceClientWaitPublicGETRequestSlotAfter403Cooldown(t *testing.T) {
	client := NewBinanceClient(time.Second)
	client.applyNetworkConfig(NetworkConfig{
		PublicGETCooldown403: 25 * time.Millisecond,
	})
	client.notePublicGETResponseStatus(http.StatusForbidden)

	start := time.Now()
	if err := client.waitPublicGETRequestSlot(context.Background(), "https://api.binance.com/api/v3/exchangeInfo", 0); err != nil {
		t.Fatalf("waitPublicGETRequestSlot() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("expected cooldown wait after 403, got %s", elapsed)
	}
}

func TestBinanceClientGetJSONTracksUsedWeightHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-MBX-USED-WEIGHT-1M", "5234")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT"}`))
	}))
	defer server.Close()

	client := NewBinanceClient(time.Second)
	client.spotBaseURL = server.URL

	var payload map[string]any
	if err := client.getJSON(context.Background(), server.URL+"/api/v3/exchangeInfo", &payload); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	usedWeight, threshold := client.usedWeightStatus()
	if usedWeight != 5234 {
		t.Fatalf("expected used weight 5234, got %d", usedWeight)
	}
	if threshold != 4800 {
		t.Fatalf("expected threshold 4800, got %d", threshold)
	}
}

func TestBinanceClientUsedWeightStatusResetsAtMinuteBoundary(t *testing.T) {
	client := NewBinanceClient(time.Second)
	client.usedWeightMu.Lock()
	client.usedWeight1m = 5234
	client.usedWeightLimit1m = 6000
	client.usedWeightBlockThreshold = 4800
	client.usedWeightWindowMinuteUTC = time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	client.usedWeightMu.Unlock()

	usedWeight, threshold := client.usedWeightStatus()
	if usedWeight != 0 {
		t.Fatalf("expected used weight to reset to 0 at new minute boundary, got %d", usedWeight)
	}
	if threshold != 4800 {
		t.Fatalf("expected threshold 4800, got %d", threshold)
	}
}

func TestBinanceClientCandidateRequestURLsSkipsExternalFallbackForCustomBase(t *testing.T) {
	client := NewBinanceClient(time.Second)
	client.futuresBaseURL = "http://127.0.0.1:18080"

	got := client.candidateRequestURLs("http://127.0.0.1:18080/fapi/v1/premiumIndex")
	if len(got) != 1 || got[0] != "http://127.0.0.1:18080/fapi/v1/premiumIndex" {
		t.Fatalf("expected custom base URL to avoid external fallbacks, got %v", got)
	}
}

func TestBinanceClientReserveUsedWeightBudgetAccountsForParallelReservations(t *testing.T) {
	client := NewBinanceClient(time.Second)
	client.usedWeightMu.Lock()
	client.usedWeight1m = 4798
	client.usedWeightLimit1m = 6000
	client.usedWeightBlockThreshold = 4800
	client.usedWeightWindowMinuteUTC = time.Now().UTC().Truncate(time.Minute)
	client.usedWeightMu.Unlock()

	projected, threshold, ok := client.reserveUsedWeightBudget(1)
	if !ok {
		t.Fatalf("expected first reservation to pass, projected=%d threshold=%d", projected, threshold)
	}
	projected, threshold, ok = client.reserveUsedWeightBudget(1)
	if ok {
		t.Fatalf("expected second reservation to be blocked, projected=%d threshold=%d", projected, threshold)
	}
	client.releaseUsedWeightBudget(1)
	projected, threshold, ok = client.reserveUsedWeightBudget(1)
	if !ok {
		t.Fatalf("expected reservation after release to pass, projected=%d threshold=%d", projected, threshold)
	}
}

func TestBuildGoldenSymbolSpotPairsPrefersLowerFeeStablecoinSpot(t *testing.T) {
	spotSymbols := []SpotSymbol{
		{Symbol: "BTCUSDT", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "USDT"},
		{Symbol: "BTCFDUSD", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "FDUSD"},
		{Symbol: "ETHUSDC", Status: "TRADING", BaseAsset: "ETH", QuoteAsset: "USDC"},
		{Symbol: "ETHUSDT", Status: "TRADING", BaseAsset: "ETH", QuoteAsset: "USDT"},
	}
	futuresSymbols := []FuturesSymbol{
		{Symbol: "BTCUSDT", Pair: "BTCUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT"},
		{Symbol: "ETHUSDT", Pair: "ETHUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT"},
	}

	got := buildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, []string{"FDUSD", "USDC", "USDT"}, time.Now().UTC())
	if got["BTCUSDT"] != "BTCFDUSD" {
		t.Fatalf("expected BTC futures to map to BTCFDUSD, got %q", got["BTCUSDT"])
	}
	if got["ETHUSDT"] != "ETHUSDC" {
		t.Fatalf("expected ETH futures to map to ETHUSDC, got %q", got["ETHUSDT"])
	}
}

func TestBuildGoldenSymbolSpotPairsCanBeRestrictedToUSDTOnly(t *testing.T) {
	spotSymbols := []SpotSymbol{
		{Symbol: "BTCUSDT", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "USDT"},
		{Symbol: "BTCFDUSD", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "FDUSD"},
	}
	futuresSymbols := []FuturesSymbol{
		{Symbol: "BTCUSDT", Pair: "BTCUSDT", Status: "TRADING", ContractType: "PERPETUAL", MarginAsset: "USDT"},
	}

	got := buildGoldenSymbolSpotPairs(spotSymbols, futuresSymbols, []string{"USDT"}, time.Now().UTC())
	if got["BTCUSDT"] != "BTCUSDT" {
		t.Fatalf("expected BTC futures to map to BTCUSDT when restricted to USDT, got %q", got["BTCUSDT"])
	}
}

func TestResolveAccountTradingCostProfileUsesSignedEndpoints(t *testing.T) {
	futuresCalls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MBX-APIKEY") != "demo-key" {
			t.Fatalf("expected signed request to carry api key, got %q", r.Header.Get("X-MBX-APIKEY"))
		}
		if r.URL.Query().Get("timestamp") == "" || r.URL.Query().Get("signature") == "" {
			t.Fatalf("expected signed query params, got %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/sapi/v1/asset/tradeFee":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","makerCommission":"0.0009","takerCommission":"0.0011"},{"symbol":"ETHUSDT","makerCommission":"0.0008","takerCommission":"0.0010"}]`))
		case "/fapi/v1/commissionRate":
			got := strings.ToUpper(r.URL.Query().Get("symbol"))
			futuresCalls[got]++
			switch got {
			case "BTCUSDT":
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","makerCommissionRate":"0.0002","takerCommissionRate":"0.00045"}`))
			case "ETHUSDT":
				_, _ = w.Write([]byte(`{"symbol":"ETHUSDT","makerCommissionRate":"0.0002","takerCommissionRate":"0.00060"}`))
			default:
				t.Fatalf("unexpected futures commission symbol: %s", got)
			}
		case "/sapi/v1/margin/next-hourly-interest-rate":
			assets := strings.ToUpper(r.URL.Query().Get("assets"))
			if !strings.Contains(assets, "BTC") || !strings.Contains(assets, "ETH") {
				t.Fatalf("unexpected margin assets query: %s", assets)
			}
			_, _ = w.Write([]byte(`[{"asset":"BTC","nextHourlyInterestRate":"0.000031"},{"asset":"ETH","nextHourlyInterestRate":"0.000044"}]`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &BinanceClient{
		spotBaseURL:    server.URL,
		futuresBaseURL: server.URL,
		apiKey:         "demo-key",
		secretKey:      "demo-secret",
		httpClient:     server.Client(),
	}

	profile, err := client.resolveAccountTradingCostProfile(context.Background(), []string{"BTCUSDT", "ETHUSDT"}, []string{"BTCUSDT", "ETHUSDT"}, []string{"BTC", "ETH"})
	if err != nil {
		t.Fatalf("resolveAccountTradingCostProfile() error = %v", err)
	}
	if profile.SpotTakerFeeRate != 0.0011 || profile.SpotFeeSymbol != "BTCUSDT" {
		t.Fatalf("unexpected spot profile: %+v", profile)
	}
	if profile.FuturesTakerFeeRate != 0.00060 || profile.FuturesFeeSymbol != "ETHUSDT" {
		t.Fatalf("unexpected futures profile: %+v", profile)
	}
	if profile.BorrowHourlyRateByAsset["BTC"] != 0.000031 || profile.BorrowHourlyRateByAsset["ETH"] != 0.000044 {
		t.Fatalf("unexpected borrow rates: %+v", profile.BorrowHourlyRateByAsset)
	}
	if futuresCalls["BTCUSDT"] != 1 || futuresCalls["ETHUSDT"] != 1 {
		t.Fatalf("expected one commission query per futures symbol, got %+v", futuresCalls)
	}

	commission, err := client.fetchFuturesCommissionRate(context.Background(), "ETHUSDT")
	if err != nil {
		t.Fatalf("fetchFuturesCommissionRate() cached error = %v", err)
	}
	if commission.TakerCommissionRate != "0.00060" {
		t.Fatalf("unexpected cached commission: %+v", commission)
	}
	if futuresCalls["ETHUSDT"] != 1 {
		t.Fatalf("expected cached commission lookup to avoid extra request, got %+v", futuresCalls)
	}
}

func TestResolveAccountTradingCostProfileReturnsPartialProfileOnBorrowFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sapi/v1/asset/tradeFee":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","makerCommission":"0.0009","takerCommission":"0.0011"}]`))
		case "/fapi/v1/commissionRate":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","makerCommissionRate":"0.0002","takerCommissionRate":"0.00045"}`))
		case "/sapi/v1/margin/next-hourly-interest-rate":
			http.Error(w, `{"code":-3000,"msg":"margin disabled"}`, http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &BinanceClient{
		spotBaseURL:    server.URL,
		futuresBaseURL: server.URL,
		apiKey:         "demo-key",
		secretKey:      "demo-secret",
		httpClient:     server.Client(),
	}

	profile, err := client.resolveAccountTradingCostProfile(context.Background(), []string{"BTCUSDT"}, []string{"BTCUSDT"}, []string{"BTC"})
	if err == nil {
		t.Fatal("expected partial failure error")
	}
	if !strings.Contains(err.Error(), "margin_interest=") {
		t.Fatalf("expected partial failure to mention margin interest, got %v", err)
	}
	if profile.SpotTakerFeeRate != 0.0011 || profile.SpotFeeSymbol != "BTCUSDT" {
		t.Fatalf("expected spot fee to survive partial failure, got %+v", profile)
	}
	if profile.FuturesTakerFeeRate != 0.00045 || profile.FuturesFeeSymbol != "BTCUSDT" {
		t.Fatalf("expected futures fee to survive partial failure, got %+v", profile)
	}
	if len(profile.BorrowHourlyRateByAsset) != 0 {
		t.Fatalf("expected borrow rates to fall back when borrow endpoint fails, got %+v", profile.BorrowHourlyRateByAsset)
	}
}
