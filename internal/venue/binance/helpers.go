package binance

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	defaultRecvWindow = int64(5000)
	maxClientOrderID  = 36
)

// roundDownStep 把数量向下取整到 step 的整数倍(step<=0 时原样返回)。
func roundDownStep(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	return math.Floor(v/step) * step
}

// fmtNum 以非科学计数法格式化浮点(交易所要求纯小数字符串)。
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatQtyByStep 按 LOT_SIZE stepSize 定标后格式化为数量字符串。
func formatQtyByStep(qty, step float64) string {
	normalized := roundDownStep(qty, step)
	precision := precisionFromIncrement(step)
	return strconv.FormatFloat(normalized, 'f', precision, 64)
}

// formatPriceByTick 按 PRICE_FILTER tickSize 定标后格式化为价格字符串。
func formatPriceByTick(price, tick float64) string {
	normalized := roundDownStep(price, tick)
	precision := precisionFromIncrement(tick)
	return strconv.FormatFloat(normalized, 'f', precision, 64)
}

func precisionFromIncrement(increment float64) int {
	if increment <= 0 {
		return 8
	}
	text := strconv.FormatFloat(increment, 'f', -1, 64)
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		return len(strings.TrimRight(text[dot+1:], "0"))
	}
	return 0
}

// sanitizeClientOrderID 截断/保留币安允许的字符。币安 newClientOrderId 允许字母数字、'-'、'_',≤36 字符。
func sanitizeClientOrderID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > maxClientOrderID {
		// 超长截断:保留前缀语义,避免撞 -1013 invalid newClientOrderId
		s = s[:maxClientOrderID]
	}
	return s
}

// normalizeVenueOrderStatus 把币安原始状态归一到 venue 中性集合(new/filled/partially_filled/canceled/rejected)。
func normalizeVenueOrderStatus(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "NEW":
		return "new"
	case "FILLED":
		return "filled"
	case "PARTIALLY_FILLED":
		return "partially_filled"
	case "CANCELED", "EXPIRED":
		return "canceled"
	case "REJECTED":
		return "rejected"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// apiError 尝试从非 200 响应体解析币安 {"code":...,"msg":...}。
type apiError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func httpError(method, endpoint string, statusCode int, status, body string) error {
	body = strings.TrimSpace(body)
	var payload apiError
	if json.Unmarshal([]byte(body), &payload) == nil && (payload.Code != 0 || payload.Msg != "") {
		return fmt.Errorf("binance %s %s: HTTP %d code=%d msg=%s", method, endpoint, statusCode, payload.Code, payload.Msg)
	}
	return fmt.Errorf("binance %s %s: HTTP %d %s body=%s", method, endpoint, statusCode, status, body)
}

func isMarginTypeNoChange(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no need to change margin type") || strings.Contains(msg, "-4046")
}
