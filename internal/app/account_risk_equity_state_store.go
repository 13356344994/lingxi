package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	accountRiskEquityStateFileName      = "account_risk_equity_state.json"
	accountRiskEquityStateSchemaVersion = 1
)

// persistedAccountRiskEquityState 跨 tick/重启的账户权益状态(原子落盘)。
type persistedAccountRiskEquityState struct {
	SchemaVersion           int                `json:"schema_version"`
	DayStartCST             time.Time          `json:"day_start_cst"`
	LastTrustedTotalUSDT    float64            `json:"last_trusted_total_usdt"`
	LastTrustedByVenue      map[string]float64 `json:"last_trusted_by_venue,omitempty"`
	HasLastTrusted          bool               `json:"has_last_trusted"`
	LastTrustedAt           time.Time          `json:"last_trusted_at,omitempty"`
	DailyFundFlowAdjustUSDT float64            `json:"daily_fund_flow_adjust_usdt"`
	CumulativeFundFlowUSDT  float64            `json:"cumulative_fund_flow_usdt"`
	TradingEquityPeakUSDT   float64            `json:"trading_equity_peak_usdt"`
	LastDailyRealizedPnL    float64            `json:"last_daily_realized_pnl"`
	LastSettlementCount     int                `json:"last_settlement_count"`
	LastPositionCount       int                `json:"last_position_count"`
	// 充提平移来源与未确权额(向后兼容:旧文件无此字段则默认空/0,仍按 schema v1 读取)。
	DailyFundFlowSource        string    `json:"daily_fund_flow_source,omitempty"`
	DailyFundFlowReconcileUSDT float64   `json:"daily_fund_flow_reconcile_usdt,omitempty"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

func accountRiskEquityStatePath(recordDir string) string {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	return filepath.Join(dir, accountRiskEquityStateFileName)
}

func (st *accountRiskEquityState) toPersisted(now time.Time) persistedAccountRiskEquityState {
	if st == nil {
		return persistedAccountRiskEquityState{SchemaVersion: accountRiskEquityStateSchemaVersion, UpdatedAt: now}
	}
	return persistedAccountRiskEquityState{
		SchemaVersion:              accountRiskEquityStateSchemaVersion,
		DayStartCST:                st.DayStartCST,
		LastTrustedTotalUSDT:       st.LastTrustedTotalUSDT,
		LastTrustedByVenue:         copyVenueEquityMap(st.LastTrustedByVenue),
		HasLastTrusted:             st.HasLastTrusted,
		LastTrustedAt:              st.LastTrustedAt,
		DailyFundFlowAdjustUSDT:    st.DailyFundFlowAdjustUSDT,
		CumulativeFundFlowUSDT:     st.CumulativeFundFlowUSDT,
		TradingEquityPeakUSDT:      st.TradingEquityPeakUSDT,
		LastDailyRealizedPnL:       st.LastDailyRealizedPnL,
		LastSettlementCount:        st.LastSettlementCount,
		LastPositionCount:          st.LastPositionCount,
		DailyFundFlowSource:        st.DailyFundFlowSource,
		DailyFundFlowReconcileUSDT: st.DailyFundFlowReconcileUSDT,
		UpdatedAt:                  now,
	}
}

func (st *accountRiskEquityState) applyPersisted(p persistedAccountRiskEquityState) {
	if st == nil {
		return
	}
	st.DayStartCST = p.DayStartCST
	st.LastTrustedTotalUSDT = p.LastTrustedTotalUSDT
	st.LastTrustedByVenue = copyVenueEquityMap(p.LastTrustedByVenue)
	st.HasLastTrusted = p.HasLastTrusted
	st.LastTrustedAt = p.LastTrustedAt
	st.DailyFundFlowAdjustUSDT = p.DailyFundFlowAdjustUSDT
	st.CumulativeFundFlowUSDT = p.CumulativeFundFlowUSDT
	st.TradingEquityPeakUSDT = p.TradingEquityPeakUSDT
	st.LastDailyRealizedPnL = p.LastDailyRealizedPnL
	st.LastSettlementCount = p.LastSettlementCount
	st.LastPositionCount = p.LastPositionCount
	st.DailyFundFlowSource = p.DailyFundFlowSource
	st.DailyFundFlowReconcileUSDT = p.DailyFundFlowReconcileUSDT
}

func loadAccountRiskEquityState(recordDir string) (accountRiskEquityState, bool) {
	path := accountRiskEquityStatePath(recordDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return accountRiskEquityState{}, false
	}
	var p persistedAccountRiskEquityState
	if err := json.Unmarshal(data, &p); err != nil {
		return accountRiskEquityState{}, false
	}
	if p.SchemaVersion != accountRiskEquityStateSchemaVersion {
		return accountRiskEquityState{}, false
	}
	var st accountRiskEquityState
	st.applyPersisted(p)
	return st, true
}

func saveAccountRiskEquityState(recordDir string, st *accountRiskEquityState, now time.Time) error {
	if st == nil {
		return nil
	}
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(accountRiskEquityStatePath(recordDir), st.toPersisted(now))
}
