package exchange

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lingxi/internal/core"
)

// SecretConfig 表示交易所认证所需的最小凭证（从 secret.yaml 读取）。
type SecretConfig struct {
	APIKey   string
	Secret   string
	ProxyURL string
	// Passphrase 仅部分交易所需要(如 OKX 的 API passphrase);币安/Bybit 留空。多所 secret 每所一节时按所读取。
	Passphrase string
}

// NewHTTPClient 按网络配置创建一个可选代理的 *http.Client。
// 这是「唯一摸网络的包」对外暴露的传输构造入口（Gate 3）。
func NewHTTPClient(networkCfg core.NetworkConfig, proxyAddress string) (*http.Client, error) {
	timeout := networkCfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          networkCfg.MaxIdleConns,
		MaxIdleConnsPerHost:   networkCfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       networkCfg.IdleConnTimeout,
		TLSHandshakeTimeout:   networkCfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: networkCfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: networkCfg.ExpectContinueTimeout,
	}

	if strings.TrimSpace(proxyAddress) != "" {
		parsed, err := url.Parse(proxyAddress)
		if err != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}
