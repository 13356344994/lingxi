package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const liveOpenHedgesStateName = "live_open_hedges.json"

type persistedOpenHedgeState struct {
	UpdatedAt string                    `json:"updated_at"`
	RunID     string                    `json:"run_id,omitempty"`
	CycleID   string                    `json:"cycle_id,omitempty"`
	Hedges    []persistedOpenHedgeEntry `json:"hedges"`
}

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
		entries = append(entries, persistedOpenHedgeEntry{
			RunID:                runID,
			CycleID:              cycleID,
			Symbol:               hedge.Target.Symbol,
			Pair:                 hedge.Target.Pair,
			Direction:            hedge.carryDirection(),
			SpotBaseAsset:        hedge.Target.SpotBaseAsset,
			HedgeQty:             hedge.HedgeQty,
			SpotBaseQty:          parseFloat(hedge.SpotBaseQty),
			OpenSpotQuoteQty:     hedge.OpenSpotQuoteQty,
			OpenFuturesAvgPrice:  hedge.OpenFuturesAvgPrice,
			ReferenceSpotPrice:   hedge.LastKnownSpotPrice(),
			SpotOrderID:          hedge.SpotOrder.OrderID,
			SpotClientOrderID:    hedge.SpotOrder.ClientOrderID,
			FuturesOrderID:       hedge.FuturesOrder.OrderID,
			FuturesClientOrderID: hedge.FuturesOrder.ClientOrderID,
			OpenedAt:             openedAt,
		})
	}
	return entries
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
}

func assessPersistedOpenHedgeExposure(entries []persistedOpenHedgeEntry, spotBalances map[string]float64, futuresPositions map[string]float64, marginBalances map[string]CrossMarginAssetBalance, residualFuturesQty float64, residualSpotNotional float64) persistedOpenHedgeAssessment {
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
		assessment.Issues = append(assessment.Issues, fmt.Sprintf("%s[%s]", entry.Symbol, strings.Join(parts, " ")))
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
		CycleID: e.currentCycleID,
		Hedges:  buildPersistedOpenHedgeEntries(hedges, e.runID, e.currentCycleID),
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
	assessment := assessPersistedOpenHedgeExposure(entries, spotBalances, futuresPositions, marginBalances, e.cfg.ResidualPositionAlertQty, e.cfg.ResidualSpotAlertUSDT)
	if !assessment.HasExposure {
		if clearErr := e.clearPersistedOpenHedges(); clearErr != nil {
			return clearErr
		}
		if e.logger != nil {
			e.logger.Printf("启动时发现本地未平仓台账已陈旧，交易所无对应残留仓位，已自动清理")
		}
		return nil
	}
	e.setCurrentPositions(assessment.Labels)
	return fmt.Errorf("检测到未恢复的本地未平仓对冲，拒绝启动: %s", strings.Join(assessment.Issues, ", "))
}

func (e *FundingRateSniperEngine) canClearPersistedOpenHedges(snapshot CycleAccountSnapshot, hedges []OpenHedge) bool {
	entries := buildPersistedOpenHedgeEntries(hedges, e.runID, e.currentCycleID)
	marginBalances := map[string]CrossMarginAssetBalance(nil)
	if len(entries) > 0 {
		if marginState, err := e.fetchCrossMarginAccountState(context.Background()); err == nil {
			marginBalances = marginState.Assets
		}
	}
	assessment := assessPersistedOpenHedgeExposure(entries, snapshot.SpotAssetBalances, snapshot.FuturesPositionAmounts, marginBalances, e.cfg.ResidualPositionAlertQty, e.cfg.ResidualSpotAlertUSDT)
	return !assessment.HasExposure
}
