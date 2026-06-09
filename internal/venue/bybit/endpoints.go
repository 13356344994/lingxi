package bybit

// endpoints.go 收敛 Bybit v5 端点路径为单一事实源(与币安适配器 endpoints.go 同口径)。
const (
	pathServerTime      = "/v5/market/time"
	pathTickers         = "/v5/market/tickers"          // 公共:行情 + 资金费率(category=linear)
	pathInstrumentsInfo = "/v5/market/instruments-info" // 公共:合约元数据 + fundingInterval
	pathOrderbook       = "/v5/market/orderbook"        // 公共:盘口
	pathOpenInterest    = "/v5/market/open-interest"    // 公共:持仓量
	pathKline           = "/v5/market/kline"            // 公共:K 线(近端成交额估算)

	pathWalletBalance  = "/v5/account/wallet-balance"  // 私有只读:统一账户余额
	pathFeeRate        = "/v5/account/fee-rate"        // 私有只读:手续费率
	pathTransactionLog = "/v5/account/transaction-log" // 私有只读:账户流水(资金费结算/手续费/已实现盈亏)
	pathPositionList  = "/v5/position/list"          // 私有只读:持仓
	pathQueryAPI      = "/v5/user/query-api"         // 私有只读:API key 权限自检

	pathAssetCoinsBalance = "/v5/asset/transfer/query-account-coins-balance" // 私有只读:资金(FUND)钱包余额
	pathInterTransfer     = "/v5/asset/transfer/inter-transfer"              // 私有写:本 UID 账户类型间划转

	pathOrderCreate      = "/v5/order/create"          // 私有写:下单
	pathOrderCreateBatch = "/v5/order/create-batch"    // 私有写:批量下单
	pathOrderCancel      = "/v5/order/cancel"          // 私有写:撤单
	pathOrderRealtime    = "/v5/order/realtime"        // 私有只读:活动订单查询
	pathSetLeverage      = "/v5/position/set-leverage" // 私有写:设杠杆
	pathSwitchIsolated   = "/v5/position/switch-isolated" // 私有写:逐仓/全仓切换

	categoryLinear = "linear" // USDT 永续
	categorySpot   = "spot"   // 现货
)
