package app

import (
	"fmt"
	"math"
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

// RotationConfig 是统一资金费轮动 计划的限额与保留金配置。
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
	// MinHoldToFirstSettlementEnabled 开仓后须持有至首个 intended funding 结算(+缓冲)才允许软 CLOSE/SWITCH。默认 true。
	MinHoldToFirstSettlementEnabled bool `json:"min_hold_to_first_settlement_enabled"`
	// MinHoldToFirstSettlementBufferSeconds 首个 funding 结算后额外缓冲秒数。默认 60。
	MinHoldToFirstSettlementBufferSeconds int `json:"min_hold_to_first_settlement_buffer_seconds"`
	// MinNetProfitUSDT 开仓预期净收益下限(USDT);由 CrossVenueExecutor 注入。默认 0。
	MinNetProfitUSDT float64 `json:"min_net_profit_usdt"`
	// MinOpenNotionalUSDT 候选可成交名义下限(USDT);低于此值丢弃,避免薄仓占并发槽。默认 5。
	MinOpenNotionalUSDT float64 `json:"min_open_notional_usdt"`
	// OpenFailCooldownSeconds 同一 (strategy,symbol,venue-pair) 开仓失败后冷却秒数。默认 900(15 分钟)。
	OpenFailCooldownSeconds int `json:"open_fail_cooldown_seconds"`
	// OpenCooldownBlocked 运行时注入:按机会粒度检查开仓失败冷却(nil=不检查)。
	OpenCooldownBlocked func(opp UnifiedOpportunity) bool `json:"-"`
	// CapitalAwarePerpPerpSizingEnabled perp-perp 按 venue 可用×杠杆封顶名义;不足则缩仓。默认 true。
	CapitalAwarePerpPerpSizingEnabled bool `json:"capital_aware_perp_perp_sizing_enabled"`
	// CapitalSizingSafetyHaircut perp-perp 资金封顶安全垫(0,1];默认 0.99,避免 sizing 顶满可用后被执行闸几分钱拒单。
	CapitalSizingSafetyHaircut float64 `json:"capital_sizing_safety_haircut"`
}

// DefaultRotationConfig 返回 v1 保守默认轮动配置。
func DefaultRotationConfig() RotationConfig {
	return RotationConfig{
		MaxConcurrent:                         3,
		MaxPositionUSDT:                       0, // 由 CrossVenueExecutorConfig 注入;勿独立默认 500
		DefaultReserveUSDT:                    10,
		ContinuationCloseThreshold:            0.001,
		ContinuationMinHoldSeconds:            3600,
		MinHoldToFirstSettlementEnabled:       true,
		MinHoldToFirstSettlementBufferSeconds: 60,
		MinOpenNotionalUSDT:                   5,
		OpenFailCooldownSeconds:               900,
		CapitalAwarePerpPerpSizingEnabled:     true,
		CapitalSizingSafetyHaircut:            0.99,
	}
}

// RotationConfigFromUnified 用 unified_rotation + 执行层限额组装轮动配置(单仓上限与 exec 对齐)。
func RotationConfigFromUnified(ur core.UnifiedRotationConfig, exec CrossVenueExecutorConfig) RotationConfig {
	cfg := DefaultRotationConfig()
	if exec.MaxPositionUSDT > 0 {
		cfg.MaxPositionUSDT = exec.MaxPositionUSDT
	}
	if exec.MaxConcurrent > 0 {
		cfg.MaxConcurrent = exec.MaxConcurrent
	}
	if ur.ContinuationCloseThreshold > 0 {
		cfg.ContinuationCloseThreshold = ur.ContinuationCloseThreshold
	}
	if ur.ContinuationMinHoldSeconds > 0 {
		cfg.ContinuationMinHoldSeconds = ur.ContinuationMinHoldSeconds
	}
	cfg.MinHoldToFirstSettlementEnabled = DefaultRotationConfig().MinHoldToFirstSettlementEnabled
	if ur != (core.UnifiedRotationConfig{}) {
		cfg.MinHoldToFirstSettlementEnabled = ur.MinHoldToFirstSettlementEnabled
	}
	if ur.MinHoldToFirstSettlementBufferSeconds > 0 {
		cfg.MinHoldToFirstSettlementBufferSeconds = ur.MinHoldToFirstSettlementBufferSeconds
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
	cfg.MinNetProfitUSDT = crossVenueMinNetProfitUSDT(exec)
	cfg.CapitalAwarePerpPerpSizingEnabled = ur.CapitalAwarePerpPerpSizingEnabled
	if !cfg.CapitalAwarePerpPerpSizingEnabled && ur == (core.UnifiedRotationConfig{}) {
		cfg.CapitalAwarePerpPerpSizingEnabled = DefaultRotationConfig().CapitalAwarePerpPerpSizingEnabled
	}
	if ur.CapitalSizingSafetyHaircut > 0 && ur.CapitalSizingSafetyHaircut <= 1 {
		cfg.CapitalSizingSafetyHaircut = ur.CapitalSizingSafetyHaircut
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
	// NextFundingTime 最近一侧永续下次结算时刻;续持 min-hold 参考距结算剩余时间。
	NextFundingTime time.Time `json:"next_funding_time,omitempty"`
	// TargetFirstFundingTime 开仓时记录的首个 intended funding 结算时刻(多腿取最早);不随 scanner 刷新。
	TargetFirstFundingTime time.Time `json:"target_first_funding_time,omitempty"`
}

// ContinuationInputs 是下一结算窗口续持口径的纯函数输入(由调用方提供)。
type ContinuationInputs struct {
	ExpectedFundingUSDT    float64 `json:"expected_funding_usdt"`
	ExpectedCloseCostUSDT  float64 `json:"expected_close_cost_usdt"`
	HoldingRiskUSDT        float64 `json:"holding_risk_usdt"`
	ExpectedBorrowCostUSDT float64 `json:"expected_borrow_cost_usdt"`
}

// RotationAction 是单条 轮动动作(不驱动下单)。
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

// RotationPlan 是单轮 轮动计划输出。
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
	if cfg.MinOpenNotionalUSDT <= 0 {
		cfg.MinOpenNotionalUSDT = def.MinOpenNotionalUSDT
	}
	if cfg.OpenFailCooldownSeconds <= 0 {
		cfg.OpenFailCooldownSeconds = def.OpenFailCooldownSeconds
	}
	if cfg.MinHoldToFirstSettlementBufferSeconds <= 0 {
		cfg.MinHoldToFirstSettlementBufferSeconds = def.MinHoldToFirstSettlementBufferSeconds
	}
	if cfg.CapitalSizingSafetyHaircut <= 0 || cfg.CapitalSizingSafetyHaircut > 1 {
		cfg.CapitalSizingSafetyHaircut = def.CapitalSizingSafetyHaircut
	}
	return cfg
}

func rotationCapitalSizingSafetyHaircut(cfg RotationConfig) float64 {
	cfg = normalizeRotationConfig(cfg)
	return cfg.CapitalSizingSafetyHaircut
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

// rotationPerpPerpMaxNotionalFromVenueAvail 由单腿可用资金反解 perp-perp 等额名义上限。
// 约束 rotationPerpMarginWithBuffer(notional) ≤ avail,即 notional×(1/lev+buffer) ≤ avail。
// rotationPerpPerpMaxNotionalFromVenueAvail 按执行闸同口径反推最大名义:路由后 perp 钱包可凑齐 notional/lev。
// availAfterReserve 为 venue 总交易可用(含 spot,路由会搬进 perp);与 crossVenuePerVenueMarginUSDT 对齐。
// 乘 CapitalSizingSafetyHaircut 留安全垫,使所需保证金严格小于实时 perp 可用(避免 reserve/取整 drift 几分钱拒单)。
func rotationPerpPerpMaxNotionalFromVenueAvail(execCfg CrossVenueExecutorConfig, rotCfg RotationConfig, availAfterReserve float64) float64 {
	if availAfterReserve <= 0 {
		return 0
	}
	lev := execCfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	return availAfterReserve * lev * rotationCapitalSizingSafetyHaircut(rotCfg)
}

// rotationPerpPerpMaxNotionalByCapital 取 perp-perp 各腿 venue 扣储备后可用资金约束下的最大等额名义(min 绑定腿)。
func rotationPerpPerpMaxNotionalByCapital(execCfg CrossVenueExecutorConfig, rotCfg RotationConfig, availableUSDTByVenue map[string]float64, opp UnifiedOpportunity) float64 {
	maxN := math.MaxFloat64
	for _, leg := range opp.Legs {
		if leg.Venue == "" {
			continue
		}
		avail := rotationEffectiveAvailable(availableUSDTByVenue, rotCfg, leg.Venue)
		capN := rotationPerpPerpMaxNotionalFromVenueAvail(execCfg, rotCfg, avail)
		if capN < maxN {
			maxN = capN
		}
	}
	if maxN == math.MaxFloat64 {
		return 0
	}
	return maxN
}

// rotationScalePerpPerpOpportunityNotional 按 ratio 等比缩放 perp-perp 名义与线性 EV/成本字段(不含 borrow/rollback)。
func rotationScalePerpPerpOpportunityNotional(opp *UnifiedOpportunity, ratio float64) {
	if opp == nil || ratio <= 0 || math.Abs(ratio-1) <= 1e-9 {
		return
	}
	for i := range opp.Legs {
		opp.Legs[i].Notional *= ratio
		opp.Legs[i].Qty *= ratio
	}
	opp.ExpectedFundingUSDT *= ratio
	opp.ExpectedOpenCostUSDT *= ratio
	opp.ExpectedCloseCostUSDT *= ratio
	opp.ExpectedSlippageUSDT *= ratio
	opp.ExpectedNetEVUSDT *= ratio
}

// rotationCapPerpPerpOpportunityByCapital 按各所总可用×杠杆封顶 perp-perp 名义(路由后 perp 可补足);资金不够则缩仓而非留给执行闸误拒。
func rotationCapPerpPerpOpportunityByCapital(opp UnifiedOpportunity, execCfg CrossVenueExecutorConfig, rotCfg RotationConfig, availableUSDTByVenue map[string]float64) UnifiedOpportunity {
	if opp.StrategyType != UnifiedStrategyPerpPerp {
		return opp
	}
	originalNotional := rotationMaxLegNotional(opp)
	if originalNotional <= 0 {
		return rotationAlignPerpPerpCapital(opp, execCfg)
	}
	maxByCapital := rotationPerpPerpMaxNotionalByCapital(execCfg, rotCfg, availableUSDTByVenue, opp)
	cappedNotional := originalNotional
	if maxByCapital >= 0 && maxByCapital < originalNotional {
		cappedNotional = maxByCapital
	}
	if cappedNotional <= 0 {
		for i := range opp.Legs {
			opp.Legs[i].Notional = 0
			opp.Legs[i].Qty = 0
		}
		return rotationAlignPerpPerpCapital(opp, execCfg)
	}
	if cappedNotional+1e-9 < originalNotional {
		rotationScalePerpPerpOpportunityNotional(&opp, cappedNotional/originalNotional)
	}
	return rotationAlignPerpPerpCapital(opp, execCfg)
}

func rotationPositionNotionalUSDT(opp UnifiedOpportunity, totalCapital float64) float64 {
	if n := rotationMaxLegNotional(opp); n > 0 {
		return n
	}
	return totalCapital
}

// unifiedPrimaryScore 全链主排序口径：绝对净收益(USDT)。
func unifiedPrimaryScore(opp UnifiedOpportunity) float64 {
	return opp.ExpectedNetEVUSDT
}

// unifiedWindowScore 返回本窗口预期净 USDT / 占用资金(资本效率,tiebreaker/日志展示)。
func unifiedWindowScore(opp UnifiedOpportunity) float64 {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	if totalCapital <= 0 {
		return 0
	}
	return opp.ExpectedNetEVUSDT / totalCapital
}

// rotationComparableAbsoluteScore 与续持净收益同量纲(USDT): netEV − holdingRisk。
func rotationComparableAbsoluteScore(opp UnifiedOpportunity) float64 {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	if totalCapital <= 0 {
		return 0
	}
	return opp.ExpectedNetEVUSDT - rotationHoldingRiskUSDT(opp, totalCapital)
}

// rotationFundingRatePerHour 把 per-interval 绝对费率归一化为每小时(粗筛/排序/续持窗口折算共用)。
func rotationFundingRatePerHour(rate float64, intervalHours int) float64 {
	if intervalHours <= 0 {
		intervalHours = 8
	}
	abs := rate
	if abs < 0 {
		abs = -abs
	}
	return abs / float64(intervalHours)
}

// rotationSpotPerpWindowFundingUSDT 正/负 carry spot-perp 下一可比窗口 funding(与 scanner 候选同口径)。
func rotationSpotPerpWindowFundingUSDT(notional, rate float64, intervalHours int, nextFunding, now time.Time, negativeCarry bool) float64 {
	if notional <= 0 {
		return 0
	}
	effectiveRate := rate
	if negativeCarry {
		if rate >= 0 {
			return 0
		}
		effectiveRate = -rate
	} else if rate <= 0 {
		return 0
	}
	if intervalHours <= 0 {
		intervalHours = 8
	}
	windowHours := crossVenueComparableWindowHours(now, nextFunding, nextFunding)
	if windowHours <= 0 {
		windowHours = float64(intervalHours)
	}
	settlements := crossVenueSpotPerpFundingSettlementCount(now, windowHours, intervalHours, nextFunding)
	return notional * effectiveRate * float64(settlements)
}

// rotationPerpPerpWindowFundingUSDT perp-perp 下一可比窗口 spread funding(对齐 crossVenueComputeCurrentFutureEV)。
func rotationPerpPerpWindowFundingUSDT(notional, highRate float64, highInt int, lowRate float64, lowInt int, highNext, lowNext, now time.Time) float64 {
	windowHours := crossVenueComparableWindowHours(now, highNext, lowNext)
	if windowHours <= 0 {
		return 0
	}
	fundingPerHour := crossVenueExpectedFundingPerHour(notional, highRate, highInt, lowRate, lowInt)
	return fundingPerHour * windowHours
}

const (
	// rotationHoldingRiskPerScoreUnit 持仓尾部风险代理系数: riskScore∈[0,1] × capital × 1%。
	// 候选 comparable 分、续持 holdingRisk、allocator 风险调整 netEV 三处共用此口径。
	rotationHoldingRiskPerScoreUnit = 0.01
	// rotationContinuationDefaultRiskScore 无匹配候选时续持默认风险分(等价旧 notional×0.001)。
	rotationContinuationDefaultRiskScore = 0.1
	// rotationContinuationBorrowFallbackDailyRate 无借币探测时续持 borrow 回退日利率(3bps/日)。
	rotationContinuationBorrowFallbackDailyRate = 0.0003
)

// holdingRiskUSDT 统一持仓尾部风险扣减(USDT): clamp01(riskScore) × capital × rotationHoldingRiskPerScoreUnit。
func holdingRiskUSDT(riskScore, capital float64) float64 {
	if capital <= 0 || riskScore <= 0 {
		return 0
	}
	return clamp01(riskScore) * capital * rotationHoldingRiskPerScoreUnit
}

// continuationNetUSDT 计算续持净收益(下一窗口 funding − borrow − holdingRisk; close 仅在换仓边际扣)。
func continuationNetUSDT(in ContinuationInputs) float64 {
	return in.ExpectedFundingUSDT - in.HoldingRiskUSDT - in.ExpectedBorrowCostUSDT
}

func rotationHoldingRiskUSDT(opp UnifiedOpportunity, totalCapital float64) float64 {
	return holdingRiskUSDT(opp.RiskScore, totalCapital)
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

// RotationOpenBlocked 决策层用于排除暂不可开的 (strategy,symbol)。
type RotationOpenBlocked func(strategyType, symbol string) bool

func rotationOpportunityOpenBlocked(opp UnifiedOpportunity, blocked RotationOpenBlocked) bool {
	if blocked == nil {
		return false
	}
	return blocked(opp.StrategyType, opp.Symbol)
}

func rotationMinNetProfitUSDT(cfg RotationConfig) float64 {
	if cfg.MinNetProfitUSDT < 0 {
		return 0
	}
	return cfg.MinNetProfitUSDT
}

func rotationMinOpenNotionalUSDT(cfg RotationConfig) float64 {
	if cfg.MinOpenNotionalUSDT <= 0 {
		return 0
	}
	return cfg.MinOpenNotionalUSDT
}

func rotationOpportunityTradableNotional(opp UnifiedOpportunity) float64 {
	totalCapital := unifiedOpportunityTotalCapital(opp)
	return rotationPositionNotionalUSDT(opp, totalCapital)
}

func rotationCanOpenOpportunity(opp UnifiedOpportunity, available map[string]float64, cfg RotationConfig, openCount int, budget *rotationBudgetState, blocked RotationOpenBlocked) (bool, string) {
	if rotationOpportunityOpenBlocked(opp, blocked) {
		return false, "open_blocked"
	}
	if cfg.OpenCooldownBlocked != nil && cfg.OpenCooldownBlocked(opp) {
		return false, "open_cooldown"
	}
	if minNotional := rotationMinOpenNotionalUSDT(cfg); minNotional > 0 {
		if rotationOpportunityTradableNotional(opp)+1e-9 < minNotional {
			return false, "min_open_notional"
		}
	}
	if opp.CandidateOnly {
		return false, "candidate_only"
	}
	if !rotationOpportunityOpenEligibleForDecision(opp) {
		return false, carryScannerRejectFundingWindowNotReached
	}
	if hardReject := rotationFirstHardRejectReason(opp); hardReject != "" {
		return false, fmt.Sprintf("reject:%s", hardReject)
	}
	if opp.ExpectedNetEVUSDT <= rotationMinNetProfitUSDT(cfg) {
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

func rotationSortedPositiveCandidates(candidates []UnifiedOpportunity, cfg RotationConfig) []UnifiedOpportunity {
	minProfit := rotationMinNetProfitUSDT(cfg)
	positive := make([]UnifiedOpportunity, 0, len(candidates))
	for _, opp := range candidates {
		if opp.ExpectedNetEVUSDT > minProfit {
			positive = append(positive, opp)
		}
	}
	sort.SliceStable(positive, func(i, j int) bool {
		pi, pj := unifiedPrimaryScore(positive[i]), unifiedPrimaryScore(positive[j])
		if pi != pj {
			return pi > pj
		}
		ci, cj := unifiedWindowScore(positive[i]), unifiedWindowScore(positive[j])
		if ci != cj {
			return ci > cj
		}
		return positive[i].Symbol < positive[j].Symbol
	})
	return positive
}

func planUnifiedRotationOpensFrom(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, startOpenCount int, budget rotationBudgetState, blocked RotationOpenBlocked) []RotationAction {
	cfg = normalizeRotationConfig(cfg)
	funds := cloneVenueCapitalMap(available)
	sorted := rotationSortedPositiveCandidates(candidates, cfg)

	actions := make([]RotationAction, 0, len(sorted))
	openCount := startOpenCount
	for _, opp := range sorted {
		ok, _ := rotationCanOpenOpportunity(opp, funds, cfg, openCount, &budget, blocked)
		if !ok {
			continue
		}
		score := unifiedPrimaryScore(opp)
		capEff := unifiedWindowScore(opp)
		rotationDeductOpportunityCapital(funds, opp)
		budget.addOpportunity(opp)
		openCount++
		actions = append(actions, RotationAction{
			Kind:                   RotationActionOpen,
			OpportunityID:          opp.ID,
			StrategyType:           opp.StrategyType,
			Symbol:                 opp.Symbol,
			Score:                  score,
			Reason:                 fmt.Sprintf("open_net_ev=%.4f cap_eff=%.6f", score, capEff),
			CapitalRequiredByVenue: cloneVenueCapitalFromOpportunity(opp.CapitalRequiredByVenue),
		})
	}
	return actions
}

// PlanUnifiedRotationOpens 对开仓侧做 贪心分配(纯函数,不执行下单)。
func PlanUnifiedRotationOpens(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, equityUSDT ...float64) []RotationAction {
	return PlanUnifiedRotationOpensWithBlocked(candidates, available, cfg, nil, equityUSDT...)
}

func PlanUnifiedRotationOpensWithBlocked(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig, blocked RotationOpenBlocked, equityUSDT ...float64) []RotationAction {
	equity := 0.0
	if len(equityUSDT) > 0 {
		equity = equityUSDT[0]
	}
	budget := newRotationBudgetState(available, nil, cfg, equity)
	return planUnifiedRotationOpensFrom(candidates, available, cfg, 0, budget, blocked)
}

func rotationBestReplaceCandidate(
	held HeldPosition,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	openCount int,
	alreadyChosen map[string]struct{},
	budget *rotationBudgetState,
	blocked RotationOpenBlocked,
) (UnifiedOpportunity, float64, bool) {
	bestScore := -1.0
	var best UnifiedOpportunity
	found := false
	for _, opp := range candidates {
		if opp.ExpectedNetEVUSDT <= rotationMinNetProfitUSDT(cfg) {
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
		ok, _ := rotationCanOpenOpportunity(opp, available, cfg, openCount, budget, blocked)
		if !ok {
			continue
		}
		score := rotationComparableAbsoluteScore(opp)
		if !found || score > bestScore {
			best = opp
			bestScore = score
			found = true
		}
	}
	return best, bestScore, found
}

// rotationSwitchMarginalCostUSDT 换仓边际成本: 仅平旧仓 close(不在任一候选 netEV 中)。
// 新候选 netEV 已含 open+close+slippage;四类 carry 的 rebalancePenalty 亦在 netEV(映射为 ExpectedRollbackCostUSDT)。
func rotationSwitchMarginalCostUSDT(heldCloseCostUSDT float64, newOpp UnifiedOpportunity) float64 {
	_ = newOpp
	return heldCloseCostUSDT
}

func unifiedOpportunityParseFundingTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// rotationOpportunityFundingEntryWindowEligible 决策层进场窗闸(与执行 open gate 同口径 minimum)。
func rotationOpportunityFundingEntryWindowEligible(opp UnifiedOpportunity, execCfg CrossVenueExecutorConfig, now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch opp.StrategyType {
	case UnifiedStrategyPerpPerp:
		highNext := unifiedOpportunityParseFundingTime(opp.HighNextFundingTime)
		lowNext := unifiedOpportunityParseFundingTime(opp.LowNextFundingTime)
		ih := opp.FundingIntervalHours
		if ih <= 0 {
			ih = 8
		}
		lead := time.Duration(crossVenueResolvedOpenFundingLeadSeconds(execCfg)) * time.Second
		return crossVenueFundingEntryWindowReached(highNext, ih, lowNext, ih, now, lead)
	default:
		next := unifiedOpportunityParseFundingTime(opp.NextFundingTime)
		if next.IsZero() {
			return true // 无结算时间 → fail-open(与 perp-perp 闸一致)
		}
		ih := opp.FundingIntervalHours
		if ih <= 0 {
			ih = 8
		}
		untilHours := crossVenueComparableWindowHours(now, next, next)
		if untilHours <= 0 {
			return false
		}
		entryWindowMinutes := carryFundingEntryWindowMinutes(execCfg.FundingEntryGate(), ih, 0, false)
		return untilHours*60 <= entryWindowMinutes+1e-9
	}
}

func refreshRotationCandidatesOpenEligibleNow(candidates []UnifiedOpportunity, execCfg CrossVenueExecutorConfig, now time.Time) {
	for i := range candidates {
		candidates[i].OpenEligibleNow = rotationOpportunityFundingEntryWindowEligible(candidates[i], execCfg, now)
	}
}

func rotationOpportunityHasFundingTimeData(opp UnifiedOpportunity) bool {
	switch opp.StrategyType {
	case UnifiedStrategyPerpPerp:
		return !unifiedOpportunityParseFundingTime(opp.HighNextFundingTime).IsZero() ||
			!unifiedOpportunityParseFundingTime(opp.LowNextFundingTime).IsZero()
	default:
		return !unifiedOpportunityParseFundingTime(opp.NextFundingTime).IsZero()
	}
}

// rotationOpportunityOpenEligibleForDecision 决策层 OPEN/SWITCH 进场窗闸。
// Runner 刷新后 OpenEligibleNow 为准;无 funding 时间字段的 legacy/测试候选 fail-open。
func rotationOpportunityOpenEligibleForDecision(opp UnifiedOpportunity) bool {
	if opp.OpenEligibleNow {
		return true
	}
	if !rotationOpportunityHasFundingTimeData(opp) {
		return true
	}
	return false
}

// rotationContinuationMinHoldSatisfied 续持软 CLOSE 的 min-hold:固定秒数 + 首 settlement 持有(可配置)。
func rotationContinuationMinHoldSatisfied(pos HeldPosition, cfg RotationConfig, now time.Time) bool {
	if pos.OpenedAt.IsZero() {
		return true
	}
	if !rotationFirstSettlementHoldSatisfied(pos, cfg, now) {
		return false
	}
	minHold := time.Duration(cfg.ContinuationMinHoldSeconds) * time.Second
	if minHold <= 0 {
		return true
	}
	return !now.Before(pos.OpenedAt.Add(minHold))
}

// rotationSwitchMinHoldSatisfied 防止"刚开仓就被换"的 churn:固定 ContinuationMinHoldSeconds + 首 settlement 持有。
func rotationSwitchMinHoldSatisfied(pos HeldPosition, cfg RotationConfig, now time.Time) bool {
	return rotationContinuationMinHoldSatisfied(pos, cfg, now)
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
	if !rotationContinuationMinHoldSatisfied(pos, cfg, now) {
		return false, ""
	}
	return true, fmt.Sprintf("continuation_net=%.4f<-close_cost=%.4f score=%.6f", contNet, closeCost, contScore)
}

// DecideRotation 生成跨窗口 HOLD/CLOSE/SWITCH/OPEN 计划(纯函数,不执行下单)。
func DecideRotation(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	equityUSDT ...float64,
) RotationPlan {
	return DecideRotationWithBlocked(held, heldInputs, candidates, available, cfg, nil, equityUSDT...)
}

func DecideRotationWithBlocked(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	blocked RotationOpenBlocked,
	equityUSDT ...float64,
) RotationPlan {
	equity := 0.0
	if len(equityUSDT) > 0 {
		equity = equityUSDT[0]
	}
	return decideRotationAt(held, heldInputs, candidates, available, cfg, time.Now(), equity, blocked)
}

func decideRotationAt(
	held []HeldPosition,
	heldInputs map[string]ContinuationInputs,
	candidates []UnifiedOpportunity,
	available map[string]float64,
	cfg RotationConfig,
	now time.Time,
	equityUSDT float64,
	blocked RotationOpenBlocked,
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
		best, openScore, found := rotationBestReplaceCandidate(pos, candidates, replaceFunds, cfg, openCount, alreadyChosen, &replaceBudget, blocked)
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
		contNet := continuationNetUSDT(inputs)
		adjustedOpenScore := openScore - switchCost

		if contNet >= adjustedOpenScore {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionHold,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 fmt.Sprintf("hold continuation_net=%.4f >= adjusted_open_net=%.4f", contNet, adjustedOpenScore),
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

		if !rotationSwitchMinHoldSatisfied(pos, cfg, now) {
			plan.Actions = append(plan.Actions, RotationAction{
				Kind:                   RotationActionHold,
				OpportunityID:          pos.OpportunityID,
				StrategyType:           pos.StrategyType,
				Symbol:                 pos.Symbol,
				Score:                  contScore,
				Reason:                 fmt.Sprintf("hold_switch_min_hold continuation_net=%.4f adjusted_open_net=%.4f", contNet, adjustedOpenScore),
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
			Reason:                 fmt.Sprintf("switch continuation_net=%.4f < adjusted_open_net=%.4f", contNet, adjustedOpenScore),
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
	plan.Actions = append(plan.Actions, planUnifiedRotationOpensFrom(remaining, funds, cfg, currentOpenCount, budget, blocked)...)

	return plan
}

// RotationDecisionDiag 单轮候选诊断分布(止盲:解释 raw→可开漏斗)。
type RotationDecisionDiag struct {
	CycleID             string         `json:"cycle_id"`
	RawTotal            int            `json:"raw_total"`
	RawByStrategy       map[string]int `json:"raw_by_strategy"`
	ScannerWouldExec    int            `json:"scanner_would_execute_total"`
	RotationWouldExec   int            `json:"rotation_would_execute_total"`
	WouldExecByStrategy map[string]int `json:"rotation_would_execute_by_strategy"`
	SoftRejectCount     int            `json:"soft_reject_count"`
	HardRejectCount     int            `json:"hard_reject_count"`
	BlockedByNetEV      int            `json:"blocked_by_net_ev"`
	BlockedByHardReject int            `json:"blocked_by_hard_reject"`
	BlockedByBalance    int            `json:"blocked_by_balance"`
	TopRejectReasons    map[string]int `json:"top_reject_reasons"`
}

// rotationDiagnoseCandidates 只诊断不执行:对每个候选跑 rotationCanOpenOpportunity 并累计分布。
func rotationDiagnoseCandidates(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig) RotationDecisionDiag {
	cfg = normalizeRotationConfig(cfg)
	diag := RotationDecisionDiag{
		RawTotal:            len(candidates),
		RawByStrategy:       make(map[string]int),
		WouldExecByStrategy: make(map[string]int),
		TopRejectReasons:    make(map[string]int),
	}
	budget := newRotationBudgetState(available, nil, cfg, 0)
	for _, opp := range candidates {
		diag.RawByStrategy[opp.StrategyType]++
		if len(opp.RejectReasons) == 0 {
			diag.ScannerWouldExec++
		} else if rotationFirstHardRejectReason(opp) != "" {
			diag.HardRejectCount++
		} else {
			diag.SoftRejectCount++
		}
		ok, reason := rotationCanOpenOpportunity(opp, available, cfg, 0, &budget, nil)
		if ok {
			diag.RotationWouldExec++
			diag.WouldExecByStrategy[opp.StrategyType]++
			continue
		}
		if reason == "" {
			reason = "unknown"
		}
		diag.TopRejectReasons[reason]++
		switch {
		case reason == "net_ev<=0":
			diag.BlockedByNetEV++
		case strings.HasPrefix(reason, "venue_balance_insufficient:") || reason == "requires_rebalance_unfundable":
			diag.BlockedByBalance++
		case strings.HasPrefix(reason, "reject:"):
			diag.BlockedByHardReject++
		}
	}
	return diag
}

// rotationDiagTopRejectReason 返回本轮最高频拒绝原因(无则空串)。
func rotationDiagTopRejectReason(diag RotationDecisionDiag) string {
	top := ""
	topCount := 0
	for reason, count := range diag.TopRejectReasons {
		if count > topCount {
			topCount = count
			top = reason
		}
	}
	return top
}

// filterRotationCandidates 决策输入池:剔除 scanner 硬拒/仅诊断/无资金/非正 EV 对象。
func filterRotationCandidates(candidates []UnifiedOpportunity, cfg RotationConfig) []UnifiedOpportunity {
	minProfit := rotationMinNetProfitUSDT(cfg)
	out := make([]UnifiedOpportunity, 0, len(candidates))
	for _, opp := range candidates {
		if rotationFirstHardRejectReason(opp) != "" {
			continue
		}
		if opp.CandidateOnly {
			continue
		}
		if unifiedOpportunityTotalCapital(opp) <= 0 {
			continue
		}
		if opp.ExpectedNetEVUSDT <= minProfit {
			continue
		}
		out = append(out, opp)
	}
	return out
}

// rotationCandidateWouldExecuteStatic 静态预判是否可进决策池(不含余额/并发/冷却等运行时闸)。
func rotationCandidateWouldExecuteStatic(opp UnifiedOpportunity, cfg RotationConfig) bool {
	if rotationFirstHardRejectReason(opp) != "" {
		return false
	}
	if opp.CandidateOnly {
		return false
	}
	if unifiedOpportunityTotalCapital(opp) <= 0 {
		return false
	}
	return opp.ExpectedNetEVUSDT > rotationMinNetProfitUSDT(cfg)
}
