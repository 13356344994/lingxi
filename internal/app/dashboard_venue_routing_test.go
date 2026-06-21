package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// newRoutingTestServer 造一个仅用于路由解析的 DashboardServer:挂一个带 [binance, okx] 的编排器,
// venueRuntime 的 store/controller 留空(解析逻辑只看名字与指针,不触达 store)。auditStore 留 nil → 审计早退。
func newRoutingTestServer() (*DashboardServer, *venueRuntime, *venueRuntime) {
	b := &venueRuntime{name: "binance"}
	k := &venueRuntime{name: "okx"}
	o := NewOrchestrator()
	o.Register(b)
	o.Register(k)
	return &DashboardServer{orchestrator: o}, b, k
}

func reqWithVenue(venue string) *http.Request {
	url := "/api/x"
	if venue != "" {
		url += "?venue=" + venue
	}
	return httptest.NewRequest(http.MethodGet, url, nil)
}

func TestVenueParamNormalization(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"binance": "binance",
		"  OKX  ": "okx",
		"Bybit":   "bybit",
		"ALL":     "all",
	}
	for in, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.URL.RawQuery = url.Values{"venue": {in}}.Encode()
		if got := venueParam(r); got != want {
			t.Fatalf("venueParam(%q) = %q, want %q", in, got, want)
		}
	}
	if got := venueParam(httptest.NewRequest(http.MethodGet, "/x", nil)); got != "" {
		t.Fatalf("缺省 venue 应为空, got %q", got)
	}
}

func TestResolveVenue(t *testing.T) {
	s, b, k := newRoutingTestServer()

	// 空 / all → Primary(binance,后向兼容)。
	for _, name := range []string{"", "all"} {
		rt, err := s.resolveVenue(name)
		if err != nil || rt != b {
			t.Fatalf("resolveVenue(%q) 应回落 Primary(binance), got rt=%v err=%v", name, rt, err)
		}
	}
	// 具名命中。
	if rt, err := s.resolveVenue("okx"); err != nil || rt != k {
		t.Fatalf("resolveVenue(okx) 应命中 okx, got rt=%v err=%v", rt, err)
	}
	// 未知所报错。
	if _, err := s.resolveVenue("gate"); err == nil {
		t.Fatalf("resolveVenue(gate) 应报未知 venue 错")
	}
}

func TestVenueScopeLabel(t *testing.T) {
	if got := venueScopeLabel("all"); got != venueScopePrimaryOnly {
		t.Fatalf("venueScopeLabel(all) = %q, want %q", got, venueScopePrimaryOnly)
	}
	for _, sel := range []string{"", "binance", "okx"} {
		if got := venueScopeLabel(sel); got != "" {
			t.Fatalf("venueScopeLabel(%q) = %q, want empty", sel, got)
		}
	}
}

func TestReadTargets(t *testing.T) {
	s, b, k := newRoutingTestServer()

	// 缺省 → [primary]。
	got, err := s.readTargets(reqWithVenue(""))
	if err != nil || len(got) != 1 || got[0] != b {
		t.Fatalf("缺省 readTargets 应为 [binance], got %v err=%v", got, err)
	}
	// 具名 → [named]。
	got, err = s.readTargets(reqWithVenue("okx"))
	if err != nil || len(got) != 1 || got[0] != k {
		t.Fatalf("readTargets(okx) 应为 [okx], got %v err=%v", got, err)
	}
	// all → 全部(注册序)。
	got, err = s.readTargets(reqWithVenue("all"))
	if err != nil || len(got) != 2 || got[0] != b || got[1] != k {
		t.Fatalf("readTargets(all) 应为 [binance, okx], got %v err=%v", got, err)
	}
	// 未知 → 错。
	if _, err := s.readTargets(reqWithVenue("gate")); err == nil {
		t.Fatalf("readTargets(gate) 应报错")
	}
}

func TestResolveControlTargetRejectsAll(t *testing.T) {
	s, b, k := newRoutingTestServer()

	// 缺省 → primary,可继续。
	w := httptest.NewRecorder()
	if rt, ok := s.resolveControlTarget(w, reqWithVenue(""), "stop"); !ok || rt != b {
		t.Fatalf("缺省控制目标应为 binance 且可继续")
	}
	// 具名 → 命中。
	w = httptest.NewRecorder()
	if rt, ok := s.resolveControlTarget(w, reqWithVenue("okx"), "stop"); !ok || rt != k {
		t.Fatalf("控制目标 okx 应命中")
	}
	// all → 400 拒绝写扇出。
	w = httptest.NewRecorder()
	if _, ok := s.resolveControlTarget(w, reqWithVenue("all"), "stop"); ok {
		t.Fatalf("控制类 venue=all 应被拒绝")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("venue=all 写操作应返回 400, got %d", w.Code)
	}
	// 未知 → 404。
	w = httptest.NewRecorder()
	if _, ok := s.resolveControlTarget(w, reqWithVenue("gate"), "stop"); ok {
		t.Fatalf("未知所控制应被拒绝")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知 venue 控制应返回 404, got %d", w.Code)
	}
}

// TestAggregateRecordsInjectsVenue 验证 venue=all 跨所聚合读时为每条记录注入来源所标签(§9.2),
// 且 injectVenue=false(单所/默认)时不注入(落盘/默认输出零变化)。
func TestAggregateRecordsInjectsVenue(t *testing.T) {
	mkStore := func(cycle string) *ReportStore {
		store, err := NewReportStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewReportStore() error = %v", err)
		}
		ts, _ := time.Parse(time.RFC3339, "2026-03-01T08:00:00Z")
		if err := store.AppendSettlement(CycleSettlement{
			RunID: "r", CycleID: cycle, CloseExecutedAt: ts, FundingSettledAt: ts, NetPnL: 1,
		}); err != nil {
			t.Fatalf("AppendSettlement() error = %v", err)
		}
		return store
	}
	b := &venueRuntime{name: "binance", store: mkStore("cb")}
	k := &venueRuntime{name: "okx", store: mkStore("ck")}
	targets := []*venueRuntime{b, k}

	// injectVenue=true:每条记录带来源所标签。
	recs, err := aggregateRecords(targets, 0, (*ReportStore).ListSettlementRecords, true)
	if err != nil {
		t.Fatalf("aggregateRecords() error = %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("应聚合两所各 1 条, got %d", len(recs))
	}
	if recs[0].Venue != "binance" || recs[1].Venue != "okx" {
		t.Fatalf("venue 注入错误: %q / %q", recs[0].Venue, recs[1].Venue)
	}

	// injectVenue=false:不注入 → Venue 为空(默认/单所输出逐字节同改造前)。
	recs2, err := aggregateRecords(targets, 0, (*ReportStore).ListSettlementRecords, false)
	if err != nil {
		t.Fatalf("aggregateRecords() error = %v", err)
	}
	for i, rec := range recs2 {
		if rec.Venue != "" {
			t.Fatalf("injectVenue=false 不应注入 venue, 条目%d=%q", i, rec.Venue)
		}
	}
}
