package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

func TestDeadManRenewInterval(t *testing.T) {
	if got := deadManRenewInterval(0); got != 0 {
		t.Fatalf("countdown=0 应返回 0, got %v", got)
	}
	if got := deadManRenewInterval(30 * time.Second); got != 10*time.Second {
		t.Fatalf("30s 应每 10s 续期, got %v", got)
	}
	// 极短倒计时下限保护。
	if got := deadManRenewInterval(6 * time.Second); got < deadManMinRenewInterval {
		t.Fatalf("续期间隔不应低于下限, got %v", got)
	}
	// 续期间隔必须严格小于倒计时(到期前能续上)。
	if got := deadManRenewInterval(30 * time.Second); got >= 30*time.Second {
		t.Fatalf("续期间隔须 < 倒计时, got %v", got)
	}
}

func TestDeadManArmSymbols(t *testing.T) {
	hedges := []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT"}},
		{Target: FundingCandidate{Symbol: "ETHUSDT"}},
		{Target: FundingCandidate{Symbol: "BTCUSDT"}}, // 去重
		{Target: FundingCandidate{Symbol: ""}},        // 跳过空
	}
	got := deadManArmSymbols(hedges)
	if len(got) != 2 || got[0] != "BTCUSDT" || got[1] != "ETHUSDT" {
		t.Fatalf("应去重并排序为 [BTCUSDT ETHUSDT], got %v", got)
	}
	if got := deadManArmSymbols(nil); len(got) != 0 {
		t.Fatalf("空入参应返回空, got %v", got)
	}
}

// TestSetCountdownCancelAllArmAndDisarm 钉死 arm(countdownTime=ms)与 disarm(0)都打到正确端点与参数。
func TestSetCountdownCancelAllArmAndDisarm(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var countdowns []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		paths = append(paths, r.URL.Path)
		countdowns = append(countdowns, r.Form.Get("countdownTime"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","countdownTime":"30000"}`))
	}))
	t.Cleanup(server.Close)

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "k",
			SecretKey:      "s",
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}

	if err := engine.setCountdownCancelAll(context.Background(), "BTCUSDT", 30000); err != nil {
		t.Fatalf("arm 应成功: %v", err)
	}
	if err := engine.setCountdownCancelAll(context.Background(), "BTCUSDT", 0); err != nil {
		t.Fatalf("disarm 应成功: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 {
		t.Fatalf("应有 2 次调用, got %d", len(paths))
	}
	for _, p := range paths {
		if p != "/fapi/v1/countdownCancelAll" {
			t.Fatalf("端点错误: %s", p)
		}
	}
	if countdowns[0] != "30000" || countdowns[1] != "0" {
		t.Fatalf("countdownTime 参数错误: %v", countdowns)
	}
}

// TestStartDeadMansSwitchDisabledNoop 钉死未启用时不发任何请求。
func TestStartDeadMansSwitchDisabledNoop(t *testing.T) {
	var called bool
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.DeadMansSwitchEnabled = false
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{FuturesBaseURL: server.URL, APIKey: "k", SecretKey: "s", HTTPClient: server.Client()}),
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine.startDeadMansSwitch(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatal("未启用时不应发任何请求")
	}
}
