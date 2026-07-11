// Package mechanism 定义机制专家的纯逻辑内核（对应计划第 6 节「机制专家与连续 EventCard」）。
//
// 本包只依赖 internal/money 定点金额，不依赖引擎、网络或交易所类型，可以完全脱离网络单测。
// 核心对象有两个：
//   - MechanismEventCard：把「天气」压成连续背景，为每个事件生成一张包含制度、被迫流、传导、
//     市场环境、可执行性、账户状态和质量标记七个维度的连续事件卡；卡片本身不给方向。
//   - Thesis：专家消费一张卡后输出的方向论点，统一携带 payer、forced action、direction、
//     deadline、evidence、invalidators、extremeness、heaven confidence、capacity、cluster、
//     exit thesis。缺 payer / 缺期限 / 缺运行时可观测证伪器的论点一律被拒（见 thesis.go）。
//
// 刻意不硬编码任何旧的 8 格 / key / casc / sqz 命名：相关簇一律用 opaque 字符串 ID 表示，
// 机制特有的数值观测走 Signals 命名信号表，随机制扩展而扩展。
package mechanism

import "lingxi/internal/money"

// Direction 表示机制专家给出的（或被迫流施加的）价格方向。
type Direction string

const (
	// DirectionFlat 空仓 / 无方向。
	DirectionFlat Direction = "flat"
	// DirectionLong 做多（价格上行压力）。
	DirectionLong Direction = "long"
	// DirectionShort 做空（价格下行压力）。
	DirectionShort Direction = "short"
)

// Opposite 返回相反方向；Flat 的相反仍是 Flat。
func (d Direction) Opposite() Direction {
	switch d {
	case DirectionLong:
		return DirectionShort
	case DirectionShort:
		return DirectionLong
	default:
		return DirectionFlat
	}
}

// IsDirectional 报告方向是否为可交易的多 / 空（非 Flat / 非空串）。
func (d Direction) IsDirectional() bool {
	return d == DirectionLong || d == DirectionShort
}

// RegimeKind 标识事件所处的制度类别。unknown 是开放世界默认值：
// 落入未知制度的卡片不会被任何首批专家接单。
type RegimeKind string

const (
	// RegimeUnknown 未知制度（开放世界默认，OOD 的一种来源）。
	RegimeUnknown RegimeKind = "unknown"
	// RegimePanicLiquidation 系统性恐慌中的被迫卖出。
	RegimePanicLiquidation RegimeKind = "panic_liquidation"
	// RegimeFundingSettlement 资金费结算窗口。
	RegimeFundingSettlement RegimeKind = "funding_settlement"
	// RegimeTieredLiquidation 逐档强平梯队。
	RegimeTieredLiquidation RegimeKind = "tiered_liquidation"
	// RegimeRuleEvent 规则事件（下架 / 档位 / 指数传导等）。
	RegimeRuleEvent RegimeKind = "rule_event"
)

// PayerRole 标识「谁被迫付款 / 被迫行动」。空值 PayerUnknown 表示找不到被迫方，
// 由此产生的论点会因缺 payer 被拒。
type PayerRole string

const (
	// PayerUnknown 找不到明确被迫方。
	PayerUnknown PayerRole = ""
	// PayerLongLiquidations 被强平的多头（被迫卖出）。
	PayerLongLiquidations PayerRole = "long_liquidations"
	// PayerShortLiquidations 被强平的空头（被迫买入）。
	PayerShortLiquidations PayerRole = "short_liquidations"
	// PayerFundingPayers 结算时支付资金费的一方。
	PayerFundingPayers PayerRole = "funding_payers"
	// PayerFundingReceivers 结算后了结头寸的收款方（收完即走）。
	PayerFundingReceivers PayerRole = "funding_receivers"
	// PayerIndexTrackers 被迫跟踪指数调整的资金。
	PayerIndexTrackers PayerRole = "index_trackers"
	// PayerForcedClosers 规则生效前被迫平仓的一方（如下架）。
	PayerForcedClosers PayerRole = "forced_closers"
)

// MechanismEventCard 是连续事件卡：一次事件的七维快照。
// 字段全部以观测量表达，不携带任何策略决策；决策交给专家与路由器。
type MechanismEventCard struct {
	// ID 事件卡唯一标识（贯穿 decision_id / intent_id 归因）。
	ID string
	// Venue / Instrument 事件所在交易所与标的。
	Venue      string
	Instrument string
	// EventTimeUnixMs / ReceiveTimeUnixMs 交易所事件时间与本地接收时间（unix 毫秒）。
	EventTimeUnixMs   int64
	ReceiveTimeUnixMs int64

	Regime        Regime        // 制度
	ForcedFlow    ForcedFlow    // 被迫流
	Transmission  Transmission  // 传导
	Environment   Environment   // 市场环境（连续背景）
	Executability Executability // 可执行性
	Account       AccountState  // 账户状态
	Quality       Quality       // 质量标记

	// Signals 机制特有的命名数值观测（如 funding_rate / tiers_remaining / tier_index）。
	// 用命名信号表而非固定字段，避免为每个机制堆砌硬编码字段，也避免复用旧的格 / key 命名。
	Signals map[string]float64
}

// Signal 安全读取命名信号，缺失返回 (0, false)。
func (c MechanismEventCard) Signal(name string) (float64, bool) {
	if c.Signals == nil {
		return 0, false
	}
	v, ok := c.Signals[name]
	return v, ok
}

// SignalOr 读取命名信号，缺失返回 fallback。
func (c MechanismEventCard) SignalOr(name string, fallback float64) float64 {
	if v, ok := c.Signal(name); ok {
		return v
	}
	return fallback
}

// Regime 制度维度：事件属于哪类制度、制度性期限在何时。
type Regime struct {
	Kind RegimeKind
	// DeadlineUnixMs 制度性期限（结算时刻 / 下架生效时刻 / 档位调整时刻），unix 毫秒；0 表示无期限。
	DeadlineUnixMs int64
	// Descriptor 自由文本制度描述，仅供审计，不参与硬编码分支。
	Descriptor string
}

// ForcedFlow 被迫流维度：谁被迫、被迫方向、已释放与未释放的被迫名义、是否在衰减。
type ForcedFlow struct {
	Payer PayerRole
	// Side 被迫流施加的价格压力方向（如被迫卖出 = 下行压力 = Short）。
	Side Direction
	// ObservedNotional 已观测释放的被迫名义流（USDT）。
	ObservedNotional money.Micros
	// RemainingNotional 估计仍未释放的被迫名义流（USDT）。
	RemainingNotional money.Micros
	// Decaying 被迫流一阶差分是否为负（正在衰减）。
	Decaying bool
}

// Transmission 传导维度：相关簇、beta、对手盘吸收比例、领先滞后。
type Transmission struct {
	// Cluster 相关簇 opaque ID（不硬编码旧格名）。
	Cluster string
	// Beta 相对簇 / 大盘的 beta。
	Beta float64
	// AbsorptionRatio 被市场吸收的被迫流占比（0~1），越高说明对手盘越强。
	AbsorptionRatio float64
	// LeadLagMs 领先-滞后（正 = 本标的领先簇，负 = 滞后），毫秒。
	LeadLagMs int64
}

// Environment 市场环境维度：连续背景，只做背景，不直接产生方向。
type Environment struct {
	Volatility float64 // 连续波动背景
	Breadth    float64 // 广度 / 参与度背景
	TrendSign  int     // -1 / 0 / +1 连续趋势背景
}

// Executability 可执行性维度：盘口、滑点、最小交易单位、taker/maker 可行性。
type Executability struct {
	SpreadBps           float64      // 盘口价差（bps）
	TopDepthNotional    money.Micros // 盘口一档名义
	ExpectedSlippageBps float64      // 预估滑点（bps）
	LotNotional         money.Micros // 单个最小 lot 的名义（USDT）
	MinNotional         money.Micros // 交易所最小下单名义
	TakerFeasible       bool
	MakerFeasible       bool
}

// AccountState 账户状态维度：偿付权益、可用保证金、当前簇敞口、在场仓位数。
type AccountState struct {
	SolvencyEquity  money.Micros // 偿付权益
	FreeMargin      money.Micros // 可用保证金
	ClusterExposure money.Micros // 当前相关簇已用名义
	OpenPositions   int
}

// Quality 质量标记维度：数据新鲜度、缺口、修正、开放世界 OOD、观测本身信心。
type Quality struct {
	DataAgeMs     int64 // 数据年龄（毫秒，越小越新）
	HasGap        bool  // 是否存在数据缺口
	HasCorrection bool  // 是否发生过修正
	// OutOfDistribution 开放世界：观测落在已知分布之外。
	OutOfDistribution bool
	// ObservationConfidence 对本卡观测本身的信心（0~1）。
	ObservationConfidence float64
}
