package binance

// endpoints.go 是币安 REST 端点路径的**单一事实源**(P1「下沉端点知识」)。
//
// 引擎层(internal/app)不再内联 "/fapi/..."、"/api/v3/..."、"/sapi/..." 字面量,改引用这些常量,
// 使「该打哪个 URL」的知识收敛到 venue/binance 包。常量为相对路径,调用方仍与 client 的
// SpotBaseURL()/FuturesBaseURL() 拼接,产生与改造前**逐字节一致**的最终 URL。
//
// 接 OKX/Bybit/Gate 时各自有同名职责的端点目录,引擎不感知差异。
const (
	// ---- 现货 /api/v3 ----
	PathSpotOrder      = "/api/v3/order"
	PathSpotBookTicker = "/api/v3/ticker/bookTicker"
	PathSpotDepth      = "/api/v3/depth"
	PathSpotKlines     = "/api/v3/klines"
	PathSpotAccount    = "/api/v3/account"

	// ---- U 本位合约 /fapi ----
	PathFuturesOrder            = "/fapi/v1/order"
	PathFuturesBatchOrders      = "/fapi/v1/batchOrders"
	PathFuturesBookTicker       = "/fapi/v1/ticker/bookTicker"
	PathFuturesDepth            = "/fapi/v1/depth"
	PathFuturesKlines           = "/fapi/v1/klines"
	PathFuturesPremiumIndex     = "/fapi/v1/premiumIndex"
	PathFuturesIncome           = "/fapi/v1/income"
	PathFuturesTime             = "/fapi/v1/time"
	PathFuturesOpenInterest     = "/fapi/v1/openInterest"
	PathFuturesUserTrades       = "/fapi/v1/userTrades"
	PathFuturesMarginType       = "/fapi/v1/marginType"
	PathFuturesLeverage         = "/fapi/v1/leverage"
	PathFuturesLeverageBracket  = "/fapi/v1/leverageBracket"
	PathFuturesPositionSideDual = "/fapi/v1/positionSide/dual"
	PathFuturesCountdownCancel  = "/fapi/v1/countdownCancelAll"
	PathFuturesListenKey        = "/fapi/v1/listenKey"
	PathFuturesAccountV3        = "/fapi/v3/account"
	PathFuturesAccountV2        = "/fapi/v2/account"
	PathFuturesBalance          = "/fapi/v2/balance"
	PathFuturesPositionRisk     = "/fapi/v2/positionRisk"
	PathFuturesCommissionRate   = "/fapi/v1/commissionRate"
	PathSpotExchangeInfo        = "/api/v3/exchangeInfo"
	PathFuturesExchangeInfo     = "/fapi/v1/exchangeInfo"

	// ---- 杠杆/资产/账户 /sapi ----
	PathMarginOrder            = "/sapi/v1/margin/order"
	PathMarginAccount          = "/sapi/v1/margin/account"
	PathMarginBorrowRepay      = "/sapi/v1/margin/borrow-repay"
	PathMarginInterestHistory  = "/sapi/v1/margin/interestHistory"
	PathMarginCrossMarginData  = "/sapi/v1/margin/crossMarginData" // USER_DATA:全仓杠杆费率(含 dailyInterest)
	PathMarginMaxBorrowable    = "/sapi/v1/margin/maxBorrowable"   // USER_DATA:当前最大可借
	PathMarginMaxTransferable  = "/sapi/v1/margin/maxTransferable" // USER_DATA:全仓最大可转出
	PathAssetTransfer          = "/sapi/v1/asset/transfer"
	PathCapitalDepositHisrec   = "/sapi/v1/capital/deposit/hisrec"
	PathCapitalWithdrawHistory = "/sapi/v1/capital/withdraw/history"
	PathAssetFundingAsset      = "/sapi/v1/asset/get-funding-asset"
	PathBNBBurn                = "/sapi/v1/bnbBurn"
	PathAPIRestrictions        = "/sapi/v1/account/apiRestrictions"
)
