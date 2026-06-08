package app

import (
	"fmt"
	"os"
	"strings"

	"lingxi/internal/venue/binance"

	"gopkg.in/yaml.v3"
)

// secret_venues.go 实现 MULTI_VENUE_REFACTOR §8 的「secret.yaml 每所一节」:在保持现有**扁平**格式
// (api/secret/proxy_url/Telegram_* 顶层键 = 币安,后向兼容)的同时,支持每所一个嵌套节:
//
//	binance: { api: ..., secret: ... }
//	okx:     { api: ..., secret: ..., passphrase: ..., proxy_url: ... }
//	bybit:   { api: ..., secret: ... }
//
// 解析铁律:
//   - 顶层若直接有 api/secret(扁平)→ 视为币安凭证(与改造前 readSimpleYAML 行为等价),不破坏现网 secret.yaml。
//   - 顶层若有 `<venue>:` 嵌套映射 → 该所凭证;字段 api/secret/passphrase/proxy_url。
//   - 二者可并存:扁平兜底币安,嵌套覆盖对应所。某所节缺 api/secret → 该所报错(由调用方按启动隔离处理)。

// venueSecretFields 是某所 secret 节的原始字段集合(小写键)。
type venueSecretFields map[string]string

// parseVenueSecrets 解析 secret.yaml 字节为「venue 名(小写)→ 字段集」。扁平顶层 api/secret 归到 binance。
// 非映射/空文档 → 返回空 map(无凭证),不报错(交由调用方判定缺失)。
func parseVenueSecrets(data []byte) (map[string]venueSecretFields, error) {
	out := make(map[string]venueSecretFields)
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 secret.yaml 失败: %w", err)
	}
	if len(doc.Content) == 0 {
		return out, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("secret.yaml 根节点必须是映射(key: value)")
	}
	flat := make(venueSecretFields)
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		valNode := root.Content[i+1]
		key := strings.TrimSpace(keyNode.Value)
		if key == "" {
			continue
		}
		if valNode.Kind == yaml.MappingNode {
			// 嵌套所节:venue 名 → 字段。
			fields := make(venueSecretFields)
			for j := 0; j+1 < len(valNode.Content); j += 2 {
				fk := strings.ToLower(strings.TrimSpace(valNode.Content[j].Value))
				fv := strings.TrimSpace(valNode.Content[j+1].Value)
				if fk != "" {
					fields[fk] = fv
				}
			}
			out[strings.ToLower(key)] = fields
			continue
		}
		// 扁平标量键 → 收进币安兜底集(保留原始大小写键名,Telegram_* 等不在此用)。
		flat[strings.ToLower(key)] = strings.TrimSpace(valNode.Value)
	}
	// 扁平顶层若含 api/secret 且未被显式 binance 节覆盖 → 作为币安凭证(后向兼容)。
	if _, hasBinanceSection := out[binance.VenueName]; !hasBinanceSection {
		if flat["api"] != "" || flat["secret"] != "" {
			out[binance.VenueName] = flat
		}
	}
	return out, nil
}

// secretConfigFromFields 把字段集装配成 SecretConfig;缺 api/secret 报错(venue 名用于报错定位)。
func secretConfigFromFields(venueName string, fields venueSecretFields) (SecretConfig, error) {
	cfg := SecretConfig{
		APIKey:     fields["api"],
		Secret:     fields["secret"],
		ProxyURL:   fields["proxy_url"],
		Passphrase: fields["passphrase"],
	}
	if cfg.APIKey == "" || cfg.Secret == "" {
		return SecretConfig{}, fmt.Errorf("secret.yaml 中 venue %q 缺少 api 或 secret", venueName)
	}
	return cfg, nil
}

// LoadVenueSecretConfig 从 secret.yaml 读取某 venue 的凭证(每所一节;币安兼容扁平顶层)。
// 这是多所凭证解析的单一入口:每所引擎启动时按自己的 venue 名取节,字段隔离、互不串用。
func LoadVenueSecretConfig(path string, venueName string) (SecretConfig, error) {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	if venueName == "" {
		venueName = binance.VenueName
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SecretConfig{}, fmt.Errorf("读取 secret 文件失败: %w", err)
	}
	secrets, err := parseVenueSecrets(data)
	if err != nil {
		return SecretConfig{}, err
	}
	fields, ok := secrets[venueName]
	if !ok {
		return SecretConfig{}, fmt.Errorf("secret.yaml 未找到 venue %q 的凭证节", venueName)
	}
	return secretConfigFromFields(venueName, fields)
}
