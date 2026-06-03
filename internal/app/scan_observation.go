package app

import (
	"fmt"
	"math"
	"time"
)

// ScanObservationRecord 是一条「海选阶段对单个标的的评估观测」。它记录的是【过程】而非【结果】：
// 不论该标的最终是否通过闸门，都会把它在本轮看到的资金费率/基差/跳变以及是否通过、被哪条规则拒绝、
// 距离门槛差多少落盘。用于事后回答「市场到底给了什么、我为什么没接、把门槛降到 X 能多抓多少单」。
//
// 该文件【刻意不参与 ReportStore 的 6 小时自动压缩】，以便用户能跨多天累积分析；体量很小
// （每个结算点几百行、约几百 KB/天），需要清空时走面板的「一键删除所有日志和记录」即可。
type ScanObservationRecord struct {
	RecordedAt      string  `json:"recorded_at"`
	RunID           string  `json:"run_id"`
	CycleID         string  `json:"cycle_id"`
	Symbol          string  `json:"symbol"`
	FundingTime     string  `json:"funding_time"`
	Direction       string  `json:"direction"`
	EstimatedRate   float64 `json:"estimated_rate"`
	LastFundingRate float64 `json:"last_funding_rate"`
	FundingJump     float64 `json:"funding_jump"`
	BasisRate       float64 `json:"basis_rate"`
	// BreakEvenRate 为一次单周期对冲往返的真实盈亏平衡线（coherentMinFundingRate）；
	// MinRateGate 为叠加 profile 倍率后的实际正向放行地板线，MaxRateGate 为上限。
	// 三者一起让「擦肩而过」可被量化：estimated_rate 与 min_rate_gate 的差距即近失幅度。
	BreakEvenRate float64 `json:"break_even_rate"`
	MinRateGate   float64 `json:"min_rate_gate"`
	MaxRateGate   float64 `json:"max_rate_gate"`
	IsStale       bool    `json:"is_stale"`
	Passed        bool    `json:"passed"`
	RejectReason  string  `json:"reject_reason"`
}

// AppendScanObservations 把本轮海选的逐标的评估观测追加落盘（JSONL，按行可流式分析）。
// 这是只写的可观测性旁路，失败不应影响主交易流程，调用方仅记录告警即可。
func (s *ReportStore) AppendScanObservations(records []ScanObservationRecord) error {
	if s == nil || len(records) == 0 {
		return nil
	}
	if err := appendJSONLRecords(s.scanObsPath, records); err != nil {
		return fmt.Errorf("写入扫描观测记录失败: %w", err)
	}
	return nil
}

// flushScanObservations 输出本轮海选的概要日志并把逐标的观测落盘。它是只写旁路，任何失败都只记
// 告警、绝不打断主交易流程。概要行让用户直接在 server 日志里看清「本轮有没有货、最肥的费率离平衡线
// 差多远」，无需打开 JSONL。
func (e *FundingRateSniperEngine) flushScanObservations(observations []ScanObservationRecord) {
	if e == nil || len(observations) == 0 {
		return
	}
	passed := 0
	bestRate := 0.0
	bestSymbol := ""
	for _, obs := range observations {
		if obs.Passed {
			passed++
		}
		if math.Abs(obs.EstimatedRate) > math.Abs(bestRate) {
			bestRate = obs.EstimatedRate
			bestSymbol = obs.Symbol
		}
	}
	if e.logger != nil {
		e.logger.Printf(
			"扫描观测: 评估=%d 通过=%d 最高预估费率=%.6f(%s) 平衡线=%.6f",
			len(observations), passed, bestRate, bestSymbol, coherentMinFundingRate(e.cfg),
		)
	}
	if e.reportStore != nil {
		if err := e.reportStore.AppendScanObservations(observations); err != nil && e.logger != nil {
			e.logger.Printf("写入扫描观测记录失败(不影响交易): %v", err)
		}
	}
}

// newScanObservationRecord 基于一次评估结果构造观测记录。recordedAt 用 UTC，便于跨机器对齐。
func newScanObservationRecord(
	runID string,
	cycleID string,
	recordedAt time.Time,
	symbol string,
	fundingTime time.Time,
	direction fundingCarryDirection,
	estimatedRate float64,
	lastFundingRate float64,
	fundingJump float64,
	basisRate float64,
	breakEvenRate float64,
	minRateGate float64,
	maxRateGate float64,
	isStale bool,
	passed bool,
	rejectReason string,
) ScanObservationRecord {
	return ScanObservationRecord{
		RecordedAt:      recordedAt.UTC().Format(time.RFC3339Nano),
		RunID:           runID,
		CycleID:         cycleID,
		Symbol:          symbol,
		FundingTime:     fundingTime.UTC().Format(time.RFC3339),
		Direction:       string(direction),
		EstimatedRate:   estimatedRate,
		LastFundingRate: lastFundingRate,
		FundingJump:     fundingJump,
		BasisRate:       basisRate,
		BreakEvenRate:   breakEvenRate,
		MinRateGate:     minRateGate,
		MaxRateGate:     maxRateGate,
		IsStale:         isStale,
		Passed:          passed,
		RejectReason:    rejectReason,
	}
}
