package app

import (
	"fmt"
	"log"
	"os"
	"strings"

	"lingxi/internal/core"
)

const (
	defaultEngineConfigPath = "config.yaml"
	allowUnknownConfigEnv   = "LINGXI_ALLOW_UNKNOWN_CONFIG"
)

// loadEngineConfig 加载运行配置:默认值 → YAML 文件覆盖 → 环境变量覆盖 → 校验。
//
// 文件经 yaml.v3 解析并拍平成「section.leaf → 原始字符串」,环境变量由结构体 tag 自动派生
// (LINGXI_SECTION_LEAF),统一交给 core.BindConfig 做类型感知绑定(M-S1.3)。可配键集与类型完全来自
// EngineConfig 的 cfg tag(单一事实源),不再有手工白名单。
func loadEngineConfig() (EngineConfig, error) {
	cfg := DefaultEngineConfig()
	allowUnknown := strings.EqualFold(strings.TrimSpace(os.Getenv(allowUnknownConfigEnv)), "true")

	configPath := strings.TrimSpace(os.Getenv("LINGXI_CONFIG"))
	if configPath == "" {
		configPath = defaultEngineConfigPath
	}
	if _, err := os.Stat(configPath); err == nil {
		data, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return EngineConfig{}, fmt.Errorf("读取配置文件失败: %w", readErr)
		}
		values, flErr := flattenYAMLConfig(data)
		if flErr != nil {
			return EngineConfig{}, fmt.Errorf("加载 %s 失败: %w", configPath, flErr)
		}
		if allowUnknown {
			logUnknownConfigKeys(values)
		}
		if err := core.BindConfig(&cfg, values, allowUnknown); err != nil {
			return EngineConfig{}, fmt.Errorf("加载 %s 失败: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return EngineConfig{}, fmt.Errorf("检查配置文件失败: %w", err)
	}

	// env 键由 tag 派生,恒为合法键,无需 allowUnknown。
	if err := core.BindConfig(&cfg, envConfigOverrides(), false); err != nil {
		return EngineConfig{}, fmt.Errorf("加载环境变量配置失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return EngineConfig{}, err
	}
	mode := core.ParseStrategyMode(cfg.StrategyConfig.StrategyMode)
	core.ApplyStrategyModeToEconomics(&cfg, mode)
	logEffectiveConfigDiff(cfg)
	return cfg, nil
}

// envConfigOverrides 由结构体 tag 自动派生所有可配置键的环境变量覆盖(LINGXI_SECTION_LEAF),
// 取代旧的手工 env 白名单——新增字段写进结构体即自动支持 env(R17/R24)。
func envConfigOverrides() map[string]string {
	pairs := map[string]string{}
	for _, key := range core.ConfigKeys() {
		envKey := "LINGXI_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			pairs[key] = value
		}
	}
	return pairs
}

// logUnknownConfigKeys 在兼容模式(LINGXI_ALLOW_UNKNOWN_CONFIG=true)下,把文件里不被识别的键以告警列出,
// 保留旧 loader 的可观测行为(严格模式下未知键由 BindConfig 直接报错阻断,无需在此告警)。
func logUnknownConfigKeys(values map[string]string) {
	for rawKey := range values {
		if strings.TrimSpace(rawKey) == "" {
			continue
		}
		if !core.IsConfigKey(rawKey) {
			log.Printf("[警告] 使用了兼容模式，忽略了未知配置字段: %s", rawKey)
		}
	}
}

// logEffectiveConfigDiff 在启动时打印生效配置相对默认值的字段级差异（M-S1.0，纯观测）。
// 让「实际跑的参数 vs 代码默认」一眼可核对，便于审计 YAML/env 覆盖是否如预期；不参与控制流。
func logEffectiveConfigDiff(cfg EngineConfig) {
	diff := cfg.DiffFromDefaults()
	if len(diff) == 0 {
		log.Printf("[配置] 生效配置与默认值完全一致（无 YAML/env 覆盖）")
		return
	}
	log.Printf("[配置] 生效配置相对默认值有 %d 处覆盖:", len(diff))
	for _, entry := range diff {
		log.Printf("[配置]   %s: %s => %s", entry.Path, entry.Default, entry.Current)
	}
}
