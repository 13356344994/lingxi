package okx

// endpoints.go 收敛 OKX v5 端点路径为单一事实源(与 bybit 适配器 endpoints.go 同口径)。
const (
	pathServerTime   = "/api/v5/public/time"
	pathInstruments  = "/api/v5/public/instruments"
	pathFundingRate  = "/api/v5/public/funding-rate"
	pathTickers      = "/api/v5/market/tickers"
	pathTicker       = "/api/v5/market/ticker"
	pathBooks        = "/api/v5/market/books"
	pathOpenInterest = "/api/v5/public/open-interest"
	pathCandles      = "/api/v5/market/candles"

	pathBalance        = "/api/v5/account/balance"
	pathPositions      = "/api/v5/account/positions"
	pathTradeFee       = "/api/v5/account/trade-fee"
	pathAccountConfig  = "/api/v5/account/config"
	pathInterestLimits = "/api/v5/account/interest-limits" // 私有读:借币利率+剩余额度(type=2 市场借币)
	pathSetLeverage    = "/api/v5/account/set-leverage"

	pathPlaceOrder     = "/api/v5/trade/order"
	pathCancelOrder    = "/api/v5/trade/cancel-order"
	pathQueryOrder     = "/api/v5/trade/order"
	pathCancelAllAfter = "/api/v5/trade/cancel-all-after"

	pathAssetBalances     = "/api/v5/asset/balances"           // 资金账户余额
	pathAssetTransfer     = "/api/v5/asset/transfer"           // 所内划转(绝不跨所/提币)
	pathDepositHistory    = "/api/v5/asset/deposit-history"    // 私有只读:链上充值
	pathWithdrawalHistory = "/api/v5/asset/withdrawal-history" // 私有只读:链上提现

	instTypeSpot = "SPOT"
	instTypeSwap = "SWAP"
)
