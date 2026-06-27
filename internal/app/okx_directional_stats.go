package app

import (
	"sort"
	"strings"
	"time"
)

type directionalReasonStat struct {
	Reason  string  `json:"reason"`
	Count   int     `json:"count"`
	Wins    int     `json:"wins"`
	NetUSDT float64 `json:"net_usdt"`
}

type directionalStats struct {
	TradeCount  int                     `json:"trade_count"`
	Wins        int                     `json:"wins"`
	WinRate     float64                 `json:"win_rate"`
	NetUSDT     float64                 `json:"net_usdt"`
	AvgWinUSDT  float64                 `json:"avg_win_usdt"`
	AvgLossUSDT float64                 `json:"avg_loss_usdt"`
	ByReason    []directionalReasonStat `json:"by_reason"`
	ByRegime    []directionalReasonStat `json:"by_regime"`
}

// directionalReasonKey 归并出场原因:去掉括号内后缀,避免同类分裂。
func directionalReasonKey(reason string) string {
	reason = strings.TrimSpace(reason)
	for {
		start := strings.Index(reason, "(")
		if start < 0 {
			break
		}
		end := strings.Index(reason[start:], ")")
		if end < 0 {
			break
		}
		reason = strings.TrimSpace(reason[:start] + reason[start+end+1:])
	}
	if reason == "" {
		return "未知"
	}
	return reason
}

func directionalRegimeKey(regime string) string {
	regime = strings.TrimSpace(regime)
	if regime == "" {
		return "未知"
	}
	return regime
}

type directionalStatBucket struct {
	count   int
	wins    int
	netUSDT float64
}

func computeDirectionalStats(settles []okxDirectionalSettlement, since time.Time) directionalStats {
	filtered := make([]okxDirectionalSettlement, 0, len(settles))
	for _, s := range settles {
		if !since.IsZero() && s.Time.Before(since) {
			continue
		}
		filtered = append(filtered, s)
	}

	reasonBuckets := map[string]*directionalStatBucket{}
	regimeBuckets := map[string]*directionalStatBucket{}

	var wins int
	var netUSDT float64
	var winSum, lossSum float64
	var winCount, lossCount int

	for _, s := range filtered {
		pnl := s.RealizedUSDT
		netUSDT += pnl
		if pnl > 0 {
			wins++
			winSum += pnl
			winCount++
		} else if pnl < 0 {
			lossSum += pnl
			lossCount++
		}

		rKey := directionalReasonKey(s.Reason)
		rb := reasonBuckets[rKey]
		if rb == nil {
			rb = &directionalStatBucket{}
			reasonBuckets[rKey] = rb
		}
		rb.count++
		rb.netUSDT += pnl
		if pnl > 0 {
			rb.wins++
		}

		gKey := directionalRegimeKey(s.Regime)
		gb := regimeBuckets[gKey]
		if gb == nil {
			gb = &directionalStatBucket{}
			regimeBuckets[gKey] = gb
		}
		gb.count++
		gb.netUSDT += pnl
		if pnl > 0 {
			gb.wins++
		}
	}

	tradeCount := len(filtered)
	winRate := 0.0
	if tradeCount > 0 {
		winRate = float64(wins) / float64(tradeCount)
	}
	avgWin := 0.0
	if winCount > 0 {
		avgWin = winSum / float64(winCount)
	}
	avgLoss := 0.0
	if lossCount > 0 {
		avgLoss = lossSum / float64(lossCount)
	}

	return directionalStats{
		TradeCount:  tradeCount,
		Wins:        wins,
		WinRate:     winRate,
		NetUSDT:     netUSDT,
		AvgWinUSDT:  avgWin,
		AvgLossUSDT: avgLoss,
		ByReason:    directionalStatsFromBuckets(reasonBuckets),
		ByRegime:    directionalStatsFromBuckets(regimeBuckets),
	}
}

func directionalStatsFromBuckets(buckets map[string]*directionalStatBucket) []directionalReasonStat {
	out := make([]directionalReasonStat, 0, len(buckets))
	for reason, b := range buckets {
		out = append(out, directionalReasonStat{
			Reason:  reason,
			Count:   b.count,
			Wins:    b.wins,
			NetUSDT: b.netUSDT,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NetUSDT != out[j].NetUSDT {
			return out[i].NetUSDT < out[j].NetUSDT
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
