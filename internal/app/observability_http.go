package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// observability_http.go 「存活探针 + 指标导出 + 心跳文件」:
//   - /livez   纯进程存活(只要 HTTP 在跑就 200);
//   - /healthz 就绪/健康(进程在跑即 200;响应体带各所方向引擎状态);
//   - /metrics Prometheus 文本格式指标(无新依赖,手写 exposition);
//   - 心跳文件(records/heartbeat.json)由进程周期写入,供外部看门狗判活。
// 三个 HTTP 端点均**不鉴权**(探针/抓取不带 token),且只暴露非敏感的运行健康信息。

const (
	heartbeatFileName        = "heartbeat.json"
	defaultHeartbeatInterval = 15 * time.Second
)

// heartbeatPayload 是心跳文件内容:外部看门狗据 ts 判活。
type heartbeatPayload struct {
	TS    int64  `json:"ts"`
	Time  string `json:"time"`
	PID   int    `json:"pid"`
	Stage string `json:"stage"`
}

// startProcessHeartbeatWriter 进程级心跳:只要 lingxi 进程在跑就周期写 records/heartbeat.json。
func startProcessHeartbeatWriter(ctx context.Context, recordDir string, logger *log.Logger) {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, heartbeatFileName)
	writeProcessHeartbeat(path, "process", logger)
	go func() {
		ticker := time.NewTicker(defaultHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeProcessHeartbeat(path, "process", logger)
			}
		}
	}()
}

// writeProcessHeartbeat 写进程级心跳(best-effort,失败仅记日志)。
func writeProcessHeartbeat(path, stage string, logger *log.Logger) {
	now := time.Now().UTC()
	payload := heartbeatPayload{
		TS:    now.Unix(),
		Time:  now.Format(time.RFC3339),
		PID:   os.Getpid(),
		Stage: stage,
	}
	if err := writeJSONFile(path, payload); err != nil && logger != nil {
		logger.Printf("写进程心跳失败: %v", err)
	}
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

// directionalMetricSample 是单个所方向引擎的指标样本。
type directionalMetricSample struct {
	Venue         string
	Active        bool
	OpenPositions int
}

// directionalMetricSamples 收集各所方向引擎状态(venue 名排序,稳定输出)。
func (s *DashboardServer) directionalMetricSamples() []directionalMetricSample {
	if s == nil || len(s.directionalEngines) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.directionalEngines))
	for name := range s.directionalEngines {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]directionalMetricSample, 0, len(names))
	for _, name := range names {
		eng := s.directionalEngines[name]
		if eng == nil {
			continue
		}
		st := eng.OKXDirectionalStatus()
		out = append(out, directionalMetricSample{
			Venue:         name,
			Active:        st.Active,
			OpenPositions: len(st.Positions),
		})
	}
	return out
}

// buildPrometheusMetrics 渲染 Prometheus 文本格式(纯函数,可单测)。只暴露非敏感健康/计数指标。
func buildPrometheusMetrics(samples []directionalMetricSample, uptimeSeconds float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP lingxi_up Process is up (always 1 when scraped).\n# TYPE lingxi_up gauge\nlingxi_up 1\n")
	fmt.Fprintf(&b, "# HELP lingxi_uptime_seconds Seconds since dashboard server started.\n# TYPE lingxi_uptime_seconds gauge\nlingxi_uptime_seconds %g\n", uptimeSeconds)

	fmt.Fprintf(&b, "# HELP lingxi_directional_active Directional engine accepting new opens (1) or not (0).\n# TYPE lingxi_directional_active gauge\n")
	for _, sm := range samples {
		fmt.Fprintf(&b, "lingxi_directional_active{venue=\"%s\"} %d\n", sanitizeLabelValue(sm.Venue), boolGauge(sm.Active))
	}
	fmt.Fprintf(&b, "# HELP lingxi_directional_open_positions Open directional positions per venue.\n# TYPE lingxi_directional_open_positions gauge\n")
	for _, sm := range samples {
		fmt.Fprintf(&b, "lingxi_directional_open_positions{venue=\"%s\"} %d\n", sanitizeLabelValue(sm.Venue), sm.OpenPositions)
	}
	return b.String()
}

// handleLivez 进程存活探针:只要 HTTP 在响应即 200。
func (s *DashboardServer) handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleHealthz 就绪/健康探针:进程与面板在跑即 200;响应体带各所方向引擎状态。
// (方向引擎默认停用、需面板点开始;「未激活」是合法运维状态,不应让 watchdog 误杀健康进程。)
func (s *DashboardServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	samples := s.directionalMetricSamples()
	venues := make(map[string]any, len(samples))
	for _, sm := range samples {
		venues[sm.Venue] = map[string]any{
			"active":         sm.Active,
			"open_positions": sm.OpenPositions,
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ready":       true,
		"reason":      "ok",
		"directional": venues,
	})
}

// handleMetrics 暴露 Prometheus 文本格式指标。
func (s *DashboardServer) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	samples := s.directionalMetricSamples()
	uptime := time.Since(s.startedAt).Seconds()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(buildPrometheusMetrics(samples, uptime)))
}
