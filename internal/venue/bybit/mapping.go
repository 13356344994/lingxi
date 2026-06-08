package bybit

import (
	"strings"

	"lingxi/internal/venue"
)

// Bybit 统一账户钱包标识(Stage 4 调仓用;Stage 1 仅声明)。
const (
	WalletUnified venue.Wallet = "unified" // 统一交易账户(现货/合约共用)
	WalletFunding venue.Wallet = "funding" // 资金账户
)

// bybitQuoteAssets 是 ToCanonical 拆分 base/quote 的候选计价币(按长度降序,避免 USDT 被 USD 误拆)。
var bybitQuoteAssets = []string{"USDT", "USDC", "USD", "BTC", "ETH"}

// ToVenue 把规范符号 "BTC-USDT" 翻译成 Bybit 原始符号 "BTCUSDT"(linear 永续与现货同形,去分隔符)。
func (a *Adapter) ToVenue(canonical venue.Symbol, m venue.Market) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(string(canonical)))
	return strings.ReplaceAll(s, "-", ""), nil
}

// ToCanonical 把 Bybit 原始符号 "BTCUSDT" 翻译回规范符号 "BTC-USDT"。
// Bybit 符号不含分隔符,按已知计价币后缀做最长匹配拆分;无法识别时退化为原样(大写)。
func (a *Adapter) ToCanonical(raw string, m venue.Market) (venue.Symbol, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if strings.Contains(s, "-") { // 已是规范形式
		return venue.Symbol(s), nil
	}
	for _, q := range bybitQuoteAssets {
		if len(s) > len(q) && strings.HasSuffix(s, q) {
			base := strings.TrimSuffix(s, q)
			return venue.Symbol(base + "-" + q), nil
		}
	}
	return venue.Symbol(s), nil
}
