package gate

import (
	"fmt"
	"strings"

	"lingxi/internal/venue"
)

// 所内调仓钱包标识(与 venue.CapitalWallets / CapitalAllocationPlan 对齐)。
const (
	WalletSpot    venue.Wallet = "spot"    // 现货主账户
	WalletFutures venue.Wallet = "futures" // USDT 永续账户
)

// gateQuoteAssets 是 ToCanonical 拆分 base/quote 的候选计价币(按长度降序,避免 USDT 被 USD 误拆)。
var gateQuoteAssets = []string{"USDT", "USDC", "USD", "BTC", "ETH"}

// ToVenue 把规范符号 "BTC-USDT" 翻译成 Gate 原始符号 "BTC_USDT"。
// 现货与 USDT 永续同形(下划线分隔),由请求路径区分市场(/spot/ vs /futures/usdt/)。
func (a *Adapter) ToVenue(canonical venue.Symbol, m venue.Market) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(string(canonical)))
	if s == "" {
		return "", fmt.Errorf("gate: 空符号")
	}
	return strings.ReplaceAll(s, "-", "_"), nil
}

// ToCanonical 把 Gate 原始符号 "BTC_USDT" 翻译回规范符号 "BTC-USDT"。
func (a *Adapter) ToCanonical(raw string, m venue.Market) (venue.Symbol, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("gate: 空符号")
	}
	if strings.Contains(s, "-") {
		return venue.Symbol(s), nil
	}
	if strings.Contains(s, "_") {
		return venue.Symbol(strings.ReplaceAll(s, "_", "-")), nil
	}
	for _, q := range gateQuoteAssets {
		if len(s) > len(q) && strings.HasSuffix(s, q) {
			base := strings.TrimSuffix(s, q)
			return venue.Symbol(base + "-" + q), nil
		}
	}
	return venue.Symbol(s), nil
}
