package app

import (
	"path/filepath"
	"strings"
	"time"
)

// missed_opportunity_log.go —— "错过机会复盘"台账(#1)。
//
// 目标:回答"暴力行情出现时,系统为什么没吃到?"。每当一个已被决策层选中、进入派发的开仓动作
// 在执行/闸口被拒(或进场窗未到被推迟),就落一条结构化记录,带:机会画像 + 拒绝原因 + 拒绝分层 +
// 反事实提示(taker/小仓/提前 是否可能改变结果)。纯观测,零交易副作用。
//
// 与 settlements(成交后 PnL)互补:settlements 记"做了什么、赚亏多少",本台账记"想做却没做成、为什么"。

const missedOpportunityLogFileName = "missed_opportunities.jsonl"

// missedOpportunityHighViolenceThreshold "真暴力币"判定阈值:ViolenceScore≥此值即视为值得吃的真暴力币
// (既高费又持续)。用于专项量化"真暴力币到底漏了多少、被哪层挡的"。
const missedOpportunityHighViolenceThreshold = 0.3

// MissedOpportunityAggregation 错过机会聚合视图(供 API / 面板量化"漏单结构")。
type MissedOpportunityAggregation struct {
	ByCategory map[string]int `json:"by_category"`
	ByLayer    map[string]int `json:"by_layer"`
	// 以下为"真暴力币"(violence≥阈值 且 非良性推迟)专项:量化"该吃没吃到"的漏单。
	HighViolenceThreshold        float64        `json:"high_violence_threshold"`
	HighViolenceCount            int            `json:"high_violence_count"`
	HighViolenceByLayer          map[string]int `json:"high_violence_by_layer"`
	HighViolenceByCategory       map[string]int `json:"high_violence_by_category"`
	HighViolenceBlockedByCostLiq int            `json:"high_violence_blocked_by_cost_liq"`
}

// aggregateMissedOpportunities 把错过记录聚合成结构化计数;headline 是"真暴力币被 cost/liquidity 挡掉"的笔数。
// 良性推迟(进场窗未到,下窗会再判)不算真漏单,故 high-violence 专项里剔除。
func aggregateMissedOpportunities(records []MissedOpportunity, highViolenceThreshold float64) MissedOpportunityAggregation {
	agg := MissedOpportunityAggregation{
		ByCategory:             make(map[string]int),
		ByLayer:                make(map[string]int),
		HighViolenceThreshold:  highViolenceThreshold,
		HighViolenceByLayer:    make(map[string]int),
		HighViolenceByCategory: make(map[string]int),
	}
	for _, rec := range records {
		agg.ByCategory[rec.RejectCategory]++
		agg.ByLayer[rec.BlockingLayer]++
		if rec.BenignTiming || rec.ViolenceScore < highViolenceThreshold {
			continue
		}
		agg.HighViolenceCount++
		agg.HighViolenceByLayer[rec.BlockingLayer]++
		agg.HighViolenceByCategory[rec.RejectCategory]++
		if rec.BlockingLayer == "cost" || rec.BlockingLayer == "liquidity" {
			agg.HighViolenceBlockedByCostLiq++
		}
	}
	return agg
}

// MissedOpportunity 单条错过机会记录。
type MissedOpportunity struct {
	Timestamp            string  `json:"timestamp"`
	Symbol               string  `json:"symbol"`
	StrategyType         string  `json:"strategy_type"`
	OpportunityID        string  `json:"opportunity_id,omitempty"`
	HighVenue            string  `json:"high_venue,omitempty"`
	LowVenue             string  `json:"low_venue,omitempty"`
	SpreadAPR            float64 `json:"spread_apr"`
	HighFundingRate      float64 `json:"high_funding_rate"`
	LowFundingRate       float64 `json:"low_funding_rate"`
	ExpectedFundingUSDT  float64 `json:"expected_funding_usdt"`
	ExpectedNetEVUSDT    float64 `json:"expected_net_ev_usdt"`
	TradableNotionalUSDT float64 `json:"tradable_notional_usdt"`
	ViolenceScore        float64 `json:"violence_score"`
	// RejectReason 原始拒绝串(执行/闸口返回)。RejectCategory 归一分类;BlockingLayer 拒绝分层。
	RejectReason   string `json:"reject_reason"`
	RejectCategory string `json:"reject_category"`
	BlockingLayer  string `json:"blocking_layer"`
	// Counterfactual 廉价反事实提示:基于分类给出"换 taker / 缩小仓位 / 提前进场 是否可能改变结果"的判断。
	Counterfactual string `json:"counterfactual"`
	// BenignTiming 为 true 表示"进场窗未到"类良性推迟(非真正错过,下个窗会再判)。
	BenignTiming bool `json:"benign_timing,omitempty"`
	Live         bool `json:"live"`
}

// classifyOpenReject 把执行/闸口拒绝串归一为 (category, blockingLayer, counterfactual)。
// 分层语义:execution=执行事故风险(应≈0);liquidity/cost/window/quality=策略下注质量(可调)。
func classifyOpenReject(reason string) (category, layer, counterfactual string) {
	r := reason
	switch {
	case strings.Contains(r, "盘口价差"):
		// maker 腿盘口宽已由 maker-aware 闸放宽;若仍因 taker(低费)腿被拒,taker 换不掉、缩仓也无济(bps 口径)。
		return "liquidity_book_spread", "liquidity", "taker腿盘口过宽:换taker/缩仓均无效,只能等盘口收窄或弃单"
	case strings.Contains(r, "中间价偏离") || strings.Contains(r, "基差") || strings.Contains(r, "价格比"):
		return "basis_mid_deviation", "liquidity", "两所瞬时背离过大:提前/延后到背离收敛可能可入,缩仓无效"
	case strings.Contains(r, "资费/成本比") || strings.Contains(r, "netProfit") || strings.Contains(r, "EV<0"):
		return "cost_ev", "cost", "资费不敌成本:bps口径下缩仓无效;唯有更高资费/更低费率/更深盘口才转正"
	case strings.Contains(r, "进场窗") || strings.Contains(r, "FundingWindow") || strings.Contains(r, "window"):
		return "funding_window", "window", "进场窗未到:良性,下个目标结算窗会再判(提前进场=承担更久基差暴露)"
	case strings.Contains(r, "余额") || strings.Contains(r, "balance") || strings.Contains(r, "备资") || strings.Contains(r, "insufficient"):
		return "balance", "balance", "可用资金不足:缩仓可能可入(受小资金容量约束)"
	case strings.Contains(r, "冷却") || strings.Contains(r, "cooldown") || strings.Contains(r, "cooling"):
		return "cooldown", "cooldown", "回滚冷却中:本币近期开仓失败被罚停,等冷却结束"
	case strings.Contains(r, "幽灵腿") || strings.Contains(r, "快照无效") || strings.Contains(r, "stale") || strings.Contains(r, "过旧"):
		return "stale_data", "quality", "资费/盘口快照失真:数据问题,非策略问题,等新快照"
	case strings.Contains(r, "lot size") || strings.Contains(r, "51121") || strings.Contains(r, "multiple of"):
		return "exec_lot_size", "execution", "下单数量未对齐lot size:执行bug(已修fmtStepDown),不应再现"
	case strings.Contains(r, "maker") && (strings.Contains(r, "未成交") || strings.Contains(r, "unfilled")):
		return "exec_maker_unfilled", "execution", "maker首腿未成交:暴力币可开taker兜底;否则等价格回挂区"
	case strings.Contains(r, "第一腿") || strings.Contains(r, "第二腿") || strings.Contains(r, "裸腿") || strings.Contains(r, "回滚") || strings.Contains(r, "查单"):
		return "exec_leg_failure", "execution", "下单/对冲执行失败:执行事故风险,需排查交易所/网络"
	case strings.Contains(r, "滑点") || strings.Contains(r, "slippage"):
		return "slippage", "cost", "预估滑点超限:缩仓可能降低吃单滑点,但暴力薄盘改善有限"
	case strings.Contains(r, "min_open_notional") || strings.Contains(r, "名义") || strings.Contains(r, "min_notional"):
		return "min_open_notional", "cost", "可投名义被砍到最小开仓线以下:多为出窗/低置信降仓所致,在窗内高置信币不受影响"
	case strings.Contains(r, "急停") || strings.Contains(r, "halt") || strings.Contains(r, "禁开"):
		return "halt", "control", "控制面急停/禁开:人工/风控触发,非机会问题"
	case strings.Contains(r, "已被") && strings.Contains(r, "持有"):
		return "symbol_held", "control", "该币已被其他引擎/持仓占用:避免重复暴露"
	default:
		return "other", "other", "未归类拒绝原因,需人工查看 reject_reason"
	}
}

func unifiedOppHighLowVenue(opp UnifiedOpportunity) (high, low string) {
	for _, leg := range opp.Legs {
		switch strings.ToLower(strings.TrimSpace(leg.Side)) {
		case "sell", "short":
			high = leg.Venue
		case "buy", "long":
			low = leg.Venue
		}
	}
	if high == "" && len(opp.Legs) > 0 {
		high = opp.Legs[0].Venue
	}
	if low == "" && len(opp.Legs) > 1 {
		low = opp.Legs[1].Venue
	}
	return high, low
}

func unifiedOppTradableNotional(opp UnifiedOpportunity) float64 {
	var sum float64
	for _, leg := range opp.Legs {
		if leg.Notional > sum {
			sum = leg.Notional
		}
	}
	return sum
}

// recordMissedOpportunity 落一条错过机会台账(失败/推迟的开仓尝试)。recordDir 为空则跳过。
func (r *UnifiedRotationRunner) recordMissedOpportunity(opp UnifiedOpportunity, reason string, benignTiming, live bool) {
	if r == nil {
		return
	}
	dir := strings.TrimSpace(r.recordDir)
	if dir == "" {
		return
	}
	now := time.Now().UTC()
	if r.nowFn != nil {
		now = r.nowFn().UTC()
	}
	category, layer, counterfactual := classifyOpenReject(reason)
	high, low := unifiedOppHighLowVenue(opp)
	rec := MissedOpportunity{
		Timestamp:            now.Format(time.RFC3339),
		Symbol:               opp.Symbol,
		StrategyType:         opp.StrategyType,
		OpportunityID:        opp.ID,
		HighVenue:            high,
		LowVenue:             low,
		SpreadAPR:            opp.SpreadAPR,
		HighFundingRate:      opp.HighFundingRate,
		LowFundingRate:       opp.LowFundingRate,
		ExpectedFundingUSDT:  opp.ExpectedFundingUSDT,
		ExpectedNetEVUSDT:    opp.ExpectedNetEVUSDT,
		TradableNotionalUSDT: unifiedOppTradableNotional(opp),
		ViolenceScore:        opp.ViolenceScore,
		RejectReason:         strings.TrimSpace(reason),
		RejectCategory:       category,
		BlockingLayer:        layer,
		Counterfactual:       counterfactual,
		BenignTiming:         benignTiming,
		Live:                 live,
	}
	path := filepath.Join(opportunitiesRecordDir(dir), missedOpportunityLogFileName)
	_ = appendJSONLRecord(path, rec)
}
