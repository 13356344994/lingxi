package app

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// shadowBlockedError 是影子(只读冒烟)模式下拦截「状态变更类签名请求」时返回的哨兵错误。
// 调用方可用 isShadowBlockedError 识别它，从而把它与真实的交易所/网络错误区分开。
type shadowBlockedError struct {
	method   string
	endpoint string
}

func (e shadowBlockedError) Error() string {
	return fmt.Sprintf("shadow 模式已拦截状态变更请求(未触达交易所): method=%s endpoint=%s", e.method, e.endpoint)
}

func isShadowBlockedError(err error) bool {
	if err == nil {
		return false
	}
	var target shadowBlockedError
	return errors.As(err, &target)
}

// shadowShouldBlockSignedRequest 是纯决策函数：影子模式开启时，拦截一切非 GET 的签名请求
// (下单/撤单/改杠杆/划转/借还币等任何会改变交易所状态或动用资金的调用)。GET 只读请求放行，
// 以便引擎照常获取真实行情与账户状态——这正是影子冒烟需要的：真实输入 + 零真实副作用。
func shadowShouldBlockSignedRequest(shadowEnabled bool, method string) bool {
	if !shadowEnabled {
		return false
	}
	return strings.ToUpper(strings.TrimSpace(method)) != http.MethodGet
}

// noteShadowBlocked 把被拦截的下单/状态变更意图打成一行结构化日志，作为影子冒烟的核查产物：
// 人工据此判断引擎是否在尝试「下错方向 / 异常数量 / 失控重复下单」等灾难性行为。
func (e *FundingRateSniperEngine) noteShadowBlocked(method string, endpoint string, params url.Values) {
	logger := e.logger
	if logger == nil {
		logger = log.Default()
	}
	field := func(keys ...string) string {
		if params == nil {
			return "-"
		}
		for _, k := range keys {
			if v := strings.TrimSpace(params.Get(k)); v != "" {
				return v
			}
		}
		return "-"
	}
	logger.Printf(
		"SHADOW_BLOCKED method=%s endpoint=%s symbol=%s side=%s type=%s qty=%s price=%s reduceOnly=%s clientOrderId=%s",
		strings.ToUpper(strings.TrimSpace(method)),
		strings.TrimSpace(endpoint),
		field("symbol"),
		field("side"),
		field("type"),
		field("quantity", "batchOrders"),
		field("price", "stopPrice"),
		field("reduceOnly"),
		field("newClientOrderId"),
	)
}
