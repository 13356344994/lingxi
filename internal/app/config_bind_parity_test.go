package app

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"lingxi/internal/core"
)

// sampleConfigValue 按字段类型给出一个合法取值,用于奇偶测试。
func sampleConfigValue(kind string) string {
	switch kind {
	case "duration":
		return "7s"
	case "bool":
		return "true"
	case "int":
		return "5"
	case "float":
		return "0.123"
	default:
		return "testval"
	}
}

// legacySupportedConfigKeys 自动探测旧 applyEngineConfigOverrides 实际支持的完整键集合:
// 给每个候选键喂一个类型合法的值,旧 loader 不报「未知配置字段」即视为支持。
func legacySupportedConfigKeys(t *testing.T) map[string]string {
	t.Helper()
	kinds := core.ConfigKeyKinds()
	supported := make(map[string]string)
	for key, kind := range kinds {
		val := sampleConfigValue(kind)
		cfg := DefaultEngineConfig()
		err := applyEngineConfigOverrides(&cfg, map[string]string{key: val})
		if err != nil {
			if strings.Contains(err.Error(), "未知配置字段") {
				continue // 旧 loader 不支持该键(白名单未覆盖)
			}
			t.Fatalf("旧 loader 对 %s=%s 报意外错误: %v", key, val, err)
		}
		supported[key] = val
	}
	return supported
}

// TestBindConfigParityPerKey 钉死 M-S1.3a 零行为变更:对旧 loader 支持的每个键,
// core.BindConfig 必须产出与旧 applyEngineConfigOverrides 逐字段相等的 EngineConfig。
func TestBindConfigParityPerKey(t *testing.T) {
	supported := legacySupportedConfigKeys(t)
	if len(supported) < 50 {
		t.Fatalf("预期旧 loader 支持大量键,仅匹配到 %d 个(探测逻辑可能有误)", len(supported))
	}
	for key, val := range supported {
		oldCfg := DefaultEngineConfig()
		if err := applyEngineConfigOverrides(&oldCfg, map[string]string{key: val}); err != nil {
			t.Fatalf("旧 loader %s=%s 报错: %v", key, val, err)
		}
		newCfg := DefaultEngineConfig()
		if err := core.BindConfig(&newCfg, map[string]string{key: val}, false); err != nil {
			t.Fatalf("BindConfig %s=%s 报错: %v", key, val, err)
		}
		if !reflect.DeepEqual(oldCfg, newCfg) {
			t.Fatalf("键 %s=%s 绑定结果与旧 loader 不一致", key, val)
		}
	}
}

// TestBindConfigParityAllKeysAtOnce 把全部受支持键一次性喂给两个 loader,断言整体结果一致
// (覆盖跨字段交互,虽预期无)。
func TestBindConfigParityAllKeysAtOnce(t *testing.T) {
	supported := legacySupportedConfigKeys(t)
	oldCfg := DefaultEngineConfig()
	if err := applyEngineConfigOverrides(&oldCfg, supported); err != nil {
		t.Fatalf("旧 loader 全键报错: %v", err)
	}
	newCfg := DefaultEngineConfig()
	if err := core.BindConfig(&newCfg, supported, false); err != nil {
		t.Fatalf("BindConfig 全键报错: %v", err)
	}
	if !reflect.DeepEqual(oldCfg, newCfg) {
		t.Fatal("全键一次性绑定结果与旧 loader 不一致")
	}
}

// TestBindConfigParityBareAliases 对旧 loader 支持裸别名的键,校验 BindConfig 裸别名行为一致。
func TestBindConfigParityBareAliases(t *testing.T) {
	supported := legacySupportedConfigKeys(t)
	fullKeys := make([]string, 0, len(supported))
	for k := range supported {
		fullKeys = append(fullKeys, k)
	}
	sort.Strings(fullKeys)
	checked := 0
	for _, fullKey := range fullKeys {
		val := supported[fullKey]
		leaf := fullKey
		if idx := strings.LastIndex(fullKey, "."); idx >= 0 {
			leaf = fullKey[idx+1:]
		}
		oldCfg := DefaultEngineConfig()
		if err := applyEngineConfigOverrides(&oldCfg, map[string]string{leaf: val}); err != nil {
			continue // 旧 loader 不支持该裸别名
		}
		newCfg := DefaultEngineConfig()
		if err := core.BindConfig(&newCfg, map[string]string{leaf: val}, false); err != nil {
			t.Fatalf("旧 loader 支持裸别名 %s 但 BindConfig 拒绝: %v", leaf, err)
		}
		if !reflect.DeepEqual(oldCfg, newCfg) {
			t.Fatalf("裸别名 %s=%s 绑定结果与旧 loader 不一致", leaf, val)
		}
		checked++
	}
	if checked < 50 {
		t.Fatalf("预期校验大量裸别名,仅校验 %d 个", checked)
	}
}
