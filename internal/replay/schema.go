// Package replay 实现账户级回放器的基础层(replay-engine todo 第一阶段):
//
//   - 订单尝试 / fill / 未成交反事实 / fee / funding / slippage / partial fill 的统一 Trade 口径;
//   - episode 相关簇(同一 BTC/强平簇按一个相关 episode 处理);
//   - 账户路径指标:净 USDT、净对数增长、最大回撤、episode 级 CVaR;
//   - 账户级压力场景(相关性=1、全多暴跌、全空暴涨、流动性蒸发、API 断流、pending 转出);
//   - 成交动作 NO_ORDER / MAKER / TAKER 的条件净收益「下界」比较(优化下界,不优化成交率);
//   - 固定种子 bootstrap 的净收益下分位与破产概率置信上界。
//
// 本包是纯计算层:不读网络、不写盘、不依赖 app/risk/execution;JSONL 映射见 loader.go,
// 巨量文件的流式读取由调用方控制(单测只用内存小样本)。
package replay

import "time"

// Side 持仓方向(与 settlements.jsonl 的 side 字段同词表)。
type Side string

const (
	SideLong  Side = "long"
	SideShort Side = "short"
)

// Sign 返回方向符号:long=+1、short=-1、其余=0(视为无效)。
func (s Side) Sign() float64 {
	switch s {
	case SideLong:
		return 1
	case SideShort:
		return -1
	default:
		return 0
	}
}

// ExecStyle 成交动作词表(计划文档第 3 节动态执行动作的回放子集)。
type ExecStyle string

const (
	ExecNoOrder ExecStyle = "NO_ORDER"
	ExecMaker   ExecStyle = "MAKER"
	ExecTaker   ExecStyle = "TAKER"
)

// Trade 一次账户级回放交易(一次订单尝试及其(可能的)往返结果)。
//
// 设计要点:
//   - FillRatio=0 表示尝试未成交(maker_not_filled 等),回放时净 PnL=0,
//     但会用 mid-to-mid 计算未成交反事实毛收益(TradeResult.CounterfactualGrossUSDT);
//   - 0<FillRatio<1 表示部分成交,名义按比例参与 PnL/费用已由调用方给出实扣值;
//   - 滑点不单独输入:由 EntryPrice/EntryMid、ExitPrice/ExitMid 推导并在结果中拆解;
//   - GrossPnLUSDT 若非 nil 则直接采用(例如来自 settlements.realized_usdt 的交易所真相),
//     否则由价格推导;两者都以「未扣 fee/funding」为口径。
type Trade struct {
	OpenTime  time.Time
	CloseTime time.Time
	Venue     string
	Symbol    string
	Side      Side
	Style     ExecStyle

	EntryMid   float64
	EntryPrice float64
	ExitMid    float64
	ExitPrice  float64

	NotionalUSDT float64 // 计划名义(未成交也有,供反事实)
	FillRatio    float64 // 0=未成交, (0,1]=成交比例
	FeeUSDT      float64 // 实扣手续费(开+平,付出为正)
	FundingUSDT  float64 // 持仓期资金费(付出为正,收到为负)

	// GrossPnLUSDT 非 nil 时覆盖价格推导的毛 PnL(交易所真相优先)。
	GrossPnLUSDT *float64

	// EpisodeID 显式相关簇 ID(如强平接力事件 ID);同 ID 的交易合并为一个 episode。
	EpisodeID string
	// Cluster 币簇/机制簇(如 "btc_liq");无 EpisodeID 时,同簇且时间相邻的交易
	// 会被链入同一 episode(见 GroupEpisodes)。
	Cluster string
}

// TradeResult 单笔交易的回放结果拆解。
type TradeResult struct {
	Trade       Trade
	Unfilled    bool
	GrossUSDT   float64 // 毛 PnL(未扣费)
	FeeUSDT     float64
	FundingUSDT float64
	NetUSDT     float64 // Gross - Fee - Funding
	// 滑点拆解(正=成本):相对 mid 的入场/出场执行损耗,已包含在 GrossUSDT 内,仅供归因。
	EntrySlipUSDT float64
	ExitSlipUSDT  float64
	// CounterfactualGrossUSDT 未成交反事实:若当时以 mid-to-mid 成交本应得到的毛收益
	// (未扣任何费用;仅在 Unfilled 且 mid 有效时非零)。
	CounterfactualGrossUSDT float64
}

// PathPoint 账户路径上的一个权益观测点(每笔交易结算后)。
type PathPoint struct {
	Time       time.Time
	EquityUSDT float64
}

// Episode 相关簇聚合后的一个风险单元:CVaR / bootstrap 都以 episode 为最小重采样单位,
// 避免把同一 BTC 暴跌里的 8 笔强平接力当成 8 个独立样本而低估尾部。
type Episode struct {
	Key      string
	Cluster  string
	Start    time.Time
	NetUSDT  float64
	Trades   int
	Unfilled int
}
