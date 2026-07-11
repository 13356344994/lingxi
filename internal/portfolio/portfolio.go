// Package portfolio 实现账户级分拣的纯逻辑内核（对应计划第 7 节的账户级部分）：
//   - 提案拍卖：按保守净收益 / 压力损失 / 保证金占用时间给专家提案排序并在预算内择优；
//   - 相关簇预算：账户总名义与逐簇名义双重约束；
//   - 止错（StopError）与偿付（Solvency）两个彻底解耦的状态机：前者只看论点死亡，后者只看
//     最差场景缓冲；任何专家都不能关闭偿付防线。
//
// 本包只依赖 internal/money 与 internal/mechanism，不依赖引擎 / 网络。
package portfolio

import (
	"sort"

	"lingxi/internal/mechanism"
	"lingxi/internal/money"
)

// Proposal 是一条账户级候选提案：某专家论点 + 单标的仓位测算的结果。
type Proposal struct {
	// ID 提案唯一标识。
	ID string
	// Expert / Cluster 来源专家与相关簇。
	Expert  string
	Cluster string
	// Direction 交易方向。
	Direction mechanism.Direction
	// Notional 建议名义（USDT，>0）。
	Notional money.Micros
	// LotNotional / MinNotional 用于预算裁剪后的整数 lot 复核。LotNotional <=0 时不允许部分裁剪；
	// 完整建议名义仍可直接分配（它应已由 decision.AllocateLots 量化）。
	LotNotional money.Micros
	MinNotional money.Micros
	// ConservativeNetUSDT 保守净收益下界（USDT）。<=0 的提案会被直接淘汰（明确空仓）。
	ConservativeNetUSDT money.Micros
	// StressLossUSDT 最差场景下的损失额（USDT，正数表示损失）。
	StressLossUSDT money.Micros
	// MarginOccupation 保证金占用（USDT）。
	MarginOccupation money.Micros
	// MarginTimeSec 保证金占用时长（秒）。
	MarginTimeSec int64
	// DataRed 数据红灯。
	DataRed bool
	// OutOfDistribution 观测落在已知分布之外。
	OutOfDistribution bool
}

// Budget 账户与相关簇的名义预算。
type Budget struct {
	// AccountNotional 账户总可分配名义上限。
	AccountNotional money.Micros
	// ClusterNotional 各相关簇的名义上限；未列出的簇视为上限 0（不予分配）。
	ClusterNotional map[string]money.Micros
}

// AcceptedProposal 是拍卖胜出的提案及其实际获配名义（可能被预算裁剪到小于建议名义）。
type AcceptedProposal struct {
	Proposal          Proposal
	AllocatedNotional money.Micros
}

// RejectedProposal 是被淘汰的提案及理由。
type RejectedProposal struct {
	Proposal Proposal
	Reason   string
}

// 拒绝理由常量。
const (
	// RejectOutOfDistribution 提案处于 OOD → 明确空仓。
	RejectOutOfDistribution = "out_of_distribution"
	// RejectDataRed 数据红灯 → 明确空仓。
	RejectDataRed = "data_red"
	// RejectNoPositiveNet 无正净收益下界 → 明确空仓。
	RejectNoPositiveNet = "no_positive_net"
	// RejectNonPositiveNotional 建议名义非正。
	RejectNonPositiveNotional = "non_positive_notional"
	// RejectNoDirection 提案没有可交易方向。
	RejectNoDirection = "no_direction"
	// RejectInvalidRiskEstimate 压力损失、保证金占用或占用时间缺失，无法保守比较。
	RejectInvalidRiskEstimate = "invalid_risk_estimate"
	// RejectBudgetExhausted 账户或簇预算已耗尽，无名义可配。
	RejectBudgetExhausted = "budget_exhausted"
)

// AuctionResult 拍卖结果。
type AuctionResult struct {
	Accepted    []AcceptedProposal
	Rejected    []RejectedProposal
	UsedAccount money.Micros
	UsedCluster map[string]money.Micros
}

// efficiency 计算提案的分拣效率：保守净收益同时除以压力损失、保证金占用和占用时间。
// 调用前已拒绝非正风险输入，因此结果总是有限可比；数值越大越优先。
func efficiency(p Proposal) float64 {
	net := p.ConservativeNetUSDT.USDT()
	stress := p.StressLossUSDT.USDT()
	margin := p.MarginOccupation.USDT()
	return net / stress / margin / float64(p.MarginTimeSec)
}

// RunAuction 运行账户级提案拍卖：
//  1. 淘汰：OOD / 数据红灯 / 无正净收益下界 / 名义非正 → 明确空仓；
//  2. 排序：按分拣效率降序（保守净收益 / 压力损失 / 保证金占用时间），并列时名义大者优先、再按 ID 稳定；
//  3. 择优：在账户总预算与逐簇预算内逐个分配，放不下的裁剪到剩余预算，剩余为 0 则拒绝。
//
// 该函数是纯函数：不修改入参，结果只由输入决定。
func RunAuction(proposals []Proposal, budget Budget) AuctionResult {
	res := AuctionResult{UsedCluster: map[string]money.Micros{}}

	survivors := make([]Proposal, 0, len(proposals))
	for _, p := range proposals {
		switch {
		case p.OutOfDistribution:
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectOutOfDistribution})
		case p.DataRed:
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectDataRed})
		case p.ConservativeNetUSDT <= 0:
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectNoPositiveNet})
		case p.Notional <= 0:
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectNonPositiveNotional})
		case !p.Direction.IsDirectional():
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectNoDirection})
		case p.StressLossUSDT <= 0 || p.MarginOccupation <= 0 || p.MarginTimeSec <= 0:
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectInvalidRiskEstimate})
		default:
			survivors = append(survivors, p)
		}
	}

	sort.SliceStable(survivors, func(i, j int) bool {
		ei, ej := efficiency(survivors[i]), efficiency(survivors[j])
		if ei != ej {
			return ei > ej
		}
		if survivors[i].Notional != survivors[j].Notional {
			return survivors[i].Notional > survivors[j].Notional
		}
		return survivors[i].ID < survivors[j].ID
	})

	remainingAccount := budget.AccountNotional
	remainingCluster := map[string]money.Micros{}
	for k, v := range budget.ClusterNotional {
		remainingCluster[k] = v
	}
	clusterBudget := func(cluster string) money.Micros {
		if v, ok := remainingCluster[cluster]; ok {
			return v
		}
		return 0 // 未列出的簇不予分配
	}

	for _, p := range survivors {
		allow := remainingAccount
		if cb := clusterBudget(p.Cluster); cb < allow {
			allow = cb
		}
		if allow <= 0 {
			res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectBudgetExhausted})
			continue
		}
		alloc := p.Notional
		if alloc > allow {
			if p.LotNotional <= 0 {
				res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectBudgetExhausted})
				continue
			}
			lots := int64(allow) / int64(p.LotNotional)
			if lots <= 0 {
				res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectBudgetExhausted})
				continue
			}
			alloc = money.Micros(lots * int64(p.LotNotional))
			if p.MinNotional > 0 && alloc < p.MinNotional {
				res.Rejected = append(res.Rejected, RejectedProposal{Proposal: p, Reason: RejectBudgetExhausted})
				continue
			}
		}
		res.Accepted = append(res.Accepted, AcceptedProposal{Proposal: p, AllocatedNotional: alloc})
		remainingAccount -= alloc
		remainingCluster[p.Cluster] = clusterBudget(p.Cluster) - alloc
		res.UsedAccount += alloc
		res.UsedCluster[p.Cluster] += alloc
	}
	return res
}
