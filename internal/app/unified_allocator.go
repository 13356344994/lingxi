package app

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const unifiedAllocatorPlanLogFileName = "allocator_plan.jsonl"

// UnifiedAllocatorConfig 是 贪心分配器的风险与质量限额(v1 保守默认)。
type UnifiedAllocatorConfig struct {
	// MaxGlobalCapitalUsagePct 全局资金使用率上限(相对 TotalCapitalUSDT)。
	MaxGlobalCapitalUsagePct float64 `json:"max_global_capital_usage_pct"`
	// MaxStrategyCapitalUsagePct 单策略资金占比上限。
	MaxStrategyCapitalUsagePct float64 `json:"max_strategy_capital_usage_pct"`
	// MaxSymbolCapitalUsagePct 单 symbol 资金占比上限。
	MaxSymbolCapitalUsagePct float64 `json:"max_symbol_capital_usage_pct"`
	// MaxSingleTradeExpectedLossUSDT 单笔最大预期亏损(尾部风险代理)。
	MaxSingleTradeExpectedLossUSDT float64 `json:"max_single_trade_expected_loss_usdt"`
	// MaxConcurrentPlans 本轮最多选中的真实执行计划数。
	MaxConcurrentPlans int `json:"max_concurrent_plans"`
	// MinExecutionScore 低于此值直接拒绝。
	MinExecutionScore float64 `json:"min_execution_score"`
	// MinLiquidityScoreProfit 低于此值不允许进入 profit 真实计划(仅 plan)。
	MinLiquidityScoreProfit float64 `json:"min_liquidity_score_profit"`
	// MinDataQualityScoreProfit 低于此值不允许进入 profit 真实计划(仅 plan)。
	MinDataQualityScoreProfit float64 `json:"min_data_quality_score_profit"`
	// AllowPerpPerpRealAllocation profit mode v1 默认 false: perp_perp 只标 plan。
	AllowPerpPerpRealAllocation bool `json:"allow_perp_perp_real_allocation"`
}

// DefaultUnifiedAllocatorConfig 返回 v1 保守默认(全局≤20%、单策略≤10%)。
func DefaultUnifiedAllocatorConfig() UnifiedAllocatorConfig {
	return UnifiedAllocatorConfig{
		MaxGlobalCapitalUsagePct:       0.20,
		MaxStrategyCapitalUsagePct:     0.10,
		MaxSymbolCapitalUsagePct:       0.10,
		MaxSingleTradeExpectedLossUSDT: 5,
		MaxConcurrentPlans:             3,
		MinExecutionScore:              0.50,
		MinLiquidityScoreProfit:        0.35,
		MinDataQualityScoreProfit:      0.35,
		AllowPerpPerpRealAllocation:    false,
	}
}

// UnifiedAllocatorInput 是单轮 贪心分配输入。
type UnifiedAllocatorInput struct {
	CycleID              string
	Opportunities        []UnifiedOpportunity
	AvailableUSDTByVenue map[string]float64
	TotalCapitalUSDT     float64
	Config               UnifiedAllocatorConfig
	RecordDir            string
	Now                  time.Time
}

// UnifiedAllocatorRealPlanItem 是本轮选中的理论真实执行计划条目(plan-only,不驱动下单)。
type UnifiedAllocatorRealPlanItem struct {
	Rank                int                `json:"rank"`
	OpportunityID       string             `json:"opportunity_id"`
	StrategyType        string             `json:"strategy_type"`
	Symbol              string             `json:"symbol"`
	CapitalRequiredUSDT float64            `json:"capital_required_usdt"`
	CapitalByVenue      map[string]float64 `json:"capital_by_venue"`
	RiskAdjustedNetEV   float64            `json:"risk_adjusted_net_ev_usdt"`
	PrimarySortKey      float64            `json:"primary_sort_key"`
	ExpectedNetEVUSDT   float64            `json:"expected_net_ev_usdt"`
	ExpectedHoldHours   float64            `json:"expected_hold_hours"`
}

// UnifiedAllocatorPlanSuggestionItem 是需要再平衡或仅 计划观察的机会。
type UnifiedAllocatorPlanSuggestionItem struct {
	OpportunityID       string             `json:"opportunity_id"`
	StrategyType        string             `json:"strategy_type"`
	Symbol              string             `json:"symbol"`
	PlanKind            string             `json:"plan_kind"`
	Reason              string             `json:"reason"`
	CapitalRequiredUSDT float64            `json:"capital_required_usdt"`
	CapitalByVenue      map[string]float64 `json:"capital_by_venue"`
	DeficitByVenue      map[string]float64 `json:"deficit_by_venue,omitempty"`
	PrimarySortKey      float64            `json:"primary_sort_key"`
	RiskAdjustedNetEV   float64            `json:"risk_adjusted_net_ev_usdt"`
}

// UnifiedAllocatorSkippedItem 是被跳过机会及原因。
type UnifiedAllocatorSkippedItem struct {
	OpportunityID string   `json:"opportunity_id"`
	StrategyType  string   `json:"strategy_type"`
	Symbol        string   `json:"symbol"`
	Reasons       []string `json:"reasons"`
}

// UnifiedAllocatorSummary 汇总本轮分配结果。
type UnifiedAllocatorSummary struct {
	TotalOpportunities    int     `json:"total_opportunities"`
	SelectedRealPlans     int     `json:"selected_real_plans"`
	PlanSuggestionCount   int     `json:"plan_suggestion_count"`
	SkippedCount          int     `json:"skipped_count"`
	AllocatedCapitalUSDT  float64 `json:"allocated_capital_usdt"`
	GlobalCapitalUsagePct float64 `json:"global_capital_usage_pct"`
	GlobalCapitalLimitPct float64 `json:"global_capital_limit_pct"`
}

// UnifiedAllocatorPlan 是单轮 贪心分配输出。
type UnifiedAllocatorPlan struct {
	CycleID         string                               `json:"cycle_id"`
	GeneratedAt     time.Time                            `json:"generated_at"`
	RealPlans       []UnifiedAllocatorRealPlanItem       `json:"real_plans"`
	PlanSuggestions []UnifiedAllocatorPlanSuggestionItem `json:"plan_suggestions"`
	Skipped         []UnifiedAllocatorSkippedItem        `json:"skipped"`
	Summary         UnifiedAllocatorSummary              `json:"summary"`
}

const (
	unifiedAllocatorPlanKindStrategy  = "strategy_plan"
	unifiedAllocatorPlanKindRebalance = "rebalance_required"
)

type unifiedAllocatorEval struct {
	opp               UnifiedOpportunity
	riskAdjustedNetEV float64
	totalCapital      float64
	primarySortKey    float64
	hardRejectReasons []string
	profitPlanReasons []string
	deficitByVenue    map[string]float64
	localFundsOK      bool
}

func unifiedAllocatorPlanLogPath(recordDir string) string {
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	return filepath.Join(opportunitiesRecordDir(base), unifiedAllocatorPlanLogFileName)
}

func unifiedOpportunityTotalCapital(opp UnifiedOpportunity) float64 {
	total := 0.0
	for _, amt := range opp.CapitalRequiredByVenue {
		if amt > 0 {
			total += amt
		}
	}
	return total
}

func unifiedOpportunityRiskAdjustedNetEV(opp UnifiedOpportunity, totalCapital float64) float64 {
	// v1: ExpectedNetEVUSDT 已含主要成本; 再用 RiskScore 扣持仓尾部风险代理(与轮动 comparable/续持同口径)。
	return opp.ExpectedNetEVUSDT - holdingRiskUSDT(opp.RiskScore, totalCapital)
}

func unifiedOpportunityPrimarySortKey(riskAdjustedNetEV, totalCapital, holdHours float64) float64 {
	if riskAdjustedNetEV <= 0 || totalCapital <= 0 || holdHours <= 0 {
		return 0
	}
	return riskAdjustedNetEV / (totalCapital * holdHours)
}

func normalizeUnifiedAllocatorConfig(cfg UnifiedAllocatorConfig) UnifiedAllocatorConfig {
	def := DefaultUnifiedAllocatorConfig()
	if cfg.MaxGlobalCapitalUsagePct <= 0 {
		cfg.MaxGlobalCapitalUsagePct = def.MaxGlobalCapitalUsagePct
	}
	if cfg.MaxStrategyCapitalUsagePct <= 0 {
		cfg.MaxStrategyCapitalUsagePct = def.MaxStrategyCapitalUsagePct
	}
	if cfg.MaxSymbolCapitalUsagePct <= 0 {
		cfg.MaxSymbolCapitalUsagePct = def.MaxSymbolCapitalUsagePct
	}
	if cfg.MaxSingleTradeExpectedLossUSDT <= 0 {
		cfg.MaxSingleTradeExpectedLossUSDT = def.MaxSingleTradeExpectedLossUSDT
	}
	if cfg.MaxConcurrentPlans <= 0 {
		cfg.MaxConcurrentPlans = def.MaxConcurrentPlans
	}
	if cfg.MinExecutionScore <= 0 {
		cfg.MinExecutionScore = def.MinExecutionScore
	}
	if cfg.MinLiquidityScoreProfit <= 0 {
		cfg.MinLiquidityScoreProfit = def.MinLiquidityScoreProfit
	}
	if cfg.MinDataQualityScoreProfit <= 0 {
		cfg.MinDataQualityScoreProfit = def.MinDataQualityScoreProfit
	}
	return cfg
}

func evaluateUnifiedOpportunityForAllocator(opp UnifiedOpportunity, cfg UnifiedAllocatorConfig, available map[string]float64) unifiedAllocatorEval {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	riskAdj := unifiedOpportunityRiskAdjustedNetEV(opp, totalCapital)
	sortKey := unifiedOpportunityPrimarySortKey(riskAdj, totalCapital, opp.ExpectedHoldHours)

	eval := unifiedAllocatorEval{
		opp:               opp,
		riskAdjustedNetEV: riskAdj,
		totalCapital:      totalCapital,
		primarySortKey:    sortKey,
		localFundsOK:      true,
	}

	if opp.ExpectedNetEVUSDT <= 0 {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "net_ev<=0")
	}
	if riskAdj <= 0 {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "risk_adjusted_net_ev<=0")
	}
	if opp.ExpectedHoldHours <= 0 {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "expected_hold_hours<=0")
	}
	if totalCapital <= 0 {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "capital_required<=0")
	}
	if opp.ExecutionScore < cfg.MinExecutionScore {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "execution_score_below_min")
	}
	tailLoss := totalCapital*clamp01(opp.RiskScore) + opp.ExpectedRollbackCostUSDT
	if tailLoss > cfg.MaxSingleTradeExpectedLossUSDT {
		eval.hardRejectReasons = append(eval.hardRejectReasons, "expected_loss_above_limit")
	}

	if opp.StrategyType == UnifiedStrategyPerpPerp && !cfg.AllowPerpPerpRealAllocation {
		eval.profitPlanReasons = append(eval.profitPlanReasons, "perp_perp_profit_mode_v1_plan")
	}
	if opp.LiquidityScore < cfg.MinLiquidityScoreProfit {
		eval.profitPlanReasons = append(eval.profitPlanReasons, "liquidity_below_profit_min")
	}
	if opp.DataQualityScore < cfg.MinDataQualityScoreProfit {
		eval.profitPlanReasons = append(eval.profitPlanReasons, "data_quality_below_profit_min")
	}

	deficit := make(map[string]float64)
	for venue, need := range opp.CapitalRequiredByVenue {
		if need <= 0 {
			continue
		}
		have := available[venue]
		if have+1e-9 < need {
			eval.localFundsOK = false
			deficit[venue] = need - have
		}
	}
	if len(deficit) > 0 {
		eval.deficitByVenue = deficit
	}
	return eval
}

func cloneVenueCapitalMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneVenueCapitalFromOpportunity(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		if v > 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RunUnifiedAllocator 对统一机会做 贪心分配(纯函数;可选 best-effort 写盘)。
func RunUnifiedAllocator(in UnifiedAllocatorInput) UnifiedAllocatorPlan {
	cfg := normalizeUnifiedAllocatorConfig(in.Config)
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	totalCapital := in.TotalCapitalUSDT
	if totalCapital <= 0 {
		for _, amt := range in.AvailableUSDTByVenue {
			if amt > 0 {
				totalCapital += amt
			}
		}
	}

	available := cloneVenueCapitalMap(in.AvailableUSDTByVenue)
	evals := make([]unifiedAllocatorEval, 0, len(in.Opportunities))
	for _, opp := range in.Opportunities {
		evals = append(evals, evaluateUnifiedOpportunityForAllocator(opp, cfg, available))
	}
	sort.SliceStable(evals, func(i, j int) bool {
		return evals[i].primarySortKey > evals[j].primarySortKey
	})

	plan := UnifiedAllocatorPlan{
		CycleID:     strings.TrimSpace(in.CycleID),
		GeneratedAt: now,
		Summary: UnifiedAllocatorSummary{
			TotalOpportunities:    len(in.Opportunities),
			GlobalCapitalLimitPct: cfg.MaxGlobalCapitalUsagePct,
		},
	}

	usedGlobal := 0.0
	usedByStrategy := map[string]float64{}
	usedBySymbol := map[string]float64{}
	globalLimit := totalCapital * cfg.MaxGlobalCapitalUsagePct
	strategyLimit := totalCapital * cfg.MaxStrategyCapitalUsagePct
	symbolLimit := totalCapital * cfg.MaxSymbolCapitalUsagePct

	for _, eval := range evals {
		opp := eval.opp
		if len(eval.hardRejectReasons) > 0 {
			plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
				OpportunityID: opp.ID,
				StrategyType:  opp.StrategyType,
				Symbol:        opp.Symbol,
				Reasons:       append([]string(nil), eval.hardRejectReasons...),
			})
			continue
		}

		planReason := ""
		planKind := ""
		if len(eval.profitPlanReasons) > 0 {
			planKind = unifiedAllocatorPlanKindStrategy
			planReason = strings.Join(eval.profitPlanReasons, ";")
		}
		if !eval.localFundsOK {
			if planKind == "" {
				planKind = unifiedAllocatorPlanKindRebalance
			}
			rebalanceReason := "local_funds_insufficient"
			if planReason != "" {
				planReason += ";" + rebalanceReason
			} else {
				planReason = rebalanceReason
				if planKind == unifiedAllocatorPlanKindStrategy {
					// 同时缺本地资金: 仍标 rebalance,但保留 strategy plan 语义。
					planKind = unifiedAllocatorPlanKindRebalance
				}
			}
		}

		if planKind != "" || !eval.localFundsOK || len(eval.profitPlanReasons) > 0 {
			plan.PlanSuggestions = append(plan.PlanSuggestions, UnifiedAllocatorPlanSuggestionItem{
				OpportunityID:       opp.ID,
				StrategyType:        opp.StrategyType,
				Symbol:              opp.Symbol,
				PlanKind:            planKind,
				Reason:              planReason,
				CapitalRequiredUSDT: eval.totalCapital,
				CapitalByVenue:      cloneVenueCapitalFromOpportunity(opp.CapitalRequiredByVenue),
				DeficitByVenue:      cloneVenueCapitalFromOpportunity(eval.deficitByVenue),
				PrimarySortKey:      eval.primarySortKey,
				RiskAdjustedNetEV:   eval.riskAdjustedNetEV,
			})
			if planKind != "" {
				continue
			}
		}

		if len(plan.RealPlans) >= cfg.MaxConcurrentPlans {
			plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
				OpportunityID: opp.ID,
				StrategyType:  opp.StrategyType,
				Symbol:        opp.Symbol,
				Reasons:       []string{"max_concurrent_plans_reached"},
			})
			continue
		}
		if usedGlobal+eval.totalCapital > globalLimit+1e-9 {
			plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
				OpportunityID: opp.ID,
				StrategyType:  opp.StrategyType,
				Symbol:        opp.Symbol,
				Reasons:       []string{"global_capital_usage_limit"},
			})
			continue
		}
		if usedByStrategy[opp.StrategyType]+eval.totalCapital > strategyLimit+1e-9 {
			plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
				OpportunityID: opp.ID,
				StrategyType:  opp.StrategyType,
				Symbol:        opp.Symbol,
				Reasons:       []string{"strategy_capital_usage_limit"},
			})
			continue
		}
		if usedBySymbol[opp.Symbol]+eval.totalCapital > symbolLimit+1e-9 {
			plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
				OpportunityID: opp.ID,
				StrategyType:  opp.StrategyType,
				Symbol:        opp.Symbol,
				Reasons:       []string{"symbol_capital_usage_limit"},
			})
			continue
		}
		balanceOK := true
		for venue, need := range opp.CapitalRequiredByVenue {
			if need <= 0 {
				continue
			}
			if available[venue]+1e-9 < need {
				balanceOK = false
				plan.Skipped = append(plan.Skipped, UnifiedAllocatorSkippedItem{
					OpportunityID: opp.ID,
					StrategyType:  opp.StrategyType,
					Symbol:        opp.Symbol,
					Reasons:       []string{"venue_balance_insufficient:" + venue},
				})
				break
			}
		}
		if !balanceOK {
			continue
		}

		for venue, need := range opp.CapitalRequiredByVenue {
			if need > 0 {
				available[venue] -= need
			}
		}
		usedGlobal += eval.totalCapital
		usedByStrategy[opp.StrategyType] += eval.totalCapital
		usedBySymbol[opp.Symbol] += eval.totalCapital

		plan.RealPlans = append(plan.RealPlans, UnifiedAllocatorRealPlanItem{
			Rank:                len(plan.RealPlans) + 1,
			OpportunityID:       opp.ID,
			StrategyType:        opp.StrategyType,
			Symbol:              opp.Symbol,
			CapitalRequiredUSDT: eval.totalCapital,
			CapitalByVenue:      cloneVenueCapitalFromOpportunity(opp.CapitalRequiredByVenue),
			RiskAdjustedNetEV:   eval.riskAdjustedNetEV,
			PrimarySortKey:      eval.primarySortKey,
			ExpectedNetEVUSDT:   opp.ExpectedNetEVUSDT,
			ExpectedHoldHours:   opp.ExpectedHoldHours,
		})
	}

	plan.Summary.SelectedRealPlans = len(plan.RealPlans)
	plan.Summary.PlanSuggestionCount = len(plan.PlanSuggestions)
	plan.Summary.SkippedCount = len(plan.Skipped)
	plan.Summary.AllocatedCapitalUSDT = usedGlobal
	if totalCapital > 0 {
		plan.Summary.GlobalCapitalUsagePct = usedGlobal / totalCapital
	}

	appendUnifiedAllocatorPlan(in.RecordDir, plan)
	return plan
}

// appendUnifiedAllocatorPlan best-effort 写入 records/opportunities/allocator_plan.jsonl。
func appendUnifiedAllocatorPlan(recordDir string, plan UnifiedAllocatorPlan) {
	if strings.TrimSpace(plan.CycleID) == "" {
		return
	}
	_ = appendJSONLRecord(unifiedAllocatorPlanLogPath(recordDir), plan)
}
