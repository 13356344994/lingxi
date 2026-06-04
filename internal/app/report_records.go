package app

import (
	"strings"
	"time"
)

// 本文件从 report_store.go 拆出：仅承载落盘记录的 schema —— 各 CSV 表头定义、落盘记录
// 结构体，以及执行事故记录到领域事件的映射（同包搬移，行为不变），让 report_store.go 收敛到
// 「存储门面」职责并满足单文件 ≤ 800 行门禁。

var settlementCSVHeader = []string{
	"recorded_at",
	"funding_time",
	"close_executed_at",
	"targets",
	"funding_income",
	"expected_funding_income",
	"funding_income_diff",
	"spot_trading_pnl",
	"futures_trading_pnl",
	"total_fees",
	"net_pnl",
	"snapshot_net_pnl",
	"residual_spot_value",
	"spot_usdt_delta",
	"futures_usdt_delta",
	"has_alert",
	"alerts",
}

var monitorCSVHeader = []string{
	"recorded_at",
	"monitor_stop_at",
	"funding_time",
	"symbol",
	"pair",
	"state",
	"valid",
	"breach_count",
	"estimated_rate",
	"last_funding_rate",
	"basis_rate",
	"scan_score_rate",
	"last_net_profit",
	"last_spread",
	"depth_stability_ratio",
	"spot_visible_notional",
	"futures_visible_notional",
	"market_risk_category",
	"market_risk_severity",
	"market_risk_action",
	"market_risk_detail",
	"invalid_reason",
}

var backtestCSVHeader = []string{
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"trades",
	"win_rate",
	"funding_income",
	"trading_pnl",
	"total_fees",
	"net_pnl",
	"max_drawdown",
	"notes",
}

var backtestRunCSVHeader = []string{
	"run_id",
	"run_mode",
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"trades",
	"win_rate",
	"funding_income",
	"trading_pnl",
	"total_fees",
	"net_pnl",
	"max_drawdown",
	"notes",
}

var residualCSVHeader = []string{
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"phase",
	"event_time",
	"monitor_state",
	"futures_leg_ok",
	"spot_leg_ok",
	"futures_fill_ratio",
	"spot_fill_ratio",
	"futures_filled_usdt",
	"spot_filled_usdt",
	"residual_usdt",
	"emergency_hedge_cost_usdt",
	"emergency_hedge_fee_usdt",
	"emergency_hedge_pnl_estimate",
	"severity",
	"reason",
	"notes",
}

var executionCSVHeader = []string{
	"recorded_at",
	"level",
	"category",
	"severity",
	"phase",
	"symbol",
	"action",
	"incident_count",
	"incident_limit",
	"fatal",
	"detail",
}

// SettlementCSVRecord 表示模块五单轮落盘记录。
type SettlementCSVRecord struct {
	TraceRecordMetadata
	RunID                 string `json:"run_id"`
	CycleID               string `json:"cycle_id"`
	RecordedAt            string `json:"recorded_at"`
	FundingTime           string `json:"funding_time"`
	CloseExecutedAt       string `json:"close_executed_at"`
	Targets               int    `json:"targets"`
	FundingIncome         string `json:"funding_income"`
	ExpectedFundingIncome string `json:"expected_funding_income"`
	FundingIncomeDiff     string `json:"funding_income_diff"`
	FundingIncomePending  bool   `json:"funding_income_pending"`
	FundingSettledAt      string `json:"funding_settled_at,omitempty"`
	SpotTradingPnL        string `json:"spot_trading_pnl"`
	FuturesTradingPnL     string `json:"futures_trading_pnl"`
	TotalFees             string `json:"total_fees"`
	TotalBorrowCosts      string `json:"total_borrow_costs"`
	NetPnL                string `json:"net_pnl"`
	SnapshotNetPnL        string `json:"snapshot_net_pnl"`
	ResidualSpotValue     string `json:"residual_spot_value"`
	SpotUSDTDelta         string `json:"spot_usdt_delta"`
	FuturesUSDTDelta      string `json:"futures_usdt_delta"`
	HasAlert              bool   `json:"has_alert"`
	Alerts                string `json:"alerts"`
	// M-S3.0 观测字段(纯观测,不参与风控):执行摩擦聚合与折入摩擦后的完整净利影子口径。
	// omitempty 让历史记录无此字段时解码为零、零回归。
	TotalOpenSlippageUSDT     string `json:"total_open_slippage_usdt,omitempty"`
	TotalCloseSlippageUSDT    string `json:"total_close_slippage_usdt,omitempty"`
	TotalRecoveryFrictionUSDT string `json:"total_recovery_friction_usdt,omitempty"`
	NetPnLAfterFrictions      string `json:"net_pnl_after_frictions,omitempty"`
	// M-S3.1(R6):成本侧模型预期手续费与本轮名义,供 reconcile 暴露成本侧模型误差。omitempty 让历史记录
	// 无此字段时为空 → 对账回退到实测费用(旧行为),零回归。
	ExpectedTotalFees string `json:"expected_total_fees,omitempty"`
	CycleNotionalUSDT string `json:"cycle_notional_usdt,omitempty"`
}

type DustLedgerRecord struct {
	TraceRecordMetadata
	RunID        string `json:"run_id"`
	CycleID      string `json:"cycle_id"`
	RecordedAt   string `json:"recorded_at"`
	Phase        string `json:"phase"`
	Symbol       string `json:"symbol"`
	Pair         string `json:"pair"`
	Asset        string `json:"asset"`
	Quantity     string `json:"quantity"`
	NotionalUSDT string `json:"notional_usdt"`
	Reason       string `json:"reason"`
}

type PendingFundingSettlementRecord struct {
	TraceRecordMetadata
	RunID         string          `json:"run_id"`
	CycleID       string          `json:"cycle_id"`
	RecordedAt    string          `json:"recorded_at"`
	NextAttemptAt string          `json:"next_attempt_at"`
	LastAttemptAt string          `json:"last_attempt_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	AttemptCount  int             `json:"attempt_count"`
	Settlement    CycleSettlement `json:"settlement"`
	Hedges        []OpenHedge     `json:"hedges"`
}

// MonitorCSVRecord 表示模块三单目标落盘记录。
type MonitorCSVRecord struct {
	TraceRecordMetadata
	RunID                  string `json:"run_id"`
	CycleID                string `json:"cycle_id"`
	RecordedAt             string `json:"recorded_at"`
	MonitorStopAt          string `json:"monitor_stop_at"`
	FundingTime            string `json:"funding_time"`
	Symbol                 string `json:"symbol"`
	Pair                   string `json:"pair"`
	State                  string `json:"state"`
	Valid                  bool   `json:"valid"`
	BreachCount            int    `json:"breach_count"`
	EstimatedRate          string `json:"estimated_rate"`
	LastFundingRate        string `json:"last_funding_rate"`
	BasisRate              string `json:"basis_rate"`
	ScanScoreRate          string `json:"scan_score_rate"`
	LastNetProfit          string `json:"last_net_profit"`
	LastSpread             string `json:"last_spread"`
	DepthStabilityRatio    string `json:"depth_stability_ratio"`
	SpotVisibleNotional    string `json:"spot_visible_notional"`
	FuturesVisibleNotional string `json:"futures_visible_notional"`
	MarketRiskCategory     string `json:"market_risk_category"`
	MarketRiskSeverity     string `json:"market_risk_severity"`
	MarketRiskAction       string `json:"market_risk_action"`
	MarketRiskDetail       string `json:"market_risk_detail"`
	InvalidReason          string `json:"invalid_reason"`
}

// BacktestCSVRecord 表示回测结果的单行记录。
type BacktestCSVRecord struct {
	TraceRecordMetadata
	RecordedAt    string `json:"recorded_at"`
	Scenario      string `json:"scenario"`
	Symbol        string `json:"symbol"`
	Period        string `json:"period"`
	Trades        int    `json:"trades"`
	WinRate       string `json:"win_rate"`
	FundingIncome string `json:"funding_income"`
	TradingPnL    string `json:"trading_pnl"`
	TotalFees     string `json:"total_fees"`
	NetPnL        string `json:"net_pnl"`
	MaxDrawdown   string `json:"max_drawdown"`
	Notes         string `json:"notes"`
}

// BacktestRunSummaryRecord 表示按运行批次归档的回测摘要记录。
type BacktestRunSummaryRecord struct {
	TraceRecordMetadata
	RunID         string `json:"run_id"`
	RunMode       string `json:"run_mode"`
	RecordedAt    string `json:"recorded_at"`
	Scenario      string `json:"scenario"`
	Symbol        string `json:"symbol"`
	Period        string `json:"period"`
	Trades        int    `json:"trades"`
	WinRate       string `json:"win_rate"`
	FundingIncome string `json:"funding_income"`
	TradingPnL    string `json:"trading_pnl"`
	TotalFees     string `json:"total_fees"`
	NetPnL        string `json:"net_pnl"`
	MaxDrawdown   string `json:"max_drawdown"`
	Notes         string `json:"notes"`
}

// ResidualRiskCSVRecord 表示回放中产生的残腿/数量不一致风险记录。
type ResidualRiskCSVRecord struct {
	RecordedAt         string `json:"recorded_at"`
	Scenario           string `json:"scenario"`
	Symbol             string `json:"symbol"`
	Period             string `json:"period"`
	Phase              string `json:"phase"`
	EventTime          string `json:"event_time"`
	MonitorState       string `json:"monitor_state"`
	FuturesLegOK       bool   `json:"futures_leg_ok"`
	SpotLegOK          bool   `json:"spot_leg_ok"`
	FuturesFillRatio   string `json:"futures_fill_ratio"`
	SpotFillRatio      string `json:"spot_fill_ratio"`
	FuturesFilledUSDT  string `json:"futures_filled_usdt"`
	SpotFilledUSDT     string `json:"spot_filled_usdt"`
	ResidualUSDT       string `json:"residual_usdt"`
	EmergencyHedgeCost string `json:"emergency_hedge_cost_usdt"`
	EmergencyHedgeFee  string `json:"emergency_hedge_fee_usdt"`
	EmergencyHedgePnL  string `json:"emergency_hedge_pnl_estimate"`
	Severity           string `json:"severity"`
	Reason             string `json:"reason"`
	Notes              string `json:"notes"`
}

// ResidualSeverityStats 表示残腿风险的分级统计。
type ResidualSeverityStats struct {
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

// ResidualScenarioStats 表示按场景聚合的残腿风险统计。
type ResidualScenarioStats struct {
	Scenario            string `json:"scenario"`
	Count               int    `json:"count"`
	AverageResidualUSDT string `json:"average_residual_usdt"`
	WorstEmergencyPnL   string `json:"worst_emergency_hedge_pnl"`
	HighRiskCount       int    `json:"high_risk_count"`
}

// ExecutionIncidentRecord 表示实盘执行事故、恢复动作与熔断事件。
type ExecutionIncidentRecord struct {
	TraceRecordMetadata
	RunID         string `json:"run_id"`
	CycleID       string `json:"cycle_id"`
	RecordedAt    string `json:"recorded_at"`
	Level         string `json:"level"`
	Category      string `json:"category"`
	Severity      string `json:"severity"`
	Phase         string `json:"phase"`
	Symbol        string `json:"symbol"`
	Action        string `json:"action"`
	IncidentCount int    `json:"incident_count"`
	IncidentLimit int    `json:"incident_limit"`
	Fatal         bool   `json:"fatal"`
	Detail        string `json:"detail"`
}

func (r ExecutionIncidentRecord) DomainEvent() (DomainEvent, bool) {
	eventType, ok := executionIncidentEventType(r)
	if !ok {
		return DomainEvent{}, false
	}
	occurredAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(r.RecordedAt)); err == nil {
		occurredAt = parsed.UTC()
	}
	return DomainEvent{
		TraceRecordMetadata: r.TraceRecordMetadata.normalized(),
		Type:                eventType,
		Level:               executionIncidentEventLevel(r),
		OccurredAt:          occurredAt,
		RunID:               strings.TrimSpace(r.RunID),
		CycleID:             strings.TrimSpace(r.CycleID),
		Protocol:            strings.TrimSpace("legacy." + nonEmpty(r.Category, "execution")),
		Stage:               executionIncidentLifecycleStage(r),
		Successful:          !r.Fatal,
		Reason:              strings.TrimSpace(r.Detail),
	}, true
}

func executionIncidentEventType(r ExecutionIncidentRecord) (EventType, bool) {
	phase := strings.TrimSpace(r.Phase)
	action := strings.TrimSpace(r.Action)
	category := strings.TrimSpace(r.Category)
	switch {
	case r.Fatal && (action == "trip" || strings.Contains(phase, "fuse") || category == RiskCategoryPlatform || category == RiskCategorySafety):
		return EventTypeFuseTripped, true
	case category == RiskCategoryRecovery || strings.Contains(action, "recover") || strings.Contains(phase, "recovery"):
		return EventTypeRecoveryEscalated, true
	case category == RiskCategoryOrderUncertain || strings.Contains(phase, "uncertain"):
		return EventTypeOrderUncertain, true
	case action == "set" && strings.Contains(phase, "cooldown"):
		return EventTypeSymbolCooldownSet, true
	default:
		return "", false
	}
}

func executionIncidentEventLevel(r ExecutionIncidentRecord) EventLevel {
	switch strings.TrimSpace(r.Severity) {
	case RiskSeverityCritical:
		return EventLevelCritical
	case RiskSeverityWarning:
		return EventLevelWarning
	default:
		return EventLevelInfo
	}
}

func executionIncidentLifecycleStage(r ExecutionIncidentRecord) LifecycleStage {
	switch eventType, ok := executionIncidentEventType(r); {
	case !ok:
		return ""
	case eventType == EventTypeFuseTripped:
		return LifecycleStageFused
	case eventType == EventTypeRecoveryEscalated:
		return LifecycleStageRecovering
	case eventType == EventTypeOrderUncertain:
		return LifecycleStageRecovering
	case eventType == EventTypeSymbolCooldownSet:
		return LifecycleStageCoolingDown
	default:
		return ""
	}
}
