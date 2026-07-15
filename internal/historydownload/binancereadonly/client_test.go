package binancereadonly

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAsyncEndpointsUseSignedGETOnly(t *testing.T) {
	var method, path, signature string
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	client := Client{
		Credentials: Credentials{APIKey: "key", Secret: "secret"}, Now: func() time.Time { return now },
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			method, path, signature = r.Method, r.URL.Path, r.URL.Query().Get("signature")
			if r.Body != nil || r.Header.Get("X-MBX-APIKEY") != "key" {
				t.Fatal("request body or API key boundary invalid")
			}
			return responseFor(`{"downloadId":"download_1"}`), nil
		})},
	}
	receipt, err := client.RequestOrderHistory(context.Background(), PilotStart, PilotEnd, DefaultRecvWindow)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := "endTime=1783900800000&recvWindow=5000&startTime=1783296000000&timestamp=1783987200000"
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(wantQuery))
	if method != http.MethodGet || path != OrderAsyncEndpoint || signature != hex.EncodeToString(mac.Sum(nil)) ||
		receipt.DownloadID != "download_1" || receipt.QuotaSideEffect != "ASYNC_ORDER_HISTORY_JOB_REQUESTED" {
		t.Fatalf("unexpected async request: method=%s path=%s signature=%s receipt=%+v", method, path, signature, receipt)
	}
}

func TestRejectsUnsafeHostsWindowsAndURLs(t *testing.T) {
	client := Client{BaseURL: "https://evil.example", Credentials: Credentials{APIKey: "k", Secret: "s"}}
	if _, err := client.RequestOrderHistory(context.Background(), PilotStart, PilotEnd, DefaultRecvWindow); err == nil {
		t.Fatal("unapproved base URL accepted")
	}
	for _, raw := range []string{"http://fapi.binance.com/a", "https://evil.example/a", "https://fapi.binance.com@evil.example/a"} {
		if _, err := ValidateDownloadURL(raw); err == nil {
			t.Fatalf("unsafe download URL accepted: %s", raw)
		}
	}
	if _, err := client.RequestOrderHistory(context.Background(), PilotStart.Add(-time.Millisecond), PilotEnd, DefaultRecvWindow); err == nil {
		t.Fatal("overlong window accepted")
	}
	if err := validRecvWindow(MaxRecvWindow + 1); err == nil {
		t.Fatal("unsafe recvWindow accepted")
	}
}

func TestRedirectAndTransportErrorsDoNotLeakSecrets(t *testing.T) {
	client := Client{
		Credentials: Credentials{APIKey: "key", Secret: "secret"},
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("downloadId=super-secret")
		})},
	}
	_, err := client.RequestOrderHistory(context.Background(), PilotStart, PilotEnd, DefaultRecvWindow)
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("transport leaked private detail: %v", err)
	}
	calls := 0
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader("x")), Request: r}, nil
	})}
	_, err = client.RequestOrderHistory(context.Background(), PilotStart, PilotEnd, DefaultRecvWindow)
	if err == nil || !strings.Contains(err.Error(), "http_rejected") || calls != 1 {
		t.Fatalf("redirect was not stopped: %v calls=%d", err, calls)
	}
}

func responseFor(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
