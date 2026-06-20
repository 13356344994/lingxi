package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

const (
	recentFlattenTTL         = 30 * time.Minute
	recentFlattenJSONLName   = "recent_flatten.jsonl"
	unresolvedOrphanReason   = "unresolved_orphan"
)

const defaultDustCloseThresholdUSDT = 1.0

// dustCloseDeferUntilFunding 在下次资金费结算前返回 true, 表示应推迟 dust_orphan 名义平仓。
func dustCloseDeferUntilFunding(now, nextFunding time.Time) bool {
	if nextFunding.IsZero() {
		return true
	}
	return now.Before(nextFunding)
}

// carryDeferDustOrphanDecision 对非裸腿 dust 孤腿, 结算点前挂账不平仓。
func carryDeferDustOrphanDecision(now, nextFunding time.Time, decision dustOrphanDecision) bool {
	if !decision.IsDustOrphan || decision.NakedRisk {
		return false
	}
	return dustCloseDeferUntilFunding(now, nextFunding)
}

// carryEVWeakFundingCloseAllowed 是否允许以 funding=0 / EV转弱 理由平仓:
// 须已过 NextFundingTime, 且至少 sync 过一次(LastFundingSyncAt >= NextFundingTime)。
func carryEVWeakFundingCloseAllowed(now, nextFunding, lastFundingSync time.Time) bool {
	if nextFunding.IsZero() {
		return false
	}
	if now.Before(nextFunding) {
		return false
	}
	if lastFundingSync.IsZero() || lastFundingSync.Before(nextFunding) {
		return false
	}
	return true
}

// dustOrphanDecision 是两腿策略孤腿检测结论。
type dustOrphanDecision struct {
	IsOrphan              bool
	IsDustOrphan          bool
	NakedRisk             bool
	RemainingNotionalUSDT float64
}

func dustCloseThresholdUSDT(cfg CrossVenueExecutorConfig) float64 {
	if cfg.DustCloseThresholdUSDT > 0 {
		return cfg.DustCloseThresholdUSDT
	}
	return defaultDustCloseThresholdUSDT
}

func effectiveDustThresholdUSDT(cfg CrossVenueExecutorConfig, exchangeMinNotional float64) float64 {
	floor := dustCloseThresholdUSDT(cfg)
	if exchangeMinNotional > floor {
		return exchangeMinNotional
	}
	return floor
}

func venueInstrumentMinNotional(ctx context.Context, v venue.Venue, symbol string, market venue.Market) float64 {
	if v == nil {
		return 0
	}
	insts, err := v.FetchInstruments(ctx)
	if err != nil {
		return 0
	}
	want := strings.ToUpper(strings.TrimSpace(symbol))
	for _, inst := range insts {
		if inst.Market != market {
			continue
		}
		if strings.ToUpper(string(inst.Symbol)) == want {
			return inst.MinNotional
		}
	}
	return 0
}

func legNotionalUSDT(ctx context.Context, v venue.Venue, sym venue.Symbol, market venue.Market, qty, entryPrice float64) float64 {
	if qty <= executionQtyTolerance {
		return 0
	}
	price := entryPrice
	if v != nil {
		if bt, err := v.FetchBookTicker(ctx, market, sym); err == nil {
			if mid := (bt.BidPrice + bt.AskPrice) / 2; mid > 0 {
				price = mid
			} else if bt.AskPrice > 0 {
				price = bt.AskPrice
			} else if bt.BidPrice > 0 {
				price = bt.BidPrice
			}
		}
	}
	if price <= 0 {
		return 0
	}
	return qty * price
}

// evaluatePositiveCarryOrphan 正 carry(现货多 + 永续空):永续为风险腿,现货残余可为 dust。
func evaluatePositiveCarryOrphan(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotQty, perpQty, spotEntryPrice float64,
) dustOrphanDecision {
	if spotOpen == perpOpen {
		return dustOrphanDecision{}
	}
	d := dustOrphanDecision{IsOrphan: true}
	if perpOpen && !spotOpen {
		d.NakedRisk = true
		return d
	}
	sym := venue.Symbol(symbol)
	spotV := venues[spotVenue]
	notional := legNotionalUSDT(ctx, spotV, sym, venue.MarketSpot, spotQty, spotEntryPrice)
	minN := venueInstrumentMinNotional(ctx, spotV, symbol, venue.MarketSpot)
	threshold := effectiveDustThresholdUSDT(cfg, minN)
	d.RemainingNotionalUSDT = notional
	if notional > 0 && notional < threshold {
		d.IsDustOrphan = true
	} else {
		d.NakedRisk = true
	}
	return d
}

// evaluateNegativeCarryOrphan 负 carry(现货空/借 + 永续多):永续残余可为 dust;
// 现货空孤腿若名义低于 minNotional 亦视为 dust(交易所下不出还币单,应收口而非无限重试)。
func evaluateNegativeCarryOrphan(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotShort, perpLong, spotEntryPrice, perpEntryPrice float64,
) dustOrphanDecision {
	if spotOpen == perpOpen {
		return dustOrphanDecision{}
	}
	d := dustOrphanDecision{IsOrphan: true}
	sym := venue.Symbol(symbol)

	if spotOpen && !perpOpen {
		spotV := venues[spotVenue]
		notional := legNotionalUSDT(ctx, spotV, sym, venue.MarketSpot, spotShort, spotEntryPrice)
		minN := venueInstrumentMinNotional(ctx, spotV, symbol, venue.MarketSpot)
		threshold := effectiveDustThresholdUSDT(cfg, minN)
		d.RemainingNotionalUSDT = notional
		if notional > 0 && notional < threshold {
			d.IsDustOrphan = true
		} else {
			d.NakedRisk = true
		}
		return d
	}

	perpV := venues[perpVenue]
	notional := legNotionalUSDT(ctx, perpV, sym, venue.MarketPerp, perpLong, perpEntryPrice)
	minN := venueInstrumentMinNotional(ctx, perpV, symbol, venue.MarketPerp)
	threshold := effectiveDustThresholdUSDT(cfg, minN)
	d.RemainingNotionalUSDT = notional
	if notional > 0 && notional < threshold {
		d.IsDustOrphan = true
	} else {
		d.NakedRisk = true
	}
	return d
}

// isNotionalFilterError 识别交易所 minNotional 拒单(-1013 NOTIONAL filter)。
func isNotionalFilterError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	if !strings.Contains(msg, "NOTIONAL") {
		return false
	}
	var ve *venue.VenueError
	if errors.As(err, &ve) && ve.RawCode == "-1013" {
		return true
	}
	return strings.Contains(msg, "-1013") || strings.Contains(msg, "FILTER FAILURE")
}

// negativeCarryDustFromNotionalReject 平仓回 -1013 NOTIONAL 时补判 dust(与 evaluate 口径一致)。
func negativeCarryDustFromNotionalReject(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotShort, perpLong, spotEntryPrice, perpEntryPrice float64,
) dustOrphanDecision {
	return evaluateNegativeCarryOrphan(ctx, cfg, venues, symbol, spotVenue, perpVenue, spotOpen, perpOpen, spotShort, perpLong, spotEntryPrice, perpEntryPrice)
}

type marginBorrowRepayer interface {
	BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error
}

// bestEffortRepayMarginBorrow 对借币空头 dust 尝试 margin 直接还币(不经买卖);失败静默跳过。
func bestEffortRepayMarginBorrow(ctx context.Context, v venue.Venue, symbol string, qty float64) {
	if v == nil || qty <= executionQtyTolerance {
		return
	}
	repayer, ok := v.(marginBorrowRepayer)
	if !ok {
		return
	}
	asset := baseAssetFromCanonicalSymbol(symbol)
	_ = repayer.BorrowRepay(ctx, asset, qty, venue.BorrowDirRepay)
}

func negativeCarryDustResidualQty(spotOpen, perpOpen bool, spotShort, perpLong float64) float64 {
	switch {
	case spotOpen && !perpOpen:
		return spotShort
	case perpOpen && !spotOpen:
		return perpLong
	default:
		return 0
	}
}

// appendCarryDustLedgerBestEffort 把 carry 执行器 dust 残腿记入 dust_ledger.json(失败静默)。
func appendCarryDustLedgerBestEffort(recordDir, phase, symbol, asset string, qty, notionalUSDT float64, reason string) {
	if strings.TrimSpace(recordDir) == "" || qty <= executionQtyTolerance {
		return
	}
	path := filepath.Join(strings.TrimSpace(recordDir), "dust_ledger.json")
	records, _ := readJSONFile[DustLedgerRecord](path)
	record := DustLedgerRecord{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Phase:        phase,
		Symbol:       symbol,
		Pair:         symbol,
		Asset:        asset,
		Quantity:     formatFloat(qty),
		NotionalUSDT: formatFloat(maxFloat(notionalUSDT, 0)),
		Reason:       reason,
	}
	records = append(records, record)
	_ = writeJSONFile(path, records)
}

func logDustOrphanClosed(logger *log.Logger, strategyLabel, symbol string, notional float64) {
	if logger == nil {
		return
	}
	logger.Printf("[%s] 残余 dust ≈$%.4f 低于最小可平,标记已平,保留零头 %s", strategyLabel, notional, symbol)
}

func appendDustCloseReason(base string, notional float64) string {
	suffix := fmt.Sprintf("dust_residual≈$%.4f", notional)
	if strings.TrimSpace(base) == "" {
		return suffix
	}
	return base + ";" + suffix
}

type carryUntrackedLeg struct {
	Venue      string
	Symbol     string
	Market     venue.Market
	Qty        float64
	Side       venue.Side
	ReduceOnly bool
}

// carryUntrackedLegMaterialOrRecordDust 材料级未登记腿才返回 true;低于 dust 阈值只记 dust_ledger 并打 debug 日志。
func carryUntrackedLegMaterialOrRecordDust(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	recordDir string,
	logger *log.Logger,
	venues map[string]venue.Venue,
	leg carryUntrackedLeg,
) bool {
	v := venues[leg.Venue]
	sym := venue.Symbol(leg.Symbol)
	minN := venueInstrumentMinNotional(ctx, v, leg.Symbol, leg.Market)
	threshold := effectiveDustThresholdUSDT(cfg, minN)
	notional := legNotionalUSDT(ctx, v, sym, leg.Market, leg.Qty, 0)
	if notional > 0 && notional < threshold {
		appendCarryDustLedgerBestEffort(recordDir, "untracked_scan", leg.Symbol, baseAssetFromCanonicalSymbol(leg.Symbol), leg.Qty, notional, "carry_untracked_dust")
		if logger != nil {
			logger.Printf("[carry-dust] reconcile skip untracked dust: venue=%s market=%s %s qty≈%.8f notional≈$%.4f threshold=$%.2f",
				leg.Venue, leg.Market, leg.Symbol, leg.Qty, notional, threshold)
		}
		return false
	}
	return true
}

func carryAppendMaterialUntrackedLeg(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	recordDir string,
	logger *log.Logger,
	venues map[string]venue.Venue,
	leg carryUntrackedLeg,
	out *[]carryUntrackedLeg,
) {
	if carryIsUnresolvedOrphan(recordDir, leg) {
		return
	}
	if carryUntrackedLegMaterialOrRecordDust(ctx, cfg, recordDir, logger, venues, leg) {
		*out = append(*out, leg)
	}
}

func carryPerpLegOwnedByPerpPerp(recordDir, venueName string, sym venue.Symbol) bool {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return false
	}
	store, err := newCrossVenuePositionStore(recordDir)
	if err != nil {
		return false
	}
	ps, err := store.load()
	if err != nil {
		return false
	}
	return crossVenueHasTrackedLeg(ps, venueName, sym)
}

func carryPerpLegSkipUntrackedScan(recordDir, venueName string, sym venue.Symbol, selfTracked bool) bool {
	if selfTracked {
		return true
	}
	if carryStrategyOwnsPerpLeg(recordDir, venueName, sym) {
		return true
	}
	return carryPerpLegOwnedByPerpPerp(recordDir, venueName, sym)
}

func carrySpotLegSkipUntrackedScan(recordDir, venueName, sym string, selfTracked bool) bool {
	if selfTracked {
		return true
	}
	return carryStrategyOwnsSpotLeg(recordDir, venueName, venue.Symbol(sym))
}

type recentFlattenKey struct {
	Venue   string
	Symbol  string
	Side    string
	QtyBand int
}

type recentFlattenEntry struct {
	Key         recentFlattenKey
	FlattenedAt time.Time
}

type recentFlattenRecord struct {
	RecordedAt string `json:"recorded_at"`
	Venue      string `json:"venue"`
	Symbol     string `json:"symbol"`
	Side       string `json:"side"`
	QtyBand    int    `json:"qty_band"`
}

type recentFlattenRegistry struct {
	mu      sync.Mutex
	entries map[string][]recentFlattenEntry
	loaded  map[string]bool
}

var globalRecentFlatten recentFlattenRegistry

func init() {
	globalRecentFlatten.entries = make(map[string][]recentFlattenEntry)
	globalRecentFlatten.loaded = make(map[string]bool)
}

func recentFlattenQtyBand(qty float64) int {
	if qty <= executionQtyTolerance {
		return 0
	}
	return int(math.Floor(math.Log10(qty)))
}

func recentFlattenKeyFromLeg(leg carryUntrackedLeg) recentFlattenKey {
	return recentFlattenKey{
		Venue:   strings.TrimSpace(leg.Venue),
		Symbol:  canonicalTradingSymbol(leg.Symbol),
		Side:    leg.Side.String(),
		QtyBand: recentFlattenQtyBand(leg.Qty),
	}
}

func recentFlattenJSONLPath(recordDir string) string {
	return filepath.Join(strings.TrimSpace(recordDir), recentFlattenJSONLName)
}

func (r *recentFlattenRegistry) ensureLoaded(recordDir string) {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" || r.loaded[recordDir] {
		return
	}
	records, err := readJSONLFile[recentFlattenRecord](recentFlattenJSONLPath(recordDir))
	if err == nil {
		cutoff := time.Now().UTC().Add(-recentFlattenTTL)
		for _, rec := range records {
			at, perr := time.Parse(time.RFC3339, strings.TrimSpace(rec.RecordedAt))
			if perr != nil || at.Before(cutoff) {
				continue
			}
			key := recentFlattenKey{
				Venue:   strings.TrimSpace(rec.Venue),
				Symbol:  canonicalTradingSymbol(rec.Symbol),
				Side:    strings.TrimSpace(rec.Side),
				QtyBand: rec.QtyBand,
			}
			r.entries[recordDir] = append(r.entries[recordDir], recentFlattenEntry{Key: key, FlattenedAt: at})
		}
	}
	r.loaded[recordDir] = true
}

func (r *recentFlattenRegistry) prune(recordDir string, now time.Time) {
	cutoff := now.Add(-recentFlattenTTL)
	kept := r.entries[recordDir][:0]
	for _, e := range r.entries[recordDir] {
		if !e.FlattenedAt.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	r.entries[recordDir] = kept
}

func recentlyFlattened(recordDir string, key recentFlattenKey, within time.Duration) bool {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return false
	}
	now := time.Now().UTC()
	cutoff := now.Add(-within)
	globalRecentFlatten.mu.Lock()
	defer globalRecentFlatten.mu.Unlock()
	globalRecentFlatten.ensureLoaded(recordDir)
	globalRecentFlatten.prune(recordDir, now)
	for _, e := range globalRecentFlatten.entries[recordDir] {
		if e.Key != key || e.FlattenedAt.Before(cutoff) {
			continue
		}
		return true
	}
	return false
}

func markRecentFlatten(recordDir string, key recentFlattenKey) {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return
	}
	now := time.Now().UTC()
	globalRecentFlatten.mu.Lock()
	globalRecentFlatten.ensureLoaded(recordDir)
	globalRecentFlatten.entries[recordDir] = append(globalRecentFlatten.entries[recordDir], recentFlattenEntry{
		Key:         key,
		FlattenedAt: now,
	})
	globalRecentFlatten.prune(recordDir, now)
	globalRecentFlatten.mu.Unlock()
	_ = appendJSONLRecord(recentFlattenJSONLPath(recordDir), recentFlattenRecord{
		RecordedAt: now.Format(time.RFC3339),
		Venue:      key.Venue,
		Symbol:     key.Symbol,
		Side:       key.Side,
		QtyBand:    key.QtyBand,
	})
}

func carryIsUnresolvedOrphan(recordDir string, leg carryUntrackedLeg) bool {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		return false
	}
	path := filepath.Join(recordDir, "dust_ledger.json")
	records, err := readJSONFile[DustLedgerRecord](path)
	if err != nil {
		return false
	}
	wantVenue := strings.TrimSpace(leg.Venue)
	wantSymbol := canonicalTradingSymbol(leg.Symbol)
	wantMarket := leg.Market.String()
	wantSide := leg.Side.String()
	for _, rec := range records {
		if rec.Reason != unresolvedOrphanReason {
			continue
		}
		if canonicalTradingSymbol(rec.Symbol) != wantSymbol {
			continue
		}
		phase := strings.TrimSpace(rec.Phase)
		if phase != "" && !strings.Contains(phase, wantVenue+"|"+wantMarket+"|"+wantSide) {
			continue
		}
		return true
	}
	return false
}

func markUnresolvedOrphan(recordDir string, leg carryUntrackedLeg, notionalUSDT float64) {
	appendCarryDustLedgerBestEffort(
		recordDir,
		fmt.Sprintf("untracked_flatten|%s|%s|%s", strings.TrimSpace(leg.Venue), leg.Market.String(), leg.Side.String()),
		leg.Symbol,
		baseAssetFromCanonicalSymbol(leg.Symbol),
		leg.Qty,
		notionalUSDT,
		unresolvedOrphanReason,
	)
}

func carryShouldSkipUntrackedFlatten(recordDir string, logger *log.Logger, leg carryUntrackedLeg) bool {
	if carryIsUnresolvedOrphan(recordDir, leg) {
		if logger != nil {
			logger.Printf("[carry-flatten] venue=%s %s %s≈%.6f 未清 orphan 已登记，跳过重复 flatten",
				leg.Venue, leg.Market, leg.Symbol, leg.Qty)
		}
		return true
	}
	key := recentFlattenKeyFromLeg(leg)
	if recentlyFlattened(recordDir, key, recentFlattenTTL) {
		if logger != nil {
			logger.Printf("[carry-flatten] venue=%s %s %s≈%.6f 已近期处理，跳过重复 flatten",
				leg.Venue, leg.Market, leg.Symbol, leg.Qty)
		}
		return true
	}
	return false
}

func carryLegStillPresent(ctx context.Context, venues map[string]venue.Venue, leg carryUntrackedLeg) bool {
	v := venues[leg.Venue]
	if v == nil {
		return true
	}
	sym := venue.Symbol(canonicalTradingSymbol(leg.Symbol))
	switch leg.Market {
	case venue.MarketSpot:
		bal, err := v.FetchBalances(ctx)
		if err != nil {
			return true
		}
		base := baseAssetFromCanonicalSymbol(leg.Symbol)
		switch leg.Side {
		case venue.SideSell:
			if b, ok := bal.ByAsset[base]; ok && b.Free > executionQtyTolerance {
				return true
			}
			return false
		case venue.SideBuy:
			return marginShortQtyFromBalance(bal, base) > executionQtyTolerance
		default:
			return true
		}
	case venue.MarketPerp:
		positions, err := v.FetchPositions(ctx)
		if err != nil {
			return true
		}
		amt := perpPositionQty(positions, sym)
		switch leg.Side {
		case venue.SideBuy:
			return amt < -executionQtyTolerance
		case venue.SideSell:
			return amt > executionQtyTolerance
		default:
			return absQty(amt) > executionQtyTolerance
		}
	default:
		return true
	}
}

func carryAfterUntrackedFlatten(ctx context.Context, recordDir string, logger *log.Logger, venues map[string]venue.Venue, leg carryUntrackedLeg, placed bool) {
	if !placed {
		return
	}
	if carryLegStillPresent(ctx, venues, leg) {
		v := venues[leg.Venue]
		notional := legNotionalUSDT(ctx, v, venue.Symbol(leg.Symbol), leg.Market, leg.Qty, 0)
		markUnresolvedOrphan(recordDir, leg, notional)
		if logger != nil {
			logger.Printf("[carry-flatten] venue=%s %s %s≈%.6f flatten 后仍有余量，标记 unresolved_orphan",
				leg.Venue, leg.Market, leg.Symbol, leg.Qty)
		}
		return
	}
	markRecentFlatten(recordDir, recentFlattenKeyFromLeg(leg))
	if logger != nil {
		logger.Printf("[carry-flatten] venue=%s %s %s≈%.6f flatten 核验通过",
			leg.Venue, leg.Market, leg.Symbol, leg.Qty)
	}
}

func carryScanUntrackedSVSPLegs(ctx context.Context, cfg CrossVenueExecutorConfig, recordDir string, logger *log.Logger, venues map[string]venue.Venue, ps []SingleVenueSpotPerpPosition) []carryUntrackedLeg {
	var out []carryUntrackedLeg
	for name, v := range venues {
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err == nil {
			for _, ep := range positions {
				if ep.PositionAmt >= -executionQtyTolerance {
					continue
				}
				sym := canonicalTradingSymbol(string(ep.Symbol))
				if sym == "" || carryPerpLegSkipUntrackedScan(recordDir, name, ep.Symbol, svspHasTrackedSymbolVenue(ps, name, sym)) {
					continue
				}
				carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketPerp, Qty: absQty(ep.PositionAmt), Side: venue.SideBuy, ReduceOnly: true}, &out)
			}
		}
		bal, err := v.FetchBalances(ctx)
		if err != nil {
			continue
		}
		for asset, b := range bal.ByAsset {
			if strings.EqualFold(asset, "USDT") || b.Free <= executionQtyTolerance {
				continue
			}
			sym := canonicalTradingSymbol(asset + "-USDT")
			if sym == "" || carrySpotLegSkipUntrackedScan(recordDir, name, sym, svspHasTrackedSymbolVenue(ps, name, sym)) {
				continue
			}
			carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketSpot, Qty: b.Free, Side: venue.SideSell, ReduceOnly: false}, &out)
		}
	}
	return out
}

func carryScanUntrackedSVPSLegs(ctx context.Context, cfg CrossVenueExecutorConfig, recordDir string, logger *log.Logger, venues map[string]venue.Venue, ps []SingleVenuePerpSpotPosition) []carryUntrackedLeg {
	var out []carryUntrackedLeg
	for name, v := range venues {
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err == nil {
			for _, ep := range positions {
				if ep.PositionAmt <= executionQtyTolerance {
					continue
				}
				sym := canonicalTradingSymbol(string(ep.Symbol))
				if sym == "" || carryPerpLegSkipUntrackedScan(recordDir, name, ep.Symbol, svpsHasTrackedSymbolVenue(ps, name, sym)) {
					continue
				}
				carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketPerp, Qty: ep.PositionAmt, Side: venue.SideSell, ReduceOnly: true}, &out)
			}
		}
		bal, err := v.FetchBalances(ctx)
		if err != nil {
			continue
		}
		for asset := range bal.ByAsset {
			if strings.EqualFold(asset, "USDT") {
				continue
			}
			short := marginShortQtyFromBalance(bal, asset)
			if short <= executionQtyTolerance {
				continue
			}
			sym := canonicalTradingSymbol(asset + "-USDT")
			if sym == "" || carrySpotLegSkipUntrackedScan(recordDir, name, sym, svpsHasTrackedSymbolVenue(ps, name, sym)) {
				continue
			}
			carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketSpot, Qty: short, Side: venue.SideBuy, ReduceOnly: false}, &out)
		}
	}
	return out
}

func carryScanUntrackedCVSPLegs(ctx context.Context, cfg CrossVenueExecutorConfig, recordDir string, logger *log.Logger, venues map[string]venue.Venue, ps []CrossVenueSpotPerpPosition) []carryUntrackedLeg {
	var out []carryUntrackedLeg
	for name, v := range venues {
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err == nil {
			for _, ep := range positions {
				if ep.PositionAmt >= -executionQtyTolerance {
					continue
				}
				sym := canonicalTradingSymbol(string(ep.Symbol))
				if sym == "" || carryPerpLegSkipUntrackedScan(recordDir, name, ep.Symbol, cvspHasTrackedLeg(ps, name, sym)) {
					continue
				}
				carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketPerp, Qty: absQty(ep.PositionAmt), Side: venue.SideBuy, ReduceOnly: true}, &out)
			}
		}
		bal, err := v.FetchBalances(ctx)
		if err != nil {
			continue
		}
		for asset, b := range bal.ByAsset {
			if strings.EqualFold(asset, "USDT") || b.Free <= executionQtyTolerance {
				continue
			}
			sym := canonicalTradingSymbol(asset + "-USDT")
			if sym == "" || carrySpotLegSkipUntrackedScan(recordDir, name, sym, cvspHasTrackedLeg(ps, name, sym)) {
				continue
			}
			carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketSpot, Qty: b.Free, Side: venue.SideSell, ReduceOnly: false}, &out)
		}
	}
	return out
}

func carryScanUntrackedCVPSLegs(ctx context.Context, cfg CrossVenueExecutorConfig, recordDir string, logger *log.Logger, venues map[string]venue.Venue, ps []CrossVenuePerpSpotPosition) []carryUntrackedLeg {
	var out []carryUntrackedLeg
	for name, v := range venues {
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err == nil {
			for _, ep := range positions {
				if ep.PositionAmt <= executionQtyTolerance {
					continue
				}
				sym := canonicalTradingSymbol(string(ep.Symbol))
				if sym == "" || carryPerpLegSkipUntrackedScan(recordDir, name, ep.Symbol, cvpsHasTrackedLeg(ps, name, sym)) {
					continue
				}
				carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketPerp, Qty: ep.PositionAmt, Side: venue.SideSell, ReduceOnly: true}, &out)
			}
		}
		bal, err := v.FetchBalances(ctx)
		if err != nil {
			continue
		}
		for asset := range bal.ByAsset {
			if strings.EqualFold(asset, "USDT") {
				continue
			}
			short := marginShortQtyFromBalance(bal, asset)
			if short <= executionQtyTolerance {
				continue
			}
			sym := canonicalTradingSymbol(asset + "-USDT")
			if sym == "" || carrySpotLegSkipUntrackedScan(recordDir, name, sym, cvpsHasTrackedLeg(ps, name, sym)) {
				continue
			}
			carryAppendMaterialUntrackedLeg(ctx, cfg, recordDir, logger, venues, carryUntrackedLeg{Venue: name, Symbol: sym, Market: venue.MarketSpot, Qty: short, Side: venue.SideBuy, ReduceOnly: false}, &out)
		}
	}
	return out
}
