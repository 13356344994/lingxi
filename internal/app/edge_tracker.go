package app

// edge_tracker.go 实现 S6-A「滚动窗实测净 edge 跟踪」(R27):在最近 N 个已结算(funding 实收后)周期上,
// 滚动统计实测净 edge 率(Σ NetPnL / Σ 名义)、胜率与样本数。它是 S6-B 自动降档/停机的输入信号。
// 设计:窗口与统计是纯逻辑(computeRealizedEdgeStats / decideEdgeHealth 可单测),与引擎状态、锁、I/O 解耦。

// edgeSample 是一个已结算周期的最小 edge 观测样本。
type edgeSample struct {
	NetPnL       float64
	NotionalUSDT float64
}

// realizedEdgeStats 是滚动窗的聚合统计(纯值)。
type realizedEdgeStats struct {
	Samples     int     `json:"samples"`
	SumNetPnL   float64 `json:"sum_net_pnl"`
	SumNotional float64 `json:"sum_notional_usdt"`
	NetEdgeRate float64 `json:"net_edge_rate"` // Σ NetPnL / Σ 名义;Σ 名义≤0 时为 0
	WinRate     float64 `json:"win_rate"`      // NetPnL>0 的样本占比
}

// realizedEdgeWindow 是一个定长滚动窗(FIFO),保存最近 maxSamples 个已结算周期的 edge 样本。
type realizedEdgeWindow struct {
	maxSamples int
	samples    []edgeSample
}

// newRealizedEdgeWindow 构造滚动窗;maxSamples<=0 表示不跟踪(observe 为 no-op,stats 恒空)。
func newRealizedEdgeWindow(maxSamples int) *realizedEdgeWindow {
	return &realizedEdgeWindow{maxSamples: maxSamples}
}

// observe 追加一个已结算周期样本,超出窗口容量则丢弃最旧。maxSamples<=0 时 no-op。
func (w *realizedEdgeWindow) observe(netPnL, notionalUSDT float64) {
	if w == nil || w.maxSamples <= 0 {
		return
	}
	w.samples = append(w.samples, edgeSample{NetPnL: netPnL, NotionalUSDT: notionalUSDT})
	if len(w.samples) > w.maxSamples {
		w.samples = w.samples[len(w.samples)-w.maxSamples:]
	}
}

// stats 返回当前窗口的聚合统计。
func (w *realizedEdgeWindow) stats() realizedEdgeStats {
	if w == nil {
		return realizedEdgeStats{}
	}
	return computeRealizedEdgeStats(w.samples)
}

// computeRealizedEdgeStats 由样本集计算聚合统计(纯函数,可单测)。
func computeRealizedEdgeStats(samples []edgeSample) realizedEdgeStats {
	stats := realizedEdgeStats{Samples: len(samples)}
	if len(samples) == 0 {
		return stats
	}
	wins := 0
	for _, s := range samples {
		stats.SumNetPnL += s.NetPnL
		stats.SumNotional += s.NotionalUSDT
		if s.NetPnL > 0 {
			wins++
		}
	}
	if stats.SumNotional > 0 {
		stats.NetEdgeRate = stats.SumNetPnL / stats.SumNotional
	}
	stats.WinRate = float64(wins) / float64(len(samples))
	return stats
}

// decideEdgeHealth 判定滚动窗实测 edge 是否健康(纯函数,供 S6-B 降档/停机决策)。样本数不足(< minSamples)时
// 视为「尚不可判定」→ healthy=true(不因数据不足误降档);minNetEdgeRate<=0 表示不启用 edge 阈值闸(恒健康)。
func decideEdgeHealth(stats realizedEdgeStats, minSamples int, minNetEdgeRate float64) (healthy bool, reason string) {
	if minNetEdgeRate <= 0 {
		return true, ""
	}
	if minSamples > 0 && stats.Samples < minSamples {
		return true, ""
	}
	if stats.NetEdgeRate < minNetEdgeRate {
		return false, "rolling_net_edge_below_threshold"
	}
	return true, ""
}

// observeRealizedEdge 把一个已结算(funding 实收后)周期记入滚动窗并落观测日志。受 platformRiskMu 保护(与
// noteCycleFinalSettlement 同路径,worker goroutine 调用)。edgeWindow 未启用时 no-op。
func (e *FundingRateSniperEngine) observeRealizedEdge(netPnL, notionalUSDT float64) {
	if e == nil || e.edgeWindow == nil {
		return
	}
	e.platformRiskMu.Lock()
	e.edgeWindow.observe(netPnL, notionalUSDT)
	stats := e.edgeWindow.stats()
	e.platformRiskMu.Unlock()
	if e.logger != nil {
		e.logger.Printf("实测净 edge 观测: 样本=%d 净edge率=%.6f 胜率=%.1f%% Σ净利=%.4f Σ名义=%.2f",
			stats.Samples, stats.NetEdgeRate, stats.WinRate*100, stats.SumNetPnL, stats.SumNotional)
	}
}

// realizedEdgeStatsSnapshot 读取当前滚动窗统计快照(锁安全)。未启用时返回零值。
func (e *FundingRateSniperEngine) realizedEdgeStatsSnapshot() realizedEdgeStats {
	if e == nil || e.edgeWindow == nil {
		return realizedEdgeStats{}
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.edgeWindow.stats()
}

// backfillRealizedEdge 启动时用已落盘的结算记录回填滚动窗,使重启后 edge 信号立即可用(不必从零重新积累)。
// 只取已结算(FundingIncomePending=false)记录,按时间正序喂入,窗口自动保留最近 N 个。
func (e *FundingRateSniperEngine) backfillRealizedEdge() {
	if e == nil || e.edgeWindow == nil || e.reportStore == nil {
		return
	}
	records, err := e.reportStore.ListSettlementRecords(0) // 降序(最新在前)
	if err != nil {
		if e.logger != nil {
			e.logger.Printf("回填实测 edge 读取结算记录失败: %v", err)
		}
		return
	}
	// 收集已结算记录(降序),截到窗口容量,再反转成时间正序喂入。
	finalized := make([]SettlementCSVRecord, 0, len(records))
	for _, rec := range records {
		if rec.FundingIncomePending {
			continue
		}
		finalized = append(finalized, rec)
		if len(finalized) >= e.edgeWindow.maxSamples {
			break
		}
	}
	for i := len(finalized) - 1; i >= 0; i-- {
		e.edgeWindow.observe(parseFloat(finalized[i].NetPnL), parseFloat(finalized[i].CycleNotionalUSDT))
	}
	if e.logger != nil && len(finalized) > 0 {
		stats := e.edgeWindow.stats()
		e.logger.Printf("启动回填实测 edge: 样本=%d 净edge率=%.6f 胜率=%.1f%%", stats.Samples, stats.NetEdgeRate, stats.WinRate*100)
	}
}
