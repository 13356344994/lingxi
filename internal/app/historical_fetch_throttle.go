package app

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的 REST 抓取节流自适应内核
// —— 客户端构造、错误分类(-1021/418/429/403/202)、退避暂停、并发自适应、熔断中止判定，
// 以及小型整数/时长助手（同包搬移，行为不变），让主文件收敛到「编排 + 模拟状态机」职责并
// 向单文件 ≤ 800 行门禁靠拢。

func newHistoricalBacktestClient(secretPath string, timeout time.Duration, ncfg NetworkConfig, hcfg historicalConfig) (*BinanceClient, error) {
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	cache, cacheErr := newHistoricalPayloadCache(hcfg.CacheDir)
	if cacheErr != nil {
		return nil, cacheErr
	}
	secret, err := LoadSecretConfig(secretPath)
	if err == nil {
		if client, clientErr := NewAuthenticatedBinanceClient(secret, ncfg); clientErr == nil {
			client.SetHistoricalPayloadCache(cache)
			client.SetHistoricalFetchTuning(hcfg.FundingRetryMax, hcfg.ReferenceKlineLookback, hcfg.EstimateKlineLookback, hcfg.FundingFetchSlot)
			return client, nil
		}
	}
	client, clientErr := NewBinanceClientWithNetworkConfig(ncfg)
	if clientErr != nil {
		client = NewBinanceClient(timeout)
		client.ApplyNetworkConfig(ncfg)
	}
	client.SetHistoricalPayloadCache(cache)
	client.SetHistoricalFetchTuning(hcfg.FundingRetryMax, hcfg.ReferenceKlineLookback, hcfg.EstimateKlineLookback, hcfg.FundingFetchSlot)
	return client, nil
}

func classifyMonthlyFetchError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if statusCode := extractHTTPStatusCode(text); statusCode > 0 {
		switch statusCode {
		case http.StatusAccepted:
			return "http_202"
		case http.StatusForbidden:
			return "http_403"
		case http.StatusTeapot:
			return "http_418"
		case http.StatusTooManyRequests:
			return "http_429"
		default:
			if statusCode >= 400 && statusCode < 600 {
				return fmt.Sprintf("http_%d", statusCode)
			}
		}
	}
	switch {
	case strings.Contains(text, "429"):
		return "http_429"
	case strings.Contains(text, "202"):
		return "http_202"
	case strings.Contains(text, "403"):
		return "http_403"
	case strings.Contains(text, "418"):
		return "http_418"
	case strings.Contains(text, "context canceled"):
		return "context_canceled"
	case strings.Contains(text, "deadline exceeded"):
		return "deadline_exceeded"
	case strings.Contains(text, "service unavailable"):
		return "service_unavailable"
	case strings.Contains(text, "bad gateway"):
		return "bad_gateway"
	case strings.Contains(text, "gateway timeout"):
		return "gateway_timeout"
	case strings.Contains(text, "解析"):
		return "decode_error"
	default:
		text = strings.ReplaceAll(text, "|", "/")
		if len(text) > 48 {
			text = text[:48]
		}
		return text
	}
}

func extractHTTPStatusCode(text string) int {
	prefixes := []string{"http 状态异常: ", "状态异常: "}
	for _, prefix := range prefixes {
		idx := strings.Index(text, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		if start >= len(text) {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(text[start:], "%d", &code); err == nil && code >= 100 && code <= 599 {
			return code
		}
	}
	return 0
}

func monthlyFundingEscalatedPause(reasonCounts map[string]int) time.Duration {
	switch {
	case reasonCounts["http_418"] > 0:
		return monthlyFundingPause418
	case reasonCounts["http_403"] > 0:
		return monthlyFundingPause403
	case reasonCounts["http_429"] > 0:
		return monthlyFundingPause429
	case reasonCounts["http_202"] > 0:
		return monthlyFundingPause202
	}
	other4xxCount := 0
	for reason, count := range reasonCounts {
		if strings.HasPrefix(reason, "http_4") {
			other4xxCount += count
		}
	}
	if other4xxCount > 0 {
		return monthlyFundingPauseOther4xx
	}
	return 0
}

func adaptiveMonthlyFundingBatchPause(reasonCounts map[string]int, batchSize int) time.Duration {
	if len(reasonCounts) == 0 {
		return 0
	}
	pause := monthlyFundingEscalatedPause(reasonCounts)
	if pause <= 0 {
		return 0
	}
	if batchSize <= 0 || len(reasonCounts) == 0 {
		return pause
	}
	pressure := float64(reasonCounts["http_202"]) +
		float64(reasonCounts["http_429"])*2 +
		float64(reasonCounts["http_403"])*3 +
		float64(reasonCounts["http_418"])*4
	if pressure <= 0 {
		return pause
	}
	multiplier := 1.0 + math.Min(3.0, pressure/float64(batchSize))
	adjusted := time.Duration(float64(pause) * multiplier)
	maxPause := 4 * time.Minute
	if adjusted > maxPause {
		return maxPause
	}
	return adjusted
}

func adaptiveMonthlyFundingFetchConcurrency(base int, current int, batchSize int, reasonCounts map[string]int) int {
	base = maxInt(1, base)
	current = maxInt(1, current)
	if batchSize <= 0 {
		batchSize = monthlyFundingFetchBatchSize
	}
	threshold := maxInt(1, batchSize/2)
	severeCount := reasonCounts["http_418"] + reasonCounts["http_403"] + reasonCounts["http_429"]
	softCount := reasonCounts["http_202"]
	stepDown := maxInt(1, base/4)

	switch {
	case severeCount >= threshold:
		return maxInt(1, current/2)
	case softCount >= threshold:
		return maxInt(1, current-maxInt(1, stepDown/2))
	case severeCount > 0 || softCount > 0:
		return maxInt(1, current-1)
	case current < base:
		return current + 1
	default:
		return current
	}
}

func shouldAbortMonthlyFundingFetch(reasonCounts map[string]int, batchSize int, consecutive202Batches *int, consecutive429Batches *int, consecutive403Batches *int, consecutive418Batches *int) bool {
	if batchSize <= 0 {
		batchSize = monthlyFundingFetchBatchSize
	}
	threshold := maxInt(1, batchSize/2)
	if reasonCounts["http_202"] >= batchSize {
		*consecutive202Batches = *consecutive202Batches + 1
	} else {
		*consecutive202Batches = 0
	}
	if reasonCounts["http_429"] >= threshold {
		*consecutive429Batches = *consecutive429Batches + 1
	} else {
		*consecutive429Batches = 0
	}
	if reasonCounts["http_418"] >= threshold {
		*consecutive418Batches = *consecutive418Batches + 1
	} else {
		*consecutive418Batches = 0
	}
	if reasonCounts["http_403"] >= threshold {
		*consecutive403Batches = *consecutive403Batches + 1
	} else {
		*consecutive403Batches = 0
	}
	return *consecutive202Batches >= monthlyFundingAbort202Batches ||
		*consecutive429Batches >= monthlyFundingAbort429Batches ||
		*consecutive418Batches >= monthlyFundingAbort418Batches ||
		*consecutive403Batches >= monthlyFundingAbort403Batches
}

func adaptiveMonthlyGroupTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 90 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*20*time.Second
	maxTimeout := 3 * time.Minute
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func adaptiveMonthlyCandidateBuildTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 20 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*4*time.Second
	maxTimeout := 40 * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func adaptiveMonthlyCandidateFinalizeTimeout(base time.Duration, candidateCount int) time.Duration {
	if base <= 0 {
		base = 20 * time.Second
	}
	extraSteps := candidateCount / 120
	timeout := base + time.Duration(extraSteps)*3*time.Second
	maxTimeout := 35 * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func summarizeMonthlyReasonCounts(counts map[string]int, samples map[string][]string) string {
	if len(counts) == 0 {
		return ""
	}
	type entry struct {
		reason string
		count  int
	}
	items := make([]entry, 0, len(counts))
	for reason, count := range counts {
		items = append(items, entry{reason: reason, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].reason < items[j].reason
		}
		return items[i].count > items[j].count
	})
	parts := make([]string, 0, len(items))
	for index, item := range items {
		if index >= 5 {
			break
		}
		part := fmt.Sprintf("%s=%d", item.reason, item.count)
		if len(samples[item.reason]) > 0 {
			part += "[" + strings.Join(samples[item.reason], ",") + "]"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ; ")
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
