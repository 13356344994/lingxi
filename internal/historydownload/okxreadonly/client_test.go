package okxreadonly

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"lingxi/internal/historydownload"
)

func TestRejectsEndpointAndHost(t *testing.T) {
	client := Client{}
	if _, err := client.Fetch(context.Background(), historydownload.Request{Endpoint: "/api/v5/unsupported"}); err == nil {
		t.Fatal("unapproved endpoint accepted")
	}
	client.BaseURL = "https://example.test"
	if _, err := client.Fetch(context.Background(), historydownload.Request{Endpoint: "/api/v5/public/instruments"}); err == nil {
		t.Fatal("unapproved host accepted")
	}
}

func TestFetchEnvelopeUsesOnlyGETAndAllowedAfterCursor(t *testing.T) {
	var gotMethod, gotHost, gotAfter, gotTimestamp, gotSign string
	client := Client{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotMethod, gotHost, gotAfter = request.Method, request.URL.Host, request.URL.Query().Get("after")
		gotTimestamp = request.Header.Get("OK-ACCESS-TIMESTAMP")
		gotSign = request.Header.Get("OK-ACCESS-SIGN")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":"0","data":[{"ordId":"9"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}, Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
		Now: func() time.Time { return time.Date(2026, 7, 13, 17, 30, 45, 123456789, time.UTC) }}
	response, err := client.FetchEnvelope(context.Background(), historydownload.Request{
		Endpoint: historydownload.OrdersEndpoint, Private: true, Query: map[string]string{"instType": "SWAP", "after": "10", "limit": "100"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte("2026-07-13T17:30:45.123ZGET/api/v5/trade/orders-history-archive?after=10&instType=SWAP&limit=100"))
	wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if gotMethod != http.MethodGet || gotHost != "www.okx.com" || gotAfter != "10" ||
		gotTimestamp != "2026-07-13T17:30:45.123Z" || gotSign != wantSign || len(response.Data) != 1 {
		t.Fatalf("unexpected request/result: method=%s host=%s after=%s timestamp=%s sign_match=%v data=%#v", gotMethod, gotHost, gotAfter, gotTimestamp, gotSign == wantSign, response.Data)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAccountConfigCapabilityRemoved(t *testing.T) {
	called := false
	client := Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})}, Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"}}
	if _, err := client.Fetch(context.Background(), historydownload.Request{Endpoint: "/api/v5/account/config", Private: true}); err == nil {
		t.Fatal("removed account/config endpoint accepted")
	}
	if called {
		t.Fatal("removed account/config reached transport")
	}
}

func TestTransportErrorsDoNotLeakAfterCursor(t *testing.T) {
	const rawCursor = "987654321012345678"
	client := Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport echoed " + rawCursor)
		})},
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
	}
	_, err := client.FetchEnvelope(context.Background(), historydownload.Request{
		Endpoint: historydownload.OrdersEndpoint, Private: true,
		Query: map[string]string{"instType": "SWAP", "after": rawCursor, "limit": "100"},
	})
	if err == nil || strings.Contains(err.Error(), rawCursor) {
		t.Fatalf("transport error leaked raw cursor: %v", err)
	}
}

func TestDiscoveryQueryIsExactSingleDaySingleInstrument(t *testing.T) {
	valid := map[string]string{
		"module": "1", "instType": "SWAP", "instIdList": "BTC-USDT-SWAP",
		"dateAggrType": "daily", "begin": "1767225600000", "end": "1767311999999",
	}
	if err := validateQuery(historydownload.MarketDataDiscovery, valid); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]string){
		"text-module":  func(query map[string]string) { query["module"] = "trades" },
		"books-module": func(query map[string]string) { query["module"] = "4" },
		"multi-inst":   func(query map[string]string) { query["instIdList"] = "BTC-USDT-SWAP,ETH-USDT-SWAP" },
		"aggregation":  func(query map[string]string) { query["dateAggrType"] = "monthly" },
		"range":        func(query map[string]string) { query["end"] = "1767312000000" },
		"extra":        func(query map[string]string) { query["year"] = "2026" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			query := map[string]string{}
			for key, value := range valid {
				query[key] = value
			}
			mutate(query)
			if err := validateQuery(historydownload.MarketDataDiscovery, query); err == nil {
				t.Fatal("invalid discovery query accepted")
			}
		})
	}
}

func TestFetchEnvelopeRejectsRedirectWithoutSecondRequest(t *testing.T) {
	for _, test := range []struct {
		name        string
		location    string
		request     historydownload.Request
		credentials historydownload.Credentials
	}{
		{
			name: "public-evil-host", location: "https://evil.example/archive.zip",
			request: historydownload.Request{
				Endpoint: historydownload.PublicInstruments, Query: map[string]string{"instType": "SPOT"},
			},
		},
		{
			name: "private-http", location: "http://www.okx.com/leak",
			request: historydownload.Request{
				Endpoint: historydownload.OrdersEndpoint, Private: true,
				Query: map[string]string{"instType": "SWAP", "limit": "100"},
			},
			credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := Client{
				Credentials: test.credentials,
				HTTP: &http.Client{
					CheckRedirect: func(*http.Request, []*http.Request) error {
						t.Fatal("caller redirect policy should be replaced")
						return nil
					},
					Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						if calls > 1 {
							t.Fatalf("redirect issued second request to %s", request.URL)
						}
						if test.request.Private && request.Header.Get("OK-ACCESS-KEY") == "" {
							t.Fatal("initial private request was not signed")
						}
						return &http.Response{
							StatusCode: http.StatusFound,
							Header:     http.Header{"Location": []string{test.location}},
							Body:       io.NopCloser(strings.NewReader(`{"code":"0"}`)),
							Request:    request,
						}, nil
					}),
				},
			}
			_, err := client.FetchEnvelope(context.Background(), test.request)
			if err == nil || !strings.Contains(err.Error(), "http_rejected") || calls != 1 {
				t.Fatalf("redirect was not safely rejected: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestAPIErrorExposesOnlySafeCode(t *testing.T) {
	client := Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"50113","msg":"sensitive upstream detail","data":[]}`)),
				Header:     make(http.Header),
			}, nil
		})},
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
	}
	_, err := client.FetchEnvelope(context.Background(), historydownload.Request{
		Endpoint: historydownload.OrdersEndpoint, Private: true,
		Query: map[string]string{"instType": "SWAP", "limit": "100"},
	})
	if err == nil || !strings.Contains(err.Error(), "api_code=50113") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe or unclassified API error: %v", err)
	}
}

func TestHTTPErrorExposesOnlyStatusAndSafeCode(t *testing.T) {
	client := Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader(`{"code":"50113","msg":"sensitive upstream detail","data":[]}`)),
				Header:     make(http.Header),
			}, nil
		})},
		Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
	}
	_, err := client.FetchEnvelope(context.Background(), historydownload.Request{
		Endpoint: historydownload.OrdersEndpoint, Private: true,
		Query: map[string]string{"instType": "SWAP", "limit": "100"},
	})
	if err == nil || !strings.Contains(err.Error(), "http_status=401 api_code=50113") ||
		strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe or unclassified HTTP error: %v", err)
	}
}

func TestUnsafeAPICodeAndBodyErrorsAreCollapsed(t *testing.T) {
	for name, body := range map[string]io.ReadCloser{
		"unsafe-code": io.NopCloser(strings.NewReader(`{"code":"50113\ncursor=secret","msg":"ignored","data":[]}`)),
		"body-error":  failingReadCloser{},
	} {
		t.Run(name, func(t *testing.T) {
			client := Client{
				HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusUnauthorized, Body: body, Header: make(http.Header)}, nil
				})},
				Credentials: historydownload.Credentials{Key: "key", Secret: "secret", Passphrase: "pass"},
			}
			_, err := client.FetchEnvelope(context.Background(), historydownload.Request{
				Endpoint: historydownload.OrdersEndpoint, Private: true,
				Query: map[string]string{"instType": "SWAP", "limit": "100"},
			})
			if err == nil || strings.Contains(err.Error(), "cursor") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe upstream detail escaped: %v", err)
			}
		})
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("cursor=secret") }
func (failingReadCloser) Close() error             { return nil }
