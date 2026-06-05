package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// alerting.go 实现「关键事件实时告警」分发(S5-A / R25):把引擎产生的结构化领域事件(DomainEvent)按级别过滤、
// 按(级别+标题+symbol)去重后,经一个或多个出口(webhook / Telegram / 本地日志)异步推送。设计要点:
//   - 默认关闭(AlertingConfig.Enabled=false → newAlertDispatcher 返回 nil),禁用时引擎不产生任何外部调用;
//   - 推送 fire-and-forget(各出口独立超时),绝不阻塞引擎热路径;
//   - 路由/去重/分级是可单测的纯逻辑(shouldDispatch),与 I/O 出口(AlertSink)解耦。

// Alert 是一条对外推送的告警。Level 复用 DomainEvent 的 EventLevel(info|warning|critical)。
type Alert struct {
	Level      EventLevel
	Title      string
	Detail     string
	Symbol     string
	CycleID    string
	OccurredAt time.Time
}

// AlertSink 是告警出口的最小接口。Dispatch 须自带超时与幂等,不得对引擎状态产生副作用。
type AlertSink interface {
	Dispatch(ctx context.Context, alert Alert) error
	Name() string
}

// severityRank 把级别映射成可比较的序:info(1) < warning(2) < critical(3);未知级别为 0(低于一切阈值)。
func severityRank(level EventLevel) int {
	switch level {
	case EventLevelCritical:
		return 3
	case EventLevelWarning:
		return 2
	case EventLevelInfo:
		return 1
	default:
		return 0
	}
}

// minSeverityRank 解析配置的最低推送级别;空或非法时回退到 warning(只推 warning 及以上)。
func minSeverityRank(minSeverity string) int {
	switch strings.ToLower(strings.TrimSpace(minSeverity)) {
	case "info":
		return severityRank(EventLevelInfo)
	case "critical":
		return severityRank(EventLevelCritical)
	default: // "" / "warning" / 非法 → warning
		return severityRank(EventLevelWarning)
	}
}

// AlertDispatcher 路由 + 去重 + 分级 + 异步推送。经 newAlertDispatcher 构造;禁用时为 nil(所有方法 nil-safe)。
type AlertDispatcher struct {
	sinks       []AlertSink
	minRank     int
	dedupWindow time.Duration
	timeout     time.Duration
	logger      *log.Logger
	now         func() time.Time // 可注入时钟,便于单测去重窗口

	mu       sync.Mutex
	lastSent map[string]time.Time
}

// newAlertDispatcher 按配置构造分发器。Enabled=false → 返回 nil(零开销、零外部调用)。
// 出口装配:始终包含本地日志出口(带 [ALERT] 前缀,便于在无外部通道时仍有可观测落点);
// 配了 WebhookURL → 追加 webhook 出口;配了 Telegram token+chatID → 追加 Telegram 出口。
func newAlertDispatcher(cfg AlertingConfig, logger *log.Logger) *AlertDispatcher {
	if !cfg.Enabled {
		return nil
	}
	timeout := cfg.DispatchTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}

	sinks := []AlertSink{loggerAlertSink{logger: logger}}
	if strings.TrimSpace(cfg.WebhookURL) != "" {
		sinks = append(sinks, &webhookAlertSink{url: strings.TrimSpace(cfg.WebhookURL), client: httpClient})
	}
	if strings.TrimSpace(cfg.TelegramBotToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "" {
		sinks = append(sinks, &telegramAlertSink{
			token:  strings.TrimSpace(cfg.TelegramBotToken),
			chatID: strings.TrimSpace(cfg.TelegramChatID),
			client: httpClient,
		})
	}
	return &AlertDispatcher{
		sinks:       sinks,
		minRank:     minSeverityRank(cfg.MinSeverity),
		dedupWindow: cfg.DedupWindow,
		timeout:     timeout,
		logger:      logger,
		now:         time.Now,
		lastSent:    make(map[string]time.Time),
	}
}

// shouldDispatch 判定一条告警是否应被推送:级别达标 + 不在去重窗口内。返回 true 时已记录本次发送时刻。纯逻辑、可单测。
func (d *AlertDispatcher) shouldDispatch(alert Alert) bool {
	if d == nil {
		return false
	}
	if severityRank(alert.Level) < d.minRank {
		return false
	}
	if d.dedupWindow <= 0 {
		return true
	}
	key := string(alert.Level) + "|" + alert.Title + "|" + alert.Symbol
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.lastSent[key]; ok && now.Sub(last) < d.dedupWindow {
		return false
	}
	d.lastSent[key] = now
	return true
}

// Notify 是引擎侧的入口:级别/去重达标后,对每个出口 fire-and-forget 异步推送(独立超时),不阻塞调用方。nil-safe。
func (d *AlertDispatcher) Notify(alert Alert) {
	if d == nil || !d.shouldDispatch(alert) {
		return
	}
	d.dispatch(alert)
}

// NotifyTrade 推送交易生命周期通知(开仓/平仓/收益)。与 Notify 不同:不受 MinSeverity 过滤、不做去重——
// 这些是用户主动要的「每笔都通知」,频率低且每条内容不同,去重反而会吞掉合法重复。nil-safe。
func (d *AlertDispatcher) NotifyTrade(alert Alert) {
	if d == nil {
		return
	}
	d.dispatch(alert)
}

// dispatch 对每个出口 fire-and-forget 异步推送(各出口独立超时),不阻塞调用方。
func (d *AlertDispatcher) dispatch(alert Alert) {
	if alert.OccurredAt.IsZero() {
		alert.OccurredAt = d.now()
	}
	sinks := d.sinks
	timeout := d.timeout
	go func() {
		for _, sink := range sinks {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			if err := sink.Dispatch(ctx, alert); err != nil && d.logger != nil {
				d.logger.Printf("告警出口[%s]推送失败: %v", sink.Name(), err)
			}
			cancel()
		}
	}()
}

// domainEventToAlert 把结构化领域事件映射为告警(纯函数)。标题取事件类型,明细取 Reason。
func domainEventToAlert(event DomainEvent) Alert {
	return Alert{
		Level:      event.Level,
		Title:      string(event.Type),
		Detail:     event.Reason,
		CycleID:    event.CycleID,
		OccurredAt: event.OccurredAt,
	}
}

// loggerAlertSink 是兜底出口:把告警写到本地日志(带 [ALERT] 前缀),保证无外部通道时仍有落点。
type loggerAlertSink struct {
	logger *log.Logger
}

func (s loggerAlertSink) Name() string { return "logger" }

func (s loggerAlertSink) Dispatch(_ context.Context, alert Alert) error {
	if s.logger == nil {
		return nil
	}
	s.logger.Printf("[ALERT][%s] %s | %s | cycle=%s", strings.ToUpper(string(alert.Level)), alert.Title, alert.Detail, alert.CycleID)
	return nil
}

// alertWebhookPayload 是通用 webhook 出口的 JSON 体(钉钉/飞书/自建均可消费)。
type alertWebhookPayload struct {
	Level      string `json:"level"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Symbol     string `json:"symbol,omitempty"`
	CycleID    string `json:"cycle_id,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

// webhookAlertSink 把告警以 JSON POST 到通用 webhook。
type webhookAlertSink struct {
	url    string
	client *http.Client
}

func (s *webhookAlertSink) Name() string { return "webhook" }

func (s *webhookAlertSink) Dispatch(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(alertWebhookPayload{
		Level:      string(alert.Level),
		Title:      alert.Title,
		Detail:     alert.Detail,
		Symbol:     alert.Symbol,
		CycleID:    alert.CycleID,
		OccurredAt: alert.OccurredAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// telegramAlertSink 通过 Telegram Bot API sendMessage 推送。
type telegramAlertSink struct {
	token  string
	chatID string
	client *http.Client
}

func (s *telegramAlertSink) Name() string { return "telegram" }

func (s *telegramAlertSink) Dispatch(ctx context.Context, alert Alert) error {
	text := fmt.Sprintf("[%s] %s\n%s", strings.ToUpper(string(alert.Level)), alert.Title, alert.Detail)
	if alert.CycleID != "" {
		text += "\ncycle=" + alert.CycleID
	}
	body, err := json.Marshal(map[string]string{"chat_id": s.chatID, "text": text})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram 返回状态码 %d", resp.StatusCode)
	}
	return nil
}
