package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

var errTest = errors.New("test sink failure")

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

// scriptedAlertSink 按预设错误序列响应,记录被调用次数,用于断言重试行为。
type scriptedAlertSink struct {
	mu    sync.Mutex
	errs  []error // 第 i 次调用返回 errs[i];超出长度返回 nil(成功)
	calls int
}

func (s *scriptedAlertSink) Name() string { return "scripted" }

func (s *scriptedAlertSink) Dispatch(_ context.Context, _ Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.calls
	s.calls++
	if idx < len(s.errs) {
		return s.errs[idx]
	}
	return nil
}

func (s *scriptedAlertSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestClassifyHTTPStatus(t *testing.T) {
	for _, c := range []struct {
		code int
		want bool
	}{{429, true}, {500, true}, {503, true}, {400, false}, {401, false}, {403, false}, {404, false}} {
		if got := classifyHTTPStatus(c.code); got != c.want {
			t.Fatalf("classifyHTTPStatus(%d)=%v want %v", c.code, got, c.want)
		}
	}
}

func TestDispatchRetriesTransientThenSucceeds(t *testing.T) {
	sink := &scriptedAlertSink{errs: []error{
		&sinkError{Retryable: true, err: errTest},
		&sinkError{Retryable: true, err: errTest},
	}}
	d := &AlertDispatcher{sinks: []AlertSink{sink}, minRank: severityRank(EventLevelInfo), timeout: time.Second, now: time.Now, lastSent: map[string]time.Time{}}
	d.dispatchToSinkWithRetry(sink, Alert{Level: EventLevelCritical, Title: "x"}, time.Second)
	if sink.count() != 3 {
		t.Fatalf("两次可重试失败后应在第3次成功, calls=%d", sink.count())
	}
}

func TestDispatchStopsOnPermanentError(t *testing.T) {
	sink := &scriptedAlertSink{errs: []error{&sinkError{Retryable: false, err: errTest}}}
	d := &AlertDispatcher{sinks: []AlertSink{sink}, timeout: time.Second, now: time.Now, lastSent: map[string]time.Time{}}
	d.dispatchToSinkWithRetry(sink, Alert{Level: EventLevelCritical, Title: "x"}, time.Second)
	if sink.count() != 1 {
		t.Fatalf("永久失败应只尝试1次, calls=%d", sink.count())
	}
}

func TestTelegramRetryAfterParsing(t *testing.T) {
	body := []byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`)
	if got := telegramRetryAfter(body); got != 7*time.Second {
		t.Fatalf("应解析 retry_after=7s, got %s", got)
	}
	if got := telegramRetryAfter([]byte(`{"ok":true}`)); got != 0 {
		t.Fatalf("无 retry_after 应为 0, got %s", got)
	}
}

func TestTelegramAlertSinkStubbed(t *testing.T) {
	var gotChat, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ChatID string `json:"chat_id"`
			Text   string `json:"text"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		gotChat, gotText = payload.ChatID, payload.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sink := &telegramAlertSink{token: "tok", chatID: "123", client: srv.Client(), baseURL: srv.URL}
	if err := sink.Dispatch(context.Background(), Alert{Level: EventLevelCritical, Title: "开仓成功", Detail: "BTC", Symbol: "BTCUSDT"}); err != nil {
		t.Fatalf("telegram 推送应成功: %v", err)
	}
	if gotChat != "123" || gotText == "" {
		t.Fatalf("telegram payload 不正确: chat=%q text=%q", gotChat, gotText)
	}

	// 429 → 返回可重试 sinkError 且带 RetryAfter。
	srv429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"parameters":{"retry_after":3}}`))
	}))
	defer srv429.Close()
	sink429 := &telegramAlertSink{token: "tok", chatID: "123", client: srv429.Client(), baseURL: srv429.URL}
	err := sink429.Dispatch(context.Background(), Alert{Level: EventLevelCritical, Title: "x"})
	if !retryableSink(err) || retryAfterHint(err) != 3*time.Second {
		t.Fatalf("429 应可重试且 retry_after=3s, retryable=%v after=%s", retryableSink(err), retryAfterHint(err))
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
