package app

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// 本文件从 backtest_runner.go 拆出：仅承载回测样本 CSV 的读取/解析逻辑（同包搬移，
// 行为不变），让 backtest_runner.go 收敛到「编排 + 回放状态机」职责并满足单文件 ≤ 800 行门禁。

func readBacktestTradeSamples(path string) ([]BacktestTradeSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开回测样本失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取回测样本失败: %w", err)
	}
	if len(rows) <= 1 {
		return nil, fmt.Errorf("回测样本为空: %s", path)
	}

	samples := make([]BacktestTradeSample, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 10 {
			continue
		}
		win, _ := strconv.ParseBool(strings.TrimSpace(row[7]))
		samples = append(samples, BacktestTradeSample{
			Scenario:      strings.TrimSpace(row[0]),
			Symbol:        strings.TrimSpace(row[1]),
			Period:        strings.TrimSpace(row[2]),
			TradeID:       strings.TrimSpace(row[3]),
			FundingIncome: parseFloat(row[4]),
			TradingPnL:    parseFloat(row[5]),
			TotalFees:     parseFloat(row[6]),
			Win:           win,
			Drawdown:      parseFloat(row[8]),
			Notes:         strings.TrimSpace(row[9]),
		})
	}
	return samples, nil
}

func readBacktestReplaySamples(path string) ([]BacktestReplaySample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开历史回放样本失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取历史回放样本失败: %w", err)
	}
	if len(rows) <= 1 {
		return nil, fmt.Errorf("历史回放样本为空: %s", path)
	}

	samples := make([]BacktestReplaySample, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 13 {
			continue
		}
		liquidityOK, _ := strconv.ParseBool(strings.TrimSpace(row[11]))
		samples = append(samples, BacktestReplaySample{
			Scenario:        strings.TrimSpace(row[0]),
			Symbol:          strings.TrimSpace(row[1]),
			Period:          strings.TrimSpace(row[2]),
			Timestamp:       strings.TrimSpace(row[3]),
			FundingRate:     parseFloat(row[4]),
			BasisRate:       parseFloat(row[5]),
			EntrySpreadRate: parseFloat(row[6]),
			ExitSpreadRate:  parseFloat(row[7]),
			FeeRate:         parseFloat(row[8]),
			NotionalUSDT:    parseFloat(row[9]),
			DrawdownRate:    parseFloat(row[10]),
			LiquidityOK:     liquidityOK,
			Notes:           strings.TrimSpace(row[12]),
		})
	}
	return samples, nil
}

func readBacktestTimelineSamples(path string) ([]BacktestTimelineSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开时序回放样本失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取时序回放样本失败: %w", err)
	}
	if len(rows) <= 1 {
		return nil, fmt.Errorf("时序回放样本为空: %s", path)
	}

	samples := make([]BacktestTimelineSample, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 20 {
			continue
		}
		liquidityOK, _ := strconv.ParseBool(strings.TrimSpace(row[11]))
		futuresLegOK, _ := strconv.ParseBool(strings.TrimSpace(row[15]))
		spotLegOK, _ := strconv.ParseBool(strings.TrimSpace(row[16]))
		samples = append(samples, BacktestTimelineSample{
			Scenario:             strings.TrimSpace(row[0]),
			Symbol:               strings.TrimSpace(row[1]),
			Period:               strings.TrimSpace(row[2]),
			Phase:                strings.TrimSpace(row[3]),
			Timestamp:            strings.TrimSpace(row[4]),
			FundingRate:          parseFloat(row[5]),
			BasisRate:            parseFloat(row[6]),
			SpreadRate:           parseFloat(row[7]),
			FeeRate:              parseFloat(row[8]),
			NotionalUSDT:         parseFloat(row[9]),
			DrawdownRate:         parseFloat(row[10]),
			LiquidityOK:          liquidityOK,
			ScanScoreRate:        parseFloat(row[12]),
			FuturesLegSpreadRate: parseFloat(row[13]),
			SpotLegSpreadRate:    parseFloat(row[14]),
			FuturesLegOK:         futuresLegOK,
			SpotLegOK:            spotLegOK,
			FuturesFillRatio:     defaultFillRatio(row[17]),
			SpotFillRatio:        defaultFillRatio(row[18]),
			Notes:                strings.TrimSpace(row[19]),
		})
	}

	sort.Slice(samples, func(i, j int) bool {
		left := samples[i]
		right := samples[j]
		if left.Scenario != right.Scenario {
			return left.Scenario < right.Scenario
		}
		if left.Symbol != right.Symbol {
			return left.Symbol < right.Symbol
		}
		if left.Period != right.Period {
			return left.Period < right.Period
		}
		if left.Timestamp != right.Timestamp {
			return left.Timestamp < right.Timestamp
		}
		return phaseOrder(left.Phase) < phaseOrder(right.Phase)
	})

	return samples, nil
}
