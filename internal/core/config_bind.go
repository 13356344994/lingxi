package core

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// config_bind.go 是 M-S1.3 的核心:用「结构体 cfg tag + 反射」把扁平 key→value 映射绑定到 EngineConfig,
// 取代手写的 ~79 个 case 巨型 switch 与 env 白名单。字段集与可配键完全来自结构体 tag(单一事实源),
// 「写进结构体即可配」,从根上消灭白名单漂移(R17/R24)。
//
// 键约定:嵌入子配置(NetworkConfig 等)用其 cfg tag 作 section 前缀,字段 cfg tag 作 leaf,
// 完整键形如 "risk.min_margin_level"。为向后兼容,非跨 section 冲突的 leaf 额外支持「裸别名」
// (如 "http_timeout"),冲突的 leaf 必须写完整键。
//
// 类型语义与旧 parser 对齐:time.Duration 走 time.ParseDuration(保住 "9s" 磁盘格式),
// int/float/bool 分别 Atoi/ParseFloat/ParseBool,字符串原样(已 Trim)。

var durationType = reflect.TypeOf(time.Duration(0))

type configFieldRef struct {
	fullKey string
	value   reflect.Value
}

// buildConfigIndex 反射遍历 *EngineConfig,产出「完整键→可设字段」索引,以及无歧义的裸别名索引。
func buildConfigIndex(cfg *EngineConfig) (full map[string]reflect.Value, bareUnique map[string]reflect.Value) {
	full = make(map[string]reflect.Value, 160)
	bareCount := make(map[string]int, 160)
	bareValue := make(map[string]reflect.Value, 160)

	register := func(key string, fv reflect.Value) {
		full[key] = fv
		leaf := key
		if idx := strings.LastIndex(key, "."); idx >= 0 {
			leaf = key[idx+1:]
		}
		bareCount[leaf]++
		bareValue[leaf] = fv
	}

	root := reflect.ValueOf(cfg).Elem()
	rootType := root.Type()
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		tag := strings.TrimSpace(field.Tag.Get("cfg"))
		if tag == "" || tag == "-" {
			continue
		}
		fv := root.Field(i)
		if fv.Kind() == reflect.Struct {
			section := tag
			sub := fv.Type()
			for j := 0; j < sub.NumField(); j++ {
				sf := sub.Field(j)
				leaf := strings.TrimSpace(sf.Tag.Get("cfg"))
				if leaf == "" || leaf == "-" {
					continue
				}
				register(section+"."+leaf, fv.Field(j))
			}
			continue
		}
		register(tag, fv)
	}

	bareUnique = make(map[string]reflect.Value, len(bareCount))
	for leaf, count := range bareCount {
		if count == 1 {
			bareUnique[leaf] = bareValue[leaf]
		}
	}
	return full, bareUnique
}

// BindConfig 把扁平 key→value(可来自 YAML 拍平或 env 派生)绑定到 cfg。allowUnknown 为 true 时
// 忽略未知键(兼容模式),否则未知键报错。键大小写不敏感;空键/空值跳过。
func BindConfig(cfg *EngineConfig, values map[string]string, allowUnknown bool) error {
	if cfg == nil {
		return fmt.Errorf("BindConfig: 目标配置为空")
	}
	full, bare := buildConfigIndex(cfg)

	rawKeys := make([]string, 0, len(values))
	for k := range values {
		rawKeys = append(rawKeys, k)
	}
	sort.Strings(rawKeys) // 确定性:未知键报错与重复键覆盖顺序稳定

	for _, rawKey := range rawKeys {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		value := strings.TrimSpace(values[rawKey])
		if key == "" || value == "" {
			continue
		}
		fv, ok := full[key]
		if !ok {
			fv, ok = bare[key]
		}
		if !ok {
			if allowUnknown {
				continue
			}
			return fmt.Errorf("未知配置字段: %s", rawKey)
		}
		if err := assignConfigValue(fv, value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// ConfigKeys 返回所有可配置的完整键(已排序),供 env 派生、样例导出与文档使用。
func ConfigKeys() []string {
	var cfg EngineConfig
	full, _ := buildConfigIndex(&cfg)
	keys := make([]string, 0, len(full))
	for k := range full {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ConfigKeyKinds 返回每个完整键的值类型("duration"/"int"/"float"/"bool"/"string"),
// 供 env 派生、样例导出、文档与奇偶测试按类型生成/校验取值。
func ConfigKeyKinds() map[string]string {
	var cfg EngineConfig
	full, _ := buildConfigIndex(&cfg)
	out := make(map[string]string, len(full))
	for key, fv := range full {
		out[key] = configValueKind(fv)
	}
	return out
}

// SampleConfigYAML 由 EngineConfig 的 cfg tag 与默认值反射生成一份自文档化的样例配置(嵌套 YAML),
// 列出**每一个**可配置项及其代码默认值。供 `lingxi config-sample` 导出,作为「有哪些旋钮 + 默认是多少」
// 的单一事实参考。生成物为合法 YAML,且经 flatten+BindConfig 回灌零值配置可精确复现默认值(见 app 层往返测试)。
func SampleConfigYAML() string {
	var b strings.Builder
	b.WriteString("# lingxi 配置样例(由 `lingxi config-sample` 自动生成)\n")
	b.WriteString("# - 每一项均为代码默认值;只需保留你要覆盖的项即可。\n")
	b.WriteString("# - 等价环境变量:LINGXI_<SECTION>_<LEAF>(大写,点号→下划线),如 LINGXI_NETWORK_HTTP_TIMEOUT。\n")
	b.WriteString("# - 未知字段默认报错阻断启动;设 LINGXI_ALLOW_UNKNOWN_CONFIG=true 可改为告警并忽略。\n")

	def := DefaultEngineConfig()
	root := reflect.ValueOf(def)
	rootType := root.Type()

	// 先输出顶层标量项(无 section 前缀)。
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		tag := strings.TrimSpace(field.Tag.Get("cfg"))
		if tag == "" || tag == "-" {
			continue
		}
		fv := root.Field(i)
		if fv.Kind() == reflect.Struct {
			continue
		}
		b.WriteString(fmt.Sprintf("%s: %s\n", tag, yamlScalar(fv)))
	}

	// 再按 section 分组输出嵌套项,保持结构体声明顺序以便阅读。
	for i := 0; i < rootType.NumField(); i++ {
		field := rootType.Field(i)
		tag := strings.TrimSpace(field.Tag.Get("cfg"))
		if tag == "" || tag == "-" {
			continue
		}
		fv := root.Field(i)
		if fv.Kind() != reflect.Struct {
			continue
		}
		b.WriteString("\n" + tag + ":\n")
		sub := fv.Type()
		for j := 0; j < sub.NumField(); j++ {
			leaf := strings.TrimSpace(sub.Field(j).Tag.Get("cfg"))
			if leaf == "" || leaf == "-" {
				continue
			}
			b.WriteString(fmt.Sprintf("  %s: %s\n", leaf, yamlScalar(fv.Field(j))))
		}
	}
	return b.String()
}

func yamlScalar(fv reflect.Value) string {
	if fv.Type() == durationType {
		return time.Duration(fv.Int()).String()
	}
	switch fv.Kind() {
	case reflect.String:
		s := fv.String()
		if s == "" || strings.ContainsAny(s, ":#{}[],&*!|>'\"%@` ") {
			return strconv.Quote(s)
		}
		return s
	case reflect.Bool:
		if fv.Bool() {
			return "true"
		}
		return "false"
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.String {
			n := fv.Len()
			if n == 0 {
				return "[]"
			}
			items := make([]string, n)
			for i := 0; i < n; i++ {
				items[i] = strconv.Quote(fv.Index(i).String())
			}
			return "[" + strings.Join(items, ", ") + "]"
		}
		return fmt.Sprintf("%v", fv.Interface())
	default:
		return fmt.Sprintf("%v", fv.Interface())
	}
}

// IsConfigKey 报告某键(完整键或无歧义裸别名,大小写不敏感)是否为已知可配置项。
// 供上层在兼容模式下识别未知键以告警。
func IsConfigKey(key string) bool {
	var cfg EngineConfig
	full, bare := buildConfigIndex(&cfg)
	k := strings.ToLower(strings.TrimSpace(key))
	if _, ok := full[k]; ok {
		return true
	}
	_, ok := bare[k]
	return ok
}

func configValueKind(fv reflect.Value) string {
	if fv.Type() == durationType {
		return "duration"
	}
	switch fv.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.String {
			return "string_list"
		}
		return "unknown"
	default:
		return "unknown"
	}
}

func assignConfigValue(fv reflect.Value, raw string) error {
	if !fv.CanSet() {
		return fmt.Errorf("字段不可写")
	}
	if fv.Type() == durationType {
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("非法 duration 配置: %s", raw)
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("非法 bool 配置: %s", raw)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("非法 int 配置: %s", raw)
		}
		fv.SetInt(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return fmt.Errorf("非法 float 配置: %s", raw)
		}
		fv.SetFloat(f)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("不支持的配置字段类型: %s", fv.Type())
		}
		// 逗号分隔的字符串列表(去空白、丢空项);整串为空则置空切片。
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		fv.Set(reflect.ValueOf(out))
	default:
		return fmt.Errorf("不支持的配置字段类型: %s", fv.Kind())
	}
	return nil
}
