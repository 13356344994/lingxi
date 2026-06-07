package app

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"lingxi/internal/core"

	"gopkg.in/yaml.v3"
)

// config_venues.go 实现 MULTI_VENUE_REFACTOR §8 的 venues schema(path B):`venues:` 子树独立于全局 flatten
// 解析,每所 = 全局 cfg 的副本 + 该所覆盖段(strategy/risk/execution/portfolio/active_profile/allocated_capital_usdt)
// 经 BindConfig 合并。`enabled`/`autostart` 是编排元信息(非引擎 cfg 字段),解析时单独抽出。
//
// **后向兼容铁律**:无 `venues:` 段(当前服务器上的 config.yaml 即如此)→ 返回 nil 计划,编排走旧单所路径,
// 币安行为逐字节不变。

const (
	venuesConfigKey       = "venues"
	venueEnabledKey       = "enabled"
	venueAutostartKey     = "autostart"
	venueAllocatedCapital = "allocated_capital_usdt"
)

// VenuePlan 是某 venue 的编排计划:启用/自动启动开关 + 该所合并后的引擎配置(全局 + 该所覆盖)。
type VenuePlan struct {
	Name      string
	Enabled   bool
	Autostart bool
	Config    EngineConfig
}

// loadVenuePlans 从配置文件解析 venues 计划。globalCfg 是已绑定好的全局配置(作为每所覆盖的基线)。
// 无配置文件或无 `venues:` 段 → 返回 nil(单所旧路径)。allowUnknown 透传给 per-venue 覆盖绑定。
func loadVenuePlans(globalCfg EngineConfig, allowUnknown bool) ([]VenuePlan, error) {
	configPath := resolveConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", configPath, err)
	}
	return parseVenuePlans(data, globalCfg, allowUnknown)
}

// resolveConfigPath 复用 loadEngineConfig 的配置路径解析(LINGXI_CONFIG 或默认 config.yaml)。
func resolveConfigPath() string {
	configPath := strings.TrimSpace(os.Getenv("LINGXI_CONFIG"))
	if configPath == "" {
		configPath = defaultEngineConfigPath
	}
	return configPath
}

// parseVenuePlans 是 loadVenuePlans 的纯解析内核(便于单测):从 YAML 字节提取 venues 子树并产出各所计划。
func parseVenuePlans(data []byte, globalCfg EngineConfig, allowUnknown bool) ([]VenuePlan, error) {
	venuesNode, err := extractVenuesNode(data)
	if err != nil {
		return nil, err
	}
	if venuesNode == nil {
		return nil, nil
	}

	var plans []VenuePlan
	seen := make(map[string]struct{})
	for i := 0; i+1 < len(venuesNode.Content); i += 2 {
		nameNode := venuesNode.Content[i]
		bodyNode := venuesNode.Content[i+1]
		name := strings.ToLower(strings.TrimSpace(nameNode.Value))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("venues 配置中 venue %q 重复", name)
		}
		seen[name] = struct{}{}

		if bodyNode.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("venues.%s 必须是映射(key: value)", name)
		}

		overrides := make(map[string]string, 16)
		if err := flattenYAMLMapping(bodyNode, "", overrides); err != nil {
			return nil, fmt.Errorf("解析 venues.%s 失败: %w", name, err)
		}

		plan := VenuePlan{Name: name, Enabled: true} // enabled 默认 true(显式 false 才禁用)
		if raw, ok := overrides[venueEnabledKey]; ok {
			enabled, perr := strconv.ParseBool(strings.TrimSpace(raw))
			if perr != nil {
				return nil, fmt.Errorf("venues.%s.enabled 非法布尔值 %q: %w", name, raw, perr)
			}
			plan.Enabled = enabled
			delete(overrides, venueEnabledKey)
		}
		if raw, ok := overrides[venueAutostartKey]; ok {
			autostart, perr := strconv.ParseBool(strings.TrimSpace(raw))
			if perr != nil {
				return nil, fmt.Errorf("venues.%s.autostart 非法布尔值 %q: %w", name, raw, perr)
			}
			plan.Autostart = autostart
			delete(overrides, venueAutostartKey)
		}

		// 该所配置 = 全局副本 + 该所覆盖(allocated_capital_usdt / strategy.* / risk.* / execution.* /
		// portfolio.* / active_profile 等都通过 BindConfig 逐字段覆盖,复用全局同一套类型感知绑定)。
		venueCfg := globalCfg
		if len(overrides) > 0 {
			if err := core.BindConfig(&venueCfg, overrides, allowUnknown); err != nil {
				return nil, fmt.Errorf("绑定 venues.%s 覆盖失败: %w", name, err)
			}
		}
		if err := venueCfg.Validate(); err != nil {
			return nil, fmt.Errorf("venues.%s 合并后配置非法: %w", name, err)
		}
		plan.Config = venueCfg
		plans = append(plans, plan)
	}
	return plans, nil
}

// extractVenuesNode 返回根映射下 `venues:` 的值节点(映射),无则返回 nil。
func extractVenuesNode(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("配置文件根节点必须是映射(key: value)")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if strings.TrimSpace(root.Content[i].Value) != venuesConfigKey {
			continue
		}
		node := root.Content[i+1]
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("venues 段必须是映射(venue 名 → 配置)")
		}
		return node, nil
	}
	return nil, nil
}

// enabledVenuePlans 过滤出 enabled 计划,按 venue 名排序(确定性),供编排注册循环消费。
func enabledVenuePlans(plans []VenuePlan) []VenuePlan {
	var out []VenuePlan
	for _, p := range plans {
		if p.Enabled {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
