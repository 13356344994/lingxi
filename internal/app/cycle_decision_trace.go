package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
)

// cycle_decision_trace.go 提供「单个开仓周期决策漏斗」的可观测性：把一轮从【海选】到【实际开仓】
// 的逐级流失（扫描→过海选→监控有效→借币准入→选币→开仓）汇总成一条可读日志，并在 0 开仓时显式
// 指出「最接近放行闸的候选差多少 / 降到多少能纳入」。
//
// 目的：让用户在 server 日志里直接回答两个问题——「本轮为什么没开仓」与「把入场闸调到 X 能多抓多少单」，
// 无需翻 JSONL。所有提炼/格式化都是纯函数，便于单测；不改变任何开仓判定，只增加可观测性。

// scanFunnelSummary 是海选阶段的可观测摘要：评估了多少标的、多少过闸，以及「最接近放行闸却没接」的近失候选。
type scanFunnelSummary struct {
	Scanned    int
	PassedScan int
	NearMiss   *scanNearMiss
}

// scanNearMiss 描述海选里「差一点点就过闸」的候选（|预估费率| < |放行闸| 中 gap 最小者），
// 用于回答「降低入场闸到 |EstimatedRate| 即可纳入它」。
type scanNearMiss struct {
	Symbol        string
	Direction     string
	EstimatedRate float64
	MinRateGate   float64
	Gap           float64 // |MinRateGate| - |EstimatedRate|，>0 表示还差这么多才够放行
	RejectReason  string
}

// cycleDecisionFunnel 汇总单个开仓周期的逐级漏斗与监控期作废原因。纯数据，便于格式化与单测。
type cycleDecisionFunnel struct {
	CycleID        string
	Scan           scanFunnelSummary
	AfterCooldown  int            // 冷却过滤后剩余候选
	MonitorValid   int            // 监控期判定有效
	BorrowAdmitted int            // 借币容量守卫准入后
	Selected       int            // 选币（滚动补位）后；未启用滚动持有时 == BorrowAdmitted
	Opened         int            // 实际成功建仓
	InvalidReasons map[string]int // 监控期作废原因（归类后）-> 计数
}

// summarizeScanNearMiss 从逐标的海选观测里提炼摘要：评估数、过闸数，以及未过闸里「最接近放行闸」的候选。
// 只把 |预估费率| < |放行闸| 的候选视为「近失」（差一点点就够），费率其实够了却被其他规则拒的不算近失。
func summarizeScanNearMiss(observations []ScanObservationRecord) scanFunnelSummary {
	summary := scanFunnelSummary{Scanned: len(observations)}
	bestGap := math.Inf(1)
	for i := range observations {
		obs := observations[i]
		if obs.Passed {
			summary.PassedScan++
			continue
		}
		gateAbs := math.Abs(obs.MinRateGate)
		rateAbs := math.Abs(obs.EstimatedRate)
		if gateAbs <= 0 || rateAbs >= gateAbs {
			continue // 闸无效，或费率其实够了（被基差/跳变/过期等其他规则拒）——不算「差一点点过闸」
		}
		gap := gateAbs - rateAbs
		if gap < bestGap {
			bestGap = gap
			summary.NearMiss = &scanNearMiss{
				Symbol:        obs.Symbol,
				Direction:     obs.Direction,
				EstimatedRate: obs.EstimatedRate,
				MinRateGate:   obs.MinRateGate,
				Gap:           gap,
				RejectReason:  obs.RejectReason,
			}
		}
	}
	return summary
}

// classifyRejectReason 把含具体数字的作废原因归并成可统计的粗类，便于按类别 tally。
func classifyRejectReason(reason string) string {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return "未标注"
	case strings.Contains(r, "过期"):
		return "数据过期"
	case strings.Contains(r, "跳跃") || strings.Contains(r, "跳变"):
		return "费率跳变过大"
	case strings.Contains(r, "方向") && strings.Contains(r, "翻转"):
		return "费率方向翻转"
	case strings.Contains(r, "基差"):
		return "基差过大"
	case strings.Contains(r, "费率") && (strings.Contains(r, "不足") || strings.Contains(r, "过低")):
		return "费率不足"
	case strings.Contains(r, "费率") && strings.Contains(r, "过高"):
		return "费率过高"
	case strings.Contains(r, "净收益") || strings.Contains(r, "净利"):
		return "净利率低于阈值"
	case strings.Contains(r, "深度") || strings.Contains(r, "coverage") || strings.Contains(r, "覆盖"):
		return "盘口深度/覆盖不足"
	case strings.Contains(r, "稳定"):
		return "funding稳定性门"
	case strings.Contains(r, "借币") || strings.Contains(r, "可借"):
		return "借币容量/利率"
	case strings.Contains(r, "保证金") || strings.Contains(r, "强平"):
		return "保证金/强平距离"
	default:
		return "其他"
	}
}

// tallyInvalidReasons 统计监控目标里「被作废」的标的的作废原因（归类后）-> 计数。
// 有效标的（IsValid==1）跳过。与 filterValidTargets 用同一套有效性判定，口径一致。
func tallyInvalidReasons(targets []*MonitoredTarget) map[string]int {
	tally := make(map[string]int)
	for _, target := range targets {
		if target == nil {
			continue
		}
		if atomic.LoadInt32(&target.IsValid) == 1 {
			continue
		}
		tally[classifyRejectReason(target.InvalidReason)]++
	}
	return tally
}

// formatCycleDecisionFunnel 把漏斗渲染成多行可读文本。纯函数，便于单测断言。
func formatCycleDecisionFunnel(f cycleDecisionFunnel) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		"周期决策漏斗 cycle=%s: 海选评估=%d → 过海选=%d → 冷却后=%d → 监控有效=%d → 借币准入=%d → 选币后=%d → 开仓=%d",
		f.CycleID, f.Scan.Scanned, f.Scan.PassedScan, f.AfterCooldown,
		f.MonitorValid, f.BorrowAdmitted, f.Selected, f.Opened,
	))
	if len(f.InvalidReasons) > 0 {
		b.WriteString("\n  监控期作废原因: ")
		b.WriteString(formatReasonTally(f.InvalidReasons))
	}
	if f.Opened == 0 {
		b.WriteString("\n  本轮 0 开仓。")
		if nm := f.Scan.NearMiss; nm != nil {
			b.WriteString(fmt.Sprintf(
				"最接近放行闸的候选: %s(%s) 预估费率=%.6f 放行闸=%.6f 仍差=%.6f（把入场闸 MinEstimatedFundingRate 调到 %.6f 即可纳入它；拒因: %s）",
				nm.Symbol, nm.Direction, nm.EstimatedRate, nm.MinRateGate, nm.Gap, math.Abs(nm.EstimatedRate), nm.RejectReason,
			))
		} else if f.Scan.PassedScan == 0 {
			b.WriteString("海选阶段无任何候选过闸——本轮市场未提供达到平衡线的资金费机会（属正常，非故障）。")
		} else {
			b.WriteString("有候选过海选但后续阶段全部流失，见上方作废原因。")
		}
	}
	return b.String()
}

// formatReasonTally 把原因->计数渲染成「原因×N, 原因×N」的稳定有序文本（按计数降序、同计数按名字升序）。
func formatReasonTally(tally map[string]int) string {
	type kv struct {
		reason string
		count  int
	}
	items := make([]kv, 0, len(tally))
	for reason, count := range tally {
		items = append(items, kv{reason, count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].reason < items[j].reason
	})
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s×%d", it.reason, it.count))
	}
	return strings.Join(parts, ", ")
}

// logCycleDecisionFunnel 把漏斗汇总打到 server 日志（nil-safe，纯旁路，不影响交易）。
func (e *FundingRateSniperEngine) logCycleDecisionFunnel(f cycleDecisionFunnel) {
	if e == nil || e.logger == nil {
		return
	}
	e.logger.Print(formatCycleDecisionFunnel(f))
}
