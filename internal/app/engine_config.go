package app

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lingxi/internal/core"
	"lingxi/internal/exchange"
)

// redactSecretPath 把凭证文件路径脱敏为仅文件名,避免日志泄露目录结构/部署布局(S5-D 凭证卫生)。
func redactSecretPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "(unset)"
	}
	return filepath.Base(p)
}

// SecretConfig 表示从 secret.yaml 读取的最小认证配置。
// SecretConfig 已迁移到 internal/exchange（凭证由交易所客户端消费）。保留别名以零改动兼容。
type SecretConfig = exchange.SecretConfig

// 配置内核类型已迁移到 internal/core（Gate 3 解耦）。这里保留同名别名，
// 让既有的大量引用无需改动即可继续编译。
type (
	EngineProfile               = core.EngineProfile
	NetworkConfig               = core.NetworkConfig
	StrategyConfig              = core.StrategyConfig
	ExecutionConfig             = core.ExecutionConfig
	RiskConfig                  = core.RiskConfig
	HistoricalConfig            = core.HistoricalConfig
	EngineConfig                = core.EngineConfig
	PortfolioConfig             = core.PortfolioConfig
	AlertingConfig              = core.AlertingConfig
	CrossVenueScannerConfig     = core.CrossVenueScannerConfig
	CrossVenueExecutorConfig    = core.CrossVenueExecutorConfig
	CarryFundingEntryGateConfig = core.CarryFundingEntryGateConfig
	CapitalAllocatorConfig      = core.CapitalAllocatorConfig
	DataCollectionConfig        = core.DataCollectionConfig
	OKXDirectionalConfig        = core.OKXDirectionalConfig
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

// loadAlertingSecretsOrEmpty 从 secret.yaml 读取 Telegram 凭证(Telegram_Token / Telegram_Chat_ID)。
// 凭证天然属于机密,放在 secret.yaml 而非主配置;缺失或读取失败时返回空串(告警通道随之保持关闭),不报错。
func loadAlertingSecretsOrEmpty(path string) (token string, chatID string) {
	values, err := readSimpleYAML(path)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(values["Telegram_Token"]), strings.TrimSpace(values["Telegram_Chat_ID"])
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
		raw := scanner.Text()
		// 跳过缩进的「嵌套子键」行:本函数只解析顶层扁平 key: value(币安 + Telegram_*)。
		// 多所凭证用 venues 嵌套节(bybit/okx/gate),由 yaml.v3 的 parseVenueSecrets 解析;
		// 这里若把嵌套节的 `  api:`/`  secret:` 当成顶层键读入,会覆盖币安凭证,故按缩进显式忽略。
		if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') {
			continue
		}
		line := strings.TrimSpace(raw)
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
