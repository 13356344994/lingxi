package app

import (
	"net/http"
	"strings"
	"time"

	"lingxi/internal/metrics"
)

// performance_api.go 落地 §9 绩效指标的读 API(P3f.3)。数据管道:读该所(或 venue=all 组合)的 settlements,
// 只取资金费已到账的最终净利,按 FundingSettledAt 的 UTC 日聚合 + 补零,÷分配本金得日收益序列,喂
// internal/metrics 纯函数算 Sharpe/Sortino/Calmar/MaxDD/WinRate/Alpha/Beta 与权益曲线。
//
// 基准(BTC 现货日 K → Alpha/Beta)留作后续(需在面板侧引入公共 K 线拉取);当前 Benchmark 缺省 → 指标里
// BenchmarkMissing=true、Alpha/Beta=0(§9.5 小样本本就主推 MaxDD/Sortino/胜率,不阻塞)。

// PerformanceResponse 是 /api/performance 的响应:绩效指标 + 权益/水下回撤曲线点。
type PerformanceResponse struct {
	Venue   string                     `json:"venue"`
	Metrics metrics.PerformanceMetrics `json:"metrics"`
	Equity  []metrics.EquityPoint      `json:"equity"`
}

// settlementDayTimestamp 取结算记录用于按日归组的时刻:优先 FundingSettledAt(资金费到账时刻),
// 回退 CloseExecutedAt、再回退 RecordedAt(与 seedDailyRealizedPnL 的兜底口径一致)。
func settlementDayTimestamp(rec SettlementCSVRecord) (time.Time, bool) {
	for _, raw := range []string{rec.FundingSettledAt, rec.CloseExecutedAt, rec.RecordedAt} {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// venueDailyPnL 把一所的结算记录转成 (UTC 日, 最终净利) 条目:只保留 FundingIncomePending=false 的最终净利
// (§9.5 铁律,避免序列抖动/双计),无可解析时刻的记录跳过。
func venueDailyPnL(records []SettlementCSVRecord) []metrics.DailyPnL {
	var out []metrics.DailyPnL
	for _, rec := range records {
		if rec.FundingIncomePending {
			continue
		}
		ts, ok := settlementDayTimestamp(rec)
		if !ok {
			continue
		}
		out = append(out, metrics.DailyPnL{Day: metrics.UTCDay(ts), PnL: parseFloat(rec.NetPnL)})
	}
	return out
}

// computePerformanceForTargets 聚合一组所的结算为组合绩效(单所=该所;venue=all=组合:Σpnl_t / Σ分配本金,
// 在组合曲线上独立计算,§9.3 步骤 4)。
func computePerformanceForTargets(targets []*venueRuntime) (metrics.PerformanceMetrics, []metrics.EquityPoint, error) {
	var entries []metrics.DailyPnL
	var capital float64
	for _, rt := range targets {
		if rt == nil {
			continue
		}
		capital += rt.allocatedCapitalUSDT
		if rt.store == nil {
			continue
		}
		records, err := rt.store.ListSettlementRecords(0) // 0 = 全部
		if err != nil {
			return metrics.PerformanceMetrics{}, nil, err
		}
		entries = append(entries, venueDailyPnL(records)...)
	}
	series := metrics.BuildDailySeries(entries)
	returns := metrics.ReturnsFromDaily(series, capital) // capital≤0 → nil → 指标 Insufficient(诚实)
	days := make([]string, len(series))
	for i, d := range series {
		days[i] = d.Day
	}
	m := metrics.ComputePerformance(metrics.PerformanceInputs{
		Returns:          returns,
		AllocatedCapital: capital,
	})
	equity := metrics.EquityCurve(days, returns, capital)
	return m, equity, nil
}

func (s *DashboardServer) handlePerformanceAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	m, equity, err := computePerformanceForTargets(targets)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	label := venueParam(r)
	if label == "" {
		if primary := s.orchestrator.Primary(); primary != nil {
			label = primary.name
		}
	}
	writeJSON(w, PerformanceResponse{Venue: label, Metrics: m, Equity: equity})
}
