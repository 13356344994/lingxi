package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// cross_venue_gates.go 跨所开仓闸门:全部满足才动手。

// crossVenueExecCapitalSizingHaircut 执行闸资金封顶安全垫:按 99% 的两腿实时 perp 可用反解名义上限,
// 使所需保证金严格小于实时可用,避免 reserve/取整 drift 造成 49.99<50 这类几分钱误拒(与 rotation 决策层同口径)。
const crossVenueExecCapitalSizingHaircut = 0.99

var crossVenueTradeableVenues = map[string]bool{
	binance.VenueName: true,
	okx.VenueName:     true,
	gate.VenueName:    true,
	bybit.VenueName:   true,
}

// crossVenueGateInput 是闸门评估所需的上下文。
type crossVenueGateInput struct {
	Opp                 CrossVenueOpportunity
	Positions           []CrossVenuePosition
	AllowExistingSymbol bool
	BybitHeld           func(sym venue.Symbol) bool
	BinanceHeld         func(sym venue.Symbol) bool // 币安遗留 FundingRateSniper 台账在场
	FundingHeld         func(sym venue.Symbol) bool // 任一 okx/gate 单所 funding 引擎台账在场
	HaltActive          bool
	SymbolCooling       func(sym string) bool
	Now                 time.Time
}

// crossVenueGateResult 是闸门评估结果。
type crossVenueGateResult struct {
	OK              bool
	Reason          string
	NotionalUSDT    float64
	HighQty         float64
	LowQty          float64
	HighMid         float64
	LowMid          float64
	HighInstr       venue.Instrument
	LowInstr        venue.Instrument
	HighFundingSnap venue.FundingSnapshot
	LowFundingSnap  venue.FundingSnapshot
}

// crossVenueGateDeps 是闸门所需的最小 venue 能力。
type crossVenueGateDeps struct {
	lookupInstrument       func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.Instrument, bool)
	fetchBookTicker        func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error)
	fetchDepth             func(ctx context.Context, v venue.Venue, sym venue.Symbol, limit int) (venue.Depth, error)
	fetchFunding           func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.FundingSnapshot, bool)
	fetchPerpAvailableUSDT func(ctx context.Context, v venue.Venue) (float64, error)
	venueCaps              func(v venue.Venue) venue.Capabilities
	allocatedUSDT          func(venueName string) float64
}

func evaluateCrossVenueOpenGate(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	deps crossVenueGateDeps,
	venues map[string]venue.Venue,
	in crossVenueGateInput,
) crossVenueGateResult {
	fail := func(reason string) crossVenueGateResult {
		return crossVenueGateResult{OK: false, Reason: reason}
	}
	if in.HaltActive {
		return fail("急停(.halt-live)生效,禁止开新仓")
	}
	if cfg.OpenSpreadAPR > 0 && in.Opp.SpreadAPR < cfg.OpenSpreadAPR {
		return fail(fmt.Sprintf("价差年化 %.1f%% < 开仓阈值 %.1f%%", in.Opp.SpreadAPR*100, cfg.OpenSpreadAPR*100))
	}
	if !crossVenueTradeableVenues[in.Opp.HighVenue] || !crossVenueTradeableVenues[in.Opp.LowVenue] {
		return fail("venue 不在可交易集合")
	}
	highV := venues[in.Opp.HighVenue]
	lowV := venues[in.Opp.LowVenue]
	if highV == nil || lowV == nil {
		return fail("venue 适配器未就绪")
	}
	highSym := venue.Symbol(crossVenueOppHighSymbol(in.Opp))
	lowSym := venue.Symbol(crossVenueOppLowSymbol(in.Opp))
	if in.BybitHeld != nil && (in.BybitHeld(highSym) || in.BybitHeld(lowSym)) {
		return fail("symbol 已被 Bybit 单所引擎持有")
	}
	if in.BinanceHeld != nil && (in.BinanceHeld(highSym) || in.BinanceHeld(lowSym)) {
		return fail("symbol 已被 Binance 遗留引擎持有")
	}
	if in.FundingHeld != nil && (in.FundingHeld(highSym) || in.FundingHeld(lowSym)) {
		return fail("symbol 已被 OKX/Gate 单所 funding 引擎持有")
	}
	for _, p := range in.Positions {
		if in.AllowExistingSymbol {
			continue
		}
		if p.Symbol == in.Opp.Symbol && p.Status != CrossVenueStatusClosed {
			return fail("symbol 已有在场跨所持仓")
		}
	}
	if in.SymbolCooling != nil && in.SymbolCooling(in.Opp.Symbol) {
		return fail("symbol 处于回滚冷却期")
	}

	// 注:不再用 FetchRealizedFunding 做开仓前置闸——binance/gate/okx 该方法未实现(返回 ErrUnsupported),
	// 用它当闸会拒掉所有含这三所的开仓对(=几乎全部跨所机会),把策略整个搞死。
	// 排除美股代币等伪高价差标的,改靠下面的"快照有效性"(NextFundingTime/间隔/有限 rate)+ 扫描器价差上限。
	highSnap, ok := deps.fetchFunding(ctx, highV, highSym)
	if !ok || !crossVenueFundingSnapshotValid(highSnap, in.Now) {
		if snap, okLeg := crossVenueFundingSnapFromOppLeg(in.Opp, true, in.Now); okLeg {
			highSnap = snap
		} else {
			return fail("高费所幽灵腿:资费快照无效(缺 rate/结算间隔/NextFundingTime)")
		}
	}
	lowSnap, ok := deps.fetchFunding(ctx, lowV, lowSym)
	if !ok || !crossVenueFundingSnapshotValid(lowSnap, in.Now) {
		if snap, okLeg := crossVenueFundingSnapFromOppLeg(in.Opp, false, in.Now); okLeg {
			lowSnap = snap
		} else {
			return fail("低费所幽灵腿:资费快照无效(缺 rate/结算间隔/NextFundingTime)")
		}
	}

	highInstr, ok := deps.lookupInstrument(ctx, highV, highSym)
	if !ok {
		return fail("高费所 lookupInstrument 失败")
	}
	lowInstr, ok := deps.lookupInstrument(ctx, lowV, lowSym)
	if !ok {
		return fail("低费所 lookupInstrument 失败")
	}

	highBook, err := deps.fetchBookTicker(ctx, highV, highSym)
	if err != nil || highBook.BidPrice <= 0 || highBook.AskPrice <= 0 {
		return fail("高费所盘口无效")
	}
	lowBook, err := deps.fetchBookTicker(ctx, lowV, lowSym)
	if err != nil || lowBook.BidPrice <= 0 || lowBook.AskPrice <= 0 {
		return fail("低费所盘口无效")
	}
	highMid := (highBook.BidPrice + highBook.AskPrice) / 2
	lowMid := (lowBook.BidPrice + lowBook.AskPrice) / 2
	if highMid <= 0 || lowMid <= 0 {
		return fail("中间价无效")
	}
	highMult := crossVenueOppHighMult(in.Opp)
	lowMult := crossVenueOppLowMult(in.Opp)
	// 跨计价硬护栏:经济 base 相同 + 归一 mid 价比≈倍数比;任一不满足则拒单(等同不配,绝不 fallback)。
	if crossVenueIsCrossDenomPair(in.Opp) {
		if !crossVenueCrossDenomGuardOK(in.Opp, highMid, lowMid) {
			return fail("跨计价护栏:经济 base/价格比未通过(禁止错配)")
		}
	}
	dev := crossVenueMidDeviation(highMid, lowMid, highMult, lowMult)
	maxDev := crossVenueResolvedMidPriceDeviationMax(cfg, in.Opp)
	if dev > maxDev {
		return fail(fmt.Sprintf("两所中间价偏离 %.2f%% > %.2f%%", dev*100, maxDev*100))
	}

	maxSpreadBps := cfg.MaxBookSpreadBps
	if maxSpreadBps <= 0 {
		maxSpreadBps = 30
	}
	if bookSpreadBps(highBook) > maxSpreadBps {
		return fail(fmt.Sprintf("高费所盘口价差 %.1fbps 超限", bookSpreadBps(highBook)))
	}
	if bookSpreadBps(lowBook) > maxSpreadBps {
		return fail(fmt.Sprintf("低费所盘口价差 %.1fbps 超限", bookSpreadBps(lowBook)))
	}

	notional := cfg.MaxPositionUSDT
	if notional <= 0 {
		notional = 50
	}
	if cfg.SizingMaxSlippageBps > 0 || cfg.SizingMaxParticipationPct24h > 0 {
		if liqCap := crossVenueGateLiquidityNotionalCapPerLeg(ctx, cfg, deps, highV, lowV, highSym, lowSym); liqCap < math.MaxFloat64 {
			notional = math.Min(notional, liqCap)
		}
	} else {
		capHigh := depthCapacityUSDT(deps.fetchDepth, ctx, highV, highSym, cfg.DepthUsageRatio)
		capLow := depthCapacityUSDT(deps.fetchDepth, ctx, lowV, lowSym, cfg.DepthUsageRatio)
		if capHigh > 0 && capHigh < notional {
			notional = capHigh
		}
		if capLow > 0 && capLow < notional {
			notional = capLow
		}
	}
	if notional <= 0 {
		return fail("深度容量不足")
	}

	levCap := cfg.MaxEffectiveLeverage
	if levCap <= 0 {
		levCap = 2
	}
	highBal, highBalErr := deps.fetchPerpAvailableUSDT(ctx, highV)
	if highBalErr != nil {
		return fail(fmt.Sprintf("高费所永续可用保证金读取失败: %v", highBalErr))
	}
	lowBal, lowBalErr := deps.fetchPerpAvailableUSDT(ctx, lowV)
	if lowBalErr != nil {
		return fail(fmt.Sprintf("低费所永续可用保证金读取失败: %v", lowBalErr))
	}
	// 资金封顶(与 rotation 决策层 rotationPerpPerpMaxNotionalFromVenueAvail 同口径):执行下单的名义也必须按
	// "两腿实时 perp 可用×杠杆×安全垫"封顶,否则名义死顶 MaxPositionUSDT → 所需保证金=可用 → 49.99<50 这类
	// 几分钱误拒,极端肥币(如 H)永远开不出来。够开多少开多少:资金不足时缩仓而非拒单,缩仓后所需严格小于可用。
	if capBal := math.Min(highBal, lowBal); capBal > 0 {
		maxByCapital := capBal * levCap * crossVenueExecCapitalSizingHaircut
		if maxByCapital < notional {
			notional = maxByCapital
		}
	}
	if notional <= 0 {
		return fail("资金封顶后名义不足")
	}

	maxSlipBps := crossVenueResolvedMaxOpenSlippageBps(cfg)
	if slipBps, ok := estimateCrossVenueLegOpenSlippageBps(deps.fetchDepth, ctx, highV, highSym, venue.SideSell, notional, highMid); !ok {
		return fail("高费所深度不足,无法估算开仓滑点")
	} else if slipBps > maxSlipBps {
		return fail(fmt.Sprintf("高费所预估滑点 %.1f bps > %.0f", slipBps, maxSlipBps))
	}
	if slipBps, ok := estimateCrossVenueLegOpenSlippageBps(deps.fetchDepth, ctx, lowV, lowSym, venue.SideBuy, notional, lowMid); !ok {
		return fail("低费所深度不足,无法估算开仓滑点")
	} else if slipBps > maxSlipBps {
		return fail(fmt.Sprintf("低费所预估滑点 %.1f bps > %.0f", slipBps, maxSlipBps))
	}

	// 与 sizing(crossVenuePerVenueMarginUSDT) 同口径:名义/杠杆;强平安全由 MinLiqDistance 校验,不再乘 MarginSafetyFactor。
	// 名义此前已按两腿实时可用×杠杆×安全垫封顶,故 requiredMargin 必严格小于两腿可用(除非资金为 0)。
	requiredMargin := crossVenuePerVenueMarginUSDT(cfg, notional)
	if highBal < requiredMargin {
		return fail(fmt.Sprintf("高费所永续可用 USDT %.2f < 所需 %.2f", highBal, requiredMargin))
	}
	if lowBal < requiredMargin {
		return fail(fmt.Sprintf("低费所永续可用 USDT %.2f < 所需 %.2f", lowBal, requiredMargin))
	}
	highAlloc := deps.allocatedUSDT(in.Opp.HighVenue)
	lowAlloc := deps.allocatedUSDT(in.Opp.LowVenue)
	if highAlloc > 0 && notional/highAlloc > levCap+1e-9 {
		return fail(fmt.Sprintf("高费所有效杠杆 %.2fx > %.2fx", notional/highAlloc, levCap))
	}
	if lowAlloc > 0 && notional/lowAlloc > levCap+1e-9 {
		return fail(fmt.Sprintf("低费所有效杠杆 %.2fx > %.2fx", notional/lowAlloc, levCap))
	}

	evLegs := crossVenuePerpPerpEVSlippageLegsPerSymbol(deps.fetchDepth, ctx, highV, lowV, highSym, lowSym, highMid, lowMid)
	holdHours := crossVenueOpenEVHoldHours(cfg, in.Now, in.Opp, crossVenueComparableWindowHours(in.Now, highSnap.NextFundingTime, lowSnap.NextFundingTime))
	evOpp := in.Opp
	if evOpp.HighNextFundingTime.IsZero() {
		evOpp.HighNextFundingTime = highSnap.NextFundingTime
		evOpp.HighIntervalHours = highSnap.IntervalHours
	}
	if evOpp.LowNextFundingTime.IsZero() {
		evOpp.LowNextFundingTime = lowSnap.NextFundingTime
		evOpp.LowIntervalHours = lowSnap.IntervalHours
	}
	if evRes := evaluateCrossVenueOpenEV(cfg, evOpp, notional, evLegs, in.Now, holdHours, dev); !evRes.OK {
		return fail(evRes.Reason)
	}

	_ = deps.venueCaps // 预留 contract-based 校验
	highCaps := highV.Capabilities()
	lowCaps := lowV.Capabilities()
	alignedQty := alignCrossVenueQtyPerLeg(notional, highMid, lowMid, highMult, lowMult, highInstr, lowInstr, highCaps, lowCaps)
	if alignedQty.HighQty <= executionQtyTolerance || alignedQty.LowQty <= executionQtyTolerance {
		return fail("对齐后下单数量低于最小可交易量")
	}
	return crossVenueGateResult{
		OK:              true,
		NotionalUSDT:    notional,
		HighQty:         alignedQty.HighQty,
		LowQty:          alignedQty.LowQty,
		HighMid:         highMid,
		LowMid:          lowMid,
		HighInstr:       highInstr,
		LowInstr:        lowInstr,
		HighFundingSnap: highSnap,
		LowFundingSnap:  lowSnap,
	}
}

type crossVenueEVResult struct {
	OK     bool
	Reason string
}

// evaluateCrossVenueOpenEV 净收益闸:预期持有 horizon 内 funding 收入须覆盖四腿往返成本+风险/策略缓冲。
// 费率须来自扫描器平滑+封顶后的 Opp 字段;缺省(单测直调 gate 且无结算窗口)则跳过 EV。
func evaluateCrossVenueOpenEV(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional float64, evLegs []crossVenueEVSlippageLeg, now time.Time, holdHours float64, midDev float64) crossVenueEVResult {
	pass := func() crossVenueEVResult { return crossVenueEVResult{OK: true} }
	failEV := func(reason string) crossVenueEVResult {
		return crossVenueEVResult{OK: false, Reason: reason}
	}
	holdHours = crossVenueOpenEVHoldHours(cfg, now, opp, holdHours)
	if holdHours <= 0 {
		if opp.HighAnnualizedAPR == 0 && opp.LowAnnualizedAPR == 0 {
			return pass()
		}
		return failEV("EV<0: hold window ≤0 (no funding settlement before exit)")
	}
	if opp.HighFundingRate == 0 && opp.LowFundingRate == 0 {
		return pass()
	}
	expectedFunding := crossVenueOpenGateExpectedFundingUSDT(cfg, now, holdHours, notional, opp)
	if expectedFunding <= 0 {
		return failEV(fmt.Sprintf("EV<0: expectedFunding=%.6f USDT ≤0 over hold=%.2fh", expectedFunding, holdHours))
	}
	openCost := crossVenueOpenEVRoundTripFeeUSDT(cfg, opp, notional)
	closeCost := openCost
	roundTripSlippage := 0.0
	legVenues := []string{opp.HighVenue, opp.LowVenue, opp.HighVenue, opp.LowVenue}
	evSlipBps := crossVenueOpenEVEslippageBpsPerLeg(cfg)
	for i := range legVenues {
		slipBps := evSlipBps
		if len(evLegs) > i {
			slipBps = crossVenueEVSlippageBpsPerLeg(cfg, notional, evLegs[i])
		}
		roundTripSlippage += notional * slipBps / 10000
	}
	roundTripCost := openCost + closeCost + roundTripSlippage
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(notional, opp.FreshnessAgeSeconds, holdHours, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT()
	entryBasisCost := crossVenuePerpPerpEntryBasisCostUSDT(cfg, midDev, notional, opp)
	netProfit := expectedFunding - roundTripCost - observableRiskCost - policyBuffer - dustCost - entryBasisCost
	minProfit := crossVenueMinNetProfitUSDT(cfg)
	if netProfit <= minProfit+1e-9 {
		return failEV(fmt.Sprintf("EV<0: netProfit=%.4f ≤ min %.4f USDT (funding=%.4f cost=%.4f hold=%.2fh)",
			netProfit, minProfit, expectedFunding, roundTripCost+observableRiskCost+policyBuffer, holdHours))
	}
	return pass()
}

func crossVenueTakerFeeBps(cfg CrossVenueExecutorConfig, venueName string) float64 {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	switch venueName {
	case binance.VenueName:
		return 5
	case bybit.VenueName:
		return 5.5
	case okx.VenueName:
		return 5
	case gate.VenueName:
		return 5
	}
	if cfg.DefaultTakerFeeBps > 0 {
		return cfg.DefaultTakerFeeBps
	}
	return 6
}

func crossVenueMakerFeeBps(cfg CrossVenueExecutorConfig, venueName string) float64 {
	venueName = strings.ToLower(strings.TrimSpace(venueName))
	switch venueName {
	case binance.VenueName:
		return 2
	case bybit.VenueName:
		return 1
	case okx.VenueName:
		return 2
	case gate.VenueName:
		return 2
	}
	if cfg.DefaultMakerFeeBps > 0 {
		return cfg.DefaultMakerFeeBps
	}
	return 2
}

func crossVenueAssumeMakerFills(cfg CrossVenueExecutorConfig) bool {
	return cfg.AssumeMakerFills
}

func crossVenuePerpFeeBps(cfg CrossVenueExecutorConfig, venueName string) float64 {
	if crossVenueAssumeMakerFills(cfg) {
		return crossVenueMakerFeeBps(cfg, venueName)
	}
	return crossVenueTakerFeeBps(cfg, venueName)
}

func crossVenueOpenEVHoldHours(cfg CrossVenueExecutorConfig, now time.Time, opp CrossVenueOpportunity, comparableWindowHours float64) float64 {
	if cfg.ExpectedHoldHours > 0 {
		return cfg.ExpectedHoldHours
	}
	if comparableWindowHours > 0 {
		return comparableWindowHours
	}
	if opp.ExpectedHoldHours > 0 {
		return opp.ExpectedHoldHours
	}
	if opp.ComparableWindowHours > 0 {
		return opp.ComparableWindowHours
	}
	return crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
}

// crossVenueOpenEVEslippageBpsPerLeg EV 闸成本侧滑点:优先 EVExpectedSlippageBps(规划口径),与 SlippageBudget(执行闸)分离。
func crossVenueOpenEVEslippageBpsPerLeg(cfg CrossVenueExecutorConfig) float64 {
	if cfg.EVExpectedSlippageBps > 0 {
		return cfg.EVExpectedSlippageBps
	}
	return crossVenueEVSlippageFallbackBpsPerLeg(cfg)
}

// crossVenueOpenGateExpectedFundingUSDT 开仓 EV 闸 funding 收入:按可比窗口内离散结算次数封顶,并乘捕获折现。
func crossVenueOpenGateExpectedFundingUSDT(cfg CrossVenueExecutorConfig, now time.Time, holdHours, notional float64, opp CrossVenueOpportunity) float64 {
	effectiveHold := holdHours
	if effectiveHold <= 0 {
		effectiveHold = crossVenueOpenEVHoldHours(cfg, now, opp, 0)
	}
	funding := crossVenueExpectedFundingOverHold(now, effectiveHold, notional, opp)
	return funding * carryScannerNormalizedFundingCaptureProbability(0)
}

func bookSpreadBps(t venue.BookTicker) float64 {
	mid := (t.BidPrice + t.AskPrice) / 2
	if mid <= 0 {
		return math.MaxFloat64
	}
	return (t.AskPrice - t.BidPrice) / mid * 10000
}

const crossVenueOpenSlippageDepthLimit = 20

// estimateCrossVenueLegOpenSlippageBps 按名义额模拟 taker 吃单,估算相对最优价的滑点(bps)。
// 高费腿卖出吃 bids,低费腿买入吃 asks;深度不足以覆盖全量则 ok=false。
func estimateCrossVenueLegOpenSlippageBps(
	fetch func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error),
	ctx context.Context,
	v venue.Venue,
	sym venue.Symbol,
	side venue.Side,
	notionalUSDT, refMid float64,
) (float64, bool) {
	if fetch == nil || v == nil || notionalUSDT <= 0 || refMid <= 0 {
		return 0, false
	}
	depth, err := fetch(ctx, v, sym, crossVenueOpenSlippageDepthLimit)
	if err != nil {
		return 0, false
	}
	var levels []venue.DepthLevel
	switch side {
	case venue.SideBuy:
		levels = depth.Asks
	case venue.SideSell:
		levels = depth.Bids
	default:
		return 0, false
	}
	return simulateTakerSlippageBps(side, levels, notionalUSDT, refMid)
}

// simulateTakerSlippageBps 沿订单簿档位模拟吃单,返回相对最优价的滑点(bps)。
func simulateTakerSlippageBps(side venue.Side, levels []venue.DepthLevel, notionalUSDT, refPrice float64) (float64, bool) {
	if refPrice <= 0 || notionalUSDT <= 0 || len(levels) == 0 {
		return 0, false
	}
	bestPrice := 0.0
	for _, lv := range levels {
		if lv.Price > 0 && lv.Qty > 0 {
			bestPrice = lv.Price
			break
		}
	}
	if bestPrice <= 0 {
		return 0, false
	}
	qtyNeed := notionalUSDT / refPrice
	remaining := qtyNeed
	totalCost := 0.0
	filledQty := 0.0
	for _, lv := range levels {
		if lv.Price <= 0 || lv.Qty <= 0 {
			continue
		}
		take := lv.Qty
		if take > remaining {
			take = remaining
		}
		totalCost += take * lv.Price
		filledQty += take
		remaining -= take
		if remaining <= 1e-12 {
			break
		}
	}
	if filledQty <= 0 {
		return 0, false
	}
	if remaining > 1e-9 {
		return math.MaxFloat64, false
	}
	avgPrice := totalCost / filledQty
	var slipBps float64
	switch side {
	case venue.SideBuy:
		slipBps = (avgPrice - bestPrice) / bestPrice * 10000
	case venue.SideSell:
		slipBps = (bestPrice - avgPrice) / bestPrice * 10000
	default:
		return 0, false
	}
	if slipBps < 0 {
		slipBps = 0
	}
	return slipBps, true
}

func depthCapacityUSDT(
	fetch func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error),
	ctx context.Context,
	v venue.Venue,
	sym venue.Symbol,
	ratio float64,
) float64 {
	if ratio <= 0 {
		ratio = 0.20
	}
	depth, err := fetch(ctx, v, sym, 3)
	if err != nil {
		return 0
	}
	total := 0.0
	for i := 0; i < len(depth.Asks) && i < 3; i++ {
		total += depth.Asks[i].Price * depth.Asks[i].Qty
	}
	for i := 0; i < len(depth.Bids) && i < 3; i++ {
		total += depth.Bids[i].Price * depth.Bids[i].Qty
	}
	// 买卖各取一半深度 conservatively
	return total / 2 * ratio
}

func lookupVenueInstrument(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.Instrument, bool) {
	instrs, err := v.FetchInstruments(ctx)
	if err != nil {
		return venue.Instrument{}, false
	}
	for _, in := range instrs {
		if in.Symbol == sym && in.Market == venue.MarketPerp {
			return in, true
		}
	}
	return venue.Instrument{}, false
}

// crossVenueFundingSnapFromOppLeg venue 快照缺 symbol 时,用扫描器 Opp 已填 funding 字段作闸门回退(单测/新币首扫)。
func crossVenueFundingSnapFromOppLeg(opp CrossVenueOpportunity, highLeg bool, now time.Time) (venue.FundingSnapshot, bool) {
	var rate float64
	var interval int
	var next time.Time
	if highLeg {
		rate = opp.HighFundingRate
		interval = opp.HighIntervalHours
		next = opp.HighNextFundingTime
	} else {
		rate = opp.LowFundingRate
		interval = opp.LowIntervalHours
		next = opp.LowNextFundingTime
	}
	snap := venue.FundingSnapshot{
		LastFundingRate: rate,
		IntervalHours:   interval,
		NextFundingTime: next,
	}
	if !crossVenueFundingSnapshotValid(snap, now) {
		return venue.FundingSnapshot{}, false
	}
	return snap, true
}

// crossVenueFundingSnapshotValid 校验 bulk 快照里该 symbol 的 rate/结算间隔/下次结算时刻可用于 carry 判断。
func crossVenueFundingSnapshotValid(snap venue.FundingSnapshot, now time.Time) bool {
	if snap.NextFundingTime.IsZero() || !snap.NextFundingTime.After(now) {
		return false
	}
	if snap.IntervalHours <= 0 {
		return false
	}
	if math.IsNaN(snap.LastFundingRate) || math.IsInf(snap.LastFundingRate, 0) {
		return false
	}
	return true
}

// crossVenueGateLiquidityNotionalCapPerLeg 跨所 perp-perp 执行期流动性上限(每腿独立 symbol 拉 depth)。
func crossVenueGateLiquidityNotionalCapPerLeg(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	deps crossVenueGateDeps,
	highV, lowV venue.Venue,
	highSym, lowSym venue.Symbol,
) float64 {
	slipBps := crossVenueResolvedSizingMaxSlippageBps(cfg)
	gateSlipBps := crossVenueResolvedMaxOpenSlippageBps(cfg)
	if gateSlipBps > 0 && (slipBps <= 0 || gateSlipBps < slipBps) {
		slipBps = gateSlipBps
	}
	partPct := cfg.SizingMaxParticipationPct24h
	if slipBps <= 0 && partPct <= 0 {
		return math.MaxFloat64
	}
	if deps.fetchDepth == nil {
		return math.MaxFloat64
	}
	caps := make([]float64, 0, 2)
	if highV != nil {
		if depth, err := deps.fetchDepth(ctx, highV, highSym, crossVenueOpenSlippageDepthLimit); err == nil {
			vol := fetchVenuePerpQuoteVolume24h(ctx, highV, highSym)
			if cap := liquidityNotionalCap(depth, venue.SideSell, slipBps, vol, partPct); cap < math.MaxFloat64 {
				caps = append(caps, cap)
			}
		}
	}
	if lowV != nil {
		if depth, err := deps.fetchDepth(ctx, lowV, lowSym, crossVenueOpenSlippageDepthLimit); err == nil {
			vol := fetchVenuePerpQuoteVolume24h(ctx, lowV, lowSym)
			if cap := liquidityNotionalCap(depth, venue.SideBuy, slipBps, vol, partPct); cap < math.MaxFloat64 {
				caps = append(caps, cap)
			}
		}
	}
	if len(caps) == 0 {
		return math.MaxFloat64
	}
	out := caps[0]
	for i := 1; i < len(caps); i++ {
		if caps[i] < out {
			out = caps[i]
		}
	}
	return out
}

func fetchVenueFundingForSymbol(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.FundingSnapshot, bool) {
	snaps, err := v.FetchFundingSnapshot(ctx)
	if err != nil {
		return venue.FundingSnapshot{}, false
	}
	for _, s := range snaps {
		if s.Symbol == sym {
			return s, true
		}
	}
	return venue.FundingSnapshot{}, false
}

func isBybitSymbolHeld(recordDir string, sym venue.Symbol) bool {
	bybitDir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: recordDir}}, bybit.VenueName)
	store, err := newBybitPositionStore(bybitDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if p.Symbol == sym {
			return true
		}
	}
	return false
}
