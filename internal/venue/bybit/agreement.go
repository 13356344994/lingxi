package bybit

import (
	"context"
	"log/slog"
	"strings"
)

const pathUserAgreement = "/v5/user/agreement"

// SignTradingAgreements 尝试通过 API 签署 TradFi/商品永续所需协议(categoryV2=1 金属/股票永续, 2 原油)。
// 已签署或无需签署时可能返回错误,调用方 best-effort 忽略。
func (a *Adapter) SignTradingAgreements(ctx context.Context) error {
	var lastErr error
	for _, cat := range []int{1, 2} {
		body := struct {
			Agree      bool `json:"agree"`
			CategoryV2 int  `json:"categoryV2"`
		}{Agree: true, CategoryV2: cat}
		if err := a.t.signedPost(ctx, pathUserAgreement, body, nil); err != nil {
			slog.Warn("bybit: sign agreement failed", "categoryV2", cat, "err", err)
			lastErr = err
			continue
		}
		slog.Info("bybit: signed trading agreement", "categoryV2", cat)
	}
	return lastErr
}

// IsAgreementRequiredError 判断是否为 retCode=110126「需先签协议」。
func IsAgreementRequiredError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "110126") || strings.Contains(msg, "sign the required agreement")
}
