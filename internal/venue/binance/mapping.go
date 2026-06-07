package binance

import (
	"strings"

	"lingxi/internal/venue"
)

// 币安钱包标识(与现有 runtime_capital_adjustment.go 的 capitalAccount* 常量取值一致)。
const (
	WalletSpot        venue.Wallet = "spot"
	WalletUMFutures   venue.Wallet = "um_futures"
	WalletCrossMargin venue.Wallet = "cross_margin"
	WalletFunding     venue.Wallet = "funding"
)

// binanceQuoteAssets 是 ToCanonical 拆分 base/quote 时的候选计价币(按长度降序匹配,避免 "USDT" 被 "USD" 误拆)。
var binanceQuoteAssets = []string{"FDUSD", "USDC", "USDT", "TUSD", "BUSD", "BTC", "ETH", "BNB"}

// ToVenue 把规范符号 "BTC-USDT" 翻译成币安原始符号 "BTCUSDT"(现货与永续同形)。
func (a *Adapter) ToVenue(canonical venue.Symbol, m venue.Market) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(string(canonical)))
	return strings.ReplaceAll(s, "-", ""), nil
}

// ToCanonical 把币安原始符号 "BTCUSDT" 翻译回规范符号 "BTC-USDT"。
// 币安符号不含分隔符,按已知计价币后缀做最长匹配拆分;无法识别时退化为原样(大写)。
func (a *Adapter) ToCanonical(raw string, m venue.Market) (venue.Symbol, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if strings.Contains(s, "-") { // 已是规范形式
		return venue.Symbol(s), nil
	}
	for _, q := range binanceQuoteAssets {
		if len(s) > len(q) && strings.HasSuffix(s, q) {
			base := strings.TrimSuffix(s, q)
			return venue.Symbol(base + "-" + q), nil
		}
	}
	return venue.Symbol(s), nil
}
