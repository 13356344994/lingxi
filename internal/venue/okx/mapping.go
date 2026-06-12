package okx

import (
	"fmt"
	"strings"

	"lingxi/internal/venue"
)

// 所内调仓钱包标识(与 venue.CapitalWallets / CapitalAllocationPlan 对齐)。
const (
	WalletFunding venue.Wallet = "funding" // 资金账户
	WalletTrading venue.Wallet = "trading" // 统一交易账户
)

// ToVenue 把规范符号 "BTC-USDT" 翻译成 OKX instId。
// 现货与规范形相同;永续追加 "-SWAP" 后缀(USDT 本位永续)。
func (a *Adapter) ToVenue(canonical venue.Symbol, m venue.Market) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(string(canonical)))
	if s == "" {
		return "", fmt.Errorf("okx: 空符号")
	}
	if m == venue.MarketPerp {
		if strings.HasSuffix(s, "-SWAP") {
			return s, nil
		}
		return s + "-SWAP", nil
	}
	// 现货 instId 即 base-quote(如 BTC-USDT),与规范形一致。
	return s, nil
}

// ToCanonical 把 OKX instId 翻译回规范符号 "BTC-USDT"。
// 永续去掉 "-SWAP" 后缀;现货已是规范形则幂等返回。
func (a *Adapter) ToCanonical(raw string, m venue.Market) (venue.Symbol, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("okx: 空 instId")
	}
	if m == venue.MarketPerp || strings.HasSuffix(s, "-SWAP") {
		return venue.Symbol(strings.TrimSuffix(s, "-SWAP")), nil
	}
	return venue.Symbol(s), nil
}
