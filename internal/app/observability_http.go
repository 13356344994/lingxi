package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// observability_http.go 实现 S5-B 的「存活探针 + 指标导出 + 心跳文件」:
//   - /livez   纯进程存活(只要 HTTP 在跑就 200),供 k8s/负载均衡 liveness 探测;
//   - /healthz 就绪/健康(引擎在跑、未熔断、健康态正常才 200,否则 503),供 readiness 探测与外部告警轮询;
//   - /metrics Prometheus 文本格式指标(无新依赖,手写 exposition),供 Prometheus 抓取;
//   - 心跳文件(records/heartbeat.json)由引擎周期写入,供「无 HTTP 的简单外部看门狗/cron」判活(S7 复用)。
// 三个 HTTP 端点均**不鉴权**(探针/抓取不带 token),且只暴露非敏感的运行健康信息。

const (
	heartbeatFileName        = "heartbeat.json"
	defaultHeartbeatInterval = 15 * time.Second
)

// heartbeatPayload 是心跳文件内容:外部看门狗据 ts 判活(now-ts 超阈值即视为卡死/已死)。
type heartbeatPayload struct {
	TS      int64  `json:"ts"`       // Unix 秒
	Time    string `json:"time"`     // RFC3339(人读)
	PID     int    `json:"pid"`      //
	RunID   string `json:"run_id"`   //
	CycleID string `json:"cycle_id"` //
	Stage   string `json:"stage"`    //
}

// heartbeatPath 返回心跳文件路径;RecordDir 为空时返回空串(禁用心跳)。
func (e *FundingRateSniperEngine) heartbeatPath() string {
	dir := strings.TrimSpace(e.cfg.NetworkConfig.RecordDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, heartbeatFileName)
}

// startHeartbeatWriter 启动周期心跳写入 goroutine(默认每 15s)。立即写一次再进入周期,ctx 取消即退出。
func (e *FundingRateSniperEngine) startHeartbeatWriter(ctx context.Context) {
	path := e.heartbeatPath()
	if path == "" {
		return
	}
	e.writeHeartbeat(path)
	go func() {
		ticker := time.NewTicker(defaultHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.writeHeartbeat(path)
			}
		}
	}()
}

// writeHeartbeat 原子写心跳文件(写临时文件 + rename,避免外部读到半截)。best-effort:失败仅记日志,不影响交易。
func (e *FundingRateSniperEngine) writeHeartbeat(path string) {
	runID, cycleID := e.traceIDs()
	now := e.serverNow()
	payload := heartbeatPayload{
		TS:      now.Unix(),
		Time:    now.UTC().Format(time.RFC3339),
		PID:     os.Getpid(),
		RunID:   runID,
		CycleID: cycleID,
		Stage:   string(e.currentLifecycleStage),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		if e.logger != nil {
			e.logger.Printf("写心跳文件失败: %v", err)
		}
		return
	}
	if err := os.Rename(tmp, path); err != nil && e.logger != nil {
		e.logger.Printf("提交心跳文件失败: %v", err)
	}
}

// evaluateReadiness 判定引擎是否就绪(纯函数,可单测)。未运行 / 熔断 / 健康态为 failed|paused → 未就绪。
func evaluateReadiness(status RuntimeTaskStatus) (bool, string) {
	if !status.Running {
		return false, "engine_not_running"
	}
	if status.CircuitBreakerTripped {
		reason := strings.TrimSpace(status.CircuitBreakerReason)
		if reason == "" {
			reason = "circuit_breaker_tripped"
		}
		return false, reason
	}
	switch strings.ToLower(strings.TrimSpace(status.HealthStatus)) {
	case "failed", "paused":
		return false, "health_" + strings.ToLower(strings.TrimSpace(status.HealthStatus))
	}
	return true, "ok"
}

// boolGauge 把布尔转 Prometheus gauge 值。
func boolGauge(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sanitizeLabelValue 转义 Prometheus 标签值中的反斜杠、引号、换行。
func sanitizeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// venueMetricSample 是单个所的指标样本(§12.1:四家并行时按 venue 拆指标)。Venue 为空时该所样本无 venue 标签。
type venueMetricSample struct {
	Venue  string
	Status RuntimeTaskStatus
}

// buildPrometheusMetrics 把各所运行状态渲染为 Prometheus 文本格式(纯函数,可单测)。只暴露非敏感健康/计数指标。
// 进程级指标(lingxi_up/uptime)全局无标签;引擎级指标按 §12.1 带 venue 标签——每个 metric 的 HELP/TYPE 仅一次,
// 后随每所一行带 venue 标签的样本(单所时即一行 {venue="binance"})。
func buildPrometheusMetrics(samples []venueMetricSample, uptimeSeconds float64) string {
	var b strings.Builder
	// 进程级(无 venue 标签):整进程一份。
	fmt.Fprintf(&b, "# HELP lingxi_up Process is up (always 1 when scraped).\n# TYPE lingxi_up gauge\nlingxi_up 1\n")
	fmt.Fprintf(&b, "# HELP lingxi_uptime_seconds Seconds since dashboard server started.\n# TYPE lingxi_uptime_seconds gauge\nlingxi_uptime_seconds %g\n", uptimeSeconds)

	// venueLabel 渲染 {venue="..."};venue 为空则不加标签(回退全局,保兼容)。
	venueLabel := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return fmt.Sprintf("{venue=\"%s\"}", sanitizeLabelValue(v))
	}
	writeVenueGauge := func(name, help string, val func(RuntimeTaskStatus) float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, s := range samples {
			fmt.Fprintf(&b, "%s%s %g\n", name, venueLabel(s.Venue), val(s.Status))
		}
	}
	writeVenueGauge("lingxi_engine_running", "Engine live task running (1) or not (0).", func(st RuntimeTaskStatus) float64 { return float64(boolGauge(st.Running)) })
	writeVenueGauge("lingxi_engine_ready", "Engine ready: running, not fused, health not failed/paused.", func(st RuntimeTaskStatus) float64 {
		ready, _ := evaluateReadiness(st)
		return float64(boolGauge(ready))
	})
	writeVenueGauge("lingxi_circuit_breaker_tripped", "Platform circuit breaker tripped (1) or not (0).", func(st RuntimeTaskStatus) float64 { return float64(boolGauge(st.CircuitBreakerTripped)) })
	writeVenueGauge("lingxi_health_score", "Engine health score in [0,1].", func(st RuntimeTaskStatus) float64 { return st.HealthScore })
	writeVenueGauge("lingxi_current_targets", "Current monitored/target symbols count.", func(st RuntimeTaskStatus) float64 { return float64(st.CurrentTargets) })
	writeVenueGauge("lingxi_platform_reject_count", "Cumulative platform reject count.", func(st RuntimeTaskStatus) float64 { return float64(st.PlatformRejectCount) })
	writeVenueGauge("lingxi_platform_request_errors", "Cumulative platform request error count.", func(st RuntimeTaskStatus) float64 { return float64(st.PlatformRequestErrors) })
	writeVenueGauge("lingxi_recovery_action_count", "Cumulative recovery action count.", func(st RuntimeTaskStatus) float64 { return float64(st.RecoveryActionCount) })
	writeVenueGauge("lingxi_order_uncertainty_count", "Cumulative uncertain-order count.", func(st RuntimeTaskStatus) float64 { return float64(st.OrderUncertaintyCount) })

	// gear/health info:value 恒 1,信息在标签(venue + gear/status)。
	fmt.Fprintf(&b, "# HELP lingxi_engine_gear_info Current engine gear (value always 1, gear in label).\n# TYPE lingxi_engine_gear_info gauge\n")
	for _, s := range samples {
		gear := strings.TrimSpace(s.Status.EngineGear)
		if gear == "" {
			gear = "unknown"
		}
		fmt.Fprintf(&b, "lingxi_engine_gear_info%s 1\n", labelSet(s.Venue, "gear", gear))
	}
	fmt.Fprintf(&b, "# HELP lingxi_engine_health_info Current health status (value always 1, status in label).\n# TYPE lingxi_engine_health_info gauge\n")
	for _, s := range samples {
		healthStatus := strings.TrimSpace(s.Status.HealthStatus)
		if healthStatus == "" {
			healthStatus = "unknown"
		}
		fmt.Fprintf(&b, "lingxi_engine_health_info%s 1\n", labelSet(s.Venue, "status", healthStatus))
	}
	return b.String()
}

// labelSet 渲染 Prometheus 标签集:venue 非空则带 venue 标签,外加给定 key=val(均转义)。
func labelSet(venue, key, val string) string {
	if strings.TrimSpace(venue) == "" {
		return fmt.Sprintf("{%s=\"%s\"}", key, sanitizeLabelValue(val))
	}
	return fmt.Sprintf("{venue=\"%s\",%s=\"%s\"}", sanitizeLabelValue(venue), key, sanitizeLabelValue(val))
}

// handleLivez 进程存活探针:只要 HTTP 在响应即 200。
func (s *DashboardServer) handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleHealthz 就绪/健康探针:就绪 200,否则 503。返回 JSON {ready, reason, ...}。
func (s *DashboardServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	status := s.controllerStatus()
	ready, reason := evaluateReadiness(status)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if ready {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ready":                   ready,
		"reason":                  reason,
		"running":                 status.Running,
		"health_status":           status.HealthStatus,
		"health_score":            status.HealthScore,
		"circuit_breaker_tripped": status.CircuitBreakerTripped,
		"engine_gear":             status.EngineGear,
	})
}

// handleMetrics 暴露 Prometheus 文本格式指标(§12.1 按 venue 拆引擎级指标)。
func (s *DashboardServer) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	samples := s.venueMetricSamples()
	uptime := time.Since(s.startedAt).Seconds()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(buildPrometheusMetrics(samples, uptime)))
}

// venueMetricSamples 收集各所(注册序)的运行状态样本供 /metrics 按 venue 拆指标。
// 无编排器(裸 DashboardServer,如探针单测)→ 回退单条无标签样本(取 s.controller 状态),保兼容。
func (s *DashboardServer) venueMetricSamples() []venueMetricSample {
	if s == nil || s.orchestrator == nil {
		return []venueMetricSample{{Venue: "", Status: s.controllerStatus()}}
	}
	var out []venueMetricSample
	for _, rt := range s.orchestrator.All() {
		if rt == nil {
			continue
		}
		st := RuntimeTaskStatus{}
		if rt.controller != nil {
			st = rt.controller.Status()
		}
		out = append(out, venueMetricSample{Venue: rt.name, Status: st})
	}
	if len(out) == 0 {
		out = append(out, venueMetricSample{Venue: "", Status: s.controllerStatus()})
	}
	return out
}

// controllerStatus 安全读取控制器状态;控制器为空时返回零值(未运行)。
func (s *DashboardServer) controllerStatus() RuntimeTaskStatus {
	if s.controller == nil {
		return RuntimeTaskStatus{}
	}
	return s.controller.Status()
}
