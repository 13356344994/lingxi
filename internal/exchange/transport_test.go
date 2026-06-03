package exchange

import (
	"strings"
	"testing"
	"time"
)

func TestParseHeaderInt(t *testing.T) {
	cases := map[string]int{
		"42":  42,
		"0":   0,
		" 7 ": 7,
		"":    -1,
		"abc": -1,
	}
	for raw, want := range cases {
		if got := ParseHeaderInt(raw); got != want {
			t.Errorf("ParseHeaderInt(%q)=%d want %d", raw, got, want)
		}
	}
}

func TestShouldUseBinanceFallbackBases(t *testing.T) {
	if !ShouldUseBinanceFallbackBases("https://api.binance.com", "https://api.binance.com") {
		t.Error("official base should enable fallback")
	}
	if !ShouldUseBinanceFallbackBases("https://api3.binance.com", "https://api.binance.com") {
		t.Error("any *.binance.com should enable fallback")
	}
	if ShouldUseBinanceFallbackBases("http://127.0.0.1:8080", "https://api.binance.com") {
		t.Error("local base must not enable fallback")
	}
	if ShouldUseBinanceFallbackBases("", "https://api.binance.com") {
		t.Error("empty base must not enable fallback")
	}
}

func TestReplaceBaseURLs(t *testing.T) {
	got := ReplaceBaseURLs("https://api.binance.com/api/v3/depth?symbol=BTCUSDT", "https://api.binance.com", []string{
		"https://api.binance.com",
		"https://api1.binance.com",
		"https://api1.binance.com", // duplicate dropped
		"",                         // empty skipped
	})
	want := []string{
		"https://api.binance.com/api/v3/depth?symbol=BTCUSDT",
		"https://api1.binance.com/api/v3/depth?symbol=BTCUSDT",
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParsePublicGETWhitelist(t *testing.T) {
	got := ParsePublicGETWhitelist(" /api/v3/depth , /API/V3/Depth ,, /fapi/v1/time ")
	want := []string{"/api/v3/depth", "/fapi/v1/time"}
	if len(got) != len(want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %q want %q", i, got[i], want[i])
		}
	}
}

func TestMaxRequestGovernorDuration(t *testing.T) {
	if got := MaxRequestGovernorDuration(2*time.Second, 5*time.Second); got != 5*time.Second {
		t.Errorf("got %s want 5s", got)
	}
	if got := MaxRequestGovernorDuration(8*time.Second, 1*time.Second); got != 8*time.Second {
		t.Errorf("got %s want 8s", got)
	}
}

func TestPublicGETHTTPError(t *testing.T) {
	err := NewPublicGETHTTPError("https://api.binance.com/x", 418, "418 I'm a teapot", []byte("rate\nlimited"))
	msg := err.Error()
	if !strings.Contains(msg, "418") {
		t.Errorf("error should mention status: %q", msg)
	}
	if !strings.Contains(msg, "body=rate limited") {
		t.Errorf("error should flatten body: %q", msg)
	}
	typed, ok := err.(PublicGETHTTPError)
	if !ok || typed.StatusCode != 418 {
		t.Fatalf("expected typed PublicGETHTTPError with status 418, got %#v", err)
	}
}
