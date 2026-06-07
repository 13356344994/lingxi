package app

import "testing"

// TestOrchestratorRegisterAndRoute 校验编排器骨架(P3a):注册、按名取、首个为 primary、All/Names 稳定。
func TestOrchestratorRegisterAndRoute(t *testing.T) {
	o := NewOrchestrator()
	if o.Primary() != nil {
		t.Fatalf("空编排器 Primary 应为 nil")
	}

	b := &venueRuntime{name: "binance"}
	o.Register(b)
	if got := o.Primary(); got != b {
		t.Fatalf("首个注册者应为 primary")
	}
	if got, ok := o.Get("binance"); !ok || got != b {
		t.Fatalf("按名取应命中 binance")
	}
	if _, ok := o.Get("okx"); ok {
		t.Fatalf("未注册 okx 不应命中")
	}

	// 追加第二个 venue:primary 不变,All 按注册序、Names 排序。
	k := &venueRuntime{name: "okx"}
	o.Register(k)
	if o.Primary() != b {
		t.Fatalf("追加注册不应改变 primary")
	}
	all := o.All()
	if len(all) != 2 || all[0] != b || all[1] != k {
		t.Fatalf("All 应按注册序返回 [binance, okx]")
	}
	names := o.Names()
	if len(names) != 2 || names[0] != "binance" || names[1] != "okx" {
		t.Fatalf("Names 应排序返回 [binance, okx], got %v", names)
	}

	// 重复注册同名:覆盖,不重复进 order。
	b2 := &venueRuntime{name: "binance"}
	o.Register(b2)
	if got, _ := o.Get("binance"); got != b2 {
		t.Fatalf("同名注册应覆盖")
	}
	if len(o.All()) != 2 {
		t.Fatalf("同名注册不应新增 order 条目")
	}

	// nil / 空名防御。
	o.Register(nil)
	o.Register(&venueRuntime{name: ""})
	if len(o.All()) != 2 {
		t.Fatalf("nil/空名注册应被忽略")
	}
}

// TestRecordDirForVenue 锁定 P3b 隔离的路径规则:币安沿用 records/ 后向兼容,其它所隔离到 records/<venue>/,
// 空 RecordDir 退化到默认 "records"。
func TestRecordDirForVenue(t *testing.T) {
	cases := []struct {
		name      string
		recordDir string
		venue     string
		want      string
	}{
		{"binance keeps base for back-compat", "records", "binance", "records"},
		{"binance keeps custom base", "/data/lingxi", "binance", "/data/lingxi"},
		{"okx isolated under base", "records", "okx", "records/okx"},
		{"gate isolated under custom base", "/data/lingxi", "gate", "/data/lingxi/gate"},
		{"empty base defaults to records (binance)", "", "binance", "records"},
		{"empty base defaults to records (bybit)", "  ", "bybit", "records/bybit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg EngineConfig
			cfg.NetworkConfig.RecordDir = c.recordDir
			if got := recordDirForVenue(cfg, c.venue); got != c.want {
				t.Fatalf("recordDirForVenue(%q, %q) = %q, want %q", c.recordDir, c.venue, got, c.want)
			}
		})
	}
}
