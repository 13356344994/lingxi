package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeAlertSink 是捕获型出口,供测试断言。
type fakeAlertSink struct {
	mu       sync.Mutex
	received []Alert
}

func (f *fakeAlertSink) Name() string { return "fake" }

func (f *fakeAlertSink) Dispatch(_ context.Context, alert Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, alert)
	return nil
}

func (f *fakeAlertSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// TestAlertDispatcherDisabledIsNil:Enabled=false 时构造返回 nil,且 nil 的所有方法安全空操作。
func TestAlertDispatcherDisabledIsNil(t *testing.T) {
	d := newAlertDispatcher(AlertingConfig{Enabled: false}, nil)
	if d != nil {
		t.Fatal("禁用时应返回 nil dispatcher")
	}
	if d.shouldDispatch(Alert{Level: EventLevelCritical}) {
		t.Fatal("nil dispatcher 不应放行")
	}
	d.Notify(Alert{Level: EventLevelCritical}) // 不应 panic
}

// TestAlertDispatcherSeverityFloor:低于 MinSeverity 的告警被拦截。
func TestAlertDispatcherSeverityFloor(t *testing.T) {
	d := newAlertDispatcher(AlertingConfig{Enabled: true, MinSeverity: "warning"}, nil)
	if d.shouldDispatch(Alert{Level: EventLevelInfo, Title: "x"}) {
		t.Fatal("info 低于 warning 阈值应被拦截")
	}
	if !d.shouldDispatch(Alert{Level: EventLevelWarning, Title: "y"}) {
		t.Fatal("warning 达标应放行")
	}
	if !d.shouldDispatch(Alert{Level: EventLevelCritical, Title: "z"}) {
		t.Fatal("critical 达标应放行")
	}
}

// TestAlertDispatcherDedup:去重窗口内同 key 只放行一次,窗口外再次放行;窗口=0 时不去重。
func TestAlertDispatcherDedup(t *testing.T) {
	d := newAlertDispatcher(AlertingConfig{Enabled: true, MinSeverity: "info", DedupWindow: time.Minute}, nil)
	base := time.Unix(1_700_000_000, 0)
	d.now = func() time.Time { return base }

	a := Alert{Level: EventLevelWarning, Title: "fuse", Symbol: "BTCUSDT"}
	if !d.shouldDispatch(a) {
		t.Fatal("首次应放行")
	}
	if d.shouldDispatch(a) {
		t.Fatal("窗口内重复应被去重")
	}
	// 不同 symbol → 不同 key,应放行。
	if !d.shouldDispatch(Alert{Level: EventLevelWarning, Title: "fuse", Symbol: "ETHUSDT"}) {
		t.Fatal("不同 symbol 应作为新 key 放行")
	}
	// 窗口外 → 再次放行。
	d.now = func() time.Time { return base.Add(2 * time.Minute) }
	if !d.shouldDispatch(a) {
		t.Fatal("窗口外应再次放行")
	}

	// 窗口=0 → 不去重。
	d0 := newAlertDispatcher(AlertingConfig{Enabled: true, MinSeverity: "info", DedupWindow: 0}, nil)
	if !d0.shouldDispatch(a) || !d0.shouldDispatch(a) {
		t.Fatal("窗口=0 应不去重,连续放行")
	}
}

// TestDomainEventToAlert:领域事件→告警映射。
func TestDomainEventToAlert(t *testing.T) {
	ev := DomainEvent{Type: EventTypeFuseTripped, Level: EventLevelCritical, CycleID: "c1", Reason: "熔断"}
	a := domainEventToAlert(ev)
	if a.Level != EventLevelCritical || a.Title != string(EventTypeFuseTripped) || a.Detail != "熔断" || a.CycleID != "c1" {
		t.Fatalf("映射不正确: %+v", a)
	}
}

// TestWebhookAlertSink:webhook 出口以 JSON POST,断言 payload 与状态码处理。
func TestWebhookAlertSink(t *testing.T) {
	var gotBody alertWebhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &webhookAlertSink{url: srv.URL, client: srv.Client()}
	err := sink.Dispatch(context.Background(), Alert{
		Level: EventLevelCritical, Title: "fuse_tripped", Detail: "platform fuse", CycleID: "c9", OccurredAt: time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("webhook 推送应成功: %v", err)
	}
	if gotBody.Level != "critical" || gotBody.Title != "fuse_tripped" || gotBody.CycleID != "c9" {
		t.Fatalf("webhook payload 不正确: %+v", gotBody)
	}

	// 非 2xx → 报错。
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvErr.Close()
	sinkErr := &webhookAlertSink{url: srvErr.URL, client: srvErr.Client()}
	if err := sinkErr.Dispatch(context.Background(), Alert{Level: EventLevelCritical, Title: "x"}); err == nil {
		t.Fatal("5xx 应返回错误")
	}
}

// TestAlertDispatcherNotifyReachesSink:Notify 异步推送应最终到达出口。
func TestAlertDispatcherNotifyReachesSink(t *testing.T) {
	fake := &fakeAlertSink{}
	d := &AlertDispatcher{
		sinks:    []AlertSink{fake},
		minRank:  severityRank(EventLevelWarning),
		timeout:  time.Second,
		now:      time.Now,
		lastSent: make(map[string]time.Time),
	}
	d.Notify(Alert{Level: EventLevelCritical, Title: "boom"})
	// info 被拦截。
	d.Notify(Alert{Level: EventLevelInfo, Title: "noise"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.count() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fake.count() != 1 {
		t.Fatalf("应只推送 1 条 critical(info 被拦截), got %d", fake.count())
	}
}
