package app

import (
	"path/filepath"
	"strings"
	"time"
)

const (
	unifiedCandidatesLogFileName    = "unified_candidates.jsonl"
	rotationDecisionDiagLogFileName = "rotation_decision_diag.jsonl"
)

func opportunitiesRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "opportunities")
}

func (e *CrossVenueExecutor) unifiedCandidatesLogPath() string {
	base := "records"
	if e != nil && strings.TrimSpace(e.recordDir) != "" {
		base = e.recordDir
	}
	return filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
}

// UnifiedCandidateLogRecord 是候选级诊断日志的单行 JSONL 格式(plan-only)。
type UnifiedCandidateLogRecord struct {
	CycleID                  string             `json:"cycle_id"`
	StrategyType             string             `json:"strategy_type"`
	Symbol                   string             `json:"symbol"`
	Venues                   []string           `json:"venues"`
	ExpectedNetEVUSDT        float64            `json:"expected_net_ev_usdt"`
	ExpectedFundingUSDT      float64            `json:"expected_funding_usdt,omitempty"`
	CapitalRequiredByVenue   map[string]float64 `json:"capital_required_by_venue"`
	PrimaryScore             float64            `json:"primary_score"`
	CapitalEfficiencyScore   float64            `json:"capital_efficiency_score"`
	RejectReasons            []string           `json:"reject_reasons"`
	WouldExecute             bool               `json:"would_execute"` // scanner 口径,兼容旧面板
	ScannerWouldExecute      bool               `json:"scanner_would_execute"`
	RotationWouldExecute     bool               `json:"rotation_would_execute"`
	CapitalAwareNetEVUSDT    float64            `json:"capital_aware_net_ev_usdt,omitempty"`
	CapitalAwareNotionalUSDT float64            `json:"capital_aware_notional_usdt,omitempty"`
	// CapitalAwareCost 按「可投名义」重估的成本瀑布:资金费 − 费用 − 滑点 − 基差 − dust − 观测风险 − 缓冲 = 净EV。
	// 让排行榜能看清一条机会的净EV到底被哪块成本吃掉,而不只给一个净EV数字。
	CapitalAwareCost     *UnifiedCostBreakdown `json:"capital_aware_cost,omitempty"`
	NotionalUSDT         float64               `json:"notional_usdt"`
	RawFundingRate       float64               `json:"raw_funding_rate,omitempty"`
	FundingRatePerHour   float64               `json:"funding_rate_per_hour,omitempty"`
	FundingAPR           float64               `json:"funding_apr,omitempty"`
	SpreadAPR            float64               `json:"spread_apr,omitempty"`
	HighFundingRate      float64               `json:"high_funding_rate,omitempty"`
	LowFundingRate       float64               `json:"low_funding_rate,omitempty"`
	FundingIntervalHours int                   `json:"funding_interval_hours,omitempty"`
	HighIntervalHours    int                   `json:"high_interval_hours,omitempty"`
	LowIntervalHours     int                   `json:"low_interval_hours,omitempty"`
	NextFundingTime      string                `json:"next_funding_time,omitempty"`
	HighNextFundingTime  string                `json:"high_next_funding_time,omitempty"`
	LowNextFundingTime   string                `json:"low_next_funding_time,omitempty"`
}

// UnifiedCostBreakdown 是一条机会按可投名义重估的净EV成本瀑布(USDT,均为该机会期望持有窗口内的口径)。
// 关系:NetEVUSDT = FundingUSDT − FeesUSDT − SlippageUSDT − EntryBasisUSDT − DustUSDT − ObservableRiskUSDT − PolicyBufferUSDT。
type UnifiedCostBreakdown struct {
	FundingUSDT        float64 `json:"funding_usdt"`         // 期望吃到的资金费(毛收入)
	FeesUSDT           float64 `json:"fees_usdt"`            // 开+平双边手续费
	SlippageUSDT       float64 `json:"slippage_usdt"`        // 期望滑点
	EntryBasisUSDT     float64 `json:"entry_basis_usdt"`     // 两腿入场基差成本
	DustUSDT           float64 `json:"dust_usdt"`            // 残腿/取整 dust 期望成本
	ObservableRiskUSDT float64 `json:"observable_risk_usdt"` // 新鲜度/窗口/可恢复性折算的观测风险成本
	PolicyBufferUSDT   float64 `json:"policy_buffer_usdt"`   // 保守政策缓冲
	NetEVUSDT          float64 `json:"net_ev_usdt"`          // 净EV(= 上面相减)
}

// unifiedCostBreakdownFromCrossVenue 从重估后的跨所机会提取成本瀑布(展示用,不改排序/决策)。
func unifiedCostBreakdownFromCrossVenue(o CrossVenueOpportunity) *UnifiedCostBreakdown {
	return &UnifiedCostBreakdown{
		FundingUSDT:        o.SpreadEV,
		FeesUSDT:           o.OpenCost + o.CloseCost,
		SlippageUSDT:       o.ExpectedSlippage,
		EntryBasisUSDT:     o.EntryBasisCost,
		DustUSDT:           o.DustResidualCost,
		ObservableRiskUSDT: o.ObservableRiskCost,
		PolicyBufferUSDT:   o.PolicyBuffer,
		NetEVUSDT:          o.NetEV,
	}
}

// UnifiedCandidateLogEnricher 可选上下文:在写入 JSONL 时填充 capital-aware EV/名义与真实 rotation/open 闸口径。
type UnifiedCandidateLogEnricher struct {
	AvailableUSDTByVenue map[string]float64
	RotationCfg          RotationConfig
	Now                  time.Time
	CrossVenueOppsByKey  map[string]CrossVenueOpportunity
	CrossVenueOpp        *CrossVenueOpportunity
	HasGate              bool
	GateOK               bool
	GateNotional         float64
}

func unifiedCandidateLogFromOpportunity(cfg CrossVenueExecutorConfig, cycleID string, unified UnifiedOpportunity, wouldExecute bool, notionalUSDT float64) UnifiedCandidateLogRecord {
	return unifiedCandidateLogFromOpportunityWithRotation(cfg, cycleID, unified, wouldExecute, notionalUSDT, DefaultRotationConfig(), nil)
}

func unifiedCandidateLogFromOpportunityWithRotation(cfg CrossVenueExecutorConfig, cycleID string, unified UnifiedOpportunity, wouldExecute bool, notionalUSDT float64, rotationCfg RotationConfig, enrich *UnifiedCandidateLogEnricher) UnifiedCandidateLogRecord {
	venues := make([]string, 0, len(unified.Legs))
	for _, leg := range unified.Legs {
		if leg.Venue != "" {
			venues = append(venues, leg.Venue)
		}
	}
	if len(venues) == 0 {
		venues = nil
	}
	_ = cfg
	capEff := unified.CapitalEfficiencyScore
	if capEff == 0 {
		capEff = unifiedWindowScore(unified)
	}
	rec := UnifiedCandidateLogRecord{
		CycleID:                cycleID,
		StrategyType:           unified.StrategyType,
		Symbol:                 unified.Symbol,
		Venues:                 venues,
		ExpectedNetEVUSDT:      unified.ExpectedNetEVUSDT,
		ExpectedFundingUSDT:    unified.ExpectedFundingUSDT,
		CapitalRequiredByVenue: unified.CapitalRequiredByVenue,
		PrimaryScore:           unifiedPrimaryScore(unified),
		CapitalEfficiencyScore: capEff,
		RejectReasons:          unified.RejectReasons,
		WouldExecute:           wouldExecute,
		ScannerWouldExecute:    wouldExecute,
		RotationWouldExecute:   rotationCandidateWouldExecuteStatic(unified, rotationCfg),
		NotionalUSDT:           notionalUSDT,
		RawFundingRate:         unified.RawFundingRate,
		FundingRatePerHour:     unified.FundingRatePerHour,
		FundingAPR:             unified.FundingAPR,
		SpreadAPR:              unified.SpreadAPR,
		HighFundingRate:        unified.HighFundingRate,
		LowFundingRate:         unified.LowFundingRate,
		FundingIntervalHours:   unified.FundingIntervalHours,
		HighIntervalHours:      unified.HighIntervalHours,
		LowIntervalHours:       unified.LowIntervalHours,
		NextFundingTime:        unified.NextFundingTime,
		HighNextFundingTime:    unified.HighNextFundingTime,
		LowNextFundingTime:     unified.LowNextFundingTime,
	}
	if enrich != nil {
		applyUnifiedCandidateCapitalAware(&rec, unified, cfg, *enrich)
	}
	return rec
}

func crossVenueOppKeyFromUnified(opp UnifiedOpportunity) (string, bool) {
	high, low, err := parsePerpPerpLegs(opp)
	if err != nil {
		return "", false
	}
	return crossVenueUnifiedCandidateKey(CrossVenueOpportunity{Symbol: opp.Symbol, HighVenue: high, LowVenue: low}), true
}

func lookupCrossVenueOppForUnified(unified UnifiedOpportunity, enrich UnifiedCandidateLogEnricher) *CrossVenueOpportunity {
	if enrich.CrossVenueOpp != nil {
		cv := *enrich.CrossVenueOpp
		return &cv
	}
	if key, ok := crossVenueOppKeyFromUnified(unified); ok && enrich.CrossVenueOppsByKey != nil {
		if cv, found := enrich.CrossVenueOppsByKey[key]; found {
			cvCopy := cv
			return &cvCopy
		}
	}
	return nil
}

func applyScannerEstimatesFromCrossVenue(rec *UnifiedCandidateLogRecord, cvOpp CrossVenueOpportunity) {
	if rec == nil {
		return
	}
	rec.ExpectedNetEVUSDT = cvOpp.NetEV
	if cvOpp.TradableNotional > 0 {
		rec.NotionalUSDT = cvOpp.TradableNotional
	}
}

// crossVenueApplyCapitalAwareSlippageOverride 统一捕获模型下,让"可投"EV 复算沿用 headline 真实深度算出的每单位滑点 bps,
// 使排行榜 headline 与 capital-aware 同法可比(同名义则相等,异名义按 bps 线性缩放)。flag OFF 时不改动(行为不变)。
func crossVenueApplyCapitalAwareSlippageOverride(cfg CrossVenueExecutorConfig, reOpp *CrossVenueOpportunity, headline *CrossVenueOpportunity) {
	if reOpp == nil || headline == nil || !crossVenueUnifiedActiveForSymbol(cfg, headline.Symbol) {
		return
	}
	if headline.TradableNotional > 0 && headline.ExpectedSlippage > 0 {
		reOpp.EVSlippageBpsOverride = headline.ExpectedSlippage / headline.TradableNotional * 10000
	}
}

func applyUnifiedCandidateCapitalAware(rec *UnifiedCandidateLogRecord, unified UnifiedOpportunity, cfg CrossVenueExecutorConfig, enrich UnifiedCandidateLogEnricher) {
	if rec == nil {
		return
	}
	now := enrich.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rotCfg := normalizeRotationConfig(enrich.RotationCfg)
	cvOpp := lookupCrossVenueOppForUnified(unified, enrich)

	if enrich.HasGate && cvOpp != nil {
		applyScannerEstimatesFromCrossVenue(rec, *cvOpp)
		notional := enrich.GateNotional
		if notional <= 0 {
			notional = cvOpp.TradableNotional
		}
		if notional > 0 {
			reOpp := *cvOpp
			reOpp.TradableNotional = notional
			crossVenueApplyCapitalAwareSlippageOverride(cfg, &reOpp, cvOpp)
			estimated := crossVenueEstimateOpportunityMetrics(cfg, reOpp, now)
			rec.CapitalAwareNotionalUSDT = notional
			rec.CapitalAwareNetEVUSDT = estimated.NetEV
			rec.CapitalAwareCost = unifiedCostBreakdownFromCrossVenue(estimated)
		}
		rec.RotationWouldExecute = enrich.GateOK
		return
	}

	if cvOpp != nil && len(enrich.AvailableUSDTByVenue) > 0 && unified.StrategyType == UnifiedStrategyPerpPerp {
		applyScannerEstimatesFromCrossVenue(rec, *cvOpp)
		scannerUnified := UnifiedOpportunityFromCrossVenue(cfg, *cvOpp, unified.RejectReasons, 0, 0)
		capped := rotationCapPerpPerpOpportunityByCapital(scannerUnified, cfg, rotCfg, enrich.AvailableUSDTByVenue)
		capNotional := rotationMaxLegNotional(capped)
		if capNotional > 0 {
			reOpp := *cvOpp
			reOpp.TradableNotional = capNotional
			crossVenueApplyCapitalAwareSlippageOverride(cfg, &reOpp, cvOpp)
			estimated := crossVenueEstimateOpportunityMetrics(cfg, reOpp, now)
			rec.CapitalAwareNotionalUSDT = capNotional
			rec.CapitalAwareNetEVUSDT = estimated.NetEV
			rec.CapitalAwareCost = unifiedCostBreakdownFromCrossVenue(estimated)
			capped.ExpectedNetEVUSDT = estimated.NetEV
			capped.ExpectedFundingUSDT = estimated.SpreadEV
			capped.ExpectedOpenCostUSDT = estimated.OpenCost
			capped.ExpectedCloseCostUSDT = estimated.CloseCost
			capped.ExpectedSlippageUSDT = estimated.ExpectedSlippage
		} else {
			rec.CapitalAwareNotionalUSDT = 0
			rec.CapitalAwareNetEVUSDT = 0
		}
		avail := enrich.AvailableUSDTByVenue
		budget := newRotationBudgetState(avail, nil, rotCfg, 0)
		ok, _ := rotationCanOpenOpportunity(capped, avail, rotCfg, 0, &budget, nil)
		rec.RotationWouldExecute = ok
		return
	}

	if len(enrich.AvailableUSDTByVenue) > 0 {
		notional := rotationOpportunityTradableNotional(unified)
		if notional <= 0 {
			notional = rotationMaxLegNotional(unified)
		}
		if notional > 0 {
			rec.CapitalAwareNotionalUSDT = notional
		}
		rec.CapitalAwareNetEVUSDT = unified.ExpectedNetEVUSDT
		avail := enrich.AvailableUSDTByVenue
		budget := newRotationBudgetState(avail, nil, rotCfg, 0)
		ok, _ := rotationCanOpenOpportunity(unified, avail, rotCfg, 0, &budget, nil)
		rec.RotationWouldExecute = ok
	}
}

type crossVenueUnifiedCandidateTrace struct {
	Opp             CrossVenueOpportunity
	Gate            crossVenueGateResult
	RejectReasons   []string
	WouldExecute    bool
	NotionalUSDT    float64
	HasGateNotional bool
}

func crossVenueUnifiedCandidateKey(opp CrossVenueOpportunity) string {
	return opp.Symbol + "|" + opp.HighVenue + "|" + opp.LowVenue
}

// appendUnifiedCandidateLogs best-effort 写入本轮所有候选诊断(plan);写盘失败吞掉,不影响主循环。
func (e *CrossVenueExecutor) appendUnifiedCandidateLogs(cycleID string, cfg CrossVenueExecutorConfig, traces []crossVenueUnifiedCandidateTrace) {
	if e == nil || strings.TrimSpace(cycleID) == "" || len(traces) == 0 {
		return
	}
	path := e.unifiedCandidatesLogPath()
	for _, trace := range traces {
		notional := trace.NotionalUSDT
		refPrice := 0.0
		if trace.HasGateNotional {
			refPrice = (trace.Gate.HighMid + trace.Gate.LowMid) / 2
			if notional <= 0 {
				notional = trace.Gate.NotionalUSDT
			}
		}
		if notional <= 0 {
			notional = trace.Opp.TradableNotional
		}
		unified := UnifiedOpportunityFromCrossVenue(cfg, trace.Opp, trace.RejectReasons, notional, refPrice)
		cvOpp := trace.Opp
		enrich := &UnifiedCandidateLogEnricher{
			CrossVenueOpp: &cvOpp,
			HasGate:       trace.HasGateNotional,
			GateOK:        trace.Gate.OK,
			GateNotional:  trace.Gate.NotionalUSDT,
		}
		rec := unifiedCandidateLogFromOpportunityWithRotation(cfg, cycleID, unified, trace.WouldExecute, notional, DefaultRotationConfig(), enrich)
		_ = appendJSONLRecord(path, rec)
	}
}

func unifiedRejectedOpportunityWithFunding(
	strategyType, snapshotID, symbol, highVenue, lowVenue, reason string,
	fund CrossVenueSpotPerpFundingSnap,
	now time.Time,
	displayRate float64,
) UnifiedOpportunity {
	opp := UnifiedOpportunity{
		ID:               unifiedOpportunityID(strategyType, symbol, snapshotID, highVenue, lowVenue),
		StrategyType:     strategyType,
		Symbol:           symbol,
		RejectReasons:    []string{reason},
		RiskScore:        1,
		DataQualityScore: 0,
	}
	if highVenue != "" || lowVenue != "" {
		legs := make([]UnifiedOpportunityLeg, 0, 2)
		if highVenue != "" {
			legs = append(legs, UnifiedOpportunityLeg{Venue: highVenue})
		}
		if lowVenue != "" && lowVenue != highVenue {
			legs = append(legs, UnifiedOpportunityLeg{Venue: lowVenue})
		}
		opp.Legs = legs
	}
	applyUnifiedFundingSnapDisplay(&opp, fund, now, displayRate)
	return opp
}

func unifiedMinutesUntilFunding(nextFundingRFC3339 string, now time.Time) float64 {
	return unifiedMinutesUntilFundingWithInterval(nextFundingRFC3339, now, 0)
}

// unifiedMinutesUntilFundingWithInterval 计算距下一次资费结算的分钟数。
// 当 next 已过期且 intervalHours>0 时，按间隔滚动到下一次结算；interval 未知时保持原行为(过期返回 0)。
func unifiedMinutesUntilFundingWithInterval(nextFundingRFC3339 string, now time.Time, intervalHours int) float64 {
	nextFundingRFC3339 = strings.TrimSpace(nextFundingRFC3339)
	if nextFundingRFC3339 == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, nextFundingRFC3339)
	if err != nil {
		return 0
	}
	if !t.After(now) {
		if intervalHours <= 0 {
			return 0
		}
		interval := time.Duration(intervalHours) * time.Hour
		for !t.After(now) {
			t = t.Add(interval)
		}
	}
	mins := t.Sub(now).Minutes()
	if mins < 0 {
		return 0
	}
	return mins
}

func unifiedLandscapeSortKey(rec UnifiedCandidateLogRecord) float64 {
	if rec.SpreadAPR > 0 {
		return rec.SpreadAPR
	}
	if rec.FundingAPR > 0 {
		return rec.FundingAPR
	}
	if rec.RawFundingRate > 0 && rec.FundingIntervalHours > 0 {
		return fundingAnnualizedAPR(rec.RawFundingRate, rec.FundingIntervalHours)
	}
	if rec.FundingRatePerHour > 0 {
		return rec.FundingRatePerHour * 24 * 365
	}
	return 0
}

// appendRotationDecisionDiag best-effort 写入 records/opportunities/rotation_decision_diag.jsonl。
func appendRotationDecisionDiag(recordDir string, diag RotationDecisionDiag) {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return
	}
	path := filepath.Join(opportunitiesRecordDir(recordDir), rotationDecisionDiagLogFileName)
	_ = appendJSONLRecord(path, diag)
}
