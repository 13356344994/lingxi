package app

import (
	"fmt"
	"sort"
)

const (
	RotationActionHold   = "HOLD"
	RotationActionClose  = "CLOSE"
	RotationActionSwitch = "SWITCH"
	RotationActionOpen   = "OPEN"
)

// RotationConfig 是统一资金费轮动 shadow 计划的限额与保留金配置。
type RotationConfig struct {
	MaxConcurrent        int                `json:"max_concurrent"`
	MaxPositionUSDT      float64            `json:"max_position_usdt"`
	ReserveUSDTByVenue   map[string]float64 `json:"reserve_usdt_by_venue"`
	DefaultReserveUSDT   float64            `json:"default_reserve_usdt"`
}

// DefaultRotationConfig 返回 v1 保守默认轮动配置。
func DefaultRotationConfig() RotationConfig {
	return RotationConfig{
		MaxConcurrent:      3,
		MaxPositionUSDT:    500,
		DefaultReserveUSDT: 10,
	}
}

// HeldPosition 是当前持仓的最小语义，用于跨窗口续持决策。
type HeldPosition struct {
	OpportunityID          string             `json:"opportunity_id"`
	StrategyType           string             `json:"strategy_type"`
	Symbol                 string             `json:"symbol"`
	CapitalRequiredByVenue map[string]float64 `json:"capital_required_by_venue"`
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
	if cfg.MaxPositionUSDT <= 0 {
		cfg.MaxPositionUSDT = def.MaxPositionUSDT
	}
	if cfg.DefaultReserveUSDT <= 0 && len(cfg.ReserveUSDTByVenue) == 0 {
		cfg.DefaultReserveUSDT = def.DefaultReserveUSDT
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

// continuationNetUSDT 计算续持净收益(不含沉没的开仓成本与入场基差)。
func continuationNetUSDT(in ContinuationInputs) float64 {
	return in.ExpectedFundingUSDT - in.ExpectedCloseCostUSDT - in.HoldingRiskUSDT - in.ExpectedBorrowCostUSDT
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

func rotationCanOpenOpportunity(opp UnifiedOpportunity, available map[string]float64, cfg RotationConfig, openCount int) (bool, string) {
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
			return false, fmt.Sprintf("venue_balance_insufficient:%s", venue)
		}
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
		return unifiedWindowScore(positive[i]) > unifiedWindowScore(positive[j])
	})
	return positive
}

func planUnifiedRotationOpensFrom(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, startOpenCount int) []RotationAction {
	cfg = normalizeRotationConfig(cfg)
	funds := cloneVenueCapitalMap(available)
	sorted := rotationSortedPositiveCandidates(candidates)

	actions := make([]RotationAction, 0, len(sorted))
	openCount := startOpenCount
	for _, opp := range sorted {
		ok, _ := rotationCanOpenOpportunity(opp, funds, cfg, openCount)
		if !ok {
			continue
		}
		score := unifiedWindowScore(opp)
		rotationDeductOpportunityCapital(funds, opp)
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
func PlanUnifiedRotationOpens(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig) []RotationAction {
	return planUnifiedRotationOpensFrom(candidates, available, cfg, 0)
}

func rotationBestReplaceCandidate(
	held HeldPosition,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	openCount int,
	exclude map[string]struct{},
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
		if exclude != nil {
			if _, ok := exclude[opp.ID]; ok {
				continue
			}
		}
		ok, _ := rotationCanOpenOpportunity(opp, available, cfg, openCount)
		if !ok {
			continue
		}
		score := unifiedWindowScore(opp)
		if !found || score > bestScore {
			best = opp
			bestScore = score
			found = true
		}
	}
	return best, bestScore, found
}

func rotationSwitchCostUSDT(heldCloseCost, newOpenCost float64) float64 {
	return heldCloseCost + newOpenCost
}

// DecideRotation 生成跨窗口 HOLD/CLOSE/SWITCH/OPEN shadow 计划(纯函数,不执行下单)。
func DecideRotation(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
) RotationPlan {
	cfg = normalizeRotationConfig(cfg)
	funds := cloneVenueCapitalMap(available)

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

		if contScore < 0 {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionClose,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 fmt.Sprintf("continuation_score<0 (%.6f)", contScore),
				CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(pos.CapitalRequiredByVenue),
			})
			rotationAddCapitalMap(funds, pos.CapitalRequiredByVenue)
			openCount--
			continue
		}

		replaceFunds := cloneVenueCapitalMap(funds)
		rotationAddCapitalMap(replaceFunds, pos.CapitalRequiredByVenue)
		best, openScore, found := rotationBestReplaceCandidate(pos, candidates, replaceFunds, cfg, openCount, heldIDs)
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

		switchCost := rotationSwitchCostUSDT(inputs.ExpectedCloseCostUSDT, best.ExpectedOpenCostUSDT)
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
	plan.Actions = append(plan.Actions, planUnifiedRotationOpensFrom(remaining, funds, cfg, currentOpenCount)...)

	return plan
}
