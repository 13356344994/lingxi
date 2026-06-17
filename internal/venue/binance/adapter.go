// Package binance 是币安(USDT 永续 + 现货)的 venue.Venue 适配器。
//
// P0 阶段:适配器**内部包住现有 `*exchange.BinanceClient`**,把能直接委托的能力域
// (符号映射 / 公共行情 / 资金费快照 / 账户费率画像 / 借币 / used-weight 预算)逐方法委托并翻译成
// 领域中性类型;**行为与改造前逐字节一致**(只是多一层无状态翻译)。
//
// 下单/撤单/查单(Trading)、WS 流(Streaming)、持仓/余额、调仓(CapitalManagement)等
// 当前实现逻辑仍散落在 internal/app 层(硬编码币安端点),P1「下沉端点知识」/P4「调仓抽象」
// 再迁入本包对应文件;在此之前这些方法返回 venue.ErrUnsupported,并由引擎沿用旧路径,
// 不改变任何线上行为。
package binance

import (
	"sync"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

// VenueName 是本适配器的注册名。
const VenueName = "binance"

// Adapter 实现 venue.Venue,内核是现有的 *exchange.BinanceClient。
type Adapter struct {
	client *exchange.BinanceClient
	// instCache 缓存 instruments 元数据(StepSize/TickSize),PlaceOrder 热路径复用,10 分钟 TTL。
	instMu    sync.RWMutex
	instCache map[string]instCacheEntry
	nowFn     func() time.Time // 测试注入;nil 时用 time.Now
}

// 编译期断言:Adapter 满足完整 venue.Venue 接口。
var _ venue.Venue = (*Adapter)(nil)

// New 用一个现成的 *exchange.BinanceClient 包出适配器(P0 主用法:复用引擎已构造的 client)。
func New(client *exchange.BinanceClient) *Adapter {
	return &Adapter{client: client}
}

// Client 暴露内核 client,供 P0 过渡期引擎旧路径继续直接调用(P1 后逐步移除)。
func (a *Adapter) Client() *exchange.BinanceClient { return a.client }

// Name 返回 "binance"。
func (a *Adapter) Name() string { return VenueName }

// Capabilities 声明币安能力。注:SupportsCapitalRebalance 在 P4 落地 capital.go 后再置 true。
func (a *Adapter) Capabilities() venue.Capabilities {
	return venue.Capabilities{
		SupportsNegativeCarry:    true, // 全仓杠杆 borrow-repay + 保证金现货单已迁入 venue 层
		FundingIntervalsHours:    []int{8, 4},
		HasNativeDeadMan:         true, // countdownCancelAll
		HasFeeDiscountToken:      true, // BNB 抵扣
		MaxBatchOrders:           5,
		ContractBasedPerp:        false, // 按币数量下单
		SupportsCapitalRebalance: false, // TODO(P4): 迁入 capital.go 后置 true
		SupportsBorrowQuote:      true,  // crossMarginData + maxBorrowable 只读探测
		CapitalWallets: []venue.Wallet{
			WalletSpot, WalletUMFutures, WalletCrossMargin, WalletFunding,
		},
	}
}
