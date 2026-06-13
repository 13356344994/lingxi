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

var crossVenueTradeableVenues = map[string]bool{
	binance.VenueName: true,
	okx.VenueName:     true,
	gate.VenueName:    true,
	bybit.VenueName:   true,
}

// crossVenueGateInput 是闸门评估所需的上下文。
type crossVenueGateInput struct {
	Opp           CrossVenueOpportunity
	Positions     []CrossVenuePosition
	BybitHeld     func(sym venue.Symbol) bool
	BinanceHeld   func(sym venue.Symbol) bool // 币安遗留 FundingRateSniper 台账在场
	FundingHeld   func(sym venue.Symbol) bool // 任一 okx/gate 单所 funding 引擎台账在场
	HaltActive    bool
	SymbolCooling func(sym string) bool
	Now           time.Time
}

// crossVenueGateResult 是闸门评估结果。
type crossVenueGateResult struct {
	OK              bool
	Reason          string
	NotionalUSDT    float64
	HighMid         float64
	LowMid          float64
	HighInstr       venue.Instrument
	LowInstr        venue.Instrument
	HighFundingSnap venue.FundingSnapshot
	LowFundingSnap  venue.FundingSnapshot
}

// crossVenueGateDeps 是闸门所需的最小 venue 能力。
type crossVenueGateDeps struct {
	lookupInstrument func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.Instrument, bool)
	fetchBookTicker  func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error)
	fetchDepth       func(ctx context.Context, v venue.Venue, sym venue.Symbol, limit int) (venue.Depth, error)
	fetchFunding     func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.FundingSnapshot, bool)
	fetchBalanceUSDT func(ctx context.Context, v venue.Venue) (float64, error)
	venueCaps        func(v venue.Venue) venue.Capabilities
	allocatedUSDT    func(venueName string) float64
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
	if in.Opp.SpreadAPR < cfg.OpenSpreadAPR {
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
	sym := venue.Symbol(in.Opp.Symbol)
	if in.BybitHeld != nil && in.BybitHeld(sym) {
		return fail("symbol 已被 Bybit 单所引擎持有")
	}
	if in.BinanceHeld != nil && in.BinanceHeld(sym) {
		return fail("symbol 已被 Binance 遗留引擎持有")
	}
	if in.FundingHeld != nil && in.FundingHeld(sym) {
		return fail("symbol 已被 OKX/Gate 单所 funding 引擎持有")
	}
	for _, p := range in.Positions {
		if p.Symbol == in.Opp.Symbol && p.Status != CrossVenueStatusClosed {
			return fail("symbol 已有在场跨所持仓")
		}
	}
	if in.SymbolCooling != nil && in.SymbolCooling(in.Opp.Symbol) {
		return fail("symbol 处于回滚冷却期")
	}

	highSnap, ok := deps.fetchFunding(ctx, highV, sym)
	if !ok || highSnap.NextFundingTime.IsZero() || !highSnap.NextFundingTime.After(in.Now) {
		return fail("高费所幽灵腿:NextFundingTime 无效")
	}
	lowSnap, ok := deps.fetchFunding(ctx, lowV, sym)
	if !ok || lowSnap.NextFundingTime.IsZero() || !lowSnap.NextFundingTime.After(in.Now) {
		return fail("低费所幽灵腿:NextFundingTime 无效")
	}

	highInstr, ok := deps.lookupInstrument(ctx, highV, sym)
	if !ok {
		return fail("高费所 lookupInstrument 失败")
	}
	lowInstr, ok := deps.lookupInstrument(ctx, lowV, sym)
	if !ok {
		return fail("低费所 lookupInstrument 失败")
	}

	highBook, err := deps.fetchBookTicker(ctx, highV, sym)
	if err != nil || highBook.BidPrice <= 0 || highBook.AskPrice <= 0 {
		return fail("高费所盘口无效")
	}
	lowBook, err := deps.fetchBookTicker(ctx, lowV, sym)
	if err != nil || lowBook.BidPrice <= 0 || lowBook.AskPrice <= 0 {
		return fail("低费所盘口无效")
	}
	highMid := (highBook.BidPrice + highBook.AskPrice) / 2
	lowMid := (lowBook.BidPrice + lowBook.AskPrice) / 2
	if highMid <= 0 || lowMid <= 0 {
		return fail("中间价无效")
	}
	dev := math.Abs(highMid-lowMid) / math.Max(highMid, lowMid)
	maxDev := cfg.MidPriceDeviationMax
	if maxDev <= 0 {
		maxDev = 0.03
	}
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
	capHigh := depthCapacityUSDT(deps.fetchDepth, ctx, highV, sym, cfg.DepthUsageRatio)
	capLow := depthCapacityUSDT(deps.fetchDepth, ctx, lowV, sym, cfg.DepthUsageRatio)
	if capHigh > 0 && capHigh < notional {
		notional = capHigh
	}
	if capLow > 0 && capLow < notional {
		notional = capLow
	}
	if notional <= 0 {
		return fail("深度容量不足")
	}

	levCap := cfg.MaxEffectiveLeverage
	if levCap <= 0 {
		levCap = 2
	}
	marginFactor := cfg.MarginSafetyFactor
	if marginFactor <= 0 {
		marginFactor = 1.5
	}
	requiredMargin := notional / levCap * marginFactor
	highBal, _ := deps.fetchBalanceUSDT(ctx, highV)
	lowBal, _ := deps.fetchBalanceUSDT(ctx, lowV)
	if highBal < requiredMargin {
		return fail(fmt.Sprintf("高费所可用 USDT %.2f < 所需 %.2f", highBal, requiredMargin))
	}
	if lowBal < requiredMargin {
		return fail(fmt.Sprintf("低费所可用 USDT %.2f < 所需 %.2f", lowBal, requiredMargin))
	}
	highAlloc := deps.allocatedUSDT(in.Opp.HighVenue)
	lowAlloc := deps.allocatedUSDT(in.Opp.LowVenue)
	if highAlloc > 0 && notional/highAlloc > levCap+1e-9 {
		return fail(fmt.Sprintf("高费所有效杠杆 %.2fx > %.2fx", notional/highAlloc, levCap))
	}
	if lowAlloc > 0 && notional/lowAlloc > levCap+1e-9 {
		return fail(fmt.Sprintf("低费所有效杠杆 %.2fx > %.2fx", notional/lowAlloc, levCap))
	}

	_ = deps.venueCaps // 预留 contract-based 校验
	return crossVenueGateResult{
		OK:              true,
		NotionalUSDT:    notional,
		HighMid:         highMid,
		LowMid:          lowMid,
		HighInstr:       highInstr,
		LowInstr:        lowInstr,
		HighFundingSnap: highSnap,
		LowFundingSnap:  lowSnap,
	}
}

func bookSpreadBps(t venue.BookTicker) float64 {
	mid := (t.BidPrice + t.AskPrice) / 2
	if mid <= 0 {
		return math.MaxFloat64
	}
	return (t.AskPrice - t.BidPrice) / mid * 10000
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

func fetchVenueUSDTBalance(ctx context.Context, v venue.Venue) (float64, error) {
	bals, err := v.FetchBalances(ctx)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for asset, b := range bals.ByAsset {
		if strings.EqualFold(asset, "USDT") {
			total += b.Free
		}
	}
	return total, nil
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
