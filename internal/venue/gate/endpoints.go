package gate

// endpoints.go 收敛 Gate.io v4 端点路径为单一事实源(与 bybit/okx 适配器同口径)。
const (
	pathSpotTime          = "/api/v4/spot/time"
	pathSpotCurrencyPairs = "/api/v4/spot/currency_pairs"
	pathSpotOrderBook     = "/api/v4/spot/order_book"
	pathSpotAccounts      = "/api/v4/spot/accounts"
	pathSpotOrders        = "/api/v4/spot/orders"

	pathFuturesContracts          = "/api/v4/futures/usdt/contracts"
	pathFuturesOrderBook          = "/api/v4/futures/usdt/order_book"
	pathFuturesAccounts           = "/api/v4/futures/usdt/accounts"
	pathFuturesPositions          = "/api/v4/futures/usdt/positions"
	pathFuturesOrders             = "/api/v4/futures/usdt/orders"
	pathFuturesCountdownCancelAll = "/api/v4/futures/usdt/countdown_cancel_all"

	pathWalletFee       = "/api/v4/wallet/fee"
	pathWalletTransfers = "/api/v4/wallet/transfers" // 所内 spot↔futures 划转
)
