package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/core"
)

const (
	RotationActionHold   = "HOLD"
	RotationActionClose  = "CLOSE"
	RotationActionSwitch = "SWITCH"
	RotationActionOpen   = "OPEN"
)

// RotationConfig 是统一资金费轮动 shadow 计划的限额与保留金配置。
type RotationConfig struct {
	MaxConcurrent      int                `json:"max_concurrent"`
	MaxPositionUSDT    float64            `json:"max_position_usdt"`
	ReserveUSDTByVenue map[string]float64 `json:"reserve_usdt_by_venue"`
	DefaultReserveUSDT float64            `json:"default_reserve_usdt"`
	// TotalDeployableUSDT 全局可部署资金上限(USDT);0=不单独设限,仅用账户可用合计−储备作为预算基数。
	TotalDeployableUSDT float64 `json:"total_deployable_usdt"`
	// TotalDeployablePct 全局可部署预算占真实现金权益比例;>0 且 equity>0 时有效上限=pct×equity。
	TotalDeployablePct float64 `json:"total_deployable_pct"`
	// MaxStrategyCapitalUsagePct 单策略占用占全局可部署预算的比例上限;0=不限。
	MaxStrategyCapitalUsagePct float64 `json:"max_strategy_capital_usage_pct"`
	// ContinuationCloseThreshold 续持分须低于 −此阈值才允许软 CLOSE(过滤边际负分 fee churn)。默认 0.001。
	ContinuationCloseThreshold float64 `json:"continuation_close_threshold"`
	// ContinuationMinHoldSeconds 开仓后至少持有此秒数才允许续持分软 CLOSE。默认 3600。
	ContinuationMinHoldSeconds int `json:"continuation_min_hold_seconds"`
}

// DefaultRotationConfig 返回 v1 保守默认轮动配置。
func DefaultRotationConfig() RotationConfig {
	return RotationConfig{
		MaxConcurrent:              3,
		MaxPositionUSDT:            0, // 由 CrossVenueExecutorConfig 注入;勿独立默认 500
		DefaultReserveUSDT:         10,
		ContinuationCloseThreshold: 0.001,
		ContinuationMinHoldSeconds: 3600,
	}
}

// RotationConfigFromUnified 用 unified_rotation + 执行层限额组装轮动配置(单仓上限与 exec 对齐)。
func RotationConfigFromUnified(ur core.UnifiedRotationConfig, exec CrossVenueExecutorConfig) RotationConfig {
	cfg := DefaultRotationConfig()
	if exec.MaxPositionUSDT > 0 {
		cfg.MaxPositionUSDT = exec.MaxPositionUSDT
	}
	if ur.ContinuationCloseThreshold > 0 {
		cfg.ContinuationCloseThreshold = ur.ContinuationCloseThreshold
	}
	if ur.ContinuationMinHoldSeconds > 0 {
		cfg.ContinuationMinHoldSeconds = ur.ContinuationMinHoldSeconds
	}
	if ur.TotalDeployableUSDT > 0 {
		cfg.TotalDeployableUSDT = ur.TotalDeployableUSDT
	}
	if ur.TotalDeployablePct > 0 {
		cfg.TotalDeployablePct = ur.TotalDeployablePct
	}
	if ur.MaxStrategyCapitalUsagePct > 0 {
		cfg.MaxStrategyCapitalUsagePct = ur.MaxStrategyCapitalUsagePct
	}
	return cfg
}

// HeldPosition 是当前持仓的最小语义，用于跨窗口续持决策。
type HeldPosition struct {
	OpportunityID          string             `json:"opportunity_id"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue"`
	OpenedAt               time.Time          `json:"opened_at,omitempty"`
}

// ContinuationInputs 是下一结算窗口续持口径的纯函数输入(由调用方提供)。
type ContinuationInputs struct {
	ExpectedFundingUSDT    float64 `json:"expected_funding_usdt"`
	ExpectedCloseCostUSDT  float64 `json:"expected_close_cost_usdt"`
	HoldingRiskUSDT        float64 `json:"holding_risk_usdt"`
	ExpectedBorrowCostUSDT float64 `json:"expected_borrow_cost_usdt"`
}

// RotationAction 是单条 shadow 轮动动作(不驱动下单)。
type RotationAction struct {
	Kind                   string             `json:"kind"`
	OpportunityID          string             `json:"opportunity_id"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	Score                  float64            `json:"score"`
	Reason                 string             `json:"reason"`
	ReplaceOpportunityID   string             `json:"replace_opportunity_id,omitempty"`
	ReplaceStrategyType    string             `json:"replace_strategy_type,omitempty"`
	ReplaceSymbol          string             `json:"replace_symbol,omitempty"`
	ReplaceScore           float64            `json:"replace_score,omitempty"`
	SwitchCostUSDT         float64            `json:"switch_cost_usdt,omitempty"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue,omitempty"`
}

// RotationPlan 是单轮 shadow 轮动计划输出。
type RotationPlan struct {
	Actions []RotationAction `json:"actions"`
}

func normalizeRotationConfig(cfg RotationConfig) RotationConfig {
	def := DefaultRotationConfig()
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = def.MaxConcurrent
	}
	// MaxPositionUSDT 须由 RotationConfigFromUnified(exec) 注入;零值表示不单独设单仓闸。
	if cfg.DefaultReserveUSDT <= 0 && len(cfg.ReserveUSDTByVenue) == 0 {
		cfg.DefaultReserveUSDT = def.DefaultReserveUSDT
	}
	if cfg.ContinuationCloseThreshold <= 0 {
		cfg.ContinuationCloseThreshold = def.ContinuationCloseThreshold
	}
	if cfg.ContinuationMinHoldSeconds <= 0 {
		cfg.ContinuationMinHoldSeconds = def.ContinuationMinHoldSeconds
	}
	return cfg
}

func rotationReserveUSDT(cfg RotationConfig, venue string) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venue]; ok {
			return v
		}
	}
	return cfg.DefaultReserveUSDT
}

func rotationEffectiveAvailable(available map[string]float64, cfg RotationConfig, venue string) float64 {
	return available[venue] - rotationReserveUSDT(cfg, venue)
}

func rotationMaxLegNotional(opp UnifiedOpportunity) float64 {
	maxNotional := 0.0
	for _, leg := range opp.Legs {
		if leg.Notional > maxNotional {
			maxNotional = leg.Notional
		}
	}
	return maxNotional
}

func rotationPositionNotionalUSDT(opp UnifiedOpportunity, totalCapital float64) float64 {
	if n := rotationMaxLegNotional(opp); n > 0 {
		return n
	}
	return totalCapital
}

// unifiedWindowScore 返回本窗口预期净 USDT / 占用资金(不做年化)。
func unifiedWindowScore(opp UnifiedOpportunity) float64 {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	if totalCapital <= 0 {
		return 0
	}
	return opp.ExpectedNetEVUSDT / totalCapital
}

// continuationNetUSDT 计算续持净收益(下一窗口 funding − borrow − holdingRisk; close 仅在换仓边际扣)。
func continuationNetUSDT(in ContinuationInputs) float64 {
	return in.ExpectedFundingUSDT - in.HoldingRiskUSDT - in.ExpectedBorrowCostUSDT
}

func rotationHoldingRiskUSDT(opp UnifiedOpportunity, totalCapital float64) float64 {
	if totalCapital <= 0 || opp.RiskScore <= 0 {
		return 0
	}
	return opp.RiskScore * totalCapital * 0.01
}

// rotationComparableWindowScore 与续持分同口径: netEV − holdingRisk,不含 close(已在 netEV)与 openCost(已在 netEV)。
func rotationComparableWindowScore(opp UnifiedOpportunity) float64 {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	if totalCapital <= 0 {
		return 0
	}
	return (opp.ExpectedNetEVUSDT - rotationHoldingRiskUSDT(opp, totalCapital)) / totalCapital
}

// continuationScore 返回续持净 / 占用资金(不做年化)。
func continuationScore(pos HeldPosition, nextWindow ContinuationInputs) float64 {
	totalCapital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	if totalCapital <= 0 {
		return 0
	}
	return continuationNetUSDT(nextWindow) / totalCapital
}

func unifiedOpportunityTotalCapitalFromMap(capital map[string]float64) float64 {
	total := 0.0
	for _, amt := range capital {
		if amt > 0 {
			total += amt
		}
	}
	return total
}

// rotationScannerSoftRejectReasons 是 scanner 侧软标记: 决策层另有等价检查, 不单独阻断 OPEN。
var rotationScannerSoftRejectReasons = map[string]struct{}{
	"requires_rebalance": {},
	"net_ev<=0":          {},
}

// rotationFirstHardRejectReason 返回候选上第一个 scanner 硬 reject 原因(无则空串)。
func rotationFirstHardRejectReason(opp UnifiedOpportunity) string {
	for _, reason := range opp.RejectReasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		if _, soft := rotationScannerSoftRejectReasons[reason]; soft {
			continue
		}
		return reason
	}
	return ""
}

// rotationBudgetState 跟踪跨策略全局/单策略资金占用(held + 本轮已选 open/switch)。
type rotationBudgetState struct {
	totalLimitUSDT    float64
	strategyLimitUSDT float64
	globalUsedUSDT    float64
	usedByStrategy    map[string]float64
}

func rotationBudgetEnabled(cfg RotationConfig) bool {
	return cfg.TotalDeployableUSDT > 0 || cfg.TotalDeployablePct > 0 || cfg.MaxStrategyCapitalUsagePct > 0
}

func rotationComputeDeployableBase(freeAvailable map[string]float64, held []HeldPosition, cfg RotationConfig) float64 {
	total := 0.0
	for venue := range freeAvailable {
		if eff := rotationEffectiveAvailable(freeAvailable, cfg, venue); eff > 0 {
			total += eff
		}
	}
	for _, pos := range held {
		total += unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	}
	return total
}

func newRotationBudgetState(freeAvailable map[string]float64, held []HeldPosition, cfg RotationConfig, equityUSDT float64) rotationBudgetState {
	st := rotationBudgetState{usedByStrategy: make(map[string]float64)}
	if !rotationBudgetEnabled(cfg) {
		return st
	}
	st.totalLimitUSDT = rotationComputeDeployableBase(freeAvailable, held, cfg)
	deployableCap := effectiveTotalDeployableUSDT(cfg.TotalDeployableUSDT, cfg.TotalDeployablePct, equityUSDT)
	if deployableCap > 0 && deployableCap < st.totalLimitUSDT {
		st.totalLimitUSDT = deployableCap
	}
	if cfg.MaxStrategyCapitalUsagePct > 0 {
		st.strategyLimitUSDT = st.totalLimitUSDT * cfg.MaxStrategyCapitalUsagePct
	}
	for _, pos := range held {
		st.addHeld(pos)
	}
	return st
}

func (st *rotationBudgetState) addHeld(pos HeldPosition) {
	if st.totalLimitUSDT <= 0 {
		return
	}
	capital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	st.globalUsedUSDT += capital
	st.usedByStrategy[pos.StrategyType] += capital
}

func (st *rotationBudgetState) releaseHeld(pos HeldPosition) {
	if st.totalLimitUSDT <= 0 {
		return
	}
	capital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)
	st.globalUsedUSDT -= capital
	if st.globalUsedUSDT < 0 {
		st.globalUsedUSDT = 0
	}
	st.usedByStrategy[pos.StrategyType] -= capital
	if st.usedByStrategy[pos.StrategyType] < 0 {
		st.usedByStrategy[pos.StrategyType] = 0
	}
}

func (st *rotationBudgetState) addOpportunity(opp UnifiedOpportunity) {
	if st.totalLimitUSDT <= 0 {
		return
	}
	capital := unifiedOpportunityTotalCapital(opp)
	st.globalUsedUSDT += capital
	st.usedByStrategy[opp.StrategyType] += capital
}

func (st rotationBudgetState) cloneForReplace(pos HeldPosition) rotationBudgetState {
	out := rotationBudgetState{
		totalLimitUSDT:    st.totalLimitUSDT,
		strategyLimitUSDT: st.strategyLimitUSDT,
		globalUsedUSDT:    st.globalUsedUSDT,
		usedByStrategy:    make(map[string]float64, len(st.usedByStrategy)),
	}
	for k, v := range st.usedByStrategy {
		out.usedByStrategy[k] = v
	}
	out.releaseHeld(pos)
	return out
}

func rotationBudgetBlockReason(st *rotationBudgetState, opp UnifiedOpportunity) string {
	if st == nil || st.totalLimitUSDT <= 0 {
		return ""
	}
	need := unifiedOpportunityTotalCapital(opp)
	if need <= 0 {
		return ""
	}
	if st.globalUsedUSDT+need > st.totalLimitUSDT+1e-9 {
		return "global_budget_exceeded"
	}
	if st.strategyLimitUSDT > 0 && st.usedByStrategy[opp.StrategyType]+need > st.strategyLimitUSDT+1e-9 {
		return "strategy_cap_exceeded"
	}
	return ""
}

func rotationCanOpenOpportunity(opp UnifiedOpportunity, available map[string]float64, cfg RotationConfig, openCount int, budget *rotationBudgetState) (bool, string) {
	if opp.CandidateOnly {
		return false, "candidate_only"
	}
	if hardReject := rotationFirstHardRejectReason(opp); hardReject != "" {
		return false, fmt.Sprintf("reject:%s", hardReject)
	}
	if opp.ExpectedNetEVUSDT <= 0 {
		return false, "net_ev<=0"
	}
	if openCount >= cfg.MaxConcurrent {
		return false, "max_concurrent_reached"
	}
	totalCapital := unifiedOpportunityTotalCapital(opp)
	if totalCapital <= 0 {
		return false, "capital_required<=0"
	}
	notional := rotationPositionNotionalUSDT(opp, totalCapital)
	if cfg.MaxPositionUSDT > 0 && notional > cfg.MaxPositionUSDT+1e-9 {
		return false, "max_position_usdt_exceeded"
	}
	for venue, need := range opp.CapitalRequiredByVenue {
		if need <= 0 {
			continue
		}
		if rotationEffectiveAvailable(available, cfg, venue)+1e-9 < need {
			if opp.RequiresRebalance {
				return false, "requires_rebalance_unfundable"
			}
			return false, fmt.Sprintf("venue_balance_insufficient:%s", venue)
		}
	}
	if reason := rotationBudgetBlockReason(budget, opp); reason != "" {
		return false, reason
	}
	return true, ""
}

func rotationDeductOpportunityCapital(available map[string]float64, opp UnifiedOpportunity) {
	for venue, need := range opp.CapitalRequiredByVenue {
		if need > 0 {
			available[venue] -= need
		}
	}
}

func rotationAddCapitalMap(available map[string]float64, capital map[string]float64) {
	for venue, amt := range capital {
		if amt > 0 {
			available[venue] += amt
		}
	}
}

func rotationSortedPositiveCandidates(candidates []UnifiedOpportunity) []UnifiedOpportunity {
	positive := make([]UnifiedOpportunity, 0, len(candidates))
	for _, opp := range candidates {
		if opp.ExpectedNetEVUSDT > 0 {
			positive = append(positive, opp)
		}
	}
	sort.SliceStable(positive, func(i, j int) bool {
		return rotationComparableWindowScore(positive[i]) > rotationComparableWindowScore(positive[j])
	})
	return positive
}

func planUnifiedRotationOpensFrom(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, startOpenCount int, budget rotationBudgetState) []RotationAction {
	cfg = normalizeRotationConfig(cfg)
	funds := cloneVenueCapitalMap(available)
	sorted := rotationSortedPositiveCandidates(candidates)

	actions := make([]RotationAction, 0, len(sorted))
	openCount := startOpenCount
	for _, opp := range sorted {
		ok, _ := rotationCanOpenOpportunity(opp, funds, cfg, openCount, &budget)
		if !ok {
			continue
		}
		score := rotationComparableWindowScore(opp)
		rotationDeductOpportunityCapital(funds, opp)
		budget.addOpportunity(opp)
		openCount++
		actions = append(actions, RotationAction{
			Kind:                   RotationActionOpen,
			OpportunityID:          opp.ID,
			StrategyType:           opp.StrategyType,
			Symbol:                 opp.Symbol,
			Score:                  score,
			Reason:                 fmt.Sprintf("open_window_score=%.6f", score),
			CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(opp.CapitalRequiredByVenue),
		})
	}
	return actions
}

// PlanUnifiedRotationOpens 对开仓侧做 shadow 贪心分配(纯函数,不执行下单)。
func PlanUnifiedRotationOpens(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, equityUSDT ...float64) []RotationAction {
	equity := 0.0
	if len(equityUSDT) > 0 {
		equity = equityUSDT[0]
	}
	budget := newRotationBudgetState(available, nil, cfg, equity)
	return planUnifiedRotationOpensFrom(candidates, available, cfg, 0, budget)
}

func rotationBestReplaceCandidate(
	held HeldPosition,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	openCount int,
	alreadyChosen map[string]struct{},
	budget *rotationBudgetState,
) (UnifiedOpportunity, float64, bool) {
	bestScore := -1.0
	var best UnifiedOpportunity
	found := false
	for _, opp := range candidates {
		if opp.ExpectedNetEVUSDT <= 0 {
			continue
		}
		if opp.ID == held.OpportunityID {
			continue
		}
		if alreadyChosen != nil {
			if _, ok := alreadyChosen[opp.ID]; ok {
				continue
			}
		}
		ok, _ := rotationCanOpenOpportunity(opp, available, cfg, openCount, budget)
		if !ok {
			continue
		}
		score := rotationComparableWindowScore(opp)
		if !found || score > bestScore {
			best = opp
			bestScore = score
			found = true
		}
	}
	return best, bestScore, found
}

// rotationSwitchMarginalCostUSDT 换仓边际成本: 平旧 close + 开新 slippage/rollback(openCost 已在 netEV)。
func rotationSwitchMarginalCostUSDT(heldCloseCostUSDT float64, newOpp UnifiedOpportunity) float64 {
	return heldCloseCostUSDT + newOpp.ExpectedSlippageUSDT + newOpp.ExpectedRollbackCostUSDT
}

// rotationShouldContinuationClose 判断是否因续持边际转负而软 CLOSE(含 hysteresis/min-hold/关仓费对比)。
func rotationShouldContinuationClose(pos HeldPosition, inputs ContinuationInputs, cfg RotationConfig, now time.Time) (bool, string) {
	contNet := continuationNetUSDT(inputs)
	contScore := continuationScore(pos, inputs)
	closeCost := inputs.ExpectedCloseCostUSDT
	if closeCost <= 0 {
		closeCost = 0.01
	}
	if contNet >= -closeCost {
		return false, ""
	}
	if contScore >= -cfg.ContinuationCloseThreshold {
		return false, ""
	}
	minHold := time.Duration(cfg.ContinuationMinHoldSeconds) * time.Second
	if !pos.OpenedAt.IsZero() && minHold > 0 && now.Sub(pos.OpenedAt) < minHold {
		return false, ""
	}
	return true, fmt.Sprintf("continuation_net=%.4f<-close_cost=%.4f score=%.6f", contNet, closeCost, contScore)
}

// DecideRotation 生成跨窗口 HOLD/CLOSE/SWITCH/OPEN shadow 计划(纯函数,不执行下单)。
func DecideRotation(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	equityUSDT ...float64,
) RotationPlan {
	equity := 0.0
	if len(equityUSDT) > 0 {
		equity = equityUSDT[0]
	}
	return decideRotationAt(held, heldInputs, candidates, available, cfg, time.Now(), equity)
}

func decideRotationAt(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	now time.Time,
	equityUSDT float64,
) RotationPlan {
	cfg = normalizeRotationConfig(cfg)
	funds := cloneVenueCapitalMap(available)
	budget := newRotationBudgetState(available, held, cfg, equityUSDT)

	plan := RotationPlan{}
	openCount := len(held)
	chosen := make(map[string]struct{}, len(held))
	heldIDs := make(map[string]struct{}, len(held))
	rejectedReplace := make(map[string]struct{})

	for _, pos := range held {
		heldIDs[pos.OpportunityID] = struct{}{}
	}

	for _, pos := range held {
		inputs := heldInputs[pos.OpportunityID]
		contScore := continuationScore(pos, inputs)
		heldCapital := unifiedOpportunityTotalCapitalFromMap(pos.CapitalRequiredByVenue)

		if closeOK, closeReason := rotationShouldContinuationClose(pos, inputs, cfg, now); closeOK {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionClose,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 closeReason,
				CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(pos.CapitalRequiredByVenue),
			})
			rotationAddCapitalMap(funds, pos.CapitalRequiredByVenue)
			budget.releaseHeld(pos)
			openCount--
			continue
		}

		replaceFunds := cloneVenueCapitalMap(funds)
		rotationAddCapitalMap(replaceFunds, pos.CapitalRequiredByVenue)
		replaceBudget := budget.cloneForReplace(pos)
		alreadyChosen := make(map[string]struct{}, len(chosen)+len(rejectedReplace)+1)
		for id := range chosen {
			alreadyChosen[id] = struct{}{}
		}
		for id := range rejectedReplace {
			alreadyChosen[id] = struct{}{}
		}
		for id := range heldIDs {
			alreadyChosen[id] = struct{}{}
		}
		best, openScore, found := rotationBestReplaceCandidate(pos, candidates, replaceFunds, cfg, openCount, alreadyChosen, &replaceBudget)
		if !found {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionHold,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 fmt.Sprintf("hold_no_replace_candidate continuation_score=%.6f", contScore),
				CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(pos.CapitalRequiredByVenue),
			})
			chosen[pos.OpportunityID] = struct{}{}
			continue
		}

		switchCost := rotationSwitchMarginalCostUSDT(inputs.ExpectedCloseCostUSDT, best)
		switchCostPerUnit := 0.0
		if heldCapital > 0 {
			switchCostPerUnit = switchCost / heldCapital
		}
		adjustedOpenScore := openScore - switchCostPerUnit

		if contScore >= adjustedOpenScore {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionHold,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 fmt.Sprintf("hold continuation_score=%.6f >= adjusted_open_score=%.6f", contScore, adjustedOpenScore),
				ReplaceOpportunityID:   best.ID,
				ReplaceStrategyType:    best.StrategyType,
				ReplaceSymbol:          best.Symbol,
				ReplaceScore:           openScore,
				SwitchCostUSDT:         switchCost,
				CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(pos.CapitalRequiredByVenue),
			})
			chosen[pos.OpportunityID] = struct{}{}
			rejectedReplace[best.ID] = struct{}{}
			continue
		}

		plan.Actions = append(plan.Actions, RotationAction{
			Kind:                   RotationActionSwitch,
			OpportunityID:          pos.OpportunityID,
			StrategyType:           pos.StrategyType,
			Symbol:                 pos.Symbol,
			Score:                  contScore,
			Reason:                 fmt.Sprintf("switch continuation_score=%.6f < adjusted_open_score=%.6f", contScore, adjustedOpenScore),
			ReplaceOpportunityID:   best.ID,
			ReplaceStrategyType:    best.StrategyType,
			ReplaceSymbol:          best.Symbol,
			ReplaceScore:           openScore,
			SwitchCostUSDT:         switchCost,
			CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(best.CapitalRequiredByVenue),
		})
		rotationAddCapitalMap(funds, pos.CapitalRequiredByVenue)
		rotationDeductOpportunityCapital(funds, best)
		budget.releaseHeld(pos)
		budget.addOpportunity(best)
		chosen[best.ID] = struct{}{}
	}

	remaining := make([]UnifiedOpportunity, 0, len(candidates))
	for _, opp := range candidates {
		if _, ok := chosen[opp.ID]; ok {
			continue
		}
		if _, ok := heldIDs[opp.ID]; ok {
			continue
		}
		if _, ok := rejectedReplace[opp.ID]; ok {
			continue
		}
		remaining = append(remaining, opp)
	}

	currentOpenCount := 0
	for _, act := range plan.Actions {
		switch act.Kind {
		case RotationActionHold, RotationActionSwitch:
			currentOpenCount++
		}
	}
	plan.Actions = append(plan.Actions, planUnifiedRotationOpensFrom(remaining, funds, cfg, currentOpenCount, budget)...)

	return plan
}
