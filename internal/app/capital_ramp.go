package app

import (
	"fmt"
	"strconv"
	"strings"
)

// capital_ramp.go 实现 S6-C「灰度验证门自动化」(R33):把 10k→30k→100k 的资金放行/回退从「人工凭感觉」
// 变成可量化规则——实测净 edge ≥ 阈值、滑点 ≤ 预算、成交率 ≥ 门槛、reconcile 连续 HEALTHY 才升档;任一恶化即回退。
//
// 设计:解析(parseCapitalRampSchedule)与判定(decideCapitalRampAction)都是纯函数、可单测,与引擎状态/锁/I/O 解耦。
// 安全优先:回退(收紧)优先于升档(放大);样本不足时不升也不因 edge 误判而回退;顶档保持。

type capitalRampAction string

const (
	capitalRampHold     capitalRampAction = "hold"
	capitalRampPromote  capitalRampAction = "promote"
	capitalRampRollback capitalRampAction = "rollback"
)

// parseCapitalRampSchedule 把逗号分隔的灰度计划(如 "10000,30000,100000")解析为升序档位(USDT)。
// 空串→nil(未启用)。校验:每档 > 0、严格升序;非法返回 error。
func parseCapitalRampSchedule(s string) ([]float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	tiers := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, fmt.Errorf("灰度档位 %q 非法: %w", p, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("灰度档位须为正: %v", v)
		}
		if len(tiers) > 0 && v <= tiers[len(tiers)-1] {
			return nil, fmt.Errorf("灰度档位须严格升序: %v 未大于前一档 %v", v, tiers[len(tiers)-1])
		}
		tiers = append(tiers, v)
	}
	if len(tiers) == 0 {
		return nil, nil
	}
	return tiers, nil
}

// currentRampTierIndex 返回 current 当前落在的档位下标(<= current 的最高档);current 低于首档 → -1。
func currentRampTierIndex(current float64, tiers []float64) int {
	idx := -1
	for i, t := range tiers {
		if current+1e-9 >= t {
			idx = i
		} else {
			break
		}
	}
	return idx
}

// capitalRampGate 是灰度门的量化门槛(来自配置)。
type capitalRampGate struct {
	PromoteMinNetEdgeRate  float64 // 净 edge 率 ≥ 此值才允许升档
	RollbackMaxNetEdgeRate float64 // 净 edge 率 < 此值则回退(通常 = MinRealizedNetEdgeRate)
	MaxSlippageRate        float64 // 实测滑点率上限(超过→回退/阻止升档);0=不约束
	MinFillRate            float64 // 实测成交率下限(低于→回退/阻止升档);0=不约束
	MinHealthyReconciles   int     // 升档要求的连续 HEALTHY 对账次数;0=不要求
	MinEdgeSamples         int     // 升/降 edge 判定所需最小样本数
}

// capitalRampSignals 是判定所需的实测信号快照。Has* 标志区分「无数据」与「数据为 0」。
type capitalRampSignals struct {
	NetEdgeRate        float64
	EdgeSamples        int
	SlippageRate       float64
	HasSlippage        bool
	FillRate           float64
	HasFillRate        bool
	HealthyReconciles  int
	ReconcileUnhealthy bool
}

// capitalRampDecision 是判定结果:动作 + 目标投放名义 + 人类可读理由。
type capitalRampDecision struct {
	Action         capitalRampAction
	TargetNotional float64
	Reason         string
}

// decideCapitalRampAction 纯判定:给定当前投放名义、档位表、实测信号与门槛,产出 hold/promote/rollback。
// 优先级:回退条件(任一命中)> 升档门(全部满足且非顶档)> 保持。
func decideCapitalRampAction(currentNotional float64, tiers []float64, sig capitalRampSignals, gate capitalRampGate) capitalRampDecision {
	if len(tiers) == 0 {
		return capitalRampDecision{Action: capitalRampHold, TargetNotional: currentNotional, Reason: "未配置灰度计划"}
	}
	idx := currentRampTierIndex(currentNotional, tiers)
	enoughSamples := gate.MinEdgeSamples <= 0 || sig.EdgeSamples >= gate.MinEdgeSamples

	rollbackReason := ""
	switch {
	case sig.ReconcileUnhealthy:
		rollbackReason = "对账 UNHEALTHY"
	case enoughSamples && sig.EdgeSamples > 0 && sig.NetEdgeRate < gate.RollbackMaxNetEdgeRate:
		rollbackReason = fmt.Sprintf("实测净 edge 率 %.6f 低于回退线 %.6f", sig.NetEdgeRate, gate.RollbackMaxNetEdgeRate)
	case gate.MaxSlippageRate > 0 && sig.HasSlippage && sig.SlippageRate > gate.MaxSlippageRate:
		rollbackReason = fmt.Sprintf("实测滑点率 %.6f 超预算 %.6f", sig.SlippageRate, gate.MaxSlippageRate)
	case gate.MinFillRate > 0 && sig.HasFillRate && sig.FillRate < gate.MinFillRate:
		rollbackReason = fmt.Sprintf("实测成交率 %.4f 低于门槛 %.4f", sig.FillRate, gate.MinFillRate)
	}
	if rollbackReason != "" {
		if idx <= 0 {
			target := currentNotional
			if idx == 0 {
				target = tiers[0]
			}
			return capitalRampDecision{Action: capitalRampHold, TargetNotional: target, Reason: "已在最低档仍触发回退条件(" + rollbackReason + "),建议人工停机核查"}
		}
		return capitalRampDecision{Action: capitalRampRollback, TargetNotional: tiers[idx-1], Reason: "回退一档:" + rollbackReason}
	}

	if idx >= len(tiers)-1 {
		return capitalRampDecision{Action: capitalRampHold, TargetNotional: currentNotional, Reason: "已在最高档,保持"}
	}
	promoteBlock := ""
	switch {
	case !enoughSamples:
		promoteBlock = fmt.Sprintf("样本不足(%d<%d)", sig.EdgeSamples, gate.MinEdgeSamples)
	case sig.NetEdgeRate < gate.PromoteMinNetEdgeRate:
		promoteBlock = fmt.Sprintf("净 edge 率 %.6f 未达升档线 %.6f", sig.NetEdgeRate, gate.PromoteMinNetEdgeRate)
	case gate.MaxSlippageRate > 0 && sig.HasSlippage && sig.SlippageRate > gate.MaxSlippageRate:
		promoteBlock = "滑点超预算"
	case gate.MinFillRate > 0 && sig.HasFillRate && sig.FillRate < gate.MinFillRate:
		promoteBlock = "成交率不足"
	case gate.MinHealthyReconciles > 0 && sig.HealthyReconciles < gate.MinHealthyReconciles:
		promoteBlock = fmt.Sprintf("连续 HEALTHY 对账 %d 未达 %d", sig.HealthyReconciles, gate.MinHealthyReconciles)
	}
	if promoteBlock != "" {
		return capitalRampDecision{Action: capitalRampHold, TargetNotional: currentNotional, Reason: "保持当前档:" + promoteBlock}
	}
	return capitalRampDecision{Action: capitalRampPromote, TargetNotional: tiers[idx+1], Reason: "升档门全部通过(净 edge/滑点/成交率/对账均达标)"}
}

// evaluateCapitalRampRecommendation 用当前实测信号跑灰度门,产出放行/回退建议并经日志 + 告警上报。
// 仅在 AutoCapitalRampEnabled=true 且配置了 CapitalRampSchedule 时运行(否则纯函数仍可供离线/面板使用)。
//
// 安全边界:本方法**不**直接改动 MaxPortfolioNotionalUSDT——资金放大/回退是高后果决策,由人工核实建议
// (尤其滑点/成交率/对账等需人工确认的信号)后经改参路径(带 audit/回滚)落地。回退建议以 critical 告警上报,
// 升档建议以 warning 上报,提醒运维及时处置。返回判定供调用方/测试使用。
func (e *FundingRateSniperEngine) evaluateCapitalRampRecommendation() (capitalRampDecision, bool) {
	if e == nil || !e.cfg.PortfolioConfig.AutoCapitalRampEnabled {
		return capitalRampDecision{}, false
	}
	tiers, err := parseCapitalRampSchedule(e.cfg.PortfolioConfig.CapitalRampSchedule)
	if err != nil || len(tiers) == 0 {
		return capitalRampDecision{}, false
	}
	stats := e.realizedEdgeStatsSnapshot()
	sig := capitalRampSignals{
		NetEdgeRate: stats.NetEdgeRate,
		EdgeSamples: stats.Samples,
		// 滑点率/成交率/对账健康为需人工确认的信号,引擎当前不做连续跟踪 → 留空(Has*=false),
		// 灰度门据此对升档保守(MinHealthyReconciles>0 时不会自动升档),回退主要由实测净 edge 驱动。
	}
	gate := capitalRampGate{
		PromoteMinNetEdgeRate:  e.cfg.PortfolioConfig.RampPromoteMinNetEdgeRate,
		RollbackMaxNetEdgeRate: e.cfg.RiskConfig.MinRealizedNetEdgeRate,
		MaxSlippageRate:        e.cfg.PortfolioConfig.RampMaxSlippageRate,
		MinFillRate:            e.cfg.PortfolioConfig.RampMinFillRate,
		MinHealthyReconciles:   e.cfg.PortfolioConfig.RampMinHealthyReconciles,
		MinEdgeSamples:         e.cfg.RiskConfig.EdgeGuardMinSamples,
	}
	decision := decideCapitalRampAction(e.cfg.PortfolioConfig.MaxPortfolioNotionalUSDT, tiers, sig, gate)
	if decision.Action == capitalRampHold {
		return decision, true
	}
	level := EventLevelWarning
	title := "灰度门建议升档"
	if decision.Action == capitalRampRollback {
		level = EventLevelCritical
		title = "灰度门建议回退档位"
	}
	detail := fmt.Sprintf("灰度门建议 %s:当前投放名义 %.0f → 目标 %.0f USDT;%s(实测净edge率=%.6f 样本=%d)。需人工核实后改参落地。",
		decision.Action, e.cfg.PortfolioConfig.MaxPortfolioNotionalUSDT, decision.TargetNotional, decision.Reason, stats.NetEdgeRate, stats.Samples)
	if e.logger != nil {
		e.logger.Printf("[capital-ramp] %s", detail)
	}
	if e.alertDispatcher != nil {
		e.alertDispatcher.Notify(Alert{Level: level, Title: title, Detail: detail, OccurredAt: e.serverNow()})
	}
	return decision, true
}
