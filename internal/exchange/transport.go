package exchange

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ParseHeaderInt 解析一个整数响应头；为空或非法时返回 -1（表示「缺失」，与有效的 0 区分）。
func ParseHeaderInt(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

// PublicGETHTTPError 表示一次公共 GET 请求返回的非 2xx 状态错误。
type PublicGETHTTPError struct {
	URL        string
	StatusCode int
	Status     string
	Body       string
}

// NewPublicGETHTTPError 构造一个 PublicGETHTTPError，并把响应体压成单行、截断到 160 字符。
func NewPublicGETHTTPError(rawURL string, statusCode int, status string, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.ReplaceAll(snippet, "\r", " ")
	if len(snippet) > 160 {
		snippet = snippet[:160]
	}
	return PublicGETHTTPError{
		URL:        rawURL,
		StatusCode: statusCode,
		Status:     strings.TrimSpace(status),
		Body:       snippet,
	}
}

func (e PublicGETHTTPError) Error() string {
	message := fmt.Sprintf("HTTP 状态异常: %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if strings.TrimSpace(e.Status) != "" {
		message = fmt.Sprintf("HTTP 状态异常: %s", e.Status)
	}
	if strings.TrimSpace(e.Body) != "" {
		message += " body=" + e.Body
	}
	return message
}

// ShouldUseBinanceFallbackBases 报告当前 base 是否应启用 Binance 多入口回退
// （即指向官方域名或任意 *.binance.com）。
func ShouldUseBinanceFallbackBases(currentBase, officialBase string) bool {
	currentBase = strings.TrimSpace(strings.ToLower(currentBase))
	officialBase = strings.TrimSpace(strings.ToLower(officialBase))
	if currentBase == "" || officialBase == "" {
		return false
	}
	return currentBase == officialBase || strings.Contains(currentBase, ".binance.com")
}

// ReplaceBaseURLs 把 rawURL 的 base 前缀替换为给定的多个候选 base，去重并保持顺序。
// 无有效候选时回退为原始 URL。
func ReplaceBaseURLs(rawURL, currentBase string, bases []string) []string {
	suffix := strings.TrimPrefix(rawURL, currentBase)
	result := make([]string, 0, len(bases))
	seen := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		if strings.TrimSpace(base) == "" {
			continue
		}
		candidate := base + suffix
		if _, duplicated := seen[candidate]; duplicated {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	if len(result) == 0 {
		return []string{rawURL}
	}
	return result
}

// ParsePublicGETWhitelist 解析公共 GET 附带 API Key 的白名单（逗号分隔、去重、小写）。
func ParsePublicGETWhitelist(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

// MaxRequestGovernorDuration 返回两个时长中的较大者，用于合并限速 slot/冷却时间。
func MaxRequestGovernorDuration(a, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}
