package app

import (
	"fmt"
	"math"
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
	// 事件级(每条 settlement = 1 笔,保持兼容)
	TradeCount  int                     `json:"trade_count"`
	Wins        int                     `json:"wins"`
	WinRate     float64                 `json:"win_rate"`
	NetUSDT     float64                 `json:"net_usdt"`
	AvgWinUSDT  float64                 `json:"avg_win_usdt"`
	AvgLossUSDT float64                 `json:"avg_loss_usdt"`
	ByReason    []directionalReasonStat `json:"by_reason"`
	ByRegime    []directionalReasonStat `json:"by_regime"`
	// 仓位级(多条 settlement 按 PositionID 合并为 1 仓,修正分批止盈导致的虚高胜率)
	PositionCount       int     `json:"position_count"`
	PositionWins        int     `json:"position_wins"`
	PositionWinRate     float64 `json:"position_win_rate"`
	PositionProfitFactor float64 `json:"position_profit_factor"`
	PositionMaxLossUSDT float64 `json:"position_max_loss_usdt"`  // 最亏的一仓(负值)
	PositionAvgWinUSDT  float64 `json:"position_avg_win_usdt"`   // 盈仓均值
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

	// 仓位级聚合:按 PositionID 合并分批止盈。
	// key: PositionID 非空时直接用;否则回退到 "Symbol|Side|entry8位" 作为代理键。
	posNetUSDT := map[string]float64{}
	for _, s := range filtered {
		key := s.PositionID
		if key == "" {
			key = fmt.Sprintf("%s|%s|%.8f", s.Symbol, s.Side, s.Entry)
		}
		posNetUSDT[key] += s.RealizedUSDT
	}
	var posWins int
	var posGain, posLoss float64
	var posMaxLoss float64 = 0
	var posWinSum float64
	var posWinCount int
	for _, pnl := range posNetUSDT {
		if pnl > 0 {
			posWins++
			posGain += pnl
			posWinSum += pnl
			posWinCount++
		} else if pnl < 0 {
			posLoss += pnl
			if pnl < posMaxLoss {
				posMaxLoss = pnl
			}
		}
	}
	posCount := len(posNetUSDT)
	posWinRate := 0.0
	if posCount > 0 {
		posWinRate = float64(posWins) / float64(posCount)
	}
	posPF := 0.0
	if math.Abs(posLoss) > 1e-12 {
		posPF = posGain / math.Abs(posLoss)
	} else if posGain > 0 {
		posPF = 999
	}
	posAvgWin := 0.0
	if posWinCount > 0 {
		posAvgWin = posWinSum / float64(posWinCount)
	}

	return directionalStats{
		TradeCount:           tradeCount,
		Wins:                 wins,
		WinRate:              winRate,
		NetUSDT:              netUSDT,
		AvgWinUSDT:           avgWin,
		AvgLossUSDT:          avgLoss,
		ByReason:             directionalStatsFromBuckets(reasonBuckets),
		ByRegime:             directionalStatsFromBuckets(regimeBuckets),
		PositionCount:        posCount,
		PositionWins:         posWins,
		PositionWinRate:      posWinRate,
		PositionProfitFactor: posPF,
		PositionMaxLossUSDT:  posMaxLoss,
		PositionAvgWinUSDT:   posAvgWin,
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
