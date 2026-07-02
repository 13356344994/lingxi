package app

// EventLevel 描述结构化事件/告警的观测级别(alerting 消费)。
type EventLevel string

const (
	EventLevelInfo     EventLevel = "info"
	EventLevelWarning  EventLevel = "warning"
	EventLevelCritical EventLevel = "critical"
)
