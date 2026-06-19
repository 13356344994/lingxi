package app

import (
	"sort"
	"strings"
	"time"

	"lingxi/internal/money"
)

type CrossVenueReplayPortfolioTrade struct {
	SettlementID      string  `json:"settlement_id,omitempty"`
	PositionKey       string  `json:"position_key,omitempty"`
	EntrySnapshotID   string  `json:"entry_snapshot_id,omitempty"`
	Symbol            string  `json:"symbol"`
	ChosenHighVenue   string  `json:"chosen_high_venue,omitempty"`
	ChosenLowVenue    string  `json:"chosen_low_venue,omitempty"`
	BaselineHighVenue string  `json:"baseline_high_venue,omitempty"`
	BaselineLowVenue  string  `json:"baseline_low_venue,omitempty"`
	NotionalUSDT      float64 `json:"notional_usdt,omitempty"`
	RealizedNetPnL    float64 `json:"realized_net_pnl"`
	ChosenNetEV       float64 `json:"chosen_net_ev,omitempty"`
	BaselineNetEV     float64 `json:"baseline_net_ev,omitempty"`
	NetEVGap          float64 `json:"net_ev_gap,omitempty"`
	UsedFallback      bool    `json:"used_fallback,omitempty"`
}

type CrossVenueReplayPortfolioSnapshot struct {
	MatchedTrades       int                              `json:"matched_trades"`
	ChosenReplayNetEV   float64                          `json:"chosen_replay_net_ev"`
	BaselineReplayNetEV float64                          `json:"baseline_replay_net_ev"`
	Records             []CrossVenueReplayPortfolioTrade `json:"records,omitempty"`
}

type CrossVenueCounterfactualReplayRecord struct {
	SettlementID    string    `json:"settlement_id,omitempty"`
	EntrySnapshotID string    `json:"entry_snapshot_id,omitempty"`
	SnapshotTime    time.Time `json:"snapshot_time"`
	Symbol          string    `json:"symbol"`
	NotionalUSDT    float64   `json:"notional_usdt,omitempty"`
	MaxConcurrent   int       `json:"max_concurrent,omitempty"`
	MaxPositionUSDT float64   `json:"max_position_usdt,omitempty"`
	ChosenFound     bool      `json:"chosen_found"`
	BaselineFound   bool      `json:"baseline_found"`
	ChosenNetEV     float64   `json:"chosen_net_ev,omitempty"`
	BaselineNetEV   float64   `json:"baseline_net_ev,omitempty"`
	ChosenPair      string    `json:"chosen_pair,omitempty"`
	BaselinePair    string    `json:"baseline_pair,omitempty"`
	SelectionDrag   float64   `json:"selection_drag,omitempty"`
}

type CrossVenueCounterfactualReplaySnapshot struct {
	SameOpportunitySetReady bool                                   `json:"same_opportunity_set_ready"`
	SameCostModelReady      bool                                   `json:"same_cost_model_ready"`
	SameRiskBudgetReady     bool                                   `json:"same_risk_budget_ready"`
	MatchedTrades           int                                    `json:"matched_trades"`
	MissingSnapshotTrades   int                                    `json:"missing_snapshot_trades"`
	MissingChosenTrades     int                                    `json:"missing_chosen_trades"`
	BaselineTradeCount      int                                    `json:"baseline_trade_count"`
	Records                 []CrossVenueCounterfactualReplayRecord `json:"records,omitempty"`
}

type CrossVenueBaselinePortfolioReplayRecord struct {
	SnapshotID             string    `json:"snapshot_id,omitempty"`
	SnapshotTime           time.Time `json:"snapshot_time"`
	MaxConcurrent          int       `json:"max_concurrent,omitempty"`
	MaxPositionUSDT        float64   `json:"max_position_usdt,omitempty"`
	ChosenTradeCount       int       `json:"chosen_trade_count"`
	ChosenReplayNetEV      float64   `json:"chosen_replay_net_ev"`
	BaselineTradeCount     int       `json:"baseline_trade_count"`
	BaselinePortfolioNetEV float64   `json:"baseline_portfolio_net_ev"`
	ChosenPairs            []string  `json:"chosen_pairs,omitempty"`
	BaselinePairs          []string  `json:"baseline_pairs,omitempty"`
}

type CrossVenueBaselinePortfolioReplaySnapshot struct {
	SameOpportunitySetReady   bool                                      `json:"same_opportunity_set_ready"`
	SameCostModelReady        bool                                      `json:"same_cost_model_ready"`
	SameRiskBudgetReady       bool                                      `json:"same_risk_budget_ready"`
	SnapshotCount             int                                       `json:"snapshot_count"`
	MatchedSnapshotCount      int                                       `json:"matched_snapshot_count"`
	MissingSnapshotTradeCount int                                       `json:"missing_snapshot_trade_count"`
	ChosenTradeCount          int                                       `json:"chosen_trade_count"`
	BaselineTradeCount        int                                       `json:"baseline_trade_count"`
	ChosenReplayNetEV         float64                                   `json:"chosen_replay_net_ev"`
	BaselineReplayNetEV       float64                                   `json:"baseline_replay_net_ev"`
	Records                   []CrossVenueBaselinePortfolioReplayRecord `json:"records,omitempty"`
}

type CrossVenueRollingBaselineReplayRecord struct {
	SnapshotID                    string    `json:"snapshot_id,omitempty"`
	SnapshotTime                  time.Time `json:"snapshot_time"`
	ChosenTradeCount              int       `json:"chosen_trade_count"`
	ChosenSnapshotReplayNetEV     float64   `json:"chosen_snapshot_replay_net_ev"`
	BaselineTradeCount            int       `json:"baseline_trade_count"`
	BaselineSnapshotReplayNetEV   float64   `json:"baseline_snapshot_replay_net_ev"`
	CumulativeChosenReplayNetEV   float64   `json:"cumulative_chosen_replay_net_ev"`
	CumulativeBaselineReplayNetEV float64   `json:"cumulative_baseline_replay_net_ev"`
	ChosenPairs                   []string  `json:"chosen_pairs,omitempty"`
	BaselinePairs                 []string  `json:"baseline_pairs,omitempty"`
}

type CrossVenueRollingBaselineReplaySnapshot struct {
	SameOpportunitySetReady bool                                    `json:"same_opportunity_set_ready"`
	SameCostModelReady      bool                                    `json:"same_cost_model_ready"`
	SameRiskBudgetReady     bool                                    `json:"same_risk_budget_ready"`
	SnapshotCount           int                                     `json:"snapshot_count"`
	ChosenSnapshotCount     int                                     `json:"chosen_snapshot_count"`
	BaselineSnapshotCount   int                                     `json:"baseline_snapshot_count"`
	MissedOpenSnapshots     int                                     `json:"missed_open_snapshots"`
	ChosenReplayNetEV       float64                                 `json:"chosen_replay_net_ev"`
	BaselineReplayNetEV     float64                                 `json:"baseline_replay_net_ev"`
	Records                 []CrossVenueRollingBaselineReplayRecord `json:"records,omitempty"`
}

type CrossVenueValidationSnapshot struct {
	GeneratedAt                time.Time                                 `json:"generated_at"`
	WindowStart                time.Time                                 `json:"window_start"`
	WindowEnd                  time.Time                                 `json:"window_end"`
	WindowHours                float64                                   `json:"window_hours"`
	PerpPerpRealizedNetPnL     float64                                   `json:"perp_perp_realized_net_pnl"`
	BaselineApproxNetPnL       float64                                   `json:"baseline_approx_net_pnl"`
	TradeCount                 int                                       `json:"trade_count"`
	SwitchTradeCount           int                                       `json:"switch_trade_count"`
	SwitchDrag                 float64                                   `json:"switch_drag"`
	RecoveryLoss               float64                                   `json:"recovery_loss"`
	FallbackTradeCount         int                                       `json:"fallback_trade_count"`
	FallbackNetPnL             float64                                   `json:"fallback_net_pnl"`
	FallbackDilution           float64                                   `json:"fallback_dilution"`
	SelectionDrag              float64                                   `json:"selection_drag"`
	FundingAlpha               float64                                   `json:"funding_alpha"`
	ExecutionDrag              float64                                   `json:"execution_drag"`
	FreshnessDrag              float64                                   `json:"freshness_drag"`
	FreshnessSkipCycles        int                                       `json:"freshness_skip_cycles"`
	HealthHaltCycles           int                                       `json:"health_halt_cycles"`
	OpeningsDisabledCycles     int                                       `json:"openings_disabled_cycles"`
	ScanSnapshotCount          int                                       `json:"scan_snapshot_count"`
	ScanSnapshotTotalCount     int                                       `json:"scan_snapshot_total_count"`
	ReplayMatchedTrades        int                                       `json:"replay_matched_trades"`
	EntrySnapshotLinkedTrades  int                                       `json:"entry_snapshot_linked_trades"`
	MissingEntrySnapshotTrades int                                       `json:"missing_entry_snapshot_trades"`
	ReplayReady                bool                                      `json:"replay_ready"`
	SameOpportunitySetReady    bool                                      `json:"same_opportunity_set_ready"`
	SameCostModelReady         bool                                      `json:"same_cost_model_ready"`
	SameRiskBudgetReady        bool                                      `json:"same_risk_budget_ready"`
	ReplayPortfolio            CrossVenueReplayPortfolioSnapshot         `json:"replay_portfolio"`
	CounterfactualReplay       CrossVenueCounterfactualReplaySnapshot    `json:"counterfactual_replay"`
	BaselinePortfolioReplay    CrossVenueBaselinePortfolioReplaySnapshot `json:"baseline_portfolio_replay"`
	RollingBaselineReplay      CrossVenueRollingBaselineReplaySnapshot   `json:"rolling_baseline_replay"`
	NoTradeReasonsTop          []CrossVenueReasonCount                   `json:"no_trade_reasons_top,omitempty"`
	Methodology                []string                                  `json:"methodology,omitempty"`
}

func (e *CrossVenueExecutor) ValidationSnapshot(window time.Duration) (CrossVenueValidationSnapshot, error) {
	now := e.nowFn()
	if window <= 0 {
		window = 24 * time.Hour
	}
	return e.ValidationSnapshotSince(now.Add(-window), now)
}

// ValidationSnapshotSince 汇总 [since, until] 窗口内的结算真值与 scan snapshot 回放对照。
func (e *CrossVenueExecutor) ValidationSnapshotSince(since, until time.Time) (CrossVenueValidationSnapshot, error) {
	now := until
	if now.IsZero() {
		now = e.nowFn()
	}
	settlements, err := e.loadAllCrossVenueSettlements()
	if err != nil {
		return CrossVenueValidationSnapshot{}, err
	}
	cycles, err := e.loadCycleExplains(since)
	if err != nil {
		return CrossVenueValidationSnapshot{}, err
	}
	allScans, err := loadCrossVenueScanSnapshots(e.recordDir, time.Time{})
	if err != nil {
		return CrossVenueValidationSnapshot{}, err
	}
	scans := filterCrossVenueScanSnapshotsSince(allScans, since)
	windowHours := now.Sub(since).Hours()
	if windowHours < 0 {
		windowHours = 0
	}
	out := CrossVenueValidationSnapshot{
		GeneratedAt: now,
		WindowStart: since,
		WindowEnd:   now,
		WindowHours: windowHours,
		Methodology: []string{
			"已实现净利=窗口内 crossvenue/settlements.jsonl(perp-perp)的 net_pnl 真值汇总(与今日收益面板口径一致时用北京日界)",
			"回放对照需 settlement 能关联 scan_snapshots(优先 entry_snapshot_id,次选 current_snapshot_id,再按开仓时刻就近匹配)",
			"execution_drag=手续费+不利滑点(仅计正值滑点,价格改善不计为负 drag)",
			"selection_drag=同轮 baseline 最佳 NetEV − 实际 chosen NetEV(仅 replay 链路就绪时有效)",
			"replay_ready=窗口内至少一笔 settlement 能在 scan snapshot 中匹配实际成交机会,且快照含 opportunities",
		},
	}
	out.ScanSnapshotCount = len(scans)
	out.ScanSnapshotTotalCount = len(allScans)
	out.SameOpportunitySetReady = crossVenueValidationHasOpportunities(allScans)
	out.SameCostModelReady = len(scans) > 0
	out.SameRiskBudgetReady = crossVenueValidationHasRiskBudget(scans)
	out.CounterfactualReplay.SameOpportunitySetReady = out.SameOpportunitySetReady
	out.CounterfactualReplay.SameCostModelReady = out.SameCostModelReady
	out.CounterfactualReplay.SameRiskBudgetReady = out.SameRiskBudgetReady
	out.BaselinePortfolioReplay.SameOpportunitySetReady = out.SameOpportunitySetReady
	out.BaselinePortfolioReplay.SameCostModelReady = out.SameCostModelReady
	out.BaselinePortfolioReplay.SameRiskBudgetReady = out.SameRiskBudgetReady
	out.RollingBaselineReplay.SameOpportunitySetReady = out.SameOpportunitySetReady
	out.RollingBaselineReplay.SameCostModelReady = out.SameCostModelReady
	out.RollingBaselineReplay.SameRiskBudgetReady = out.SameRiskBudgetReady
	reasonCounts := make(map[string]int)
	scanByID := make(map[string]crossVenueScanLogRecord, len(allScans))
	settlementsBySnapshot := make(map[string][]CrossVenueSettlement)
	for _, rec := range allScans {
		snapshotID := strings.TrimSpace(rec.SnapshotID)
		if snapshotID == "" {
			continue
		}
		scanByID[snapshotID] = rec
	}
	orderedAllScans := append([]crossVenueScanLogRecord(nil), allScans...)
	sort.Slice(orderedAllScans, func(i, j int) bool {
		if orderedAllScans[i].ScannedAt.Equal(orderedAllScans[j].ScannedAt) {
			return orderedAllScans[i].SnapshotID < orderedAllScans[j].SnapshotID
		}
		return orderedAllScans[i].ScannedAt.Before(orderedAllScans[j].ScannedAt)
	})
	for _, rec := range settlements {
		if rec.CloseTime.Before(since) || rec.CloseTime.After(now) {
			continue
		}
		out.TradeCount++
		out.PerpPerpRealizedNetPnL = money.SumUSDT(out.PerpPerpRealizedNetPnL, rec.NetPnL)
		out.FundingAlpha = money.SumUSDT(out.FundingAlpha, rec.RealizedFunding)
		out.ExecutionDrag = money.SumUSDT(out.ExecutionDrag, crossVenueExecutionDragUSDT(rec))
		recoveryLoss := money.SumUSDT(rec.RollbackCost, rec.RebalanceCost)
		out.RecoveryLoss = money.SumUSDT(out.RecoveryLoss, recoveryLoss)
		if rec.UsedFallback {
			out.FallbackTradeCount++
			out.FallbackNetPnL = money.SumUSDT(out.FallbackNetPnL, rec.NetPnL)
		}
		if rec.SwitchExit {
			out.SwitchTradeCount++
			out.SwitchDrag = money.SumUSDT(out.SwitchDrag, recoveryLoss)
		}
		scanRec, linkedSnapshotID, linked := crossVenueResolveSettlementEntrySnapshot(rec, scanByID, orderedAllScans)
		if linked {
			out.EntrySnapshotLinkedTrades++
			settlementsBySnapshot[linkedSnapshotID] = append(settlementsBySnapshot[linkedSnapshotID], rec)
			chosen, chosenOK := crossVenueFindLoggedOpportunity(scanRec.Opportunities, rec)
			best, bestOK := crossVenueBestLoggedOpportunity(scanRec.Opportunities, rec.NotionalUSDT)
			replayRec := CrossVenueCounterfactualReplayRecord{
				SettlementID:    rec.SettlementID,
				EntrySnapshotID: linkedSnapshotID,
				SnapshotTime:    scanRec.ScannedAt,
				Symbol:          rec.Symbol,
				NotionalUSDT:    rec.NotionalUSDT,
				MaxConcurrent:   scanRec.MaxConcurrent,
				MaxPositionUSDT: scanRec.MaxPositionUSDT,
				ChosenFound:     chosenOK,
				BaselineFound:   bestOK,
			}
			if chosenOK {
				replayRec.ChosenNetEV = chosen.NetEV
				replayRec.ChosenPair = chosen.HighVenue + "/" + chosen.LowVenue
				out.ReplayPortfolio.ChosenReplayNetEV = money.SumUSDT(out.ReplayPortfolio.ChosenReplayNetEV, chosen.NetEV)
				out.CounterfactualReplay.MatchedTrades++
			} else {
				out.CounterfactualReplay.MissingChosenTrades++
			}
			if bestOK {
				replayRec.BaselineNetEV = best.NetEV
				replayRec.BaselinePair = best.HighVenue + "/" + best.LowVenue
				out.ReplayPortfolio.BaselineReplayNetEV = money.SumUSDT(out.ReplayPortfolio.BaselineReplayNetEV, best.NetEV)
				out.CounterfactualReplay.BaselineTradeCount++
			}
			if chosenOK {
				out.ReplayMatchedTrades++
				out.ReplayPortfolio.MatchedTrades++
			}
			if chosenOK && bestOK {
				drag := best.NetEV - chosen.NetEV
				replayRec.SelectionDrag = drag
				if drag > 0 {
					out.SelectionDrag = money.SumUSDT(out.SelectionDrag, drag)
					if rec.UsedFallback {
						out.FallbackDilution = money.SumUSDT(out.FallbackDilution, drag)
					}
				}
				out.ReplayPortfolio.Records = append(out.ReplayPortfolio.Records, CrossVenueReplayPortfolioTrade{
					SettlementID:      rec.SettlementID,
					PositionKey:       rec.PositionKey,
					EntrySnapshotID:   rec.EntrySnapshotID,
					Symbol:            rec.Symbol,
					ChosenHighVenue:   chosen.HighVenue,
					ChosenLowVenue:    chosen.LowVenue,
					BaselineHighVenue: best.HighVenue,
					BaselineLowVenue:  best.LowVenue,
					NotionalUSDT:      rec.NotionalUSDT,
					RealizedNetPnL:    rec.NetPnL,
					ChosenNetEV:       chosen.NetEV,
					BaselineNetEV:     best.NetEV,
					NetEVGap:          drag,
					UsedFallback:      rec.UsedFallback,
				})
			}
			out.CounterfactualReplay.Records = append(out.CounterfactualReplay.Records, replayRec)
		} else {
			out.MissingEntrySnapshotTrades++
			out.CounterfactualReplay.MissingSnapshotTrades++
		}
	}
	out.ReplayReady = out.ReplayMatchedTrades > 0 && out.SameOpportunitySetReady
	for snapshotID, grouped := range settlementsBySnapshot {
		scanRec, ok := scanByID[snapshotID]
		if !ok {
			out.BaselinePortfolioReplay.MissingSnapshotTradeCount += len(grouped)
			continue
		}
		record := CrossVenueBaselinePortfolioReplayRecord{
			SnapshotID:      snapshotID,
			SnapshotTime:    scanRec.ScannedAt,
			MaxConcurrent:   scanRec.MaxConcurrent,
			MaxPositionUSDT: scanRec.MaxPositionUSDT,
		}
		for _, settlement := range grouped {
			chosen, chosenOK := crossVenueFindLoggedOpportunity(scanRec.Opportunities, settlement)
			record.ChosenTradeCount++
			out.BaselinePortfolioReplay.ChosenTradeCount++
			if chosenOK {
				record.ChosenReplayNetEV = money.SumUSDT(record.ChosenReplayNetEV, chosen.NetEV)
				record.ChosenPairs = append(record.ChosenPairs, chosen.HighVenue+"/"+chosen.LowVenue)
				out.BaselinePortfolioReplay.ChosenReplayNetEV = money.SumUSDT(out.BaselinePortfolioReplay.ChosenReplayNetEV, chosen.NetEV)
			}
		}
		baselineOpps := crossVenueReplayPortfolioOpportunities(scanRec.Opportunities, scanRec.MaxConcurrent)
		record.BaselineTradeCount = len(baselineOpps)
		out.BaselinePortfolioReplay.BaselineTradeCount += len(baselineOpps)
		for _, opp := range baselineOpps {
			record.BaselinePortfolioNetEV = money.SumUSDT(record.BaselinePortfolioNetEV, opp.NetEV)
			record.BaselinePairs = append(record.BaselinePairs, opp.HighVenue+"/"+opp.LowVenue)
			out.BaselinePortfolioReplay.BaselineReplayNetEV = money.SumUSDT(out.BaselinePortfolioReplay.BaselineReplayNetEV, opp.NetEV)
		}
		out.BaselinePortfolioReplay.SnapshotCount++
		out.BaselinePortfolioReplay.MatchedSnapshotCount++
		out.BaselinePortfolioReplay.Records = append(out.BaselinePortfolioReplay.Records, record)
	}
	orderedScans := append([]crossVenueScanLogRecord(nil), scans...)
	sort.Slice(orderedScans, func(i, j int) bool {
		if orderedScans[i].ScannedAt.Equal(orderedScans[j].ScannedAt) {
			return orderedScans[i].SnapshotID < orderedScans[j].SnapshotID
		}
		return orderedScans[i].ScannedAt.Before(orderedScans[j].ScannedAt)
	})
	for _, scanRec := range orderedScans {
		record := CrossVenueRollingBaselineReplayRecord{
			SnapshotID:   scanRec.SnapshotID,
			SnapshotTime: scanRec.ScannedAt,
		}
		for _, settlement := range settlementsBySnapshot[strings.TrimSpace(scanRec.SnapshotID)] {
			chosen, chosenOK := crossVenueFindLoggedOpportunity(scanRec.Opportunities, settlement)
			record.ChosenTradeCount++
			if chosenOK {
				record.ChosenSnapshotReplayNetEV = money.SumUSDT(record.ChosenSnapshotReplayNetEV, chosen.NetEV)
				record.ChosenPairs = append(record.ChosenPairs, chosen.HighVenue+"/"+chosen.LowVenue)
			}
		}
		baselineOpps := crossVenueReplayPortfolioOpportunities(scanRec.Opportunities, scanRec.MaxConcurrent)
		record.BaselineTradeCount = len(baselineOpps)
		for _, opp := range baselineOpps {
			record.BaselineSnapshotReplayNetEV = money.SumUSDT(record.BaselineSnapshotReplayNetEV, opp.NetEV)
			record.BaselinePairs = append(record.BaselinePairs, opp.HighVenue+"/"+opp.LowVenue)
		}
		if record.ChosenTradeCount > 0 {
			out.RollingBaselineReplay.ChosenSnapshotCount++
		}
		if record.BaselineTradeCount > 0 {
			out.RollingBaselineReplay.BaselineSnapshotCount++
		}
		if record.ChosenTradeCount == 0 && record.BaselineTradeCount > 0 {
			out.RollingBaselineReplay.MissedOpenSnapshots++
		}
		out.RollingBaselineReplay.SnapshotCount++
		out.RollingBaselineReplay.ChosenReplayNetEV = money.SumUSDT(out.RollingBaselineReplay.ChosenReplayNetEV, record.ChosenSnapshotReplayNetEV)
		out.RollingBaselineReplay.BaselineReplayNetEV = money.SumUSDT(out.RollingBaselineReplay.BaselineReplayNetEV, record.BaselineSnapshotReplayNetEV)
		record.CumulativeChosenReplayNetEV = out.RollingBaselineReplay.ChosenReplayNetEV
		record.CumulativeBaselineReplayNetEV = out.RollingBaselineReplay.BaselineReplayNetEV
		out.RollingBaselineReplay.Records = append(out.RollingBaselineReplay.Records, record)
	}
	out.BaselineApproxNetPnL = money.SumUSDT(out.PerpPerpRealizedNetPnL, out.SwitchDrag)
	for _, cycle := range cycles {
		switch {
		case cycle.HaltReason == "snapshot_too_old":
			out.FreshnessSkipCycles++
			if scanRec, ok := scanByID[strings.TrimSpace(cycle.SnapshotID)]; ok {
				if best, bestOK := crossVenueBestLoggedOpportunity(scanRec.Opportunities, 0); bestOK && best.NetEV > 0 {
					out.FreshnessDrag = money.SumUSDT(out.FreshnessDrag, best.NetEV)
				}
			}
		case strings.HasPrefix(cycle.HaltReason, "health_halt:"):
			out.HealthHaltCycles++
		case cycle.HaltReason == "openings_disabled":
			out.OpeningsDisabledCycles++
		}
		for _, rc := range cycle.NoTradeReasonsTop {
			reason := strings.TrimSpace(rc.Reason)
			if reason == "" || rc.Count <= 0 {
				continue
			}
			reasonCounts[reason] += rc.Count
		}
	}
	out.NoTradeReasonsTop = buildCrossVenueReasonCounts(expandCrossVenueReasons(reasonCounts), 5)
	return out, nil
}

func expandCrossVenueReasons(counts map[string]int) []string {
	if len(counts) == 0 {
		return nil
	}
	out := make([]string, 0)
	for reason, count := range counts {
		for range count {
			out = append(out, reason)
		}
	}
	return out
}

func crossVenueValidationHasRiskBudget(scans []crossVenueScanLogRecord) bool {
	for _, rec := range scans {
		if rec.MaxConcurrent > 0 || rec.MaxPositionUSDT > 0 {
			return true
		}
	}
	return false
}

func crossVenueValidationHasOpportunities(scans []crossVenueScanLogRecord) bool {
	for _, rec := range scans {
		if len(rec.Opportunities) > 0 {
			return true
		}
	}
	return false
}

func crossVenueFindLoggedOpportunity(opps []CrossVenueOpportunity, settlement CrossVenueSettlement) (CrossVenueOpportunity, bool) {
	for _, opp := range opps {
		if opp.Symbol != settlement.Symbol {
			continue
		}
		if opp.HighVenue != settlement.HighVenue || opp.LowVenue != settlement.LowVenue {
			continue
		}
		return opp, true
	}
	return CrossVenueOpportunity{}, false
}

func crossVenueBestLoggedOpportunity(opps []CrossVenueOpportunity, targetNotional float64) (CrossVenueOpportunity, bool) {
	best, ok := crossVenueBestLoggedOpportunityWithNotional(opps, targetNotional)
	if ok {
		return best, true
	}
	if targetNotional > 0 {
		return crossVenueBestLoggedOpportunityWithNotional(opps, 0)
	}
	return CrossVenueOpportunity{}, false
}

func crossVenueBestLoggedOpportunityWithNotional(opps []CrossVenueOpportunity, targetNotional float64) (CrossVenueOpportunity, bool) {
	best := CrossVenueOpportunity{}
	found := false
	for _, opp := range opps {
		if opp.NetEV <= 0 {
			continue
		}
		if targetNotional > 0 && opp.TradableNotional > 0 && opp.TradableNotional+1e-9 < targetNotional {
			continue
		}
		if !found || opp.NetEV > best.NetEV || (opp.NetEV == best.NetEV && opp.RankScore > best.RankScore) {
			best = opp
			found = true
		}
	}
	return best, found
}

func crossVenueReplayPortfolioOpportunities(opps []CrossVenueOpportunity, maxConcurrent int) []CrossVenueOpportunity {
	if len(opps) == 0 {
		return nil
	}
	candidates := make([]CrossVenueOpportunity, 0, len(opps))
	for _, opp := range opps {
		if opp.NetEV <= 0 {
			continue
		}
		candidates = append(candidates, opp)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].RankScore == candidates[j].RankScore {
			if candidates[i].NetEV == candidates[j].NetEV {
				return candidates[i].Symbol < candidates[j].Symbol
			}
			return candidates[i].NetEV > candidates[j].NetEV
		}
		return candidates[i].RankScore > candidates[j].RankScore
	})
	if maxConcurrent <= 0 || maxConcurrent > len(candidates) {
		maxConcurrent = len(candidates)
	}
	out := make([]CrossVenueOpportunity, 0, maxConcurrent)
	seenSymbols := make(map[string]struct{}, maxConcurrent)
	for _, opp := range candidates {
		if _, exists := seenSymbols[opp.Symbol]; exists {
			continue
		}
		out = append(out, opp)
		seenSymbols[opp.Symbol] = struct{}{}
		if len(out) >= maxConcurrent {
			break
		}
	}
	return out
}

func filterCrossVenueScanSnapshotsSince(scans []crossVenueScanLogRecord, since time.Time) []crossVenueScanLogRecord {
	if since.IsZero() {
		return append([]crossVenueScanLogRecord(nil), scans...)
	}
	out := make([]crossVenueScanLogRecord, 0, len(scans))
	for _, rec := range scans {
		if !rec.ScannedAt.Before(since) {
			out = append(out, rec)
		}
	}
	return out
}

func crossVenueExecutionDragUSDT(rec CrossVenueSettlement) float64 {
	drag := rec.TotalFees
	if rec.OpenSlippage > 0 {
		drag = money.SumUSDT(drag, rec.OpenSlippage)
	}
	if rec.CloseSlippage > 0 {
		drag = money.SumUSDT(drag, rec.CloseSlippage)
	}
	return drag
}

func crossVenueScanMatchesSettlement(scan crossVenueScanLogRecord, settlement CrossVenueSettlement) bool {
	for _, opp := range scan.Opportunities {
		if opp.Symbol != settlement.Symbol {
			continue
		}
		if opp.HighVenue != settlement.HighVenue || opp.LowVenue != settlement.LowVenue {
			continue
		}
		return true
	}
	return false
}

// crossVenueResolveSettlementEntrySnapshot 为 settlement 关联开仓时的 scan snapshot。
// 顺序: entry_snapshot_id → current_snapshot_id → 开仓时刻前最近且 symbol/venue 匹配的快照。
func crossVenueResolveSettlementEntrySnapshot(
	settlement CrossVenueSettlement,
	scanByID map[string]crossVenueScanLogRecord,
	orderedScans []crossVenueScanLogRecord,
) (crossVenueScanLogRecord, string, bool) {
	if id := strings.TrimSpace(settlement.EntrySnapshotID); id != "" {
		if rec, ok := scanByID[id]; ok {
			return rec, id, true
		}
	}
	if id := strings.TrimSpace(settlement.CurrentSnapshotID); id != "" {
		if rec, ok := scanByID[id]; ok {
			return rec, id, true
		}
	}
	anchor := settlement.OpenTime
	if anchor.IsZero() {
		anchor = settlement.CloseTime
	}
	for i := len(orderedScans) - 1; i >= 0; i-- {
		rec := orderedScans[i]
		if rec.ScannedAt.After(anchor) {
			continue
		}
		if !crossVenueScanMatchesSettlement(rec, settlement) {
			continue
		}
		return rec, strings.TrimSpace(rec.SnapshotID), true
	}
	return crossVenueScanLogRecord{}, "", false
}
