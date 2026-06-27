package gate

// endpoints.go 收敛 Gate.io v4 端点路径为单一事实源(与 bybit/okx 适配器同口径)。
const (
	pathSpotTime          = "/api/v4/spot/time"
	pathSpotCurrencyPairs = "/api/v4/spot/currency_pairs"
	pathSpotTickers       = "/api/v4/spot/tickers"
	pathSpotOrderBook     = "/api/v4/spot/order_book"
	pathSpotAccounts      = "/api/v4/spot/accounts"
	pathUnifiedAccounts   = "/api/v4/unified/accounts" // 统一账户:跨保证金真实可用 USDT(子账户 available 不反映)
	pathSpotOrders        = "/api/v4/spot/orders"

	pathFuturesContracts          = "/api/v4/futures/usdt/contracts"
	pathFuturesTickers            = "/api/v4/futures/usdt/tickers"
	pathFuturesOrderBook          = "/api/v4/futures/usdt/order_book"
	pathFuturesCandlesticks       = "/api/v4/futures/usdt/candlesticks"
	pathFuturesAccounts           = "/api/v4/futures/usdt/accounts"
	pathFuturesPositions          = "/api/v4/futures/usdt/positions"
	pathFuturesOrders             = "/api/v4/futures/usdt/orders"
	pathFuturesAccountBook        = "/api/v4/futures/usdt/account_book" // 私有读:U 本位账户流水(type=fund 为资金费结算)
	pathFuturesCountdownCancelAll = "/api/v4/futures/usdt/countdown_cancel_all"

	pathWalletFee         = "/api/v4/wallet/fee"
	pathWalletDeposits    = "/api/v4/wallet/deposits"    // 私有只读:链上充值
	pathWalletWithdrawals = "/api/v4/wallet/withdrawals" // 私有只读:链上提现
	pathWalletTransfers   = "/api/v4/wallet/transfers"   // 所内 spot↔futures 划转

	pathUnifiedBorrowable   = "/api/v4/unified/borrowable"    // 私有读:统一账户最大可借
	pathUnifiedEstimateRate = "/api/v4/unified/estimate_rate" // 私有读:统一账户预估小时借币利率
)
