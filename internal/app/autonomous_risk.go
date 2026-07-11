package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/risk"
)

const (
	autonomousRiskFile    = "autonomous_risk_state.json"
	autonomousRiskVenue   = "okx"
	autonomousStopPct     = 0.30
	autonomyPhaseActive   = "active"
	autonomyPhaseFlatten  = "flattening"
	autonomyPhaseLearning = "relearning"
	autonomyPhaseShadow   = "shadow_recertification"
	autonomyPhaseProbe    = "probe_recertification"
)

type autonomousRiskState struct {
	SchemaVersion         int                      `json:"schema_version"`
	Phase                 string                   `json:"phase"`
	Venue                 string                   `json:"venue"`
	Generation            int                      `json:"generation"`
	ActivatedAt           time.Time                `json:"activated_at"`
	UpdatedAt             time.Time                `json:"updated_at"`
	DayStartCST           time.Time                `json:"day_start_cst"`
	HighWaterUSDT         float64                  `json:"high_water_usdt"`
	CurrentEquityUSDT     float64                  `json:"current_equity_usdt"`
	TradingEquityUSDT     float64                  `json:"trading_equity_usdt,omitempty"`
	SolvencyEquityUSDT    float64                  `json:"solvency_equity_usdt,omitempty"`
	CleanFrontierUSDT     float64                  `json:"clean_frontier_usdt,omitempty"`
	DrawdownUSDT          float64                  `json:"drawdown_usdt"`
	StopPct               float64                  `json:"stop_pct"`
	StopAmountUSDT        float64                  `json:"stop_amount_usdt"`
	LastDailyFlowUSDT     float64                  `json:"last_daily_flow_usdt"`
	PendingOutflowUSDT    float64                  `json:"pending_outflow_usdt,omitempty"`
	PendingFlowCount      int                      `json:"pending_flow_count,omitempty"`
	HasPendingAdjustments bool                     `json:"has_pending_adjustments,omitempty"`
	ObservationSeq        int64                    `json:"observation_seq,omitempty"`
	FlowSeq               int64                    `json:"flow_seq,omitempty"`
	EquityTruth           risk.EquityTruthSnapshot `json:"equity_truth,omitempty"`
	TriggeredAt           time.Time                `json:"triggered_at,omitempty"`
	TriggerReason         string                   `json:"trigger_reason,omitempty"`
	FlattenAttempts       int                      `json:"flatten_attempts,omitempty"`
	LastFlattenError      string                   `json:"last_flatten_error,omitempty"`
	LessonsVersion        int                      `json:"lessons_version,omitempty"`
	RequireGenerationCert bool                     `json:"require_generation_cert,omitempty"`
	AllowedSources        []string                 `json:"allowed_sources,omitempty"`
}

func autonomousRiskPath(recordDir string) string {
	if strings.TrimSpace(recordDir) == "" {
		recordDir = "records"
	}
	return filepath.Join(recordDir, autonomousRiskFile)
}

func loadAutonomousRisk(recordDir string) (autonomousRiskState, bool) {
	b, err := os.ReadFile(autonomousRiskPath(recordDir))
	if err != nil {
		return autonomousRiskState{}, false
	}
	var st autonomousRiskState
	if json.Unmarshal(b, &st) != nil || st.SchemaVersion != 1 {
		return autonomousRiskState{}, false
	}
	return st, true
}

func saveAutonomousRisk(recordDir string, st autonomousRiskState) error {
	st.SchemaVersion = 1
	st.UpdatedAt = time.Now().UTC()
	return writeJSONFile(autonomousRiskPath(recordDir), st)
}

func autonomySourceAllowed(recordDir, source string) bool {
	st, ok := loadAutonomousRisk(recordDir)
	if !ok {
		return true // 首次部署尚未建立状态时保持旧行为
	}
	if !st.RequireGenerationCert {
		return st.Phase == autonomyPhaseActive
	}
	if st.Phase != autonomyPhaseActive && st.Phase != autonomyPhaseProbe {
		return false
	}
	source = strings.TrimSpace(source)
	for _, allowed := range st.AllowedSources {
		if strings.TrimSpace(allowed) == source {
			return true
		}
	}
	return false
}

func updateAutonomousHighWater(st *autonomousRiskState, snap accountRiskSnapshot, now time.Time) bool {
	if st == nil {
		return false
	}
	current, ok := snap.VenueEquities[st.Venue]
	if !ok {
		return false
	}
	if current <= 0 {
		return false
	}

	ledger := risk.NewEquityTruthLedger(st.Venue)
	ledger.Restore(st.EquityTruth)
	if st.HighWaterUSDT > 0 {
		// 兼容迁移:若旧状态已有高水位,不允许升级后回退。
		ledger.EnsureCleanFrontierAtLeast(risk.MicroUSDTFromFloat64(st.HighWaterUSDT))
	}
	st.ObservationSeq++
	view := ledger.ApplyEquityObservation(risk.EquityObservation{
		ObservationID: fmt.Sprintf("autonomy_obs_%s_%d", st.Venue, st.ObservationSeq),
		Venue:         st.Venue,
		RawEquity:     risk.MicroUSDTFromFloat64(current),
		ExchangeTime:  now,
		ReceivedAt:    now,
	})

	day := beijingCSTDayStart(now)
	if st.DayStartCST.IsZero() || !st.DayStartCST.Equal(day) {
		st.DayStartCST = day
		// 日切换时重置为当日累计值基线,避免把「日累计清零」当成真实资金事件。
		st.LastDailyFlowUSDT = snap.DailyFundFlowByVenueUSDT[st.Venue]
	}
	dailyFlow := snap.DailyFundFlowByVenueUSDT[st.Venue]
	flowDelta := dailyFlow - st.LastDailyFlowUSDT
	st.LastDailyFlowUSDT = dailyFlow
	if flowDelta != 0 {
		st.FlowSeq++
		view = ledger.ApplyFundFlow(risk.FundFlowEvent{
			EventID:      fmt.Sprintf("autonomy_flow_%s_%d", st.Venue, st.FlowSeq),
			Venue:        st.Venue,
			Amount:       risk.MicroUSDTFromFloat64(flowDelta),
			ExchangeTime: now,
			ReceivedAt:   now,
		})
	}
	view = ledger.View()
	st.EquityTruth = ledger.Snapshot()
	st.CurrentEquityUSDT = view.RawEquity.Float64()
	st.TradingEquityUSDT = view.TradingEquity.Float64()
	st.SolvencyEquityUSDT = view.SolvencyEquity.Float64()
	st.CleanFrontierUSDT = view.HighWaterCleanFrontier.Float64()
	st.HighWaterUSDT = st.CleanFrontierUSDT
	st.DrawdownUSDT = view.DrawdownFromFrontier.Float64()
	if st.DrawdownUSDT < 0 {
		st.DrawdownUSDT = 0
	}
	st.PendingOutflowUSDT = view.PendingOutflowReserve.Float64()
	st.PendingFlowCount = view.PendingFlowCount
	st.HasPendingAdjustments = view.HasPending
	st.StopPct = autonomousStopPct
	st.StopAmountUSDT = st.HighWaterUSDT * st.StopPct
	return true
}

func (w *accountRiskWatcher) handleAutonomousRisk(snap accountRiskSnapshot, now time.Time) bool {
	if w == nil {
		return false
	}
	st, ok := loadAutonomousRisk(w.recordDir)
	if !ok {
		current := snap.VenueEquities[autonomousRiskVenue]
		if current <= 0 {
			return false
		}
		day := beijingCSTDayStart(now)
		st = autonomousRiskState{
			SchemaVersion: 1, Phase: autonomyPhaseActive, Venue: autonomousRiskVenue,
			Generation: 1, ActivatedAt: now,
			DayStartCST:       day,
			LastDailyFlowUSDT: snap.DailyFundFlowByVenueUSDT[autonomousRiskVenue],
		}
		_ = updateAutonomousHighWater(&st, snap, now)
		_ = saveAutonomousRisk(w.recordDir, st)
		return false
	}
	if !updateAutonomousHighWater(&st, snap, now) {
		return st.Phase != autonomyPhaseActive
	}
	if st.Phase == "" {
		st.Phase = autonomyPhaseActive
	}
	if st.Phase == autonomyPhaseActive &&
		st.StopAmountUSDT > 0 &&
		st.DrawdownUSDT >= st.StopAmountUSDT &&
		!st.HasPendingAdjustments {
		st.Phase = autonomyPhaseFlatten
		st.TriggeredAt = now
		st.TriggerReason = fmt.Sprintf(
			"自主系统总止错: %s交易权益(clean_frontier)回撤 %.4f/%.4f USDT(%.2f%%)达到30%%，旧策略全部吊销",
			st.Venue, st.DrawdownUSDT, st.HighWaterUSDT, 100*st.DrawdownUSDT/st.HighWaterUSDT)
		st.Generation++
		st.RequireGenerationCert = true
		st.AllowedSources = nil
		st.FlattenAttempts = 0
		_ = saveAutonomousRisk(w.recordDir, st)
		if w.haltFn != nil {
			_ = w.haltFn(st.TriggerReason)
		}
		if w.notifyFn != nil {
			w.notifyFn("自主系统总止错触发", st.TriggerReason)
		}
	}
	if st.Phase == autonomyPhaseActive {
		_ = saveAutonomousRisk(w.recordDir, st)
		return false
	}
	if w.haltFn != nil {
		_ = w.haltFn("自主系统处于 " + st.Phase + "，禁止新仓")
	}
	if st.Phase == autonomyPhaseFlatten {
		st.FlattenAttempts++
		if w.flattenFn == nil {
			st.LastFlattenError = "flattenFn 未接线"
		} else {
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			err := w.flattenFn(fctx)
			cancel()
			if err != nil {
				st.LastFlattenError = err.Error()
			} else {
				st.Phase = autonomyPhaseLearning
				st.LastFlattenError = ""
				st.LessonsVersion++
				if w.notifyFn != nil {
					w.notifyFn("自主系统已全平，进入自动复盘", "旧策略已吊销；完成重新研究/验证/shadow/最小探针后才会自动恢复")
				}
			}
		}
	}
	_ = saveAutonomousRisk(w.recordDir, st)
	return true
}
