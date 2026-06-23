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
	// 基差成本只对「不利方向」(做多腿比做空腿更贵,收敛时亏损)计;顺向有利不计。
	adverseDev := crossVenueAdverseMidDeviation(highMid, lowMid, highMult, lowMult)

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

	// 结算前进场窗闸(OPEN-only):只在最近一腿资金费结算前 ≤lead 才放行;太早(距结算 >lead)拒开,
	// 避免提前进场、结算前价差收敛白付手续费。与 carry 四策略同口径(reason 复用 funding_entry_window_not_reached,
	// 排行榜已能翻译)。放在深度/滑点/EV/余额等重计算之前短路省开销;两腿结算时间均未知则 fail-CLOSED(缺数据不盲开,FIX-10)。
	// 取两腿下次结算时间的最近一个;opp 字段为零时回退已校验的 high/low 快照值(与下文 evOpp 补值同口径)。
	openWinHighNext, openWinHighInterval := in.Opp.HighNextFundingTime, in.Opp.HighIntervalHours
	if openWinHighNext.IsZero() {
		openWinHighNext, openWinHighInterval = highSnap.NextFundingTime, highSnap.IntervalHours
	}
	openWinLowNext, openWinLowInterval := in.Opp.LowNextFundingTime, in.Opp.LowIntervalHours
	if openWinLowNext.IsZero() {
		openWinLowNext, openWinLowInterval = lowSnap.NextFundingTime, lowSnap.IntervalHours
	}
	openLead := time.Duration(crossVenueResolvedOpenFundingLeadSeconds(cfg)) * time.Second
	if crossVenueUnifiedActiveForSymbol(cfg, in.Opp.Symbol) {
		// 统一捕获模型:进场对齐"付钱的腿"的结算,而非时间最近的便宜腿(消灭"为 okx 开仓却贴着 bybit 结算进场"的错配)。
		targetLeg := crossVenueTargetFundingLeg(in.Opp.HighFundingRate, in.Opp.LowFundingRate)
		if !crossVenueFundingEntryWindowReachedTargeted(targetLeg, openWinHighNext, openWinHighInterval, openWinLowNext, openWinLowInterval, in.Now, openLead) {
			return fail(carryScannerRejectFundingWindowNotReached)
		}
	} else if !crossVenueFundingEntryWindowReached(openWinHighNext, openWinHighInterval, openWinLowNext, openWinLowInterval, in.Now, openLead) {
		return fail(carryScannerRejectFundingWindowNotReached)
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
	if evRes := evaluateCrossVenueOpenEV(cfg, evOpp, notional, evLegs, in.Now, holdHours, adverseDev); !evRes.OK {
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
	unifiedActive := crossVenueUnifiedActiveForSymbol(cfg, opp.Symbol)
	makerFirst := unifiedActive && (cfg.MakerFirst || cfg.AssumeMakerFills)
	for i := range legVenues {
		if makerFirst && i == 0 {
			continue // 统一模型:高费腿首腿 maker 挂单,近零穿价滑点(与执行端 maker-first 一致)
		}
		slipBps := evSlipBps
		if len(evLegs) > i {
			slipBps = crossVenueEVSlippageBpsPerLeg(cfg, notional, evLegs[i])
		}
		roundTripSlippage += notional * slipBps / 10000
	}
	roundTripCost := openCost + closeCost + roundTripSlippage
	// 统一模型入场硬条件:预期目标资费 / 往返成本 ≥ ratio,把"滑点远大于资费"的薄盘暴力小币挡在外面。
	if unifiedActive && cfg.CaptureFundingToCostMinRatio > 0 && roundTripCost > 0 {
		targetFunding := crossVenueExpectedTargetFundingUSDT(cfg, opp, notional)
		if targetFunding < cfg.CaptureFundingToCostMinRatio*roundTripCost {
			return failEV(fmt.Sprintf("资费/成本比 %.2f < %.2f(目标资费=%.4f 往返成本=%.4f)",
				targetFunding/roundTripCost, cfg.CaptureFundingToCostMinRatio, targetFunding, roundTripCost))
		}
	}
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(notional, opp.FreshnessAgeSeconds, holdHours, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT(notional)
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

// crossVenueShortestLegFundingIntervalHours 取两腿中较短的真实 funding interval(小时);
// 用作可比窗口缺失时的近端 horizon——取较短者最保守(更少结算次数→更低 funding EV)。两腿 interval 均未知→0。
func crossVenueShortestLegFundingIntervalHours(opp CrossVenueOpportunity) float64 {
	hi, lo := opp.HighIntervalHours, opp.LowIntervalHours
	switch {
	case hi > 0 && lo > 0:
		if hi < lo {
			return float64(hi)
		}
		return float64(lo)
	case hi > 0:
		return float64(hi)
	case lo > 0:
		return float64(lo)
	default:
		return 0
	}
}

// crossVenueResolvedNearTermOpenEVExpectedSettlements 近端 horizon 预期前向结算次数 N;cfg 未配/≤0 回退 3。
func crossVenueResolvedNearTermOpenEVExpectedSettlements(cfg CrossVenueExecutorConfig) float64 {
	if cfg.NearTermOpenEVExpectedSettlements > 0 {
		return float64(cfg.NearTermOpenEVExpectedSettlements)
	}
	return 3
}

// crossVenueResolvedNearTermOpenEVMaxHoldHours 近端 horizon 上限(小时);cfg 未配/≤0 回退 8。
func crossVenueResolvedNearTermOpenEVMaxHoldHours(cfg CrossVenueExecutorConfig) float64 {
	if cfg.NearTermOpenEVMaxHoldHours > 0 {
		return cfg.NearTermOpenEVMaxHoldHours
	}
	return 8
}

// crossVenueCapturePlanHoldHours 统一捕获模型下的计划持有时长(小时)= N × 目标腿 funding interval,
// 使 EV 资费分子只计"实际计划吃满的 N 次目标结算",而非用便宜腿/长 horizon 摊薄。interval 不可用时返回 0(回退)。
func crossVenueCapturePlanHoldHours(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity) float64 {
	leg := crossVenueTargetFundingLeg(opp.HighFundingRate, opp.LowFundingRate)
	interval := opp.HighIntervalHours
	if leg == "low" {
		interval = opp.LowIntervalHours
	}
	if interval <= 0 {
		return 0
	}
	n := crossVenueEffectiveCaptureSettlements(cfg, opp.StabilityScore)
	if n < 1 {
		n = 1
	}
	return float64(n) * float64(interval)
}

func crossVenueOpenEVHoldHours(cfg CrossVenueExecutorConfig, now time.Time, opp CrossVenueOpportunity, comparableWindowHours float64) float64 {
	if crossVenueUnifiedActiveForSymbol(cfg, opp.Symbol) {
		// 统一捕获模型:horizon = 计划持有(N 次目标结算),与进场/离场同口径(消除"摊 5 次结算的 EV 却第一次结算就离场")。
		if h := crossVenueCapturePlanHoldHours(cfg, opp); h > 0 {
			return h
		}
	}
	if cfg.NearTermOpenEVHorizonEnabled {
		window := comparableWindowHours
		if window <= 0 {
			window = crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
		}
		if window <= 0 && opp.ComparableWindowHours > 0 {
			window = opp.ComparableWindowHours
		}
		// horizon = 预期前向持有,而非"距下次结算的残余时间"。在进场窗内可比窗口≈0.04h,若直接拿它当 horizon,
		// EV 只给一次即将结算的 funding 却扣全额开+平往返成本 → netProfit<0 → 结构性拒开(FIX-8 矫枉过正)。
		// 改为:horizon 至少 = N × 最短腿 funding interval(N=NearTermOpenEVExpectedSettlements,默认 3),
		// 若可比窗口更大则保留可比窗口;再用 NearTermOpenEVMaxHoldHours(默认 8h)封顶,杜绝反向膨胀回 12h 式高估。
		// 下游 funding EV 仍按离散结算次数 × 捕获概率(≤1)逐次折现(crossVenueOpenGateExpectedFundingUSDT →
		// crossVenueExpectedFundingOverHold × crossVenueExecutorFundingCaptureProbability),故延长 horizon 不会
		// 把 N 个结算的瞬时尖峰过度计入(每个被计结算都已乘捕获概率)。
		if legInterval := crossVenueShortestLegFundingIntervalHours(opp); legInterval > 0 {
			expectedHold := crossVenueResolvedNearTermOpenEVExpectedSettlements(cfg) * legInterval
			result := math.Max(window, expectedHold)
			if capHours := crossVenueResolvedNearTermOpenEVMaxHoldHours(cfg); capHours > 0 && result > capHours {
				result = capHours
			}
			return result
		}
		// interval 未知 → 无法按 N×interval 计前向持有:回退可比窗口(可比窗口也缺则 0,最保守:不凭空给收入)。
		if window > 0 {
			return window
		}
		return 0
	}
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
	return funding * crossVenueExecutorFundingCaptureProbability(cfg)
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
	if interval <= 0 {
		venueName := opp.HighVenue
		if !highLeg {
			venueName = opp.LowVenue
		}
		interval = crossVenueVenueDefaultFundingIntervalHours(venueName, nil)
	}
	// 过期结算时刻按 interval 推进到下一结算点:旧快照或刚过结算点时,NextFundingTime 会落到过去,
	// 但只要 interval 已知,该腿就是有效的(只是时间戳过期),不应被误判为"幽灵腿"。
	// 真缺数据的腿(interval<=0 或 NextFundingTime 为零)无法推进,仍会被下方校验判为无效→幽灵(行为不变)。
	next = crossVenueAdvanceFundingTime(next, interval, now)
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

// crossVenueResolvedOpenFundingLeadSeconds 结算前进场窗(秒);cfg 未配/≤0 时回退默认 180(3 分钟)。
func crossVenueResolvedOpenFundingLeadSeconds(cfg CrossVenueExecutorConfig) int {
	if cfg.OpenFundingLeadSeconds > 0 {
		return cfg.OpenFundingLeadSeconds
	}
	return 180
}

func crossVenueResolvedTimingAwarePairSelectionMinHours(cfg CrossVenueExecutorConfig) float64 {
	if cfg.TimingAwarePairSelectionMinHours > 0 {
		return cfg.TimingAwarePairSelectionMinHours
	}
	return 0.01
}

// crossVenueUnifiedActiveForSymbol 统一捕获模型是否对该 symbol 生效:总开关 ON 且(白名单为空 或 symbol 命中白名单)。
// 灰度期可只放量少数 symbol;其余 symbol 走旧逻辑(开仓不建 plan → 持有/离场自然回退,口径一致无混用)。
func crossVenueUnifiedActiveForSymbol(cfg CrossVenueExecutorConfig, symbol string) bool {
	if !cfg.UnifiedCaptureModelEnabled {
		return false
	}
	if len(cfg.UnifiedCaptureModelSymbolAllowlist) == 0 {
		return true
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	for _, s := range cfg.UnifiedCaptureModelSymbolAllowlist {
		if strings.ToUpper(strings.TrimSpace(s)) == symbol {
			return true
		}
	}
	return false
}

// crossVenueResolvedCaptureTargetSettlements 统一捕获模型默认目标结算次数 N;cfg 未配/≤0 回退 1。
func crossVenueResolvedCaptureTargetSettlements(cfg CrossVenueExecutorConfig) int {
	if cfg.CaptureTargetSettlements > 0 {
		return cfg.CaptureTargetSettlements
	}
	return 1
}

// crossVenueEffectiveCaptureSettlements 实际允许的目标结算次数:N>1 仅在基差稳定分≥阈值时放行,否则夹回 1。
// 杜绝"在不稳定/暴力币上贪多次结算 → 基差暴露翻倍"的风险。
func crossVenueEffectiveCaptureSettlements(cfg CrossVenueExecutorConfig, stabilityScore float64) int {
	n := crossVenueResolvedCaptureTargetSettlements(cfg)
	if n <= 1 {
		return n
	}
	thr := cfg.BasisStabilityMinScoreForMultiSettlement
	if thr <= 0 {
		thr = 0.7
	}
	if stabilityScore < thr {
		return 1
	}
	return n
}

// crossVenueExpectedTargetFundingUSDT 计划区间内预期从"目标腿"收到的资费(毛,USDT);仅计收钱方向,负则 0。
func crossVenueExpectedTargetFundingUSDT(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	leg := crossVenueTargetFundingLeg(opp.HighFundingRate, opp.LowFundingRate)
	rate := opp.HighFundingRate
	if leg == "low" {
		rate = -opp.LowFundingRate
	}
	per := rate * notional
	if per < 0 {
		per = 0
	}
	n := crossVenueEffectiveCaptureSettlements(cfg, opp.StabilityScore)
	return per * float64(n)
}

// crossVenueResolvedBasisStopBudgetFraction 基差止损预算系数(× 预期目标资费);cfg 未配/≤0 回退 1.0。
func crossVenueResolvedBasisStopBudgetFraction(cfg CrossVenueExecutorConfig) float64 {
	if cfg.BasisStopBudgetFraction > 0 {
		return cfg.BasisStopBudgetFraction
	}
	return 1.0
}

// crossVenueResolvedPlannedExitBufferSeconds 持有到目标结算后的离场缓冲(秒);cfg 未配/≤0 回退 90。
func crossVenueResolvedPlannedExitBufferSeconds(cfg CrossVenueExecutorConfig) int {
	if cfg.PlannedExitBufferSeconds > 0 {
		return cfg.PlannedExitBufferSeconds
	}
	return 90
}

// crossVenueNearestFundingSettlement 取两腿下次结算中最近的一个(过期时刻按 interval 推进)。
func crossVenueNearestFundingSettlement(now, highNext time.Time, highInterval int, lowNext time.Time, lowInterval int) time.Time {
	var nearest time.Time
	consider := func(next time.Time, intervalHours int) {
		if next.IsZero() {
			return
		}
		if !next.After(now) {
			next = crossVenueAdvanceFundingTime(next, intervalHours, now)
			if !next.After(now) {
				return
			}
		}
		if nearest.IsZero() || next.Before(nearest) {
			nearest = next
		}
	}
	consider(highNext, highInterval)
	consider(lowNext, lowInterval)
	return nearest
}

// crossVenueHoursUntilFundingEntryWindow 距最近结算的进场窗(≤lead)尚余小时数;已在窗内或时间未知→0。
func crossVenueHoursUntilFundingEntryWindow(now, highNext time.Time, highInterval int, lowNext time.Time, lowInterval int, lead time.Duration) float64 {
	nearest := crossVenueNearestFundingSettlement(now, highNext, highInterval, lowNext, lowInterval)
	if nearest.IsZero() {
		return 0
	}
	if lead <= 0 {
		lead = time.Duration(crossVenueResolvedOpenFundingLeadSeconds(CrossVenueExecutorConfig{})) * time.Second
	}
	untilEntry := nearest.Sub(now) - lead
	if untilEntry <= 0 {
		return 0
	}
	return untilEntry.Hours()
}

// crossVenuePairSelectionScore 扫描期 per-symbol 腿对排序分:timing-aware 时为可比窗口 funding EV / (等待+窗口)小时。
func crossVenuePairSelectionScore(cfg CrossVenueExecutorConfig, now time.Time, high, low symbolVenueFunding, highNext, lowNext time.Time) float64 {
	ev := crossVenuePairExpectedFundingOverComparableWindow(now, high, low)
	if !cfg.TimingAwarePairSelectionEnabled {
		return ev
	}
	windowHours := crossVenueComparableWindowHours(now, highNext, lowNext)
	lead := time.Duration(crossVenueResolvedOpenFundingLeadSeconds(cfg)) * time.Second
	waitHours := crossVenueHoursUntilFundingEntryWindow(now, highNext, high.IntervalHours, lowNext, low.IntervalHours, lead)
	denom := waitHours + windowHours
	minHours := crossVenueResolvedTimingAwarePairSelectionMinHours(cfg)
	if denom < minHours {
		denom = minHours
	}
	return ev / denom
}

// crossVenueAdvanceFundingTime 把过期(≤now)的结算时刻按 interval 向未来推进到下一个结算点。
// interval 未知(≤0)时无法推进,原样返回(调用方据此判定该腿时间不可用)。
func crossVenueAdvanceFundingTime(next time.Time, intervalHours int, now time.Time) time.Time {
	if next.IsZero() || intervalHours <= 0 {
		return next
	}
	interval := time.Duration(intervalHours) * time.Hour
	for !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

// crossVenueFundingEntryWindowReached 结算前进场窗闸(OPEN/SWITCH-only):取两腿下次结算时间的最近一个 nearest,
// 仅当 nearest 距今 ≤lead 才放行(进场窗已到)。过期结算时刻按该腿 interval 推进到下一结算点再判;
// FIX-10(b):两腿结算时间均未知/不可用→fail-CLOSED(返回 false,不放行)——开/换仓缺时间不盲开,宁可跳过。
func crossVenueFundingEntryWindowReached(highNext time.Time, highInterval int, lowNext time.Time, lowInterval int, now time.Time, lead time.Duration) bool {
	var nearest time.Time
	consider := func(next time.Time, intervalHours int) {
		if next.IsZero() {
			return
		}
		if !next.After(now) {
			next = crossVenueAdvanceFundingTime(next, intervalHours, now)
			if !next.After(now) {
				return // 无法推进(interval 未知)→ 该腿时间视为不可用
			}
		}
		if nearest.IsZero() || next.Before(nearest) {
			nearest = next
		}
	}
	consider(highNext, highInterval)
	consider(lowNext, lowInterval)
	if nearest.IsZero() {
		return false // 两腿结算时间均未知 → fail-closed(开/换仓宁可跳过)
	}
	return nearest.Sub(now) <= lead
}

// crossVenueTargetFundingLeg 返回实际付钱给我们的"目标腿":"high"=空高费腿,"low"=多低费腿。
// 约定:空腿在 rate>0 时收(单次现金流 +highRate),多腿在 rate<0 时收(单次现金流 −lowRate);
// 取单次结算现金流更大的那条腿。统一捕获模型据此把进场对齐到"付钱的腿",而非时间最近的便宜腿。
func crossVenueTargetFundingLeg(highRate, lowRate float64) string {
	highGain := highRate // 空腿:+rate
	lowGain := -lowRate  // 多腿:−rate
	if lowGain > highGain {
		return "low"
	}
	return "high"
}

// crossVenueTargetLegNextSettlement 取目标腿的下次结算时刻(过期则按 interval 推进到未来);零时间表示不可用。
func crossVenueTargetLegNextSettlement(targetLeg string, highNext time.Time, highInterval int, lowNext time.Time, lowInterval int, now time.Time) time.Time {
	next, interval := highNext, highInterval
	if targetLeg == "low" {
		next, interval = lowNext, lowInterval
	}
	if next.IsZero() {
		return time.Time{}
	}
	if !next.After(now) {
		next = crossVenueAdvanceFundingTime(next, interval, now)
		if !next.After(now) {
			return time.Time{}
		}
	}
	return next
}

// crossVenueFundingEntryWindowReachedTargeted 统一捕获模型的进场窗闸:只看"目标腿"的下次结算,
// 距今 ≤lead 才放行。目标腿时间不可用 → fail-CLOSED(返回 false)。
func crossVenueFundingEntryWindowReachedTargeted(targetLeg string, highNext time.Time, highInterval int, lowNext time.Time, lowInterval int, now time.Time, lead time.Duration) bool {
	target := crossVenueTargetLegNextSettlement(targetLeg, highNext, highInterval, lowNext, lowInterval, now)
	if target.IsZero() {
		return false
	}
	return target.Sub(now) <= lead
}

// crossVenueBuildCapturePlan 开仓时构建统一资费捕获计划(flag OFF 或目标腿时间不可用时返回 nil → 回退旧行为)。
// 计划钉死:目标腿、目标结算时刻、目标结算次数 N、计划离场(末次目标结算+缓冲)、预期目标资费、基差止损预算。
func crossVenueBuildCapturePlan(cfg CrossVenueExecutorConfig, symbol string, highRate, lowRate, notional float64, highInterval int, highNext time.Time, lowInterval int, lowNext time.Time, openedAt time.Time, stabilityScore float64) *CarryCapturePlan {
	if !crossVenueUnifiedActiveForSymbol(cfg, symbol) {
		return nil
	}
	if openedAt.IsZero() {
		openedAt = time.Now().UTC()
	}
	leg := crossVenueTargetFundingLeg(highRate, lowRate)
	interval := highInterval
	rate := highRate // 空腿单次收 +rate
	if leg == "low" {
		interval = lowInterval
		rate = -lowRate // 多腿单次收 −rate
	}
	target := crossVenueTargetLegNextSettlement(leg, highNext, highInterval, lowNext, lowInterval, openedAt)
	if target.IsZero() {
		return nil
	}
	n := crossVenueEffectiveCaptureSettlements(cfg, stabilityScore)
	if n < 1 {
		n = 1
	}
	lastSettlement := target
	if n > 1 && interval > 0 {
		lastSettlement = target.Add(time.Duration(n-1) * time.Duration(interval) * time.Hour)
	}
	buffer := time.Duration(crossVenueResolvedPlannedExitBufferSeconds(cfg)) * time.Second
	// 预期目标资费(毛):目标腿单次收益率 × 名义 × N(只计我们真正收钱的方向,负则置 0)。
	perSettlement := rate * notional
	if perSettlement < 0 {
		perSettlement = 0
	}
	expected := perSettlement * float64(n)
	return &CarryCapturePlan{
		TargetLeg:                 leg,
		TargetSettlementTime:      target,
		TargetSettlements:         n,
		PlannedExitTime:           lastSettlement.Add(buffer),
		ExpectedTargetFundingUSDT: expected,
		BasisBudgetUSDT:           crossVenueResolvedBasisStopBudgetFraction(cfg) * expected,
	}
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
