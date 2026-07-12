// Package mechanismpanel 实现一个独立的流式 join 框架:把 OKX 强平事件(驱动流)与
// instrument ctVal、market mids、xvenue liquidation、xmeta OI、盘口深度(上下文流)
// 按 event-time 聚合成 episode,输出被迫名义/加速度/OI下降/跨所确认/吸收/价格路径。
//
// 设计边界(与用户令一致):
//   - 只新增本目录与 cmd/mechanism-panel,不改动 internal/app 现有采集器与 internal/mechanism;
//   - 不读取/扫描真实 records 大文件,单测只用本目录 testdata 小 fixture;
//   - 未来标签(FutureLabel)与入场特征(EntryFeatures)是两个物理上互不重叠的类型,
//     标签计算函数对"决策时刻之前"的数据没有引用路径,靠 absorbFuture 的时间闸门强制。
package mechanismpanel

import (
	"time"

	"lingxi/internal/datatruth"
)

// ===== 原始输入记录形状(与 internal/app 现有 fast_* 采集器 JSON 口径保持字段级兼容,
// 但本包不 import internal/app,也不直接触碰生产 record 文件;字段名照抄方便未来接真实文件)。

// LiquidationRecord 对应 fast_liquidation_events.jsonl 的一行(OKX 全市场爆仓单,驱动流)。
type LiquidationRecord struct {
	RecordedAt time.Time `json:"recorded_at"`
	Symbol     string    `json:"symbol"`
	PosSide    string    `json:"pos_side"` // 被强平方向: long=多头爆 / short=空头爆
	Side       string    `json:"side"`     // 强平单成交方向
	Qty        float64   `json:"qty"`      // 张数
	BkPx       float64   `json:"bk_px"`    // 破产价
	BkLoss     float64   `json:"bk_loss"`  // 穿仓损失
	ExchTs     string    `json:"exch_ts,omitempty"`
}

// XVenueLiquidationRecord 对应 fast_xvenue_liq_events.jsonl 的一行(跨所被迫流,确认用)。
type XVenueLiquidationRecord struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"` // OKX 规范形
	RawSymbol  string    `json:"raw_symbol,omitempty"`
	SideRaw    string    `json:"side_raw,omitempty"`
	PosSide    string    `json:"pos_side"`
	Qty        float64   `json:"qty"`
	Price      float64   `json:"price"`
	ExchTs     int64     `json:"exch_ts,omitempty"`
}

// MarketMidsRecord 对应 fast_market_mids.jsonl 的一行(全市场 mid 标尺,单行多币)。
type MarketMidsRecord struct {
	RecordedAt time.Time          `json:"recorded_at"`
	Mids       map[string]float64 `json:"mids"`
}

// XMetaOIRecord 对应 fast_xmeta_snapshots.jsonl 的一行(venue -> symbol -> [funding, oi, next_funding_ms])。
type XMetaOIRecord struct {
	RecordedAt time.Time                       `json:"recorded_at"`
	Venues     map[string]map[string][]float64 `json:"venues"`
}

// DepthRecord 盘口深度快照(顶档 + 可选名义汇总)。
type DepthRecord struct {
	RecordedAt    time.Time `json:"recorded_at"`
	ReceiveTime   time.Time `json:"receive_time,omitempty"`
	Symbol        string    `json:"symbol"`
	BidPx         float64   `json:"bid_px"`
	BidQty        float64   `json:"bid_qty"`
	AskPx         float64   `json:"ask_px"`
	AskQty        float64   `json:"ask_qty"`
	DepthNotional float64   `json:"depth_notional,omitempty"` // 更深层名义合计(可选,缺省用顶档估算)
}

// PhaseKind 标识一个候选决策 phase:同一原始 episode 最多产出两个互相独立的候选——
// relay(接力锚)与 exhaustion(竭尽锚)。禁止用同一个"结束时"特征同时检验两者(这是本包
// 修复的核心泄漏),因此两个 phase 各自拥有独立锚点、独立 ID、独立 EntryFeatures/FutureLabel。
type PhaseKind string

const (
	PhaseRelay      PhaseKind = "relay"
	PhaseExhaustion PhaseKind = "exhaustion"
)

// Kind 标识一条 Record 来自哪条流。
type Kind string

const (
	KindLiquidation       Kind = "liquidation"
	KindXVenueLiquidation Kind = "xvenue_liquidation"
	KindMarketMids        Kind = "market_mids"
	KindXMetaOI           Kind = "xmeta_oi"
	KindDepth             Kind = "depth"
)

// Record 是 merge 引擎在各流之间统一排序的载体:三时钟信封 + 按 Kind 解出的一种载荷。
type Record struct {
	Kind     Kind
	Envelope datatruth.EventEnvelope

	Liquidation *LiquidationRecord
	XVenue      *XVenueLiquidationRecord
	Mids        *MarketMidsRecord
	OI          *XMetaOIRecord
	Depth       *DepthRecord
}

// EventTime 返回该记录的 event-time(三时钟之一,来自信封)。
func (r Record) EventTime() time.Time { return r.Envelope.EventTime }

// ===== 输出:EpisodeCard 的各分量 =====

// ForcedFlow 被迫名义(episode 内累计的强平名义,按来源venue拆分)。
type ForcedFlow struct {
	VenueNotional map[string]float64 `json:"venue_notional"`
	TotalNotional float64            `json:"total_notional"`
	DominantSide  string             `json:"dominant_side"` // long_squeezed / short_squeezed / mixed
	EventCount    int                `json:"event_count"`
}

// Acceleration 被迫流加速度(episode 前半段 vs 后半段名义速率)。
type Acceleration struct {
	FirstHalfNotional  float64 `json:"first_half_notional"`
	SecondHalfNotional float64 `json:"second_half_notional"`
	RatePerSecond      float64 `json:"rate_per_second"`
	Delta              float64 `json:"delta"` // second - first
	Accelerating       bool    `json:"accelerating"`
}

// OIChange OI 下降(episode 起止之间最近快照的持仓量变化)。
type OIChange struct {
	Venue     string  `json:"venue"`
	StartOI   float64 `json:"start_oi"`
	EndOI     float64 `json:"end_oi"`
	Drop      float64 `json:"drop"` // start-end,>0 表示下降(去杠杆)
	DropRatio float64 `json:"drop_ratio"`
	Observed  bool    `json:"observed"`
}

// CrossVenueConfirmation 跨所确认(episode 窗口内其它交易所是否也出现被迫流)。
type CrossVenueConfirmation struct {
	Confirmed bool     `json:"confirmed"`
	Venues    []string `json:"venues"`
	LagMs     int64    `json:"lag_ms"` // 跨所首次确认相对 episode 起点的滞后
	Notional  float64  `json:"notional"`
}

// Absorption 吸收(被迫名义相对顶档深度的期望冲击 vs 实际价格位移)。
type Absorption struct {
	PriceMoveBps     float64 `json:"price_move_bps"`
	ExpectedMoveBps  float64 `json:"expected_move_bps"`
	AbsorptionRatio  float64 `json:"absorption_ratio"` // 1-实际/期望,越高=越被吸收
	TopDepthNotional float64 `json:"top_depth_notional"`
	Observed         bool    `json:"observed"`
}

// PricePathPoint 相对某个基准时刻的价格路径采样点。
type PricePathPoint struct {
	OffsetMs int64   `json:"offset_ms"`
	Mid      float64 `json:"mid"`
}

// DataQuality 汇总该 episode/label 涉及的数据质量标记(复用 datatruth.QualityMark)。
type DataQuality struct {
	Marks            []datatruth.QualityMark `json:"marks,omitempty"`
	OutOfOrderInputs int                     `json:"out_of_order_inputs"`
	MissingCtVal     bool                    `json:"missing_ctval"`
	MissingOI        bool                    `json:"missing_oi"`
	MissingDepth     bool                    `json:"missing_depth"`
	EvictedCapacity  bool                    `json:"evicted_capacity"` // bounded-memory 强制提前收口
}

// EpisodeMeta episode/phase 的三时钟与身份信息。
//
// 防泄漏时间线(见 research/firstbattle/forced_liquidation_preregistration.{md,json} 第5节
// 与 phases 小节): AsOfTime(=t_signal) 是该 phase 结构性条件(relay: 触发+加速;
// exhaustion: gap 确认+衰减/吸收)最早成立的原始信号时刻;DecisionTime(=AsOfTime+
// FeatureLagMs) 是该 phase 真正的决策/标签锚点。EntryFeatures 的一切字段只能使用
// event_time <= AsOfTime 的数据;FutureLabel 只能使用 event_time > DecisionTime 的数据;
// (AsOfTime, DecisionTime] 这段 FeatureLagMs 宽的窗口对两者都不可见(等待生效延迟)。
type EpisodeMeta struct {
	EpisodeID    string    `json:"episode_id"`     // phase 唯一 ID = RawEpisodeID + "#" + Phase
	RawEpisodeID string    `json:"raw_episode_id"` // 同一原始 episode 的两个 phase 共享此 ID,仅供审计追溯
	Phase        PhaseKind `json:"phase"`
	Instrument   string    `json:"instrument"`
	StartTime    time.Time `json:"start_time"`    // 首个驱动(OKX强平)事件的 event-time
	AsOfTime     time.Time `json:"as_of_time"`    // t_signal;特征截断边界,特征只用 <= 此时刻的数据
	DecisionTime time.Time `json:"decision_time"` // t_signal + FeatureLagMs;标签边界,标签只用 > 此时刻的数据
	ProcessTime  time.Time `json:"process_time"`  // phase 决策时的处理时钟(三时钟之三)
}

// EntryFeatures 入场特征——只能由 event_time <= Episode.AsOfTime 的数据算出。
// 这是物理分离的一半:本类型永不包含任何 AsOfTime 之后的数据。
type EntryFeatures struct {
	Episode      EpisodeMeta            `json:"episode"`
	ForcedFlow   ForcedFlow             `json:"forced_flow"`
	Acceleration Acceleration           `json:"acceleration"`
	OI           OIChange               `json:"oi"`
	CrossVenue   CrossVenueConfirmation `json:"cross_venue"`
	Absorption   Absorption             `json:"absorption"`
	PricePath    []PricePathPoint       `json:"price_path"` // offset 相对 StartTime,均 <= AsOfTime
	Quality      DataQuality            `json:"quality"`
}

// LabelHorizon 单个前瞻窗口的标签。
type LabelHorizon struct {
	HorizonMs       int64   `json:"horizon_ms"`
	ForwardRetBps   float64 `json:"forward_ret_bps"`
	MaxAdverseBps   float64 `json:"max_adverse_bps"`
	MaxFavorableBps float64 `json:"max_favorable_bps"`
	Observed        bool    `json:"observed"`
}

// FutureLabel 未来标签——只能由 event_time > DecisionTime 的数据算出。
// 这是物理分离的另一半:本类型永不包含 DecisionTime 及之前的数据,且与 EntryFeatures
// 不共享任何底层切片/map(见 episode.go 的 absorbFuture 时间闸门)。EpisodeID/Phase 与
// 产出该标签的 EntryFeatures.Episode 完全一致,供 research 侧按 phase 隔离消费。
type FutureLabel struct {
	EpisodeID       string           `json:"episode_id"`
	RawEpisodeID    string           `json:"raw_episode_id"`
	Phase           PhaseKind        `json:"phase"`
	Instrument      string           `json:"instrument"`
	DecisionTime    time.Time        `json:"decision_time"`
	Horizons        []LabelHorizon   `json:"horizons"`
	FuturePricePath []PricePathPoint `json:"future_price_path"` // offset 相对 DecisionTime,均 > 0
	Complete        bool             `json:"complete"`          // 所有 horizon 均被观测到才为 true
}
