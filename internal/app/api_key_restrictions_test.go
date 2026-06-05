package app

import (
	"strings"
	"testing"
)

// TestEvaluateAPIKeyRestrictions 钉死权限自检:提现权限→致命拒绝;未开 IP 白名单/读取→非致命告警。
func TestEvaluateAPIKeyRestrictions(t *testing.T) {
	// 带提现权限 → 致命拒绝。
	if _, err := evaluateAPIKeyRestrictions(apiKeyRestrictions{EnableWithdrawals: true, IPRestrict: true, EnableReading: true}); err == nil {
		t.Fatal("带提现权限必须拒绝启动")
	}

	// 安全配置(无提现 + IP 白名单 + 读取)→ 无错误、无告警。
	warnings, err := evaluateAPIKeyRestrictions(apiKeyRestrictions{EnableWithdrawals: false, IPRestrict: true, EnableReading: true})
	if err != nil {
		t.Fatalf("安全配置不应报错: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("安全配置不应有告警, got %v", warnings)
	}

	// 无提现但未开 IP 白名单 → 放行 + 告警。
	warnings, err = evaluateAPIKeyRestrictions(apiKeyRestrictions{EnableWithdrawals: false, IPRestrict: false, EnableReading: true})
	if err != nil {
		t.Fatalf("未开 IP 白名单不应阻塞启动: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "IP 白名单") {
		t.Fatalf("应有 IP 白名单告警, got %v", warnings)
	}

	// 未开读取权限 → 告警。
	warnings, _ = evaluateAPIKeyRestrictions(apiKeyRestrictions{EnableWithdrawals: false, IPRestrict: true, EnableReading: false})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "读取权限") {
		t.Fatalf("应有读取权限告警, got %v", warnings)
	}
}
