package app

// okx_directional_universe.go OKX 方向引擎"全场选币"的纯评分核心(无 IO,可单测)。
// 每 ~30min 全场扫描时,对候选币算综合分挑 shortlist;90s 循环只在 shortlist 上出信号。
// 综合分 = 资金费幅度 + 波动质量 + 热度(成交量) + BTC 情境调整(只作权重,不过度依赖)。
// IO 采集(拉K线/量 + BTC 情境)见 okx_directional_scan.go;本文件只做"算分"。

import (
	"fmt"
	"math"
)

// okxUniverseEntry shortlist 一项(选币结果 + 可解释特征,供 90s 循环与分析记录)。
type okxUniverseEntry struct {
	Symbol      string  `json:"symbol"`
	FundingRate float64 `json:"funding_rate"`
	Score       float64 `json:"score"`
	AtrPct      float64 `json:"atr_pct"`  // 波动:近窗 ATR/价格
	VolUSDT     float64 `json:"vol_usdt"` // 热度:近窗成交额
	BtcCorr     float64 `json:"btc_corr"` // 与 BTC 收益相关性 [-1,1]
	Reason      string  `json:"reason"`
}

// okxUniverseFeatures 单币打分输入特征。
type okxUniverseFeatures struct {
	Symbol      string
	FundingRate float64
	AtrPct      float64
	VolUSDT     float64
	BtcCorr     float64
}

// okxBTCContext BTC 大盘情境(全场共享)。
type okxBTCContext struct {
	TrendStrength float64 // BTC 效率比 [0,1]:越高=BTC 单边越猛
	RetAbsPct     float64 // BTC 近窗净位移绝对值(占价)
}

// 综合分权重(内部常量;资金费/波动/热度为正,BTC 高相关在大盘剧烈时为惩罚)。
const (
	okxUniWFunding = 0.40
	okxUniWVol     = 0.30
	okxUniWHot     = 0.30
	okxUniWBTC     = 0.20 // BTC 惩罚权重小:考虑但不过度依赖
	// 归一基准。
	okxUniFundingFull = 0.003 // 0.3%/结算 视为资金费满分
	okxUniAtrSweet    = 0.015 // 1.5% ATR 视为波动满分
	okxUniAtrWild     = 0.06  // >6% ATR 视为过野,打折
	okxUniVolFullLog  = 7.0   // log10(成交额)=7(~1e7 U)视为热度满分
)

// scoreUniverseCandidate 纯函数:特征 + BTC 情境 → 综合分([0,1] 量级附近)+ 可读理由。
func scoreUniverseCandidate(f okxUniverseFeatures, btc okxBTCContext) (float64, string) {
	fundingScore := clamp01(math.Abs(f.FundingRate) / okxUniFundingFull)

	volScore := clamp01(f.AtrPct / okxUniAtrSweet)
	if f.AtrPct > okxUniAtrWild {
		volScore *= 0.5 // 过野:盘口穿价风险高,打折
	}

	hotScore := 0.0
	if f.VolUSDT > 0 {
		hotScore = clamp01(math.Log10(f.VolUSDT) / okxUniVolFullLog)
	}

	// BTC 情境惩罚:仅当 BTC 单边猛(TrendStrength 高)且该币与 BTC 高相关时扣分
	// (高相关币在 BTC 剧烈时只是被拖着走,idiosyncratic 边低,且反转易被大盘打穿)。BTC 平静时不惩罚。
	btcPenalty := okxUniWBTC * btc.TrendStrength * math.Abs(f.BtcCorr)

	score := okxUniWFunding*fundingScore + okxUniWVol*volScore + okxUniWHot*hotScore - btcPenalty
	if score < 0 {
		score = 0
	}
	reason := fmt.Sprintf("funding=%s atr=%.2f%% hot=%.2f btccorr=%.2f btcTrend=%.2f",
		formatRatePct(f.FundingRate), f.AtrPct*100, hotScore, f.BtcCorr, btc.TrendStrength)
	return score, reason
}

// rankUniverse 纯函数:对一组特征打分并按综合分降序取前 shortlistN。
func rankUniverse(feats []okxUniverseFeatures, btc okxBTCContext, shortlistN int) []okxUniverseEntry {
	if shortlistN <= 0 {
		shortlistN = 20
	}
	out := make([]okxUniverseEntry, 0, len(feats))
	for _, f := range feats {
		sc, reason := scoreUniverseCandidate(f, btc)
		out = append(out, okxUniverseEntry{
			Symbol: f.Symbol, FundingRate: f.FundingRate, Score: sc,
			AtrPct: f.AtrPct, VolUSDT: f.VolUSDT, BtcCorr: f.BtcCorr, Reason: reason,
		})
	}
	// 综合分降序(并列按 |funding| 降序、再按符号稳定)。
	sortUniverseEntries(out)
	if len(out) > shortlistN {
		out = out[:shortlistN]
	}
	return out
}

func sortUniverseEntries(xs []okxUniverseEntry) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0; j-- {
			a, b := xs[j-1], xs[j]
			less := a.Score < b.Score ||
				(a.Score == b.Score && math.Abs(a.FundingRate) < math.Abs(b.FundingRate)) ||
				(a.Score == b.Score && math.Abs(a.FundingRate) == math.Abs(b.FundingRate) && a.Symbol > b.Symbol)
			if less {
				xs[j-1], xs[j] = xs[j], xs[j-1]
			} else {
				break
			}
		}
	}
}
