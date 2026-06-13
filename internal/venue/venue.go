package venue

// venue.go 定义跨交易所抽象 `Venue` 接口族 + 能力协商 Capabilities + 适配器注册表 Registry。
//
// 设计原则(见 MULTI_VENUE_REFACTOR.md §3):
//   - 按能力域拆小接口(MarketData/AccountData/Trading/Streaming/Safety/CapitalManagement),
//     适配器组合实现,引擎按需依赖最小子集,避免巨型接口。
//   - 不支持的能力一律返回 ErrUnsupported,并在 Capabilities 里置对应 flag=false,引擎据此裁剪行为。
//   - 本文件仅依赖标准库,零交易所耦合。

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported 表示该所不支持某能力(如借币做空、交易所侧 Kill-Switch)。
// 调用方应结合 Capabilities 提前判定,而非依赖错误兜底。
var ErrUnsupported = errors.New("venue: capability unsupported")

// Venue 是单个交易所适配器的总接口,由各能力域子接口组合而成。
type Venue interface {
	Name() string // "binance"/"okx"/"gate"/"bybit"
	Capabilities() Capabilities
	SymbolMapper
	MarketData
	AccountData
	Trading
	Streaming
	Safety
	CapitalManagement
}

// SymbolMapper 负责规范符号 ↔ 该所原始符号的双向翻译。
type SymbolMapper interface {
	ToVenue(canonical Symbol, m Market) (string, error)
	ToCanonical(raw string, m Market) (Symbol, error)
}

// MarketData 是公共行情(无需签名)。
type MarketData interface {
	FetchInstruments(ctx context.Context) ([]Instrument, error)
	FetchFundingSnapshot(ctx context.Context) ([]FundingSnapshot, error)
	FetchBookTicker(ctx context.Context, m Market, s Symbol) (BookTicker, error)
	FetchDepth(ctx context.Context, m Market, s Symbol, limit int) (Depth, error)
	FetchOpenInterest(ctx context.Context, s Symbol) (float64, error)
	FetchRecentQuoteVolume(ctx context.Context, m Market, s Symbol, window time.Duration) (float64, error)
	ServerTime(ctx context.Context) (time.Time, error)
}

// AccountData 是账户/持仓/余额(签名只读)。
type AccountData interface {
	FetchBalances(ctx context.Context) (Balances, error)
	FetchPositions(ctx context.Context) ([]Position, error)
	FetchTradingCostProfile(ctx context.Context, syms []Symbol) (CostProfile, error)
	MaxBorrowable(ctx context.Context, asset string) (float64, error)        // 不支持→ErrUnsupported
	FetchBorrowQuote(ctx context.Context, asset string) (BorrowQuote, error) // 不支持→ErrUnsupported
	FetchLeverageBrackets(ctx context.Context, s Symbol) ([]LeverageBracket, error)
	VerifyAPIKeyPosture(ctx context.Context) (KeyPosture, error)
}

// Trading 是下单/撤单/查单(签名写)。
type Trading interface {
	PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error)
	PlaceBatch(ctx context.Context, reqs []OrderRequest) ([]OrderResult, error)
	CancelOrder(ctx context.Context, m Market, s Symbol, clientOrderID string) error
	QueryOrder(ctx context.Context, m Market, s Symbol, clientOrderID string) (OrderResult, error)
	SetLeverage(ctx context.Context, s Symbol, lev int) error
	SetMarginMode(ctx context.Context, s Symbol, mode MarginMode) error
	BorrowRepay(ctx context.Context, asset string, amt float64, dir BorrowDir) error // 不支持→ErrUnsupported
	FetchRealizedFunding(ctx context.Context, s Symbol, since time.Time) ([]FundingIncome, error)
	FetchUserTrades(ctx context.Context, m Market, s Symbol, since time.Time) ([]Trade, error)
}

// Streaming 是 WS 行情流 + 用户数据流。
type Streaming interface {
	StreamBookTicker(ctx context.Context, syms []Symbol) (<-chan BookTicker, error)
	StreamDepth(ctx context.Context, syms []Symbol) (<-chan Depth, error)
	StreamUserData(ctx context.Context) (<-chan UserEvent, error)
}

// Safety 是错误分类 / 限频预算 / Kill-Switch。
type Safety interface {
	Classify(err error) (category string, severity string)
	ReserveOrderBudget(units int) bool
	ReleaseOrderBudget(units int)
	ArmDeadMansSwitch(ctx context.Context, countdown time.Duration) error // 不支持→本地兜底/ErrUnsupported
}

// CapitalManagement 是所内钱包快照 + 调仓(绝不跨所)。不支持的所返回 ErrUnsupported,
// 并置 Capabilities.SupportsCapitalRebalance=false(面板只读快照、人工调)。
type CapitalManagement interface {
	CaptureMovableSnapshot(ctx context.Context) (snaps []WalletSnapshot, totalUSDT float64, err error)
	PlanAllocation(totalUSDT float64, caps Capabilities) CapitalAllocationPlan
	ExecuteRebalance(ctx context.Context, plan CapitalAllocationPlan) ([]Transfer, error)
}

// Capabilities 是「加所不重构」的关键:引擎按此裁剪行为,而非写死币安假设。
type Capabilities struct {
	SupportsNegativeCarry    bool  // 能否借币做空现货(决定该所是否做负 carry)
	FundingIntervalsHours    []int // 支持的资金费周期 {1,4,8}
	HasNativeDeadMan         bool  // 有无交易所侧 Kill-Switch
	HasFeeDiscountToken      bool  // 类 BNB 抵扣
	MaxBatchOrders           int
	ContractBasedPerp        bool     // 永续是否按「张」而非「币数量」下单
	SupportsCapitalRebalance bool     // 是否支持面板「自动调仓」
	SupportsBorrowQuote      bool     // 是否支持借币行情只读探测(FetchBorrowQuote,跨所扫描遥测用)
	CapitalWallets           []Wallet // 该所参与调仓的钱包集合
}

// VenueError 是适配器把所内错误码归一化后的中性错误(供 Safety.Classify / 重试判定)。
type VenueError struct {
	Venue     string
	Category  string // request_timeout/platform_reject/order_rate_limit/api_key_error/request_error
	Severity  string // minor/major/critical
	RawCode   string
	Retryable bool
	cause     error
}

func (e *VenueError) Error() string {
	if e == nil {
		return "<nil venue error>"
	}
	msg := e.Venue + ": " + e.Category + " (" + e.RawCode + ")"
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *VenueError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// NewVenueError 构造一个中性错误。
func NewVenueError(venue, category, severity, rawCode string, retryable bool, cause error) *VenueError {
	return &VenueError{Venue: venue, Category: category, Severity: severity, RawCode: rawCode, Retryable: retryable, cause: cause}
}

// Factory 用配置构造一个 Venue 适配器实例(每所注册一个)。
type Factory func(cfg Config) (Venue, error)

// Config 是构造适配器所需的中性配置(密钥 + 网络 + 该所参数)。
// 具体字段由调用方填充;适配器只读取自己关心的部分。
type Config struct {
	Name       string
	APIKey     string
	APISecret  string
	Passphrase string // OKX 等需要
	ProxyURL   string // 可选 per-venue 代理
	Extra      map[string]string
}

// Registry 是适配器注册表:「加一家 = 注册一行」。
type Registry struct {
	factories map[string]Factory
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register 登记某所的工厂函数。重复名字会覆盖。
func (r *Registry) Register(name string, f Factory) {
	if r.factories == nil {
		r.factories = make(map[string]Factory)
	}
	r.factories[name] = f
}

// Build 用已登记工厂构造适配器;未登记返回错误。
func (r *Registry) Build(cfg Config) (Venue, error) {
	f, ok := r.factories[cfg.Name]
	if !ok {
		return nil, errors.New("venue: no adapter registered for " + cfg.Name)
	}
	return f(cfg)
}

// Registered 返回已登记的所名(顺序不保证)。
func (r *Registry) Registered() []string {
	out := make([]string, 0, len(r.factories))
	for name := range r.factories {
		out = append(out, name)
	}
	return out
}
