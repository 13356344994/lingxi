package app

import (
	"sort"
	"strconv"
	"strings"
)

// 本文件从 report_store.go 拆出：仅承载落盘记录的「行编解码」(CSV 行 ↔ 结构体)、
// 残腿风险统计聚合，以及倒序克隆助手（同包搬移，行为不变），让 report_store.go 收敛到
// 「存储门面」职责并满足单文件 ≤ 800 行门禁。

func parseSettlementRows(rows [][]string) []SettlementCSVRecord {
	result := make([]SettlementCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(settlementCSVHeader) {
			continue
		}
		targets, _ := strconv.Atoi(row[3])
		hasAlert, _ := strconv.ParseBool(row[15])
		result = append(result, SettlementCSVRecord{
			RecordedAt:            row[0],
			FundingTime:           row[1],
			CloseExecutedAt:       row[2],
			Targets:               targets,
			FundingIncome:         row[4],
			ExpectedFundingIncome: row[5],
			FundingIncomeDiff:     row[6],
			SpotTradingPnL:        row[7],
			FuturesTradingPnL:     row[8],
			TotalFees:             row[9],
			NetPnL:                row[10],
			SnapshotNetPnL:        row[11],
			ResidualSpotValue:     row[12],
			SpotUSDTDelta:         row[13],
			FuturesUSDTDelta:      row[14],
			HasAlert:              hasAlert,
			Alerts:                row[16],
		})
	}
	return result
}

func parseMonitorRows(rows [][]string) []MonitorCSVRecord {
	result := make([]MonitorCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < 15 {
			continue
		}
		valid, _ := strconv.ParseBool(row[6])
		breachCount, _ := strconv.Atoi(row[7])
		record := MonitorCSVRecord{
			RecordedAt:      row[0],
			MonitorStopAt:   row[1],
			FundingTime:     row[2],
			Symbol:          row[3],
			Pair:            row[4],
			State:           row[5],
			Valid:           valid,
			BreachCount:     breachCount,
			EstimatedRate:   row[8],
			LastFundingRate: row[9],
			BasisRate:       row[10],
			ScanScoreRate:   row[11],
			LastNetProfit:   row[12],
			LastSpread:      row[13],
			InvalidReason:   row[14],
		}
		if len(row) >= len(monitorCSVHeader) {
			record.DepthStabilityRatio = row[14]
			record.SpotVisibleNotional = row[15]
			record.FuturesVisibleNotional = row[16]
			record.MarketRiskCategory = row[17]
			record.MarketRiskSeverity = row[18]
			record.MarketRiskAction = row[19]
			record.MarketRiskDetail = row[20]
			record.InvalidReason = row[21]
		}
		result = append(result, record)
	}
	return result
}

func parseBacktestRows(rows [][]string) []BacktestCSVRecord {
	result := make([]BacktestCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(backtestCSVHeader) {
			continue
		}
		trades, _ := strconv.Atoi(row[4])
		result = append(result, BacktestCSVRecord{
			RecordedAt:    row[0],
			Scenario:      row[1],
			Symbol:        row[2],
			Period:        row[3],
			Trades:        trades,
			WinRate:       row[5],
			FundingIncome: row[6],
			TradingPnL:    row[7],
			TotalFees:     row[8],
			NetPnL:        row[9],
			MaxDrawdown:   row[10],
			Notes:         row[11],
		})
	}
	return result
}

func parseBacktestRunSummaryRows(rows [][]string) []BacktestRunSummaryRecord {
	result := make([]BacktestRunSummaryRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(backtestRunCSVHeader) {
			continue
		}
		trades, _ := strconv.Atoi(row[6])
		result = append(result, BacktestRunSummaryRecord{
			RunID:         row[0],
			RunMode:       row[1],
			RecordedAt:    row[2],
			Scenario:      row[3],
			Symbol:        row[4],
			Period:        row[5],
			Trades:        trades,
			WinRate:       row[7],
			FundingIncome: row[8],
			TradingPnL:    row[9],
			TotalFees:     row[10],
			NetPnL:        row[11],
			MaxDrawdown:   row[12],
			Notes:         row[13],
		})
	}
	return result
}

func parseResidualRiskRows(rows [][]string) []ResidualRiskCSVRecord {
	result := make([]ResidualRiskCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(residualCSVHeader) {
			continue
		}
		futuresLegOK, _ := strconv.ParseBool(row[7])
		spotLegOK, _ := strconv.ParseBool(row[8])
		result = append(result, ResidualRiskCSVRecord{
			RecordedAt:         row[0],
			Scenario:           row[1],
			Symbol:             row[2],
			Period:             row[3],
			Phase:              row[4],
			EventTime:          row[5],
			MonitorState:       row[6],
			FuturesLegOK:       futuresLegOK,
			SpotLegOK:          spotLegOK,
			FuturesFillRatio:   row[9],
			SpotFillRatio:      row[10],
			FuturesFilledUSDT:  row[11],
			SpotFilledUSDT:     row[12],
			ResidualUSDT:       row[13],
			EmergencyHedgeCost: row[14],
			EmergencyHedgeFee:  row[15],
			EmergencyHedgePnL:  row[16],
			Severity:           row[17],
			Reason:             row[18],
			Notes:              row[19],
		})
	}
	return result
}

func parseExecutionRows(rows [][]string) []ExecutionIncidentRecord {
	result := make([]ExecutionIncidentRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < 9 {
			continue
		}
		record := ExecutionIncidentRecord{
			RecordedAt: row[0],
			Level:      row[1],
		}
		if len(row) >= len(executionCSVHeader) {
			record.Category = row[2]
			record.Severity = row[3]
			record.Phase = row[4]
			record.Symbol = row[5]
			record.Action = row[6]
			record.IncidentCount, _ = strconv.Atoi(row[7])
			record.IncidentLimit, _ = strconv.Atoi(row[8])
			record.Fatal, _ = strconv.ParseBool(row[9])
			record.Detail = row[10]
		} else {
			record.Phase = row[2]
			record.Symbol = row[3]
			record.Action = row[4]
			record.IncidentCount, _ = strconv.Atoi(row[5])
			record.IncidentLimit, _ = strconv.Atoi(row[6])
			record.Fatal, _ = strconv.ParseBool(row[7])
			record.Detail = row[8]
		}
		result = append(result, record)
	}
	return result
}

func executionIncidentRow(record ExecutionIncidentRecord) []string {
	return []string{
		record.RecordedAt,
		record.Level,
		record.Category,
		record.Severity,
		record.Phase,
		record.Symbol,
		record.Action,
		strconv.Itoa(record.IncidentCount),
		strconv.Itoa(record.IncidentLimit),
		strconv.FormatBool(record.Fatal),
		record.Detail,
	}
}

func buildResidualSeverityStats(records []ResidualRiskCSVRecord) []ResidualSeverityStats {
	counts := map[string]int{
		"low":    0,
		"medium": 0,
		"high":   0,
	}
	for _, record := range records {
		severity := strings.TrimSpace(record.Severity)
		if severity == "" {
			severity = "low"
		}
		counts[severity]++
	}
	return []ResidualSeverityStats{
		{Severity: "low", Count: counts["low"]},
		{Severity: "medium", Count: counts["medium"]},
		{Severity: "high", Count: counts["high"]},
	}
}

func buildResidualScenarioStats(records []ResidualRiskCSVRecord) []ResidualScenarioStats {
	type scenarioAggregate struct {
		count             int
		totalResidual     float64
		worstEmergencyPnL float64
		highRiskCount     int
	}

	aggregates := make(map[string]*scenarioAggregate)
	for _, record := range records {
		stats, ok := aggregates[record.Scenario]
		if !ok {
			stats = &scenarioAggregate{}
			aggregates[record.Scenario] = stats
		}
		stats.count++
		stats.totalResidual += parseFloat(record.ResidualUSDT)
		pnl := parseFloat(record.EmergencyHedgePnL)
		if stats.count == 1 || pnl < stats.worstEmergencyPnL {
			stats.worstEmergencyPnL = pnl
		}
		if record.Severity == "high" {
			stats.highRiskCount++
		}
	}

	scenarios := make([]string, 0, len(aggregates))
	for scenario := range aggregates {
		scenarios = append(scenarios, scenario)
	}
	sort.Strings(scenarios)

	result := make([]ResidualScenarioStats, 0, len(scenarios))
	for _, scenario := range scenarios {
		stats := aggregates[scenario]
		avgResidual := 0.0
		if stats.count > 0 {
			avgResidual = stats.totalResidual / float64(stats.count)
		}
		result = append(result, ResidualScenarioStats{
			Scenario:            scenario,
			Count:               stats.count,
			AverageResidualUSDT: formatFloat(avgResidual),
			WorstEmergencyPnL:   formatFloat(stats.worstEmergencyPnL),
			HighRiskCount:       stats.highRiskCount,
		})
	}
	return result
}

func cloneSettlementRecordsDescending(records []SettlementCSVRecord, limit int) []SettlementCSVRecord {
	if len(records) == 0 {
		return []SettlementCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneMonitorRecordsDescending(records []MonitorCSVRecord, limit int) []MonitorCSVRecord {
	if len(records) == 0 {
		return []MonitorCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneBacktestRecordsDescending(records []BacktestCSVRecord, limit int) []BacktestCSVRecord {
	if len(records) == 0 {
		return []BacktestCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneResidualRiskRecordsDescending(records []ResidualRiskCSVRecord, limit int) []ResidualRiskCSVRecord {
	if len(records) == 0 {
		return []ResidualRiskCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneExecutionIncidentsDescending(records []ExecutionIncidentRecord, limit int) []ExecutionIncidentRecord {
	if len(records) == 0 {
		return []ExecutionIncidentRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneResidualSeverityStats(records []ResidualSeverityStats) []ResidualSeverityStats {
	if len(records) == 0 {
		return []ResidualSeverityStats{}
	}
	return append([]ResidualSeverityStats(nil), records...)
}

func cloneResidualScenarioStats(records []ResidualScenarioStats) []ResidualScenarioStats {
	if len(records) == 0 {
		return []ResidualScenarioStats{}
	}
	return append([]ResidualScenarioStats(nil), records...)
}

func cloneDescending[T any](records []T, limit int) []T {
	if len(records) == 0 {
		return []T{}
	}
	size := len(records)
	if limit > 0 && limit < size {
		size = limit
	}
	result := make([]T, 0, size)
	for index := len(records) - 1; index >= 0 && len(result) < size; index-- {
		result = append(result, records[index])
	}
	return result
}
