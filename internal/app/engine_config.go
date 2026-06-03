package app

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strings"

	"lingxi/internal/core"
	"lingxi/internal/exchange"
)

// SecretConfig 表示从 secret.yaml 读取的最小认证配置。
// SecretConfig 已迁移到 internal/exchange（凭证由交易所客户端消费）。保留别名以零改动兼容。
type SecretConfig = exchange.SecretConfig

// 配置内核类型已迁移到 internal/core（Gate 3 解耦）。这里保留同名别名，
// 让既有的大量引用无需改动即可继续编译。
type (
	EngineProfile    = core.EngineProfile
	NetworkConfig    = core.NetworkConfig
	StrategyConfig   = core.StrategyConfig
	ExecutionConfig  = core.ExecutionConfig
	RiskConfig       = core.RiskConfig
	HistoricalConfig = core.HistoricalConfig
	EngineConfig     = core.EngineConfig
)

const (
	ProfileSmallCapital = core.ProfileSmallCapital
	ProfileStandard     = core.ProfileStandard
	ProfileResearch     = core.ProfileResearch
)

// DefaultEngineConfig 返回适合 MVP 的默认配置。
// 默认值已下沉到 internal/core，供 main 与下层包（exchange 等）共享。
func DefaultEngineConfig() EngineConfig {
	return core.DefaultEngineConfig()
}

func isValidSelfTradePreventionMode(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "NONE", "EXPIRE_TAKER", "EXPIRE_MAKER", "EXPIRE_BOTH":
		return true
	default:
		return false
	}
}

// LoadSecretConfig 读取当前项目中的 secret.yaml。
func LoadSecretConfig(path string) (SecretConfig, error) {
	values, err := readSimpleYAML(path)
	if err != nil {
		return SecretConfig{}, err
	}

	cfg := SecretConfig{
		APIKey:   values["api"],
		Secret:   values["secret"],
		ProxyURL: values["proxy_url"],
	}
	if cfg.APIKey == "" || cfg.Secret == "" {
		return SecretConfig{}, fmt.Errorf("secret.yaml 缺少 api 或 secret")
	}

	return cfg, nil
}

// NewHTTPClient 创建可选代理的 HTTP 客户端。
func NewHTTPClient(networkCfg NetworkConfig, proxyAddress string) (*http.Client, error) {
	return exchange.NewHTTPClient(networkCfg, proxyAddress)
}

// readSimpleYAML 读取当前这种 key: value 形式的简单配置文件。
func readSimpleYAML(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开配置文件失败: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("非法配置行: %s", line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		values[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	return values, nil
}
