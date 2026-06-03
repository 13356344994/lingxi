// Package exchange 收敛与 Binance 交互的底层纯逻辑：请求签名、异常/错误码分类、
// 签名下单端点识别与计重（Gate 3）。
//
// 这里只放无状态、无网络依赖的纯函数，是「唯一摸网络的层」的内核。后续会把
// BinanceClient（REST/WS/限频）本体逐步迁入本包；当前阶段先把可脱网单测的内核抽离，
// 让主包通过薄委托复用同一套口径，避免重复实现导致的漂移。
package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Sign 用 HMAC-SHA256 对已编码的查询串签名，返回十六进制签名。
// 这是 Binance 签名请求的唯一签名口径。
func Sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// ClassifyException 把一个请求错误归类为 (category, severity)，供平台风控/熔断决策。
// 纯函数：只看错误文本里的交易所错误码与关键字，不触发任何副作用。
func ClassifyException(err error) (category string, severity string) {
	if err == nil {
		return "", ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "-1015"), strings.Contains(text, "too many new orders"):
		return "order_rate_limit", "major"
	case strings.Contains(text, "403"), strings.Contains(text, "418"):
		return "platform_reject", "critical"
	case strings.Contains(text, "429"), strings.Contains(text, "too many requests"):
		return "platform_reject", "major"
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline exceeded"), strings.Contains(text, "context canceled"):
		return "request_timeout", "minor"
	case strings.Contains(text, "签名请求失败"), strings.Contains(text, "request failed"):
		return "request_error", "major"
	default:
		return "request_error", "minor"
	}
}

// IsSignedOrderEndpoint 报告端点是否为下单/撤单类签名端点（现货 / 杠杆 / 合约 / 批量）。
func IsSignedOrderEndpoint(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	return strings.Contains(endpoint, "/api/v3/order") ||
		strings.Contains(endpoint, "/sapi/v1/margin/order") ||
		strings.Contains(endpoint, "/fapi/v1/order") ||
		strings.Contains(endpoint, "/fapi/v1/batchorders")
}

// SignedOrderRequestUnits 估算一次 POST 下单请求消耗的「下单计数」单位：
// 普通下单计 1；批量下单按 batchOrders 数组长度计；非下单 POST 计 0。
// 批量参数无法解析时保守计 1。
func SignedOrderRequestUnits(method, endpoint string, params url.Values) int {
	if strings.ToUpper(strings.TrimSpace(method)) != http.MethodPost || !IsSignedOrderEndpoint(endpoint) {
		return 0
	}
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if strings.Contains(endpoint, "/fapi/v1/batchorders") {
		if params == nil {
			return 0
		}
		raw := strings.TrimSpace(params.Get("batchOrders"))
		if raw == "" {
			return 0
		}
		var items []map[string]any
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return 1
		}
		if len(items) == 0 {
			return 0
		}
		return len(items)
	}
	return 1
}
