package app

import (
	"context"
	"errors"
	"io"
	"log"
	"testing"
	"time"
)

const sampleOKXDelistAnnouncementJSON = `{
  "code": "0",
  "data": [{
    "totalPage": "1",
    "details": [{
      "annType": "announcements-delistings",
      "pTime": "1717200000000",
      "title": "OKX to delist XXX-USDT perpetual swap trading",
      "url": "https://www.okx.com/help/example"
    }]
  }]
}`

func TestParseOKXAnnouncementDetails(t *testing.T) {
	details := parseOKXAnnouncementDetails([]byte(sampleOKXDelistAnnouncementJSON))
	if len(details) != 1 {
		t.Fatalf("len(details)=%d want 1", len(details))
	}
	if details[0].AnnType != okxAnnouncementDelistType {
		t.Fatalf("annType=%q", details[0].AnnType)
	}
	if details[0].Title != "OKX to delist XXX-USDT perpetual swap trading" {
		t.Fatalf("title=%q", details[0].Title)
	}
}

func TestExtractTickersFromTitle_DelistSample(t *testing.T) {
	tickers := extractTickersFromTitle("OKX to delist XXX-USDT perpetual swap trading")
	want := map[string]bool{"XXX": true, "XXX-USDT": true}
	if len(tickers) < len(want) {
		t.Fatalf("tickers=%v", tickers)
	}
	got := make(map[string]bool, len(tickers))
	for _, sym := range tickers {
		got[sym] = true
	}
	for sym := range want {
		if !got[sym] {
			t.Fatalf("missing %q in %v", sym, tickers)
		}
	}
}

func TestOKXAnnouncementGuard_IsRiskyFromSampleJSON(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	g := newOKXAnnouncementGuard(logger)
	g.httpGet = func(_ context.Context, _ string) ([]byte, error) {
		return []byte(sampleOKXDelistAnnouncementJSON), nil
	}
	fixed := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	g.nowFn = func() time.Time { return fixed }

	g.Refresh(context.Background())

	for _, sym := range []string{"XXX-USDT", "XXX"} {
		risky, reason := g.IsRisky(sym)
		if !risky {
			t.Fatalf("IsRisky(%q) want true", sym)
		}
		if reason == "" {
			t.Fatalf("IsRisky(%q) reason empty", sym)
		}
	}
	risky, reason := g.IsRisky("BTC-USDT")
	if risky {
		t.Fatalf("IsRisky(BTC-USDT) want false got reason=%q", reason)
	}
}

func TestOKXAnnouncementGuard_FailOpenOnHTTPError(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	g := newOKXAnnouncementGuard(logger)
	g.httpGet = func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("network down")
	}
	fixed := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	g.nowFn = func() time.Time { return fixed }

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Refresh panicked: %v", r)
			}
		}()
		g.Refresh(context.Background())
	}()

	for _, sym := range []string{"XXX-USDT", "BTC-USDT", "ANY"} {
		risky, reason := g.IsRisky(sym)
		if risky {
			t.Fatalf("IsRisky(%q) want false on fetch failure, reason=%q", sym, reason)
		}
	}
}

func TestBuildRiskyMapFromDetails_SkipsNonRiskTitles(t *testing.T) {
	details := []okxAnnouncementDetail{{
		AnnType: okxAnnouncementDelistType,
		Title:   "OKX system upgrade completed",
		PTime:   "1717200000000",
	}}
	risky := buildRiskyMapFromDetails(details)
	if len(risky) != 0 {
		t.Fatalf("unexpected risky=%v", risky)
	}
}
