package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"lingxi/internal/venue"
)

const (
	carryScannerRejectFundingWindowNotReached = "funding_entry_window_not_reached"
	carryScannerRejectFirstWindowEV           = "first_funding_window_ev_negative"
)

// carryTwoLegMatch 两腿开仓成交对齐结果(leg1 为先开腿,leg2 为对冲腿)。
type carryTwoLegMatch struct {
	MatchedQty  float64
	ExcessLeg1  float64
	ExcessLeg2  float64
	OK          bool
	Leg2Partial bool
}

// carryMatchTwoLegOpens 按实际成交量对齐两腿;leg2 部分成交时以 leg2 为 matched,leg1 多余部分需 unwind。
func carryMatchTwoLegOpens(leg1Executed, leg2Executed, minTradableQty float64) carryTwoLegMatch {
	out := carryTwoLegMatch{}
	if leg1Executed <= executionQtyTolerance {
		return out
	}
	if leg2Executed <= executionQtyTolerance {
		out.ExcessLeg1 = leg1Executed
		return out
	}
	if hedgeLegFullyFilled(leg2Executed, leg1Executed) {
		out.MatchedQty = math.Min(leg1Executed, leg2Executed)
		if leg1Executed > out.MatchedQty+executionQtyTolerance {
			out.ExcessLeg1 = leg1Executed - out.MatchedQty
		}
		if leg2Executed > out.MatchedQty+executionQtyTolerance {
			out.ExcessLeg2 = leg2Executed - out.MatchedQty
		}
		out.OK = true
		return out
	}
	out.Leg2Partial = true
	out.MatchedQty = leg2Executed
	if minTradableQty > 0 && out.MatchedQty+executionQtyTolerance < minTradableQty {
		out.ExcessLeg1 = leg1Executed
		out.ExcessLeg2 = leg2Executed
		out.MatchedQty = 0
		return out
	}
	out.ExcessLeg1 = leg1Executed - out.MatchedQty
	out.OK = true
	return out
}

func carryMinTradableQty(refPrice float64, instr venue.Instrument) float64 {
	if instr.StepSize > 0 {
		return instr.StepSize
	}
	if refPrice > 0 && instr.MinNotional > 0 {
		return instr.MinNotional / refPrice
	}
	return executionQtyTolerance
}

// carryCappedSpotSellQty 卖出现货 = min(意图量, 实测可卖),按 step 向下量化。
func carryCappedSpotSellQty(intendedQty float64, bals venue.Balances, baseAsset string, stepSize float64) float64 {
	if intendedQty <= executionQtyTolerance {
		return 0
	}
	held := spotLongQtyFromBalance(bals, baseAsset)
	qty := math.Min(intendedQty, held)
	if stepSize > 0 {
		qty = quantizeQtyToStep(qty, stepSize)
	}
	if qty <= executionQtyTolerance {
		return 0
	}
	return qty
}

func carryResolveSpotSellQty(ctx context.Context, v venue.Venue, sym venue.Symbol, intendedQty, stepSize float64) float64 {
	if v == nil || intendedQty <= executionQtyTolerance {
		return 0
	}
	bals, err := v.FetchBalances(ctx)
	if err != nil {
		return 0
	}
	base := baseAssetFromCanonicalSymbol(string(sym))
	return carryCappedSpotSellQty(intendedQty, bals, base, stepSize)
}

func carryPreflightSpotPerpOpen(
	ctx context.Context,
	venues map[string]venue.Venue,
	spotVenue, perpVenue string,
	spotQuoteNeed, perpMarginNeed float64,
) error {
	spotV := venues[spotVenue]
	perpV := venues[perpVenue]
	if spotV == nil {
		return fmt.Errorf("spot venue %s 未就绪", spotVenue)
	}
	if perpV == nil {
		return fmt.Errorf("perp venue %s 未就绪", perpVenue)
	}
	bal, err := spotV.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("spot 余额预检失败: %w", err)
	}
	spotAvail := spotQuoteAvailableUSDT(bal)
	if spotAvail+1e-9 < spotQuoteNeed {
		return fmt.Errorf("spot USDT 不足: avail=%.4f need≈%.4f", spotAvail, spotQuoteNeed)
	}
	perpAvail, err := fetchVenuePerpAvailableUSDT(ctx, perpV)
	if err != nil {
		return fmt.Errorf("perp 保证金预检失败: %w", err)
	}
	if perpAvail+1e-9 < perpMarginNeed {
		return fmt.Errorf("perp 保证金不足: avail=%.4f need≈%.4f", perpAvail, perpMarginNeed)
	}
	return nil
}

func carryPreflightSingleVenueSpotPerpOpen(
	ctx context.Context,
	v venue.Venue,
	spotQuoteNeed, perpMarginNeed float64,
) error {
	if v == nil {
		return fmt.Errorf("venue 未就绪")
	}
	bal, err := v.FetchBalances(ctx)
	if err != nil {
		return fmt.Errorf("spot 余额预检失败: %w", err)
	}
	spotAvail := spotQuoteAvailableUSDT(bal)
	if spotAvail+1e-9 < spotQuoteNeed {
		return fmt.Errorf("spot USDT 不足: avail=%.4f need≈%.4f", spotAvail, spotQuoteNeed)
	}
	perpAvail, err := fetchVenuePerpAvailableUSDT(ctx, v)
	if err != nil {
		return fmt.Errorf("perp 保证金预检失败: %w", err)
	}
	if perpAvail+1e-9 < perpMarginNeed {
		return fmt.Errorf("perp 保证金不足: avail=%.4f need≈%.4f", perpAvail, perpMarginNeed)
	}
	return nil
}

func carryLogPreflightSkip(logger *log.Logger, tag, symbol, reason string) {
	if logger == nil {
		return
	}
	logger.Printf("[%s] 跳过开仓 %s: %s", tag, symbol, reason)
}

func normalizeCarryFundingEntryGateConfig(cfg CarryFundingEntryGateConfig) CarryFundingEntryGateConfig {
	if cfg.BaseFraction <= 0 {
		cfg.BaseFraction = 0.125
	}
	if cfg.MinVolScale <= 0 {
		cfg.MinVolScale = 0.2
	}
	if cfg.MinVolScale > 1 {
		cfg.MinVolScale = 1
	}
	if cfg.VolRefPerHour <= 0 {
		cfg.VolRefPerHour = 0.0001
	}
	if cfg.FloorMinutes <= 0 {
		cfg.FloorMinutes = 5
	}
	if cfg.VolLookbackScans <= 0 {
		cfg.VolLookbackScans = 10
	}
	if cfg.MissingVolScale <= 0 {
		cfg.MissingVolScale = 0.6
	}
	if cfg.MissingVolScale > 1 {
		cfg.MissingVolScale = 1
	}
	return cfg
}

// carryFundingEntryWindowMinutes 计算 symbol 波动自适应开仓窗口(分钟)。
// W = clamp(base × volScale, floorMinutes, base); base = intervalHours×60×baseFraction。
func carryFundingEntryWindowMinutes(
	gate CarryFundingEntryGateConfig,
	intervalHours int,
	rateVolPerHour float64,
	volDataAvailable bool,
) float64 {
	gate = normalizeCarryFundingEntryGateConfig(gate)
	ih := float64(intervalHours)
	if ih <= 0 {
		ih = 8
	}
	baseMinutes := ih * 60 * gate.BaseFraction

	volScale := gate.MissingVolScale
	if volDataAvailable && gate.VolRefPerHour > 0 {
		volScale = 1 - rateVolPerHour/gate.VolRefPerHour
		if volScale < gate.MinVolScale {
			volScale = gate.MinVolScale
		}
		if volScale > 1 {
			volScale = 1
		}
	}

	w := baseMinutes * volScale
	if w < gate.FloorMinutes {
		w = gate.FloorMinutes
	}
	if w > baseMinutes {
		w = baseMinutes
	}
	return w
}

func carryFundingVolFromMap(m map[string]map[string]float64, venue, symbol string) (vol float64, ok bool) {
	if m == nil {
		return 0, false
	}
	bySym, ok := m[venue]
	if !ok {
		return 0, false
	}
	v, ok := bySym[symbol]
	return v, ok
}

func carryScannerFundingEntryEligible(
	now time.Time,
	fund CrossVenueSpotPerpFundingSnap,
	holdHours float64,
	notional, effectiveRate, expectedBasisPnL, openCost, slippage float64,
	gate CarryFundingEntryGateConfig,
	rateVolPerHour float64,
	volDataAvailable bool,
) (bool, string) {
	if fund.NextFundingTime.IsZero() {
		return false, carryScannerRejectFirstWindowEV
	}
	untilHours := crossVenueComparableWindowHours(now, fund.NextFundingTime, fund.NextFundingTime)
	if untilHours <= 0 {
		return false, carryScannerRejectFirstWindowEV
	}

	entryWindowMinutes := carryFundingEntryWindowMinutes(gate, fund.IntervalHours, rateVolPerHour, volDataAvailable)
	if untilHours*60 > entryWindowMinutes+1e-9 {
		return false, carryScannerRejectFundingWindowNotReached
	}

	windowHours := untilHours
	if holdHours <= 0 {
		holdHours = windowHours
	}
	windowFunding := notional * effectiveRate * float64(crossVenueSpotPerpFundingSettlementCount(now, windowHours, fund.IntervalHours, fund.NextFundingTime))
	basisProrated := expectedBasisPnL
	if holdHours > 0 && windowHours < holdHours {
		basisProrated = expectedBasisPnL * (windowHours / holdHours)
	}
	firstWindowEV := windowFunding + basisProrated - openCost - slippage
	if firstWindowEV <= 0 {
		return false, carryScannerRejectFirstWindowEV
	}
	return true, ""
}

// carryFundingVolatilityMap 从 CrossVenueScanner 滚动费率历史构建 per-venue/symbol 波动 map(供 carry 扫描器使用)。
func carryFundingVolatilityMap(scanner *CrossVenueScanner, fundingByVenue map[string]map[string]CrossVenueSpotPerpFundingSnap, lookback int) map[string]map[string]float64 {
	if scanner == nil || len(fundingByVenue) == 0 {
		return nil
	}
	out := make(map[string]map[string]float64)
	for venue, bySym := range fundingByVenue {
		for sym, snap := range bySym {
			vol, ok := scanner.fundingRateVolatilityPerHour(venue, sym, snap.IntervalHours, lookback)
			if !ok {
				continue
			}
			if out[venue] == nil {
				out[venue] = make(map[string]float64)
			}
			out[venue][sym] = vol
		}
	}
	return out
}
