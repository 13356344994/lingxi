package app

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// config_yaml.go 用 yaml.v3 把配置文件解析成「section.leaf → 原始字符串」的扁平映射,喂给 core.BindConfig
// 做类型感知绑定(M-S1.3)。支持真嵌套 YAML(network:\n  http_timeout: 9s)、注释、引号,同时向后兼容
// 旧扁平 key: value 文件(顶层标量即视作完整键)。
//
// 关键:叶子一律取 yaml 节点的**原始字符串** node.Value(而非 yaml 的类型推断结果),与旧 readSimpleYAML
// 的「一切皆字符串」语义一致,由 BindConfig 统一按字段类型解析,从根上规避 yaml 数字/布尔推断的边角差异。

// flattenYAMLConfig 把 YAML 文档拍平为扁平键值映射。空文件返回空映射。
func flattenYAMLConfig(data []byte) (map[string]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	out := make(map[string]string, 64)
	if len(doc.Content) == 0 {
		return out, nil // 空文件
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("配置文件根节点必须是映射(key: value)")
	}
	if err := flattenYAMLMapping(root, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func flattenYAMLMapping(node *yaml.Node, prefix string, out map[string]string) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		key := strings.TrimSpace(keyNode.Value)
		if key == "" {
			continue
		}
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch valNode.Kind {
		case yaml.MappingNode:
			if err := flattenYAMLMapping(valNode, fullKey, out); err != nil {
				return err
			}
		case yaml.ScalarNode:
			out[fullKey] = valNode.Value
		case yaml.SequenceNode:
			// 标量序列拼成逗号分隔(兼容以 YAML 数组写 CSV 风格字段,如 preferred_spot_quote_assets)。
			parts := make([]string, 0, len(valNode.Content))
			for _, item := range valNode.Content {
				if item.Kind != yaml.ScalarNode {
					return fmt.Errorf("配置字段 %s 不支持嵌套数组元素", fullKey)
				}
				parts = append(parts, item.Value)
			}
			out[fullKey] = strings.Join(parts, ",")
		default:
			return fmt.Errorf("配置字段 %s 含不支持的节点类型", fullKey)
		}
	}
	return nil
}
