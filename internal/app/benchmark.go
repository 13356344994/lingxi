package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"lingxi/internal/metrics"
	"lingxi/internal/venue/binance"
)

// benchmark.go 落地 §9.1 的 Alpha/Beta 基准:固定 Binance 现货 BTCUSDT 日 K(公共、与被测 venue 无关)。
// 即便 binance.enabled=false 也只读拉公共 K 线;基准拉取为**尽力而为**——失败 → 无基准 → ComputePerformance
// 置 BenchmarkMissing(Alpha/Beta=0,不静默换源/补 0),绝不让 /api/performance 因基准失败而整体报错。

// benchmarkSpotBaseURL 是基准日 K 的公共数据源(Binance 现货 REST,无需鉴权)。
const benchmarkSpotBaseURL = "https://api.binance.com"

// benchmarkFetchTimeout 基准日 K 拉取超时(尽力而为,不阻塞绩效响应)。
const benchmarkFetchTimeout = 6 * time.Second

// benchmarkDailyReturns 把「UTC 日 → BTC 收盘价」映射对齐到给定 series 日序,产出与 returns **同序同长**的基准
// 日收益 b_t:b_t = close(d)/close(d−1) − 1(d−1 为 d 的前一日历日)。任一日或其前一日收盘缺失/非正 →
// 无法对齐完整基准 → 返回 nil(调用方据此判 BenchmarkMissing,§9.1 不静默补 0/换源)。纯函数。
func benchmarkDailyReturns(closesByDay map[string]float64, days []string) []float64 {
	if len(days) == 0 || len(closesByDay) == 0 {
		return nil
	}
	out := make([]float64, len(days))
	for i, day := range days {
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			return nil
		}
		prevDay := d.AddDate(0, 0, -1).UTC().Format("2006-01-02")
		cur, okCur := closesByDay[day]
		prev, okPrev := closesByDay[prevDay]
		if !okCur || !okPrev || prev <= 0 || cur <= 0 {
			return nil
		}
		out[i] = cur/prev - 1.0
	}
	return out
}

// parseDailyCloses 解析 Binance klines 响应([][]any:[openTime(ms), open, high, low, close, ...]),
// 返回 UTC 日 → 收盘价。openTime 为该日 UTC 00:00。纯函数(便于单测,脱网)。
func parseDailyCloses(rows [][]any) map[string]float64 {
	out := make(map[string]float64, len(rows))
	for _, row := range rows {
		if len(row) < 5 {
			continue
		}
		openMs := int64(parseAnyFloat(row[0]))
		if openMs <= 0 {
			continue
		}
		close := parseAnyFloat(row[4])
		if close <= 0 {
			continue
		}
		day := time.UnixMilli(openMs).UTC().Format("2006-01-02")
		out[day] = close
	}
	return out
}

// fetchBTCDailyCloses 拉取 [startDay−1, endDay] 的 BTCUSDT 现货日 K 收盘价(多取前一日以算 startDay 的日收益)。
// 公共端点、无鉴权、紧超时。失败返回 err,由调用方降级为无基准。
func fetchBTCDailyCloses(ctx context.Context, httpClient *http.Client, baseURL string, startDay, endDay string) (map[string]float64, error) {
	start, err := time.Parse("2006-01-02", startDay)
	if err != nil {
		return nil, fmt.Errorf("基准起始日非法: %w", err)
	}
	end, err := time.Parse("2006-01-02", endDay)
	if err != nil {
		return nil, fmt.Errorf("基准结束日非法: %w", err)
	}
	// 多取前一日(算首日收益需要),endTime 取结束日 23:59:59.999 以含当日 K。
	startMs := start.AddDate(0, 0, -1).UTC().UnixMilli()
	endMs := end.UTC().Add(24*time.Hour - time.Millisecond).UnixMilli()
	url := fmt.Sprintf("%s%s?symbol=%s&interval=1d&startTime=%d&endTime=%d&limit=1000",
		baseURL, binance.PathSpotKlines, metrics.DefaultBenchmarkSymbol, startMs, endMs)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("基准 K 线 HTTP %d", resp.StatusCode)
	}
	var rows [][]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	return parseDailyCloses(rows), nil
}

// fetchBenchmarkReturns 为给定 series 日序尽力拉取并对齐 BTC 基准日收益;任何失败/缺日 → 返回 nil(无基准)。
func (s *DashboardServer) fetchBenchmarkReturns(ctx context.Context, days []string) []float64 {
	if len(days) == 0 {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, benchmarkFetchTimeout)
	defer cancel()
	closes, err := fetchBTCDailyCloses(fetchCtx, http.DefaultClient, benchmarkSpotBaseURL, days[0], days[len(days)-1])
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("基准 BTC 日 K 拉取失败(Alpha/Beta 置 BenchmarkMissing,不影响其它指标): %v", err)
		}
		return nil
	}
	return benchmarkDailyReturns(closes, days)
}
