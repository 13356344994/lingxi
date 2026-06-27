package app

// okx_directional_scan.go OKX 方向引擎"全场选币"IO 层:拉 K 线/量、算特征、调用纯评分。
// 纯打分见 okx_directional_universe.go;本文件不做综合分重算。

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/venue"
)

type okxScanCandidate struct {
	sym  string
	rate float64
}

// scanOKXUniverse 全场扫描:资费池 → 逐币拉特征 → rankUniverse 出 shortlist。
// 网络/单币失败容忍(跳过+日志),ctx 取消则立即返回 ctx.Err()。
func scanOKXUniverse(ctx context.Context, v DirectionalVenue, fundingSnaps []venue.FundingSnapshot, cfg OKXDirectionalConfig, logger *log.Logger) ([]okxUniverseEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("directional venue nil")
	}

	poolN := cfg.CandidatePoolN
	if poolN <= 0 {
		poolN = 40
	}
	bar := strings.TrimSpace(cfg.CandleBar)
	if bar == "" {
		bar = "5m"
	}
	limit := cfg.CandleLimit
	if limit <= 0 {
		limit = 120
	}

	candidates := selectOKXScanCandidates(fundingSnaps, cfg, poolN)
	if len(candidates) == 0 {
		if logger != nil {
			logger.Printf("[OKX方向][选币] 候选=0 BTC趋势=0.00 入选=0 头部: (空)")
		}
		return nil, nil
	}

	var btc okxBTCContext
	var btcReturns []float64
	btcCandles, err := v.FetchCandles(ctx, venue.MarketPerp, "BTC-USDT", bar, limit)
	if err != nil {
		if logger != nil {
			logger.Printf("[OKX方向][选币] BTC K线失败,用零情境继续: %v", err)
		}
	} else if err := ctx.Err(); err != nil {
		return nil, err
	} else {
		btc = buildOKXBTCContext(btcCandles)
		btcReturns = pctReturns(candleCloses(btcCandles))
	}

	feats := make([]okxUniverseFeatures, 0, len(candidates))

	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sym := venue.Symbol(cand.sym)
		candles, cerr := v.FetchCandles(ctx, venue.MarketPerp, sym, bar, limit)
		if cerr != nil {
			if logger != nil {
				logger.Printf("[OKX方向][选币] %s K线失败,跳过: %v", cand.sym, cerr)
			}
			continue
		}
		if len(candles) < 5 {
			if logger != nil {
				logger.Printf("[OKX方向][选币] %s K线不足(%d),跳过", cand.sym, len(candles))
			}
			continue
		}

		closes := candleCloses(candles)
		returns := pctReturns(closes)
		lastClose := closes[len(closes)-1]
		if lastClose <= 0 {
			continue
		}

		atrPct := calcAtrPct(candles, lastClose)
		volUSDT := fetchOKXCandidateVolUSDT(ctx, v, sym, candles, logger)
		btcCorr := pearsonAligned(returns, btcReturns)

		feats = append(feats, okxUniverseFeatures{
			Symbol:      cand.sym,
			FundingRate: cand.rate,
			AtrPct:      atrPct,
			VolUSDT:     volUSDT,
			BtcCorr:     btcCorr,
		})
	}

	shortlist := rankUniverse(feats, btc, cfg.ShortlistN)
	logOKXUniverseScanSummary(logger, len(candidates), btc.TrendStrength, shortlist)
	return shortlist, nil
}

func selectOKXScanCandidates(snaps []venue.FundingSnapshot, cfg OKXDirectionalConfig, poolN int) []okxScanCandidate {
	minAbs := cfg.MinFundingAbsPerInterval
	if minAbs < 0 {
		minAbs = 0
	}
	var cands []okxScanCandidate
	for _, s := range snaps {
		sym := string(s.Symbol)
		if sym == "" || !isUSDTPerpSymbol(sym) {
			continue
		}
		if math.Abs(s.LastFundingRate) < minAbs {
			continue
		}
		cands = append(cands, okxScanCandidate{sym: sym, rate: s.LastFundingRate})
	}
	sort.Slice(cands, func(i, j int) bool {
		ai, aj := math.Abs(cands[i].rate), math.Abs(cands[j].rate)
		if ai == aj {
			return cands[i].sym < cands[j].sym
		}
		return ai > aj
	})
	if len(cands) > poolN {
		cands = cands[:poolN]
	}
	return cands
}

func buildOKXBTCContext(candles []venue.Candle) okxBTCContext {
	var btc okxBTCContext
	closes := candleCloses(candles)
	if len(closes) < 2 {
		return btc
	}
	btc.TrendStrength = efficiencyRatio(closes)
	last := closes[len(closes)-1]
	if last > 0 {
		btc.RetAbsPct = math.Abs(last-closes[0]) / last
	}
	return btc
}

func candleCloses(candles []venue.Candle) []float64 {
	out := make([]float64, 0, len(candles))
	for _, c := range candles {
		if c.Close > 0 {
			out = append(out, c.Close)
		}
	}
	return out
}

// pctReturns 由 oldest-first 收盘价算逐根收益率(长度=len(closes)-1)。
func pctReturns(closes []float64) []float64 {
	if len(closes) < 2 {
		return nil
	}
	out := make([]float64, 0, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		prev := closes[i-1]
		if prev <= 0 {
			continue
		}
		out = append(out, (closes[i]-prev)/prev)
	}
	return out
}

// calcAtrPct 近窗平均真实波幅 / 末收(价格占比)。
func calcAtrPct(candles []venue.Candle, lastClose float64) float64 {
	if lastClose <= 0 || len(candles) == 0 {
		return 0
	}
	var trSum float64
	var n int
	for i, c := range candles {
		tr := c.High - c.Low
		if i > 0 {
			prevClose := candles[i-1].Close
			tr = math.Max(tr, math.Abs(c.High-prevClose))
			tr = math.Max(tr, math.Abs(c.Low-prevClose))
		}
		if tr > 0 {
			trSum += tr
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return (trSum / float64(n)) / lastClose
}

func fetchOKXCandidateVolUSDT(ctx context.Context, okxV venue.Venue, sym venue.Symbol, candles []venue.Candle, logger *log.Logger) float64 {
	if okxV != nil {
		vol, err := okxV.FetchRecentQuoteVolume(ctx, venue.MarketPerp, sym, time.Hour)
		if err == nil && vol > 0 {
			return vol
		}
		if err != nil && logger != nil {
			logger.Printf("[OKX方向][选币] %s 近窗量失败,回退K线: %v", sym, err)
		}
	}
	var sum float64
	for _, c := range candles {
		if c.Close > 0 && c.Volume > 0 {
			sum += c.Volume * c.Close
		}
	}
	return sum
}

// pearsonAligned 取 a/b 尾部对齐后算 Pearson 相关;样本<3 或零方差 → 0。
func pearsonAligned(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n < 3 {
		return 0
	}
	return pearson(a[len(a)-n:], b[len(b)-n:])
}

// pearson 总体 Pearson 相关(Pearson r);与 meanStd 同口径(n 分母)。
func pearson(a, b []float64) float64 {
	n := len(a)
	if n != len(b) || n < 3 {
		return 0
	}
	meanA, stdA := meanStd(a)
	meanB, stdB := meanStd(b)
	if stdA <= 0 || stdB <= 0 {
		return 0
	}
	var cov float64
	for i := 0; i < n; i++ {
		cov += (a[i] - meanA) * (b[i] - meanB)
	}
	cov /= float64(n)
	return cov / (stdA * stdB)
}

func logOKXUniverseScanSummary(logger *log.Logger, candidateN int, btcTrend float64, shortlist []okxUniverseEntry) {
	if logger == nil {
		return
	}
	head := make([]string, 0, 3)
	for i, e := range shortlist {
		if i >= 3 {
			break
		}
		if i == 0 {
			head = append(head, fmt.Sprintf("%s(%.3f)", e.Symbol, e.Score))
		} else {
			head = append(head, e.Symbol)
		}
	}
	headStr := strings.Join(head, " ")
	if headStr == "" {
		headStr = "(空)"
	}
	logger.Printf("[OKX方向][选币] 候选=%d BTC趋势=%.2f 入选=%d 头部: %s",
		candidateN, btcTrend, len(shortlist), headStr)
}
