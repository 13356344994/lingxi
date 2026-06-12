package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEvaluateReadiness 钉死就绪判定:运行+未熔断+健康正常→就绪;否则不就绪并给原因。
func TestEvaluateReadiness(t *testing.T) {
	if ready, _ := evaluateReadiness(RuntimeTaskStatus{Running: true, HealthStatus: "healthy"}); !ready {
		t.Fatal("运行+健康应就绪")
	}
	if ready, reason := evaluateReadiness(RuntimeTaskStatus{Running: false}); ready || reason != "engine_not_running" {
		t.Fatalf("未运行应不就绪, ready=%v reason=%s", ready, reason)
	}
	if ready, reason := evaluateReadiness(RuntimeTaskStatus{Running: true, CircuitBreakerTripped: true, CircuitBreakerReason: "日亏熔断"}); ready || reason != "日亏熔断" {
		t.Fatalf("熔断应不就绪并带原因, ready=%v reason=%s", ready, reason)
	}
	if ready, reason := evaluateReadiness(RuntimeTaskStatus{Running: true, HealthStatus: "paused"}); ready || reason != "health_paused" {
		t.Fatalf("paused 应不就绪, ready=%v reason=%s", ready, reason)
	}
}

// TestEvaluateMultiVenueReadiness 钉死多 venue 就绪判定:任一 venue 就绪即整体就绪
// (只跑 Bybit 时不得因币安未运行而 503,否则外部 watchdog 会误杀健康进程);
// 全部不就绪时 reason 按 venue 前缀汇总。
func TestEvaluateMultiVenueReadiness(t *testing.T) {
	if ready, reason := evaluateMultiVenueReadiness(nil); ready || reason != "no_venues" {
		t.Fatalf("无 venue 应不就绪, ready=%v reason=%s", ready, reason)
	}
	// 币安停着 + Bybit 在跑 → 就绪(修 watchdog 误杀的核心场景)。
	ready, reason := evaluateMultiVenueReadiness([]venueMetricSample{
		{Venue: "binance", Status: RuntimeTaskStatus{Running: false}},
		{Venue: "bybit", Status: RuntimeTaskStatus{Running: true, HealthStatus: "healthy"}},
	})
	if !ready || reason != "ok:bybit" {
		t.Fatalf("任一所运行即应就绪, ready=%v reason=%s", ready, reason)
	}
	// 全部不就绪 → 503,reason 按 venue 汇总。
	ready, reason = evaluateMultiVenueReadiness([]venueMetricSample{
		{Venue: "binance", Status: RuntimeTaskStatus{Running: false}},
		{Venue: "bybit", Status: RuntimeTaskStatus{Running: true, CircuitBreakerTripped: true, CircuitBreakerReason: "日亏熔断"}},
	})
	if ready || reason != "binance:engine_not_running;bybit:日亏熔断" {
		t.Fatalf("全部不就绪时应汇总原因, ready=%v reason=%s", ready, reason)
	}
	// 无名 venue(裸 DashboardServer 回退)不加前缀,保持探针兼容。
	if ready, reason := evaluateMultiVenueReadiness([]venueMetricSample{{Venue: "", Status: RuntimeTaskStatus{Running: false}}}); ready || reason != "engine_not_running" {
		t.Fatalf("无名 venue 不应加前缀, ready=%v reason=%s", ready, reason)
	}
}

// TestHealthzFourVenuesOnlyBybitRunning 四所注册、仅 bybit 运行时 healthz 仍 200(idle 的 okx/gate 不拖垮探针)。
func TestHealthzFourVenuesOnlyBybitRunning(t *testing.T) {
	orch := NewOrchestrator()
	names := []string{"binance", "bybit", "okx", "gate"}
	for _, name := range names {
		dir := t.TempDir()
		store, err := NewReportStore(dir)
		if err != nil {
			t.Fatalf("NewReportStore(%s): %v", name, err)
		}
		ctrl, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
		if err != nil {
			t.Fatalf("NewRuntimeController(%s): %v", name, err)
		}
		ctrl.venueName = name
		if name == "bybit" {
			ctrl.mu.Lock()
			ctrl.status = RuntimeTaskStatus{Running: true, HealthStatus: "healthy"}
			ctrl.mu.Unlock()
		}
		orch.Register(&venueRuntime{name: name, controller: ctrl})
	}
	s := &DashboardServer{orchestrator: orch, startedAt: time.Now()}
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("仅 bybit 运行时应 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("响应应 ready=true: %s", rec.Body.String())
	}
	for _, v := range names {
		if !strings.Contains(rec.Body.String(), `"`+v+`"`) {
			t.Fatalf("healthz 应包含 venue %q: %s", v, rec.Body.String())
		}
	}
}

// TestBuildPrometheusMetrics 钉死指标输出:进程级无标签、引擎级带 venue 标签、gear/health 标签、布尔映射正确。
func TestBuildPrometheusMetrics(t *testing.T) {
	out := buildPrometheusMetrics([]venueMetricSample{{
		Venue: "binance",
		Status: RuntimeTaskStatus{
			Running:               true,
			CircuitBreakerTripped: false,
			HealthScore:           0.8,
			HealthStatus:          "healthy",
			EngineGear:            "normal",
			CurrentTargets:        3,
			PlatformRejectCount:   2,
		},
	}}, 123.0)

	for _, want := range []string{
		"lingxi_up 1",
		"lingxi_uptime_seconds 123",
		`lingxi_engine_running{venue="binance"} 1`,
		`lingxi_engine_ready{venue="binance"} 1`,
		`lingxi_circuit_breaker_tripped{venue="binance"} 0`,
		`lingxi_health_score{venue="binance"} 0.8`,
		`lingxi_current_targets{venue="binance"} 3`,
		`lingxi_platform_reject_count{venue="binance"} 2`,
		`lingxi_engine_gear_info{venue="binance",gear="normal"} 1`,
		`lingxi_engine_health_info{venue="binance",status="healthy"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("指标输出缺少 %q\n---\n%s", want, out)
		}
	}

	// 每个 metric 的 HELP/TYPE 只出现一次(多所时不得重复)。
	if strings.Count(out, "# TYPE lingxi_engine_running gauge") != 1 {
		t.Fatalf("HELP/TYPE 应每 metric 仅一次:\n%s", out)
	}

	// 多所:两行带不同 venue 标签的样本。
	multi := buildPrometheusMetrics([]venueMetricSample{
		{Venue: "binance", Status: RuntimeTaskStatus{Running: true}},
		{Venue: "okx", Status: RuntimeTaskStatus{Running: false}},
	}, 1)
	if !strings.Contains(multi, `lingxi_engine_running{venue="binance"} 1`) || !strings.Contains(multi, `lingxi_engine_running{venue="okx"} 0`) {
		t.Fatalf("多所指标应按 venue 拆行:\n%s", multi)
	}
	if strings.Count(multi, "# TYPE lingxi_engine_running gauge") != 1 {
		t.Fatalf("多所时 HELP/TYPE 仍应每 metric 仅一次:\n%s", multi)
	}

	// 熔断时 ready=0。
	out2 := buildPrometheusMetrics([]venueMetricSample{{Venue: "binance", Status: RuntimeTaskStatus{Running: true, CircuitBreakerTripped: true, HealthStatus: "paused"}}}, 1)
	if !strings.Contains(out2, `lingxi_engine_ready{venue="binance"} 0`) || !strings.Contains(out2, `lingxi_circuit_breaker_tripped{venue="binance"} 1`) {
		t.Fatalf("熔断态指标不正确:\n%s", out2)
	}
}

// TestSanitizeLabelValue 钉死标签转义。
func TestSanitizeLabelValue(t *testing.T) {
	if got := sanitizeLabelValue(`a"b\c` + "\n"); got != `a\"b\\c\n` {
		t.Fatalf("标签转义错误: %q", got)
	}
}

// TestDashboardProbeHandlers 钉死 /livez 恒 200、/healthz 据就绪返回 200/503、/metrics 返回文本。
func TestDashboardProbeHandlers(t *testing.T) {
	s := &DashboardServer{} // controller nil → Status 零值(未运行)

	// /livez 恒 200。
	rec := httptest.NewRecorder()
	s.handleLivez(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/livez 应 200, got %d", rec.Code)
	}

	// /healthz:controller 为空 → 未运行 → 503。
	rec = httptest.NewRecorder()
	s.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未运行时 /healthz 应 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "engine_not_running") {
		t.Fatalf("/healthz 应给出原因, got %s", rec.Body.String())
	}

	// /metrics 返回 Prometheus 文本。
	rec = httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "lingxi_up 1") {
		t.Fatalf("/metrics 应 200 且含 lingxi_up, code=%d body=%s", rec.Code, rec.Body.String())
	}
}
