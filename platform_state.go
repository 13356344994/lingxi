package main

import (
	"fmt"
	"strings"
	"time"
)

// EnginePlatformState 维护和平台交互的熔断与冷却状态
type EnginePlatformState struct {
	FuseTripped         bool
	FuseReason          string
	CooldownUntil       time.Time
	
	CurrentCooldownCategory string
	CurrentCooldownSeverity string
	
	LastExceptionCategory   string
	LastExceptionSeverity   string
	LastExceptionDetail     string
	
	OrderRateLimitUntil     time.Time
}

func (s *EnginePlatformState) TripFuse(reason string) {
	s.FuseTripped = true
	s.FuseReason = fmt.Sprintf("平台级风控熔断触发: %s", reason)
}

func (s *EnginePlatformState) ClearFuse() {
	s.FuseTripped = false
	s.FuseReason = ""
	s.CooldownUntil = time.Time{}
	s.CurrentCooldownCategory = ""
	s.CurrentCooldownSeverity = ""
}

func (s *EnginePlatformState) ApplyCooldown(category, severity string, cooldown time.Duration) {
	if cooldown > 0 {
		s.CooldownUntil = time.Now().UTC().Add(cooldown)
	}
	s.CurrentCooldownCategory = strings.TrimSpace(category)
	s.CurrentCooldownSeverity = strings.TrimSpace(severity)
}

func (s *EnginePlatformState) RecordException(category, severity, detail string) {
	s.LastExceptionCategory = strings.TrimSpace(category)
	s.LastExceptionSeverity = strings.TrimSpace(severity)
	s.LastExceptionDetail = detail
}

func (s *EnginePlatformState) InCooldown(now time.Time) bool {
	return !s.CooldownUntil.IsZero() && s.CooldownUntil.After(now)
}
