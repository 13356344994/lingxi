package app

import (
	"context"
	"encoding/json"
	"fmt"
	"lingxi/internal/lifecycle"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const liveOpenHedgesStateName = "live_open_hedges.json"

type persistedOpenHedgeState struct {
	SchemaVersion int                       `json:"schema_version,omitempty"`
	Checksum      string                    `json:"checksum,omitempty"`
	UpdatedAt     string                    `json:"updated_at"`
	RunID         string                    `json:"run_id,omitempty"`
	CycleID       string                    `json:"cycle_id,omitempty"`
	Hedges        []persistedOpenHedgeEntry `json:"hedges"`
}

func (s *persistedOpenHedgeState) getSchemaVersion() int  { return s.SchemaVersion }
func (s *persistedOpenHedgeState) setSchemaVersion(v int) { s.SchemaVersion = v }
func (s *persistedOpenHedgeState) getChecksum() string    { return s.Checksum }
func (s *persistedOpenHedgeState) setChecksum(c string)   { s.Checksum = c }

type persistedOpenHedgeEntry struct {
	RunID                string                `json:"run_id,omitempty"`
	CycleID              string                `json:"cycle_id,omitempty"`
	Symbol               string                `json:"symbol"`
	Pair                 string                `json:"pair"`
	Direction            fundingCarryDirection `json:"direction,omitempty"`
	SpotBaseAsset        string                `json:"spot_base_asset"`
	HedgeQty             float64               `json:"hedge_qty"`
	SpotBaseQty          float64               `json:"spot_base_qty"`
	OpenSpotQuoteQty     float64               `json:"open_spot_quote_qty"`
	OpenFuturesAvgPrice  float64               `json:"open_futures_avg_price"`
	ReferenceSpotPrice   float64               `json:"reference_spot_price"`
	SpotOrderID          int64                 `json:"spot_order_id,omitempty"`
	SpotClientOrderID    string                `json:"spot_client_order_id,omitempty"`
	FuturesOrderID       int64                 `json:"futures_order_id,omitempty"`
	FuturesClientOrderID string                `json:"futures_client_order_id,omitempty"`
	OpenedAt             string                `json:"opened_at"`
	LifecycleState       lifecycle.State       `json:"lifecycle_state,omitempty"`
	LifecycleReason      string                `json:"lifecycle_reason,omitempty"`
	// NextFundingTime 是该持仓自身的下次资金费结算时间(RFC3339)。滚动持有(EnableRollingHold)下,组合内各币
	// 结算时刻可能不同步(8h/4h/可变),续持复核必须按各币自身时钟驱动(R31,§16.4)。落盘并跨重启恢复后,
	// 引擎才能从台账重建每个持仓的逐币 funding 时钟(symbolFundingClock),而不依赖一次性的全局 :57。
	// omitempty + 向后兼容:旧台账无此字段→零值,单周期路径不读它,零行为变更。
	NextFundingTime string `json:"next_funding_time,omitempty"`
	// BorrowSettledThroughMs 借币利息已结算到的 Unix 毫秒锚点(滚动持有增量计息,FIX_PLAN #2)。
	// omitempty 向后兼容:旧台账/单周期无此字段→0,跨重启恢复后续持仓只计锚点之后的增量利息。
	BorrowSettledThroughMs int64 `json:"borrow_settled_through_ms,omitempty"`
}

// lifecycleOrDefault 返回台账条目记录的对冲生命周期状态；旧台账（无该字段）退化为 active——
// 一笔被落盘的未平仓对冲按定义至少处于持仓态，绝不会是"空闲/未知"，从而让重启后的 SSOT 有确定起点。
func (entry persistedOpenHedgeEntry) lifecycleOrDefault() lifecycle.State {
	if entry.LifecycleState == lifecycle.StateNone {
		return lifecycle.StateActive
	}
	return entry.LifecycleState
}

func effectiveOpenHedgeStatePath(cfg EngineConfig) string {
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if recordDir == "" {
		return liveOpenHedgesStateName
	}
	return filepath.Join(recordDir, liveOpenHedgesStateName)
}

func buildPersistedOpenHedgeEntries(hedges []OpenHedge, runID string, cycleID string) []persistedOpenHedgeEntry {
	entries := make([]persistedOpenHedgeEntry, 0, len(hedges))
	for _, hedge := range hedges {
		openedAt := time.Now().UTC().Format(time.RFC3339)
		if hedge.SpotOrder.TransactTime > 0 {
			openedAt = time.UnixMilli(hedge.SpotOrder.TransactTime).UTC().Format(time.RFC3339)
		}
		lifecycleState := hedge.LifecycleState
		if lifecycleState == lifecycle.StateNone {
			lifecycleState = OpenHedgeLifecycleActive
		}
		nextFundingTime := ""
		if !hedge.Target.NextFundingTime.IsZero() {
			nextFundingTime = hedge.Target.NextFundingTime.UTC().Format(time.RFC3339)
		}
		entries = append(entries, persistedOpenHedgeEntry{
			RunID:                  runID,
			CycleID:                cycleID,
			Symbol:                 hedge.Target.Symbol,
			Pair:                   hedge.Target.Pair,
			Direction:              hedge.carryDirection(),
			SpotBaseAsset:          hedge.Target.SpotBaseAsset,
			HedgeQty:               hedge.HedgeQty,
			SpotBaseQty:            parseFloat(hedge.SpotBaseQty),
			OpenSpotQuoteQty:       hedge.OpenSpotQuoteQty,
			OpenFuturesAvgPrice:    hedge.OpenFuturesAvgPrice,
			ReferenceSpotPrice:     hedge.LastKnownSpotPrice(),
			SpotOrderID:            hedge.SpotOrder.OrderID,
			SpotClientOrderID:      hedge.SpotOrder.ClientOrderID,
			FuturesOrderID:         hedge.FuturesOrder.OrderID,
			FuturesClientOrderID:   hedge.FuturesOrder.ClientOrderID,
			OpenedAt:               openedAt,
			LifecycleState:         lifecycleState,
			LifecycleReason:        hedge.LifecycleReason,
			NextFundingTime:        nextFundingTime,
			BorrowSettledThroughMs: hedge.BorrowSettledThroughMs,
		})
	}
	return entries
}

// rebuildOpenHedgeFromEntry 从落盘台账条目重建一个携带权威生命周期状态的 OpenHedge 骨架，
// 让重启后引擎对该对冲的下单前置门（guardHedgeOrderIntent）以确定状态判定，而非"未知/空闲"态。
// 仅恢复对冲身份与生命周期所需字段；成交明细以交易所实际持仓为准，不在此臆造。
func rebuildOpenHedgeFromEntry(entry persistedOpenHedgeEntry) OpenHedge {
	hedge := OpenHedge{
		Target: FundingCandidate{
			Symbol:        entry.Symbol,
			Pair:          entry.Pair,
			SpotBaseAsset: entry.SpotBaseAsset,
		},
		Direction:              resolvedFundingCarryDirection(entry.Direction, 0),
		HedgeQty:               entry.HedgeQty,
		OpenSpotQuoteQty:       entry.OpenSpotQuoteQty,
		OpenFuturesAvgPrice:    entry.OpenFuturesAvgPrice,
		BorrowSettledThroughMs: entry.BorrowSettledThroughMs,
	}
	// 恢复逐币 funding 时钟:滚动持有跨重启后,引擎据此重建每个持仓自身的续持复核时点(R31,§16.4)。
	if ts := strings.TrimSpace(entry.NextFundingTime); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			hedge.Target.NextFundingTime = parsed.UTC()
		}
	}
	hedge.LifecycleState = entry.lifecycleOrDefault()
	hedge.LifecycleReason = entry.LifecycleReason
	hedge.LifecycleUpdatedAt = time.Now().UTC()
	return hedge
}

func loadPersistedOpenHedgeState(path string) (persistedOpenHedgeState, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return persistedOpenHedgeState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedOpenHedgeState{}, nil
		}
		return persistedOpenHedgeState{}, fmt.Errorf("读取未平仓台账失败: %w", err)
	}
	if len(data) == 0 {
		return persistedOpenHedgeState{}, nil
	}
	state := persistedOpenHedgeState{}
	if err := jsonUnmarshalNoNumberLoss(data, &state); err != nil {
		return persistedOpenHedgeState{}, fmt.Errorf("解析未平仓台账失败: %w", err)
	}
	return state, nil
}

func writePersistedOpenHedgeState(path string, state persistedOpenHedgeState) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if len(state.Hedges) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除未平仓台账失败: %w", err)
		}
		return nil
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := stampStateEnvelope(&state); err != nil {
		return fmt.Errorf("标记未平仓台账信封失败: %w", err)
	}
	return writeJSONFile(path, state)
}

func jsonUnmarshalNoNumberLoss(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	return decoder.Decode(target)
}

type persistedOpenHedgeAssessment struct {
	Issues      []string
	Labels      []string
	HasExposure bool
	// HasResidual:任一暴露持仓为「裸腿 / 两腿失衡 / 数量与台账对不上」→ 不可安全自动接管,须人工 flatten。
	HasResidual bool
	// ResumableEntries:两腿齐全、各腿数量 ≈ 台账 HedgeQty 且平衡的完整对冲 → 可安全自动接管(重建持仓继续管理)。
	ResumableEntries []persistedOpenHedgeEntry
}

func assessPersistedOpenHedgeExposure(entries []persistedOpenHedgeEntry, spotBalances map[string]float64, futuresPositions map[string]float64, marginBalances map[string]CrossMarginAssetBalance, residualFuturesQty float64, residualSpotNotional float64, imbalanceAbortRate float64) persistedOpenHedgeAssessment {
	assessment := persistedOpenHedgeAssessment{
		Issues: make([]string, 0, len(entries)),
		Labels: make([]string, 0, len(entries)),
	}
	qtyThreshold := math.Max(executionQtyTolerance, residualFuturesQty)
	for _, entry := range entries {
		direction := resolvedFundingCarryDirection(entry.Direction, 0)
		futuresQty := math.Abs(futuresPositions[entry.Symbol])
		spotQty := math.Abs(spotBalances[entry.SpotBaseAsset])
		if direction == fundingCarryDirectionNegative {
			if margin, ok := marginBalances[entry.SpotBaseAsset]; ok {
				spotQty = math.Abs(margin.NetAsset)
			} else {
				spotQty = 0
			}
		}
		spotPrice := math.Max(entry.ReferenceSpotPrice, 0)
		spotNotional := spotQty * spotPrice
		futuresOpen := futuresQty > qtyThreshold
		spotOpen := spotQty > executionQtyTolerance && spotNotional >= residualSpotNotional
		if !futuresOpen && !spotOpen {
			continue
		}
		assessment.HasExposure = true
		label := fmt.Sprintf("%s %s", entry.Symbol, direction.positionLabel(entry.SpotBaseAsset, entry.Symbol))
		assessment.Labels = append(assessment.Labels, label)
		parts := make([]string, 0, 2)
		if futuresOpen {
			parts = append(parts, fmt.Sprintf("futures=%.8f", futuresPositions[entry.Symbol]))
		}
		if spotOpen {
			parts = append(parts, fmt.Sprintf("%s=%.8f(%.6fUSDT)", entry.SpotBaseAsset, spotBalances[entry.SpotBaseAsset], spotNotional))
		}
		parts = append(parts, fmt.Sprintf("lifecycle=%s", entry.lifecycleOrDefault()))
		assessment.Issues = append(assessment.Issues, fmt.Sprintf("%s[%s]", entry.Symbol, strings.Join(parts, " ")))
		// 可接管性:两腿齐全 + 各腿数量 ≈ 台账 HedgeQty(失衡在阈值内)→ 完整对冲,可安全接管;否则记为残腿(裸腿/失衡/数量对不上)。
		tol := imbalanceAbortRate
		if tol <= 0 {
			tol = 0.10
		}
		if futuresOpen && spotOpen && entry.HedgeQty > executionQtyTolerance &&
			math.Abs(futuresQty-entry.HedgeQty)/entry.HedgeQty <= tol &&
			math.Abs(spotQty-entry.HedgeQty)/entry.HedgeQty <= tol {
			assessment.ResumableEntries = append(assessment.ResumableEntries, entry)
		} else {
			assessment.HasResidual = true
		}
	}
	sort.Strings(assessment.Issues)
	sort.Strings(assessment.Labels)
	return assessment
}

func (e *FundingRateSniperEngine) syncPersistedOpenHedges(hedges []OpenHedge) error {
	if e == nil {
		return nil
	}
	e.openHedgeStateMu.Lock()
	defer e.openHedgeStateMu.Unlock()
	return writePersistedOpenHedgeState(e.openHedgeStatePath, persistedOpenHedgeState{
		RunID:   e.runID,
		CycleID: e.cycleIDSnapshot(),
		Hedges:  buildPersistedOpenHedgeEntries(hedges, e.runID, e.cycleIDSnapshot()),
	})
}

func (e *FundingRateSniperEngine) loadPersistedOpenHedges() ([]persistedOpenHedgeEntry, error) {
	if e == nil {
		return nil, nil
	}
	e.openHedgeStateMu.Lock()
	defer e.openHedgeStateMu.Unlock()
	state, err := loadPersistedOpenHedgeState(e.openHedgeStatePath)
	if err != nil {
		return nil, err
	}
	if err := guardStateEnvelope("未平仓对冲台账", &state); err != nil {
		return nil, err
	}
	return append([]persistedOpenHedgeEntry(nil), state.Hedges...), nil
}

func (e *FundingRateSniperEngine) clearPersistedOpenHedges() error {
	if e == nil {
		return nil
	}
	e.openHedgeStateMu.Lock()
	defer e.openHedgeStateMu.Unlock()
	return writePersistedOpenHedgeState(e.openHedgeStatePath, persistedOpenHedgeState{})
}

func (e *FundingRateSniperEngine) reconcilePersistedOpenHedges(spotBalances map[string]float64, futuresPositions map[string]float64) error {
	entries, err := e.loadPersistedOpenHedges()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	marginBalances := map[string]CrossMarginAssetBalance(nil)
	for _, entry := range entries {
		if resolvedFundingCarryDirection(entry.Direction, 0) != fundingCarryDirectionNegative {
			continue
		}
		marginState, marginErr := e.fetchCrossMarginAccountState(context.Background())
		if marginErr != nil {
			return marginErr
		}
		marginBalances = marginState.Assets
		break
	}
	assessment := assessPersistedOpenHedgeExposure(entries, spotBalances, futuresPositions, marginBalances, e.cfg.ResidualPositionAlertQty, e.cfg.ResidualSpotAlertUSDT, e.cfg.RiskConfig.HedgeImbalanceAbortRate)
	if !assessment.HasExposure {
		if clearErr := e.clearPersistedOpenHedges(); clearErr != nil {
			return clearErr
		}
		if e.logger != nil {
			e.logger.Printf("启动时发现本地未平仓台账已陈旧，交易所无对应残留仓位，已自动清理")
		}
		return nil
	}
	// 自动接管(R3x):开启 AutoResumeOpenHedges + 无残腿 + 全部为「完整且平衡」的对冲 →
	//   从台账重建持仓写入 activeOpenHedges,引擎据此继续监控/滚动复核/到点平仓,而非拒启等人工。
	// 任一裸腿/失衡/数量对不上(HasResidual) → 维持拒启 + 人工 flatten(失败方向偏安全,绝不把裸腿当健康对冲接管)。
	if e.cfg.RiskConfig.AutoResumeOpenHedges && !assessment.HasResidual && len(assessment.ResumableEntries) > 0 {
		hedges := make([]OpenHedge, 0, len(assessment.ResumableEntries))
		for _, entry := range assessment.ResumableEntries {
			hedges = append(hedges, rebuildOpenHedgeFromEntry(entry))
		}
		e.setCurrentPositionsFromHedges(hedges)
		if e.logger != nil {
			e.logger.Printf("启动时检测到 %d 笔完整且平衡的对冲残留,已自动接管继续监控/平仓(未发现裸腿/失衡): %s", len(hedges), strings.Join(assessment.Labels, ", "))
		}
		return nil
	}
	e.setCurrentPositions(assessment.Labels)
	return fmt.Errorf("检测到未恢复的本地未平仓对冲(疑似裸腿/失衡或数量与台账不符,需人工核查 flatten),拒绝启动: %s", strings.Join(assessment.Issues, ", "))
}

func (e *FundingRateSniperEngine) canClearPersistedOpenHedges(snapshot CycleAccountSnapshot, hedges []OpenHedge) bool {
	entries := buildPersistedOpenHedgeEntries(hedges, e.runID, e.cycleIDSnapshot())
	marginBalances := map[string]CrossMarginAssetBalance(nil)
	if len(entries) > 0 {
		if marginState, err := e.fetchCrossMarginAccountState(context.Background()); err == nil {
			marginBalances = marginState.Assets
		}
	}
	assessment := assessPersistedOpenHedgeExposure(entries, snapshot.SpotAssetBalances, snapshot.FuturesPositionAmounts, marginBalances, e.cfg.ResidualPositionAlertQty, e.cfg.ResidualSpotAlertUSDT, e.cfg.RiskConfig.HedgeImbalanceAbortRate)
	return !assessment.HasExposure
}
