package app

import (
	"fmt"
	"testing"
)

// TestRegisterEnabledVenuesStartupIsolation 钉死启动隔离(§7.9):一所构建失败/panic 不阻塞其它所注册;
// 成功所照常进编排器,Primary 为首个成功注册者。
func TestRegisterEnabledVenuesStartupIsolation(t *testing.T) {
	plans := []VenuePlan{
		{Name: "binance", Enabled: true},
		{Name: "okx", Enabled: true},   // 模拟 key 失败
		{Name: "bybit", Enabled: true}, // 模拟构建 panic
		{Name: "gate", Enabled: true},
	}
	orch := NewOrchestrator()
	build := func(p VenuePlan) (*venueRuntime, error) {
		switch p.Name {
		case "okx":
			return nil, fmt.Errorf("API key 无效")
		case "bybit":
			panic("模拟构建 panic")
		default:
			return &venueRuntime{name: p.Name}, nil
		}
	}
	registered := registerEnabledVenues(orch, plans, build, nil)
	if registered != 2 {
		t.Fatalf("应有 2 所成功注册(binance/gate), got %d", registered)
	}
	if _, ok := orch.Get("okx"); ok {
		t.Fatal("失败所 okx 不应被注册")
	}
	if _, ok := orch.Get("bybit"); ok {
		t.Fatal("panic 所 bybit 不应被注册")
	}
	if _, ok := orch.Get("binance"); !ok {
		t.Fatal("binance 应成功注册")
	}
	if _, ok := orch.Get("gate"); !ok {
		t.Fatal("gate 应成功注册(不受前序失败影响)")
	}
	if p := orch.Primary(); p == nil || p.name != "binance" {
		t.Fatalf("Primary 应为首个成功注册者 binance, got %v", p)
	}
}
