package core

import (
	"testing"
	"time"
)

func TestBindConfigAssignsAllScalarKinds(t *testing.T) {
	cfg := DefaultEngineConfig()
	values := map[string]string{
		"network.http_timeout":           "12s",  // duration
		"network.max_idle_conns":         "77",   // int
		"execution.recv_window":          "9000", // int64
		"risk.min_margin_level":          "1.5",  // float
		"execution.use_protected_ioc":    "true", // bool
		"network.dashboard_addr":         ":9999",
		"active_profile":                 "research", // top-level string
		"strategy.per_symbol_quote_usdt": "42.5",
	}
	if err := BindConfig(&cfg, values, false); err != nil {
		t.Fatalf("BindConfig error: %v", err)
	}
	if cfg.NetworkConfig.HTTPTimeout != 12*time.Second {
		t.Fatalf("duration not bound, got %v", cfg.NetworkConfig.HTTPTimeout)
	}
	if cfg.NetworkConfig.MaxIdleConns != 77 {
		t.Fatalf("int not bound, got %d", cfg.NetworkConfig.MaxIdleConns)
	}
	if cfg.ExecutionConfig.RecvWindow != 9000 {
		t.Fatalf("int64 not bound, got %d", cfg.ExecutionConfig.RecvWindow)
	}
	if cfg.RiskConfig.MinMarginLevel != 1.5 {
		t.Fatalf("float not bound, got %v", cfg.RiskConfig.MinMarginLevel)
	}
	if !cfg.ExecutionConfig.UseProtectedIOC {
		t.Fatal("bool not bound")
	}
	if cfg.NetworkConfig.DashboardAddr != ":9999" {
		t.Fatalf("string not bound, got %q", cfg.NetworkConfig.DashboardAddr)
	}
	if cfg.ActiveProfile != "research" {
		t.Fatalf("top-level not bound, got %q", cfg.ActiveProfile)
	}
	if cfg.StrategyConfig.PerSymbolQuoteUSDT != 42.5 {
		t.Fatalf("strategy float not bound, got %v", cfg.StrategyConfig.PerSymbolQuoteUSDT)
	}
}

func TestBindConfigUnknownKeyErrorsUnlessAllowed(t *testing.T) {
	cfg := DefaultEngineConfig()
	if err := BindConfig(&cfg, map[string]string{"risk.does_not_exist": "1"}, false); err == nil {
		t.Fatal("unknown key should error when allowUnknown=false")
	}
	cfg2 := DefaultEngineConfig()
	if err := BindConfig(&cfg2, map[string]string{"risk.does_not_exist": "1"}, true); err != nil {
		t.Fatalf("unknown key should be ignored when allowUnknown=true, got %v", err)
	}
}

func TestBindConfigBareAliasForUniqueLeaf(t *testing.T) {
	cfg := DefaultEngineConfig()
	// http_timeout 是唯一 leaf,裸别名应可用。
	if err := BindConfig(&cfg, map[string]string{"http_timeout": "8s"}, false); err != nil {
		t.Fatalf("bare alias should bind, got %v", err)
	}
	if cfg.NetworkConfig.HTTPTimeout != 8*time.Second {
		t.Fatalf("bare alias not bound, got %v", cfg.NetworkConfig.HTTPTimeout)
	}
}

func TestBindConfigAmbiguousBareLeafRejected(t *testing.T) {
	cfg := DefaultEngineConfig()
	// max_funding_rate_jump 同时存在于 strategy 与 historical,裸别名有歧义 → 不注册,应报未知。
	if err := BindConfig(&cfg, map[string]string{"max_funding_rate_jump": "0.5"}, false); err == nil {
		t.Fatal("ambiguous bare leaf should not resolve (must use full section.leaf)")
	}
	// 完整键则可用。
	if err := BindConfig(&cfg, map[string]string{"strategy.max_funding_rate_jump": "0.5"}, false); err != nil {
		t.Fatalf("full key should bind, got %v", err)
	}
	if cfg.StrategyConfig.MaxFundingRateJump != 0.5 {
		t.Fatalf("full key not bound, got %v", cfg.StrategyConfig.MaxFundingRateJump)
	}
}

func TestBindConfigBadValueErrors(t *testing.T) {
	cfg := DefaultEngineConfig()
	if err := BindConfig(&cfg, map[string]string{"network.http_timeout": "not-a-duration"}, false); err == nil {
		t.Fatal("invalid duration should error")
	}
	if err := BindConfig(&cfg, map[string]string{"risk.min_margin_level": "abc"}, false); err == nil {
		t.Fatal("invalid float should error")
	}
}

func TestBindConfigSkipsEmptyValue(t *testing.T) {
	cfg := DefaultEngineConfig()
	before := cfg.NetworkConfig.DashboardAddr
	if err := BindConfig(&cfg, map[string]string{"network.dashboard_addr": "   "}, false); err != nil {
		t.Fatalf("empty value should be skipped, got %v", err)
	}
	if cfg.NetworkConfig.DashboardAddr != before {
		t.Fatal("empty value must not overwrite field")
	}
}

func TestConfigKeysCoverEveryField(t *testing.T) {
	keys := ConfigKeys()
	// 粗校验:字段数量级远超旧 switch 覆盖,确认全字段可配。
	if len(keys) < 100 {
		t.Fatalf("expected full-field coverage (>=100 keys), got %d", len(keys))
	}
	kinds := ConfigKeyKinds()
	for _, k := range keys {
		if kinds[k] == "" || kinds[k] == "unknown" {
			t.Fatalf("key %s has unsupported kind %q", k, kinds[k])
		}
	}
}
