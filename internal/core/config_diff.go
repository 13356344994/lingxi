package core

import (
	"fmt"
	"reflect"
	"sort"
)

// ConfigDiffEntry 描述 effective 配置相对默认值的单字段差异（用于启动期可观测,非控制流）。
type ConfigDiffEntry struct {
	// Path 是字段的分节路径，例如 "RiskConfig.MinMarginLevel"。
	Path string
	// Default 是 DefaultEngineConfig() 的取值（%v 文本，time.Duration 走 Stringer）。
	Default string
	// Current 是生效配置的取值。
	Current string
}

// DiffFromDefaults 返回生效配置相对 DefaultEngineConfig() 的字段级差异，按路径排序。
//
// 它是纯函数、只读：M-S1.0 用它在启动时打印「哪些字段被 YAML/env 覆盖而偏离默认」，
// 让「现实=承诺」可核对，但不参与任何控制流（零行为变更）。
func (cfg EngineConfig) DiffFromDefaults() []ConfigDiffEntry {
	return DiffEngineConfig(DefaultEngineConfig(), cfg)
}

// DiffEngineConfig 比较两份配置，返回 cur 相对 base 的字段级差异（按路径排序）。
func DiffEngineConfig(base, cur EngineConfig) []ConfigDiffEntry {
	entries := make([]ConfigDiffEntry, 0, 16)
	diffStructValues(reflect.ValueOf(base), reflect.ValueOf(cur), "", &entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// diffStructValues 递归比较两个同类型结构体值；嵌入/具名子结构体以其字段名作为分节前缀，
// 标量叶子（含 time.Duration 等定义类型）用 %v 文本比较与呈现。
func diffStructValues(base, cur reflect.Value, prefix string, out *[]ConfigDiffEntry) {
	t := base.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // 跳过未导出字段
			continue
		}
		bv := base.Field(i)
		cv := cur.Field(i)
		path := field.Name
		if prefix != "" {
			path = prefix + "." + field.Name
		}
		if bv.Kind() == reflect.Struct {
			diffStructValues(bv, cv, path, out)
			continue
		}
		if !reflect.DeepEqual(bv.Interface(), cv.Interface()) {
			*out = append(*out, ConfigDiffEntry{
				Path:    path,
				Default: fmt.Sprintf("%v", bv.Interface()),
				Current: fmt.Sprintf("%v", cv.Interface()),
			})
		}
	}
}
