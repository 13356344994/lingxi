package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lingxi/internal/core"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}
	return path
}

func TestLoadEngineConfigNestedYAML(t *testing.T) {
	path := writeTempConfig(t, `
network:
  http_timeout: 15s
  dashboard_addr: "127.0.0.1:9091"
risk:
  min_margin_level: 1.5
  hold_hedge_guard_enabled: true
execution:
  futures_leverage: 3
`)
	t.Setenv("LINGXI_CONFIG", path)
	cfg, err := loadEngineConfig()
	if err != nil {
		t.Fatalf("loadEngineConfig() error = %v", err)
	}
	if cfg.NetworkConfig.HTTPTimeout != 15*time.Second {
		t.Fatalf("nested http_timeout 未生效: %v", cfg.NetworkConfig.HTTPTimeout)
	}
	if cfg.NetworkConfig.DashboardAddr != "127.0.0.1:9091" {
		t.Fatalf("nested dashboard_addr 未生效: %q", cfg.NetworkConfig.DashboardAddr)
	}
	if cfg.RiskConfig.MinMarginLevel != 1.5 {
		t.Fatalf("nested min_margin_level 未生效: %v", cfg.RiskConfig.MinMarginLevel)
	}
	if !cfg.RiskConfig.HoldHedgeGuardEnabled {
		t.Fatal("nested hold_hedge_guard_enabled 未生效")
	}
	if cfg.ExecutionConfig.FuturesLeverage != 3 {
		t.Fatalf("nested futures_leverage 未生效: %d", cfg.ExecutionConfig.FuturesLeverage)
	}
}

func TestLoadEngineConfigFlatYAMLBackCompat(t *testing.T) {
	// 旧扁平格式:完整点号键 + 裸别名键都应继续可用。
	path := writeTempConfig(t, `
network.http_timeout: 13s
dashboard_addr: ":9099"
risk.min_margin_level: 2.0
`)
	t.Setenv("LINGXI_CONFIG", path)
	cfg, err := loadEngineConfig()
	if err != nil {
		t.Fatalf("loadEngineConfig() error = %v", err)
	}
	if cfg.NetworkConfig.HTTPTimeout != 13*time.Second {
		t.Fatalf("flat full key 未生效: %v", cfg.NetworkConfig.HTTPTimeout)
	}
	if cfg.NetworkConfig.DashboardAddr != ":9099" {
		t.Fatalf("flat bare alias 未生效: %q", cfg.NetworkConfig.DashboardAddr)
	}
	if cfg.RiskConfig.MinMarginLevel != 2.0 {
		t.Fatalf("flat risk.min_margin_level 未生效: %v", cfg.RiskConfig.MinMarginLevel)
	}
}

func TestLoadEngineConfigEnvOverride(t *testing.T) {
	path := writeTempConfig(t, "risk:\n  min_margin_level: 1.2\n")
	t.Setenv("LINGXI_CONFIG", path)
	t.Setenv("LINGXI_RISK_MIN_MARGIN_LEVEL", "1.8") // env 应覆盖文件
	t.Setenv("LINGXI_NETWORK_HTTP_TIMEOUT", "11s")
	cfg, err := loadEngineConfig()
	if err != nil {
		t.Fatalf("loadEngineConfig() error = %v", err)
	}
	if cfg.RiskConfig.MinMarginLevel != 1.8 {
		t.Fatalf("env 未覆盖文件: %v", cfg.RiskConfig.MinMarginLevel)
	}
	if cfg.NetworkConfig.HTTPTimeout != 11*time.Second {
		t.Fatalf("env http_timeout 未生效: %v", cfg.NetworkConfig.HTTPTimeout)
	}
}

func TestLoadEngineConfigUnknownKeyErrorsByDefault(t *testing.T) {
	path := writeTempConfig(t, "totally_bogus_field: 1\n")
	t.Setenv("LINGXI_CONFIG", path)
	if _, err := loadEngineConfig(); err == nil {
		t.Fatal("未知配置字段应在严格模式下报错阻断启动")
	}
}

func TestLoadEngineConfigAllowUnknownIgnores(t *testing.T) {
	path := writeTempConfig(t, "totally_bogus_field: 1\nrisk:\n  min_margin_level: 1.4\n")
	t.Setenv("LINGXI_CONFIG", path)
	t.Setenv(allowUnknownConfigEnv, "true")
	cfg, err := loadEngineConfig()
	if err != nil {
		t.Fatalf("兼容模式下未知键应被忽略而非报错: %v", err)
	}
	if cfg.RiskConfig.MinMarginLevel != 1.4 {
		t.Fatalf("兼容模式下已知键仍应生效: %v", cfg.RiskConfig.MinMarginLevel)
	}
}

func TestFlattenYAMLConfigSequenceJoinsCSV(t *testing.T) {
	values, err := flattenYAMLConfig([]byte("strategy:\n  preferred_spot_quote_assets:\n    - USDC\n    - FDUSD\n"))
	if err != nil {
		t.Fatalf("flattenYAMLConfig error = %v", err)
	}
	if got := values["strategy.preferred_spot_quote_assets"]; got != "USDC,FDUSD" {
		t.Fatalf("序列未拼成 CSV: %q", got)
	}
}

func TestFlattenYAMLConfigRejectsNonMappingRoot(t *testing.T) {
	if _, err := flattenYAMLConfig([]byte("- a\n- b\n")); err == nil {
		t.Fatal("根为序列应报错")
	}
}

// TestSampleConfigYAMLRoundTrips 钉死自文档化样例的正确性:core.SampleConfigYAML() 生成物经
// flatten + BindConfig 回灌到零值配置,必须精确复现 DefaultEngineConfig()(既证明它是合法 YAML,
// 又证明它逐字段忠实于默认值,且覆盖全部可配置项)。
func TestSampleConfigYAMLRoundTrips(t *testing.T) {
	sample := core.SampleConfigYAML()
	values, err := flattenYAMLConfig([]byte(sample))
	if err != nil {
		t.Fatalf("样例配置不是合法 YAML: %v", err)
	}
	if len(values) != len(core.ConfigKeys()) {
		t.Fatalf("样例应覆盖全部 %d 个键,实得 %d", len(core.ConfigKeys()), len(values))
	}
	var cfg EngineConfig // 零值起步
	if err := core.BindConfig(&cfg, values, false); err != nil {
		t.Fatalf("回灌样例失败(可能含未知键或类型错误): %v", err)
	}
	if !reflect.DeepEqual(cfg, DefaultEngineConfig()) {
		t.Fatal("样例回灌结果与 DefaultEngineConfig 不一致")
	}
}
