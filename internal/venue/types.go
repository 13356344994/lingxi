package venue

// types.go 定义跨交易所的「领域中性类型」(canonical types)。引擎只认这些类型,
// 各家适配器(internal/venue/<vendor>)负责把它们与该所的 REST/WS 报文双向翻译。
//
// 设计原则(见 MULTI_VENUE_REFACTOR.md §4):
//   - 本文件不依赖任何具体交易所类型,仅用标准库,保证零耦合、可独立编译。
//   - 数量统一用「币数量」(coin);永续按张的所在适配器内用 Instrument.ContractSize 换算。
//   - 符号统一为规范形式 base-quote 大写(如 "BTC-USDT"),映射在各所 mapping.go。

import "time"

// Market 区分现货与永续两个市场维度。
type Market uint8

const (
	MarketSpot Market = iota // 现货
	MarketPerp               // 永续合约
)

func (m Market) String() string {
	switch m {
	case MarketSpot:
		return "spot"
	case MarketPerp:
		return "perp"
	default:
		return "unknown"
	}
}

// Symbol 是规范化交易对:base-quote 大写,如 "BTC-USDT"。
// 各所原始符号(BTCUSDT / BTC-USDT-SWAP / BTC_USDT)由 SymbolMapper 双向翻译。
type Symbol string

// Side 是下单方向。
type Side uint8

const (
	SideBuy Side = iota
	SideSell
)

func (s Side) String() string {
	if s == SideSell {
		return "sell"
	}
	return "buy"
}

// OrderType 是订单类型(中性集合;具体所支持哪些由 Capabilities/适配器裁剪)。
type OrderType uint8

const (
	OrderTypeIOC      OrderType = iota // 立即成交否则取消
	OrderTypeLimit                     // 限价(GTC)
	OrderTypeMarket                    // 市价
	OrderTypePostOnly                  // 只挂单
)

// MarginMode 是合约保证金模式。
type MarginMode uint8

const (
	MarginModeCross    MarginMode = iota // 全仓
	MarginModeIsolated                   // 逐仓
)

// MarginEffect 描述现货腿是否伴随借/还币(负 carry 用)。
type MarginEffect uint8

const (
	MarginEffectNone   MarginEffect = iota // 普通现货
	MarginEffectBorrow                     // 下单同时借币(做空现货)
	MarginEffectRepay                      // 平仓同时还币
)

// BorrowDir 是借还方向。
type BorrowDir uint8

const (
	BorrowDirBorrow BorrowDir = iota
	BorrowDirRepay
)

// Instrument 是一个可交易标的的元数据(现货或永续)。
type Instrument struct {
	Symbol         Symbol
	Market         Market
	BaseAsset      string
	QuoteAsset     string
	TickSize       float64
	StepSize       float64
	MinNotional    float64
	PricePrecision int
	QtyPrecision   int
	ContractSize   float64 // 永续合约面值:ContractBasedPerp 时 >0,否则 1
	QtyUnit        string  // "coin" | "contract"
}

// FundingSnapshot 等价于币安 premiumIndex + fundingInfo 的合并视图,逐币给出实时费率与上下限。
type FundingSnapshot struct {
	Symbol               Symbol
	MarkPrice            float64
	IndexPrice           float64
	EstimatedSettlePrice float64
	LastFundingRate      float64
	NextFundingTime      time.Time
	IntervalHours        int     // 1 / 4 / 8 …逐币
	RateCap              float64 // 该所该币资金费上限(撞限豁免用)
	RateFloor            float64 // 下限
}

// BookTicker 是最优买卖一档。
type BookTicker struct {
	Symbol   Symbol
	Market   Market
	BidPrice float64
	BidQty   float64
	AskPrice float64
	AskQty   float64
}

// DepthLevel 是一档盘口。
type DepthLevel struct {
	Price float64
	Qty   float64
}

// Depth 是某标的的盘口快照。
type Depth struct {
	Symbol Symbol
	Market Market
	Bids   []DepthLevel
	Asks   []DepthLevel
}

// CostProfile 是账户实拉的手续费画像(平台币抵扣已计入实拉费率,由 DiscountApplied 标记)。
type CostProfile struct {
	SpotTaker       float64
	SpotMaker       float64
	FuturesTaker    float64
	FuturesMaker    float64
	DiscountApplied bool
}

// LeverageBracket 是合约杠杆档位。
type LeverageBracket struct {
	NotionalCap     float64
	MaxLeverage     int
	MaintMarginRate float64
}

// Balance 是单资产余额。
type Balance struct {
	Asset     string
	Free      float64
	Locked    float64
	WalletUSD float64
}

// Balances 汇总各市场/钱包的余额。
type Balances struct {
	ByAsset map[string]Balance
}

// Position 是合约持仓。
type Position struct {
	Symbol        Symbol
	PositionAmt   float64 // 正=多,负=空(币数量)
	EntryPrice    float64
	MarkPrice     float64
	UnrealizedPnL float64
	Leverage      int
	MarginMode    MarginMode
}

// KeyPosture 是 API key 安全自检结果(提现权限/IP 白名单等)。
type KeyPosture struct {
	CanTrade     bool
	CanWithdraw  bool // 期望 false:套利 key 不应有提现权限
	IPRestricted bool
	Notes        string
}

// OrderRequest 是中性下单请求;数量统一用币数量,适配器内部换算成该所张数。
type OrderRequest struct {
	Market        Market
	Symbol        Symbol
	Side          Side
	Type          OrderType
	Qty           float64
	Price         float64
	ClientOrderID string
	MarginEffect  MarginEffect
	ReduceOnly    bool
}

// OrderResult 是下单/查单的中性结果。
type OrderResult struct {
	Symbol          Symbol
	Market          Market
	ClientOrderID   string
	ExchangeOrderID string
	Status          string // new/filled/partially_filled/canceled/rejected
	ExecutedQty     float64
	AvgPrice        float64
	CumQuoteUSDT    float64
	// Fee 是该订单累计实际成交手续费(交易所真实扣费,含 BNB/MNT 等抵扣后的真实口径);FeeAsset 是计费币种。
	// 用于「真实成交成本」量化(估算费率 vs 实扣费率的对照),解析缺失时为 0/""。
	Fee      float64
	FeeAsset string
	RawCode  string
	// CreatedAt 是订单在交易所的创建时间(解析缺失时为零值)。用于幂等确认时识别「撞上历史旧单」:
	// 同一 (symbol, 结算窗口) 平掉后再开会复用 orderLinkId,若按旧单的成交回执确认会产生幻影持仓。
	CreatedAt time.Time
}

// Trade 是一笔用户成交。
type Trade struct {
	Symbol          Symbol
	Market          Market
	Price           float64
	Qty             float64
	QuoteQty        float64
	Commission      float64
	CommissionAsset string
	IsMaker         bool
	Time            time.Time
}

// FundingIncome 是一笔已实现资金费收入(用于结算复核)。
type FundingIncome struct {
	Symbol Symbol
	Income float64
	Asset  string
	Time   time.Time
}

// UserEventKind 区分用户数据流事件类别。
type UserEventKind uint8

const (
	UserEventOrderUpdate   UserEventKind = iota // 订单状态变更
	UserEventAccountUpdate                      // 账户/持仓变更
	UserEventOther
)

// UserEvent 是用户数据流的中性事件(封装 listenKey 续期等所内细节)。
type UserEvent struct {
	Kind    UserEventKind
	Symbol  Symbol
	Market  Market
	Order   *OrderResult
	RawType string
	Time    time.Time
}

// Wallet 是该所参与调仓的钱包标识(各所自定义取值)。
//
//	binance: "spot" / "um_futures" / "cross_margin" / "funding"
//	bybit:   "unified" / "funding"
type Wallet string

// WalletSnapshot 是单个钱包的可动用快照。
type WalletSnapshot struct {
	Wallet   Wallet
	Label    string // UI 展示名(本地化)
	FreeUSDT float64
}

// CapitalAllocationPlan 是一次调仓的目标分配(适配器按能力生成)。
type CapitalAllocationPlan struct {
	TotalTradingUSDT float64
	Targets          map[Wallet]float64
	RemainderWallet  Wallet // 超出交易目标的余量打入此钱包
}

// Transfer 是一次钱包间划转记录(仅所内,绝不跨所)。
type Transfer struct {
	From      Wallet
	To        Wallet
	AssetUSDT float64
	VenueRef  string // 该所划转单号/类型(币安 MAIN_UMFUTURE 等),仅记录用
}
