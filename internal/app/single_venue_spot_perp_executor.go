package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

// single_venue_spot_perp_executor.go 同所 spot-perp 正费率执行器(OFF 起步, live 闸透传)。

const (
	svspConfirmTimeout     = 8 * time.Second
	svspConfirmPoll        = 400 * time.Millisecond
	svspSettlementsLogName = "settlements.jsonl"
)

type singleVenueSpotPerpTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

// SingleVenueSpotPerpExecutor 同所买现货+空永续执行引擎。
type SingleVenueSpotPerpExecutor struct {
	cfg    EngineConfig
	venues map[string]venue.Venue
	store  *singleVenueSpotPerpPositionStore
	logger *log.Logger

	mu            sync.RWMutex
	positions     []SingleVenueSpotPerpPosition
	settlementsMu sync.Mutex
	recordDir     string
	nowFn         func() time.Time

	deadManUnsupportedVenues map[string]struct{}  // dead-man 声明支持但实际 ErrUnsupported 时缓存
	deadManLastArm           map[string]time.Time // 各所上次成功续期 dead-man 的时刻(账户级,按所节流)

	rollbackBlockedMu sync.Mutex
	rollbackBlocked   map[string]time.Time

	openingsEnabled atomic.Bool // 未登记材料级敞口 halt 时禁止新开
}

// NewSingleVenueSpotPerpExecutor 构造执行器;venues 由调用方注入便于单测。
func NewSingleVenueSpotPerpExecutor(cfg EngineConfig, venues map[string]venue.Venue, recordDir string, logger *log.Logger) *SingleVenueSpotPerpExecutor {
	if logger == nil {
		logger = log.Default()
	}
	if venues == nil {
		venues = make(map[string]venue.Venue)
	}
	store, err := newSingleVenueSpotPerpPositionStore(recordDir)
	if err != nil && logger != nil {
		logger.Printf("[同所spot-perp] 持仓台账初始化失败: %v", err)
	}
	e := &SingleVenueSpotPerpExecutor{
		cfg:                      cfg,
		venues:                   venues,
		store:                    store,
		logger:                   logger,
		recordDir:                strings.TrimSpace(recordDir),
		nowFn:                    time.Now,
		deadManUnsupportedVenues: make(map[string]struct{}),
		deadManLastArm:           make(map[string]time.Time),
		rollbackBlocked:          make(map[string]time.Time),
	}
	e.openingsEnabled.Store(true)
	if store != nil {
		if ps, lerr := store.load(); lerr == nil {
			e.positions = ps
		}
	}
	if e.recordDir == "" {
		e.recordDir = "records"
	}
	return e
}

func svspIdemPrefix(sym venue.Symbol, venueName string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("svsp-%s-%s-%d", base, venueName, time.Now().UTC().Unix())
}

func svspLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

func svspTakerSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	bps := cfg.TakerSlippageBudgetBps
	if bps <= 0 {
		bps = cfg.SlippageBudgetBps
	}
	if bps < cfg.SlippageBudgetBps {
		bps = cfg.SlippageBudgetBps
	}
	const maxTakerSlippageBps = 100
	if bps > maxTakerSlippageBps {
		bps = maxTakerSlippageBps
	}
	return bps
}

func (e *SingleVenueSpotPerpExecutor) execCfg() CrossVenueExecutorConfig {
	return e.cfg.CrossVenueExecutor
}

func (e *SingleVenueSpotPerpExecutor) snapshotPositions() []SingleVenueSpotPerpPosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]SingleVenueSpotPerpPosition, len(e.positions))
	copy(out, e.positions)
	return out
}

func (e *SingleVenueSpotPerpExecutor) setPositions(ps []SingleVenueSpotPerpPosition) {
	e.mu.Lock()
	e.positions = ps
	e.mu.Unlock()
}

func (e *SingleVenueSpotPerpExecutor) persistPosition(pos SingleVenueSpotPerpPosition) error {
	if e.store == nil {
		return fmt.Errorf("持仓台账未初始化")
	}
	if err := e.store.upsert(pos); err != nil {
		return err
	}
	e.mu.Lock()
	found := false
	for i := range e.positions {
		if svspSamePosition(e.positions[i], pos) {
			e.positions[i] = pos
			found = true
			break
		}
	}
	if !found {
		e.positions = append(e.positions, pos)
	}
	e.mu.Unlock()
	return nil
}

func (e *SingleVenueSpotPerpExecutor) removePosition(pos SingleVenueSpotPerpPosition) error {
	if e.store == nil {
		return nil
	}
	key := svspPositionKey(pos)
	if err := e.store.remove(key); err != nil {
		return err
	}
	e.mu.Lock()
	out := make([]SingleVenueSpotPerpPosition, 0, len(e.positions))
	for _, p := range e.positions {
		if svspPositionKey(p) != key {
			out = append(out, p)
		}
	}
	e.positions = out
	e.mu.Unlock()
	return nil
}

func parseSingleVenueSpotPerpOpportunity(opp UnifiedOpportunity) (venueName string, qty, notional float64, negativeCarry bool, err error) {
	if opp.StrategyType != "" && opp.StrategyType != UnifiedStrategySingleVenueSpotPerp {
		return "", 0, 0, false, fmt.Errorf("strategy_type=%s", opp.StrategyType)
	}
	if len(opp.Legs) < 2 {
		return "", 0, 0, false, fmt.Errorf("legs<2")
	}
	var spotVenue, perpVenue string
	for _, leg := range opp.Legs {
		mkt := strings.ToLower(strings.TrimSpace(leg.Market))
		side := strings.ToLower(strings.TrimSpace(leg.Side))
		v := strings.TrimSpace(leg.Venue)
		switch mkt {
		case "spot":
			if side == "short" || side == "sell" {
				negativeCarry = true
			} else if side != "buy" {
				return "", 0, 0, false, fmt.Errorf("spot leg side=%s", leg.Side)
			}
			spotVenue = v
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		case "perp":
			if negativeCarry {
				if side != "long" && side != "buy" {
					return "", 0, 0, false, fmt.Errorf("perp leg side=%s", leg.Side)
				}
			} else if side != "short" && side != "sell" {
				return "", 0, 0, false, fmt.Errorf("perp leg side=%s", leg.Side)
			}
			perpVenue = v
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		default:
			return "", 0, 0, false, fmt.Errorf("unknown market=%s", leg.Market)
		}
	}
	if spotVenue == "" || perpVenue == "" {
		return "", 0, 0, false, fmt.Errorf("missing spot/perp leg")
	}
	if !strings.EqualFold(spotVenue, perpVenue) {
		return "", 0, 0, false, fmt.Errorf("legs on different venues: %s vs %s", spotVenue, perpVenue)
	}
	if qty <= executionQtyTolerance {
		return "", 0, 0, false, fmt.Errorf("qty<=0")
	}
	return spotVenue, qty, notional, negativeCarry, nil
}

func svspNegativeCarry(pos SingleVenueSpotPerpPosition) bool {
	s := strings.ToLower(strings.TrimSpace(pos.SpotLeg.Side))
	return s == "short" || s == "sell"
}

func (e *SingleVenueSpotPerpExecutor) isSymbolRollbackBlocked(symbol string) bool {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	until, ok := e.rollbackBlocked[symbol]
	return ok && e.nowFn().Before(until)
}

func (e *SingleVenueSpotPerpExecutor) blockSymbolNewOpen(symbol string) {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	e.rollbackBlocked[symbol] = e.nowFn().Add(rollbackVerifyBackoff)
}

func (e *SingleVenueSpotPerpExecutor) confirmFill(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	mkt venue.Market,
	sym venue.Symbol,
	clientID string,
) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(svspConfirmTimeout)
	var last venue.OrderResult
	var lastQueryErr error
	for {
		res, err := tr.QueryOrder(ctx, mkt, sym, clientID)
		if err == nil {
			last = res
			lastQueryErr = nil
			st := strings.ToLower(res.Status)
			if st == "filled" {
				return res, nil
			}
			if st == "canceled" || st == "rejected" {
				return res, fmt.Errorf("订单终态=%s", res.Status)
			}
		} else {
			lastQueryErr = err
			if last.ClientOrderID == "" {
				last = venue.OrderResult{ClientOrderID: clientID, Symbol: sym, Market: mkt, Status: "unknown"}
			} else {
				last.Status = "unknown"
			}
		}
		if e.nowFn().After(deadline) {
			if lastQueryErr != nil {
				last.Status = "unknown"
				return last, fmt.Errorf("查单失败,状态未知: %w", lastQueryErr)
			}
			if last.ExecutedQty > executionQtyTolerance {
				return last, nil
			}
			return last, fmt.Errorf("确认成交超时")
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(svspConfirmPoll):
		}
	}
}

// placeSVSPOC IOC 下单;!live 时返回合成成交且绝不调用 PlaceOrder(照 cross_venue IOC dry-run 分支)。
func (e *SingleVenueSpotPerpExecutor) placeSVSPOC(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	limitPrice float64,
	clientID string,
	reduceOnly bool,
	marginEffect venue.MarginEffect,
	live bool,
) (venue.OrderResult, error) {
	if !live {
		return venue.OrderResult{
			Symbol:        sym,
			Market:        mkt,
			ClientOrderID: clientID,
			Status:        "filled",
			ExecutedQty:   qty,
			AvgPrice:      limitPrice,
		}, nil
	}
	res, err := tr.PlaceOrder(ctx, venue.OrderRequest{
		Market:        mkt,
		Symbol:        sym,
		Side:          side,
		Type:          venue.OrderTypeIOC,
		Qty:           qty,
		Price:         limitPrice,
		ClientOrderID: clientID,
		ReduceOnly:    reduceOnly,
		MarginEffect:  marginEffect,
	})
	if err != nil {
		return res, err
	}
	if res.ExecutedQty <= executionQtyTolerance {
		confirmed, cerr := e.confirmFill(ctx, tr, mkt, sym, clientID)
		if cerr == nil && confirmed.ExecutedQty > executionQtyTolerance {
			return confirmed, nil
		}
		return res, fmt.Errorf("IOC 未成交")
	}
	return res, nil
}

func (e *SingleVenueSpotPerpExecutor) placeSVSPLegIOC(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	marginEffect venue.MarginEffect,
	live bool,
	slippageBps float64,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, slippageBps)
	return e.placeSVSPOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live)
}

// placeSVSPCloseLegIOC 平仓专用腿:按 CloseAttempts 阶梯放宽滑点,≥阈值后穿价兜底。
func (e *SingleVenueSpotPerpExecutor) placeSVSPCloseLegIOC(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	marginEffect venue.MarginEffect,
	live bool,
	closeAttempts int,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price, _ := carryCloseIocLimitPrice(e.execCfg(), side, book, closeAttempts)
	return e.placeSVSPOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live)
}

// executeOpen 正 carry 买现货+空永续;负 carry(反向)借币空现货+多永续。
func (e *SingleVenueSpotPerpExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (SingleVenueSpotPerpPosition, error) {
	if !e.OpeningsEnabled() {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("同所spot-perp 未登记敞口 halt,禁止新开")
	}
	venueName, qty, notional, negativeCarry, err := parseSingleVenueSpotPerpOpportunity(opp)
	if err != nil {
		return SingleVenueSpotPerpPosition{}, err
	}
	if negativeCarry {
		if !e.execCfg().SingleVenueReverseFundingEnabled {
			return SingleVenueSpotPerpPosition{}, fmt.Errorf("反向资金费未启用(single_venue_reverse_funding_enabled=false)")
		}
		return e.executeOpenNegativeCarry(ctx, opp, venueName, qty, notional, live)
	}
	return e.executeOpenPositiveCarry(ctx, opp, venueName, qty, notional, live)
}

func (e *SingleVenueSpotPerpExecutor) executeOpenPositiveCarry(ctx context.Context, opp UnifiedOpportunity, venueName string, qty, notional float64, live bool) (SingleVenueSpotPerpPosition, error) {
	if e.isSymbolRollbackBlocked(opp.Symbol) {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("%s 回滚未卸净,暂禁新开", opp.Symbol)
	}
	v := e.venues[venueName]
	if v == nil {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("venue %s 未就绪", venueName)
	}
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	sym := venue.Symbol(opp.Symbol)
	cfg := e.execCfg()
	idem := svspIdemPrefix(sym, venueName)
	now := e.nowFn()

	pos := SingleVenueSpotPerpPosition{
		Symbol:            opp.Symbol,
		Venue:             venueName,
		OpportunityID:     opp.ID,
		OpenExpectedNetEV: opp.ExpectedNetEVUSDT,
		ExpectedHoldHours: opp.ExpectedHoldHours,
		NotionalUSDT:      notional,
		Status:            SVSPStatusOpeningSpot,
		TransitionState:   "open",
		OpenedAt:          now,
		UpdatedAt:         now,
		IdemPrefix:        idem,
		SpotLeg:           SingleVenueSpotPerpLegState{Market: "spot", Side: "buy"},
		PerpLeg:           SingleVenueSpotPerpLegState{Market: "perp", Side: "short"},
	}
	if t := parseCarryNextFundingTime(opp.NextFundingTime); !t.IsZero() {
		pos.NextFundingTime = t
	}
	if opp.FundingIntervalHours > 0 {
		pos.FundingIntervalHours = opp.FundingIntervalHours
	}
	if opp.RawFundingRate != 0 {
		pos.OpenFundingRate = opp.RawFundingRate
	}
	if pos.ExpectedHoldHours <= 0 {
		pos.ExpectedHoldHours = 8
	}
	if pos.NotionalUSDT <= 0 {
		pos.NotionalUSDT = qty * 100
	}

	if !live {
		logDryRunOpen(e.logger, "同所spot-perp", opp.Symbol,
			fmt.Sprintf("@%s 买现货/空永续 各≈%.6f 名义≈%.2fU EV≈%.4f", venueName, qty, pos.NotionalUSDT, opp.ExpectedNetEVUSDT))
		appendDryRunEvent(e.recordDir, "svsp_open", map[string]any{
			"symbol": opp.Symbol, "venue": venueName, "qty": qty, "notional": pos.NotionalUSDT,
		})
		return SingleVenueSpotPerpPosition{}, nil
	}

	takerBps := svspTakerSlippageBps(cfg)
	spotInstr := opportunityLegInstrument(opp, venueName, "spot")
	perpInstr := opportunityLegInstrument(opp, venueName, "perp")
	caps := v.Capabilities()
	if spotInstr.StepSize <= 0 && spotInstr.MinNotional <= 0 {
		if in, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketSpot); ok {
			spotInstr = in
		}
	}
	if caps.ContractBasedPerp && perpInstr.ContractSize <= 0 {
		if in, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketPerp); ok {
			perpInstr = carryMergeContractPerpInstrument(perpInstr, in)
		}
	} else if perpInstr.StepSize <= 0 && perpInstr.MinNotional <= 0 {
		if in, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketPerp); ok {
			perpInstr = in
		}
	}
	refPrice := 100.0
	if book, berr := tr.FetchBookTicker(ctx, venue.MarketPerp, sym); berr == nil && book.AskPrice > 0 {
		refPrice = book.AskPrice
	} else if book, berr := tr.FetchBookTicker(ctx, venue.MarketSpot, sym); berr == nil && book.AskPrice > 0 {
		refPrice = book.AskPrice
	}
	alignedQty, alignErr := carrySingleVenueOpenQtyAlign(qty, refPrice, spotInstr, perpInstr, v.Capabilities())
	if alignErr != nil {
		carryLogPreflightSkip(e.logger, "同所spot-perp", opp.Symbol, alignErr.Error())
		return SingleVenueSpotPerpPosition{}, alignErr
	}
	qty = alignedQty
	if pos.NotionalUSDT <= 0 || notional <= 0 {
		pos.NotionalUSDT = qty * refPrice
	}
	spotNeed := pos.NotionalUSDT * (1 + takerBps/10000)
	perpMargin := rotationPerpMarginWithBuffer(cfg, pos.NotionalUSDT)
	if err := carryPreflightSingleVenueSpotPerpOpen(ctx, v, spotNeed, perpMargin); err != nil {
		carryLogPreflightSkip(e.logger, "同所spot-perp", opp.Symbol, err.Error())
		return SingleVenueSpotPerpPosition{}, err
	}

	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}

	spotID := svspLegClientID(idem, "s1")
	spotRes, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, spotID, false, venue.MarginEffectNone, live, takerBps)
	if orderStatusIsUnknown(spotRes) || isUnknownOrderStateError(err) {
		if spotRes.ExecutedQty > executionQtyTolerance {
			pos.SpotLeg.Qty = spotRes.ExecutedQty
			pos.SpotLeg.EntryPrice = spotRes.AvgPrice
			pos.SpotLeg.OrderIDs = []string{spotID}
			pos.Status = SVSPStatusOpeningPerp
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("同所spot-perp查单未知", fmt.Sprintf("%s 现货腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("现货腿状态未知,禁止自动回滚: %w", err)
	}
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, v, tr, sym, &pos, spotRes.ExecutedQty, live, idem, spotInstr)
		}
		return pos, fmt.Errorf("现货腿失败: %w", err)
	}
	pos.SpotLeg.Qty = spotRes.ExecutedQty
	pos.SpotLeg.EntryPrice = spotRes.AvgPrice
	pos.SpotLeg.OrderIDs = []string{spotID}
	pos.Status = SVSPStatusOpeningPerp
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	hedgeQty := spotRes.ExecutedQty
	perpID := svspLegClientID(idem, "p1")
	perpRes, perpErr := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, hedgeQty, perpID, false, venue.MarginEffectNone, live, takerBps)
	if orderStatusIsUnknown(perpRes) {
		e.notifyCritical("同所spot-perp查单未知", fmt.Sprintf("%s 永续腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("永续腿状态未知,禁止自动回滚: %w", perpErr)
	}
	refPrice = spotRes.AvgPrice
	if refPrice <= 0 {
		refPrice = 100
	}
	match := carryMatchTwoLegOpens(spotRes.ExecutedQty, perpRes.ExecutedQty, carryMinTradableQty(refPrice, perpInstr))
	if perpErr != nil || !match.OK {
		e.rollbackSpotLeg(ctx, v, tr, sym, &pos, spotRes.ExecutedQty, live, idem, spotInstr)
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackPerpLeg(ctx, tr, sym, perpRes.ExecutedQty, live, idem)
		}
		if match.Leg2Partial {
			e.logger.Printf("[同所spot-perp] %s 永续腿未足额成交(%.6f/%.6f),已回滚现货腿", opp.Symbol, perpRes.ExecutedQty, hedgeQty)
		}
		return pos, fmt.Errorf("永续腿失败: %w", perpErr)
	}
	if match.ExcessLeg1 > executionQtyTolerance {
		e.rollbackSpotLeg(ctx, v, tr, sym, &pos, match.ExcessLeg1, live, idem, spotInstr)
	}
	if match.ExcessLeg2 > executionQtyTolerance {
		e.rollbackPerpLeg(ctx, tr, sym, match.ExcessLeg2, live, idem)
	}
	pos.SpotLeg.Qty = match.MatchedQty
	pos.PerpLeg.Qty = match.MatchedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
	pos.NotionalUSDT = match.MatchedQty * spotRes.AvgPrice
	pos.Status = SVSPStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[同所spot-perp] 开仓 %s@%s 买现货%.6f/空永续%.6f 名义≈%.2fU",
		opp.Symbol, venueName, pos.SpotLeg.Qty, pos.PerpLeg.Qty, pos.NotionalUSDT)
	return pos, nil
}

func (e *SingleVenueSpotPerpExecutor) checkNegativeCarrySupported(v venue.Venue, venueName string) bool {
	if v == nil {
		return false
	}
	return v.Capabilities().SupportsNegativeCarry
}

func (e *SingleVenueSpotPerpExecutor) executeOpenNegativeCarry(ctx context.Context, opp UnifiedOpportunity, venueName string, qty, notional float64, live bool) (SingleVenueSpotPerpPosition, error) {
	if e.isSymbolRollbackBlocked(opp.Symbol) {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("%s 回滚未卸净,暂禁新开", opp.Symbol)
	}
	v := e.venues[venueName]
	if !e.checkNegativeCarrySupported(v, venueName) {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("venue %s 不支持借币反向 carry", venueName)
	}
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok {
		return SingleVenueSpotPerpPosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	sym := venue.Symbol(opp.Symbol)
	cfg := e.execCfg()
	idem := svspIdemPrefix(sym, venueName)
	now := e.nowFn()
	pos := SingleVenueSpotPerpPosition{
		Symbol: opp.Symbol, Venue: venueName, OpportunityID: opp.ID,
		OpenExpectedNetEV: opp.ExpectedNetEVUSDT, ExpectedHoldHours: opp.ExpectedHoldHours,
		NotionalUSDT: notional, Status: SVSPStatusOpeningSpot, TransitionState: "open",
		OpenedAt: now, UpdatedAt: now, IdemPrefix: idem,
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "short"},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "long"},
	}
	if pos.ExpectedHoldHours <= 0 {
		pos.ExpectedHoldHours = 8
	}
	if pos.NotionalUSDT <= 0 {
		pos.NotionalUSDT = qty * 100
	}
	if !live {
		logDryRunOpen(e.logger, "同所spot-perp", opp.Symbol, fmt.Sprintf("@%s 借币空现货/多永续 reverse", venueName))
		return SingleVenueSpotPerpPosition{}, nil
	}
	spotInstr := opportunityLegInstrument(opp, venueName, "spot")
	perpInstr := opportunityLegInstrument(opp, venueName, "perp")
	refPrice := 100.0
	if book, berr := tr.FetchBookTicker(ctx, venue.MarketSpot, sym); berr == nil && book.AskPrice > 0 {
		refPrice = book.AskPrice
	}
	alignedQty, alignErr := carrySingleVenueOpenQtyAlign(qty, refPrice, spotInstr, perpInstr, v.Capabilities())
	if alignErr != nil {
		return SingleVenueSpotPerpPosition{}, alignErr
	}
	qty = alignedQty
	base := baseAssetFromCanonicalSymbol(opp.Symbol)
	maxBorrow, merr := v.MaxBorrowable(ctx, base)
	if merr != nil || maxBorrow+executionQtyTolerance < qty {
		return pos, fmt.Errorf("借币额度不足")
	}
	if err := carryPreflightSingleVenueSpotPerpOpen(ctx, v, 0, rotationPerpMarginWithBuffer(cfg, qty*refPrice)); err != nil {
		return SingleVenueSpotPerpPosition{}, err
	}
	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}
	takerBps := svspTakerSlippageBps(cfg)
	spotID := svspLegClientID(idem, "s1")
	spotRes, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, spotID, false, venue.MarginEffectBorrow, live, takerBps)
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotBorrowLeg(ctx, v, tr, sym, &pos, spotRes.ExecutedQty, live, idem)
		}
		return pos, fmt.Errorf("现货借币腿失败: %w", err)
	}
	pos.SpotLeg.Qty = spotRes.ExecutedQty
	pos.Status = SVSPStatusOpeningPerp
	_ = e.persistPosition(pos)
	perpID := svspLegClientID(idem, "p1")
	perpRes, perpErr := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, spotRes.ExecutedQty, perpID, false, venue.MarginEffectNone, live, takerBps)
	match := carryMatchTwoLegOpens(spotRes.ExecutedQty, perpRes.ExecutedQty, carryMinTradableQty(refPrice, perpInstr))
	if perpErr != nil || !match.OK {
		e.rollbackSpotBorrowLeg(ctx, v, tr, sym, &pos, spotRes.ExecutedQty, live, idem)
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackPerpLongLeg(ctx, tr, sym, perpRes.ExecutedQty, live, idem)
		}
		return pos, fmt.Errorf("永续腿失败: %w", perpErr)
	}
	pos.SpotLeg.Qty = match.MatchedQty
	pos.PerpLeg.Qty = match.MatchedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.Status = SVSPStatusOpen
	pos.TransitionState = "hold"
	_ = e.persistPosition(pos)
	return pos, nil
}

func (e *SingleVenueSpotPerpExecutor) rollbackSpotBorrowLeg(ctx context.Context, v venue.Venue, tr singleVenueSpotPerpTrader, sym venue.Symbol, pos *SingleVenueSpotPerpPosition, qty float64, live bool, idemPrefix string) {
	if qty <= executionQtyTolerance {
		return
	}
	id := svspLegClientID(idemPrefix, "rb")
	_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live, svspTakerSlippageBps(e.execCfg()))
	e.verifyRollbackCleared(ctx, v, sym, string(sym), true)
}

func (e *SingleVenueSpotPerpExecutor) rollbackPerpLongLeg(ctx context.Context, tr singleVenueSpotPerpTrader, sym venue.Symbol, qty float64, live bool, idemPrefix string) {
	if qty <= executionQtyTolerance {
		return
	}
	id := svspLegClientID(idemPrefix, "rbp")
	_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, qty, id, true, venue.MarginEffectNone, live, svspTakerSlippageBps(e.execCfg()))
}

func (e *SingleVenueSpotPerpExecutor) rollbackSpotLeg(
	ctx context.Context,
	v venue.Venue,
	tr singleVenueSpotPerpTrader,
	sym venue.Symbol,
	pos *SingleVenueSpotPerpPosition,
	qty float64,
	live bool,
	idemPrefix string,
	instr venue.Instrument,
) {
	if qty <= executionQtyTolerance {
		return
	}
	sellQty := carryResolveSpotSellQty(ctx, v, sym, qty, instr.StepSize)
	if sellQty <= executionQtyTolerance {
		if e.logger != nil {
			e.logger.Printf("[同所spot-perp] 回滚现货 %s 可卖量=0(意图=%.6f),跳过", sym, qty)
		}
		e.verifyRollbackCleared(ctx, v, sym, string(sym), pos != nil && svspNegativeCarry(*pos))
		return
	}
	id := svspLegClientID(idemPrefix, "rb")
	res, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, sellQty, id, false, venue.MarginEffectNone, live, svspTakerSlippageBps(e.execCfg()))
	if err != nil {
		e.logger.Printf("[同所spot-perp] 回滚现货腿失败 %s: %v", sym, err)
		e.notifyCritical("同所spot-perp回滚失败", fmt.Sprintf("%s 现货腿回滚失败: %v", sym, err), string(sym))
	} else if pos != nil {
		pos.RollbackCostUSDT += res.Fee
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(*pos)
	}
	e.verifyRollbackCleared(ctx, v, sym, string(sym), pos != nil && svspNegativeCarry(*pos))
}

func (e *SingleVenueSpotPerpExecutor) rollbackPerpLeg(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	sym venue.Symbol,
	qty float64,
	live bool,
	idemPrefix string,
) {
	if qty <= executionQtyTolerance {
		return
	}
	id := svspLegClientID(idemPrefix, "rbp")
	_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, id, true, venue.MarginEffectNone, live, svspTakerSlippageBps(e.execCfg()))
}

func (e *SingleVenueSpotPerpExecutor) verifyRollbackCleared(ctx context.Context, v venue.Venue, sym venue.Symbol, symbol string, negativeCarry bool) {
	if v == nil {
		return
	}
	spotQty, perpAmt, err := e.fetchLegState(ctx, v, sym, negativeCarry)
	if err != nil {
		e.notifyCritical("同所spot-perp回滚对账失败", fmt.Sprintf("%s: %v", symbol, err), symbol)
		e.blockSymbolNewOpen(symbol)
		return
	}
	if spotQty <= executionQtyTolerance && absQty(perpAmt) <= executionQtyTolerance {
		return
	}
	e.notifyCritical("同所spot-perp回滚未卸净",
		fmt.Sprintf("%s spot=%.6f perp=%.6f,阻塞新开并退避重试", symbol, spotQty, perpAmt), symbol)
	e.blockSymbolNewOpen(symbol)
}

func spotBalanceQty(bals venue.Balances, baseAsset string) float64 {
	return spotLongQtyFromBalance(bals, baseAsset)
}

func (e *SingleVenueSpotPerpExecutor) fetchLegState(ctx context.Context, v venue.Venue, sym venue.Symbol, negativeCarry bool) (spotQty, perpAmt float64, err error) {
	base := baseAssetFromCanonicalSymbol(string(sym))
	bals, berr := v.FetchBalances(ctx)
	positions, perr := v.FetchPositions(ctx)
	if negativeCarry {
		if berr == nil {
			spotQty = svpsSpotShortQty(bals, base)
		}
		if perr == nil {
			perpAmt = svpsPerpLongQty(positions, sym)
		}
	} else {
		if berr == nil {
			spotQty = spotBalanceQty(bals, base)
		}
		if perr == nil {
			perpAmt = perpPositionQty(positions, sym)
		}
	}
	if berr != nil && perr != nil {
		return 0, 0, fmt.Errorf("balances=%v positions=%v", berr, perr)
	}
	return spotQty, perpAmt, nil
}

// manageOpenPositions 管理所有在场持仓。
func (e *SingleVenueSpotPerpExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeSingleVenueSpotPerpPositions(e.snapshotPositions()) {
		_, shouldPersist := e.managePosition(ctx, pos, live)
		// managePosition 子路径已自行 persist 或 finalize;不可再 upsert 入参快照,否则复活已平仓仓。
		if shouldPersist {
			_ = e.persistPosition(pos)
		}
	}
}

func (e *SingleVenueSpotPerpExecutor) managePosition(ctx context.Context, pos SingleVenueSpotPerpPosition, live bool) (SingleVenueSpotPerpPosition, bool) {
	if !live {
		if reason := e.shouldClosePosition(ctx, &pos); reason != "" {
			logDryRunClose(e.logger, "同所spot-perp", pos.Symbol, reason)
		}
		return pos, false
	}
	v := e.venues[pos.Venue]
	if v == nil {
		return pos, false
	}

	if pos.Status == SVSPStatusClosing {
		_ = e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, false
	}

	spotQty, perpAmt, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol), svspNegativeCarry(pos))
	if err != nil {
		e.logger.Printf("[同所spot-perp] 拉腿状态失败 %s: %v", pos.Symbol, err)
		return pos, false
	}
	neg := svspNegativeCarry(pos)
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpAmt > executionQtyTolerance
	if !neg {
		perpOpen = absQty(perpAmt) > executionQtyTolerance
	}

	if svspIsOpeningStatus(pos.Status) {
		return e.manageOpeningContinuation(ctx, pos, spotOpen, perpOpen, spotQty, perpAmt, live)
	}

	e.syncFundingMeta(ctx, &pos)

	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpAmt)
		return pos, false
	}
	if !spotOpen && !perpOpen {
		pos.Status = SVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos, live)
		return pos, false
	}

	e.syncRealizedFunding(ctx, &pos)
	perpHedge := perpAmt
	if !neg {
		perpHedge = absQty(perpAmt)
	}
	dev := qtyDeviation(spotQty, perpHedge)
	if carryShouldRebalanceHedge(dev, pos.LastHedgeRebalanceAt, e.nowFn()) {
		e.rebalanceSVSPHedge(ctx, &pos, spotQty, perpHedge, live)
	}
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	// dead-man 续期(永续腿所在所, okx/gate 等)
	e.armDeadManForPerpVenue(ctx, pos.Venue, live)

	if reason := e.shouldClosePosition(ctx, &pos); reason != "" {
		if carryIsHardStopCloseReason(reason) || !carryShouldDeferEVWeakClose(e.nowFn(), pos.NextFundingTime, reason) {
			_ = e.closePosition(ctx, pos, live, reason)
		}
		return pos, false
	}
	return pos, false
}

func (e *SingleVenueSpotPerpExecutor) manageOpeningContinuation(
	ctx context.Context,
	pos SingleVenueSpotPerpPosition,
	spotOpen, perpOpen bool,
	spotQty, perpAmt float64,
	live bool,
) (SingleVenueSpotPerpPosition, bool) {
	now := e.nowFn()
	v := e.venues[pos.Venue]
	tr, _ := v.(singleVenueSpotPerpTrader)
	sym := venue.Symbol(pos.Symbol)

	switch {
	case !spotOpen && !perpOpen:
		pos.Status = SVSPStatusClosed
		pos.CloseReason = "open_retry"
		pos.UpdatedAt = now
		e.finalizeClosedPosition(pos, live)
		return pos, false
	case spotOpen && !perpOpen && pos.Status == SVSPStatusOpeningPerp:
		if tr == nil {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotQty, perpAmt)
			return pos, false
		}
		hedgeQty := spotQty
		if pos.SpotLeg.Qty > executionQtyTolerance {
			hedgeQty = pos.SpotLeg.Qty
		}
		perpInstr := venue.Instrument{}
		if in, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketPerp); ok {
			perpInstr = in
		}
		spotInstr := venue.Instrument{}
		if in, ok := lookupVenueInstrumentForMarket(ctx, v, sym, venue.MarketSpot); ok {
			spotInstr = in
		}
		refCont := pos.SpotLeg.EntryPrice
		if refCont <= 0 {
			refCont = 100
		}
		if aligned, alignErr := carrySingleVenueOpenQtyAlign(hedgeQty, refCont, spotInstr, perpInstr, v.Capabilities()); alignErr == nil {
			hedgeQty = aligned
		}
		perpID := svspLegClientID(pos.IdemPrefix, "p2")
		perpRes, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, hedgeQty, perpID, false, venue.MarginEffectNone, live, svspTakerSlippageBps(e.execCfg()))
		if orderStatusIsUnknown(perpRes) {
			e.notifyCritical("同所spot-perp查单未知", fmt.Sprintf("%s 续开永续腿查单失败", pos.Symbol), pos.Symbol)
			return pos, false
		}
		refPrice := pos.SpotLeg.EntryPrice
		if refPrice <= 0 {
			refPrice = 100
		}
		match := carryMatchTwoLegOpens(hedgeQty, perpRes.ExecutedQty, carryMinTradableQty(refPrice, perpInstr))
		if err != nil || !match.OK {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotQty, perpAmt)
			return pos, false
		}
		if match.ExcessLeg1 > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, v, tr, sym, &pos, match.ExcessLeg1, live, pos.IdemPrefix, perpInstr)
		}
		pos.SpotLeg.Qty = match.MatchedQty
		pos.PerpLeg.Qty = match.MatchedQty
		pos.PerpLeg.EntryPrice = perpRes.AvgPrice
		pos.PerpLeg.OrderIDs = append(pos.PerpLeg.OrderIDs, perpID)
		pos.Status = SVSPStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, false
	case spotOpen && perpOpen:
		pos.Status = SVSPStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, false
	default:
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpAmt)
		return pos, false
	}
}

func (e *SingleVenueSpotPerpExecutor) rebalanceSVSPHedge(ctx context.Context, pos *SingleVenueSpotPerpPosition, spotQty, perpQty float64, live bool) {
	if pos == nil {
		return
	}
	reduceSpot, delta := carryPositiveRebalancePlan(spotQty, perpQty)
	if delta <= executionQtyTolerance {
		return
	}
	v := e.venues[pos.Venue]
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok || tr == nil {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	takerBps := svspTakerSlippageBps(e.execCfg())
	now := e.nowFn()
	if reduceSpot {
		if live {
			id := svspLegClientID(pos.IdemPrefix, "rb-s")
			if res, _ := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, delta, id, false, venue.MarginEffectNone, live, takerBps); res.ExecutedQty > executionQtyTolerance {
				pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			}
		}
	} else if live {
		id := svspLegClientID(pos.IdemPrefix, "rb-p")
		if res, _ := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, delta, id, true, venue.MarginEffectNone, live, takerBps); res.ExecutedQty > executionQtyTolerance {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
		}
	}
	pos.LastHedgeRebalanceAt = now
	pos.UpdatedAt = now
	_ = e.persistPosition(*pos)
	if e.logger != nil {
		e.logger.Printf("[同所spot-perp] %s 对冲再平衡:偏差%.1f%% 减%s %.6f",
			pos.Symbol, qtyDeviation(spotQty, perpQty)*100, map[bool]string{true: "现货", false: "永续"}[reduceSpot], delta)
	}
}

func (e *SingleVenueSpotPerpExecutor) handleOrphanLeg(
	ctx context.Context,
	pos SingleVenueSpotPerpPosition,
	spotOpen, perpOpen, live bool,
	spotQty, perpAmt float64,
) {
	decision := evaluatePositiveCarryOrphan(ctx, e.execCfg(), e.venues, pos.Symbol, pos.Venue, pos.Venue, spotOpen, perpOpen, spotQty, absQty(perpAmt), pos.SpotLeg.EntryPrice)
	if pos.NextFundingTime.IsZero() {
		e.syncFundingMeta(ctx, &pos)
	}
	if decision.IsDustOrphan && carryDeferDustOrphanDecision(e.nowFn(), pos.NextFundingTime, decision) {
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		return
	}
	if decision.IsDustOrphan {
		v := e.venues[pos.Venue]
		tr, _ := v.(singleVenueSpotPerpTrader)
		sym := venue.Symbol(pos.Symbol)
		takerBps := svspTakerSlippageBps(e.execCfg())
		if live && tr != nil && spotOpen {
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, svspLegClientID(pos.IdemPrefix, "dust"), false, venue.MarginEffectNone, live, takerBps)
		}
		pos.Status = SVSPStatusClosed
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, "dust_orphan"), decision.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "同所spot-perp", pos.Symbol, decision.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return
	}
	pos.Status = SVSPStatusOrphan
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	v := e.venues[pos.Venue]
	tr, _ := v.(singleVenueSpotPerpTrader)
	if tr == nil {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	takerBps := svspTakerSlippageBps(e.execCfg())
	if spotOpen && !perpOpen {
		qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, svspLegClientID(pos.IdemPrefix, "os"), false, venue.MarginEffectNone, live, takerBps)
		}
	} else if perpOpen && !spotOpen {
		qty := ledgerCappedCloseQty(pos.PerpLeg.Qty, absQty(perpAmt))
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, svspLegClientID(pos.IdemPrefix, "op"), true, venue.MarginEffectNone, live, takerBps)
		}
	}
	if decision.NakedRisk {
		sq, pa, ferr := e.fetchLegState(ctx, v, sym, svspNegativeCarry(pos))
		if ferr == nil && sq <= executionQtyTolerance && absQty(pa) <= executionQtyTolerance {
			pos.Status = SVSPStatusClosed
			pos.CloseReason = nonEmpty(pos.CloseReason, "naked_orphan")
			pos.UpdatedAt = e.nowFn()
			e.finalizeClosedPosition(pos, live)
			return
		}
	}
	e.notifyCritical("同所spot-perp孤腿", fmt.Sprintf("%s@%s 检测到孤腿,已平掉剩余腿", pos.Symbol, pos.Venue), pos.Symbol)
}

func (e *SingleVenueSpotPerpExecutor) syncFundingMeta(ctx context.Context, pos *SingleVenueSpotPerpPosition) {
	if pos == nil || !pos.NextFundingTime.IsZero() {
		return
	}
	v := e.venues[pos.Venue]
	if v == nil {
		return
	}
	snaps, err := v.FetchFundingSnapshot(ctx)
	if err != nil {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	for _, snap := range snaps {
		if snap.Symbol != sym {
			continue
		}
		pos.OpenFundingRate = snap.LastFundingRate
		if snap.IntervalHours > 0 {
			pos.FundingIntervalHours = snap.IntervalHours
		}
		if !snap.NextFundingTime.IsZero() {
			pos.NextFundingTime = snap.NextFundingTime
		}
		return
	}
}

func (e *SingleVenueSpotPerpExecutor) syncRealizedFunding(ctx context.Context, pos *SingleVenueSpotPerpPosition) {
	if pos == nil {
		return
	}
	v := e.venues[pos.Venue]
	if v == nil {
		return
	}
	cfg := e.execCfg()
	pollMins := cfg.RealizedFundingPollMinutes
	if pollMins <= 0 {
		pollMins = 5
	}
	now := e.nowFn()
	if !pos.LastFundingSyncAt.IsZero() && now.Sub(pos.LastFundingSyncAt) < time.Duration(pollMins)*time.Minute {
		return
	}
	incomes, err := v.FetchRealizedFunding(ctx, venue.Symbol(pos.Symbol), pos.OpenedAt)
	if err != nil {
		return
	}
	total := 0.0
	for _, in := range incomes {
		total += in.Income
	}
	pos.RealizedFundingUSDT = total
	pos.EstFundingUSDT = total
	pos.LastFundingSyncAt = now
}

func (e *SingleVenueSpotPerpExecutor) shouldClosePosition(ctx context.Context, pos *SingleVenueSpotPerpPosition) string {
	if pos == nil {
		return ""
	}
	if reason := carryHardStopCloseReason(ctx, e.venues, true, pos.Symbol, pos.Venue, pos.Venue, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice); reason != "" {
		return reason
	}
	cfg := e.execCfg()
	now := e.nowFn()
	maxHold := time.Duration(cfg.MaxHoldHours) * time.Hour
	if maxHold <= 0 {
		maxHold = 72 * time.Hour
	}
	if now.Sub(pos.OpenedAt) > maxHold {
		return fmt.Sprintf("持仓超过 %dh", cfg.MaxHoldHours)
	}
	if pos.OpenExpectedNetEV > 0 && pos.ExpectedHoldHours > 0 {
		holdHours := now.Sub(pos.OpenedAt).Hours()
		if holdHours >= pos.ExpectedHoldHours && pos.RealizedFundingUSDT < pos.OpenExpectedNetEV*0.5 {
			if carryEVWeakFundingCloseAllowed(now, pos.NextFundingTime, pos.LastFundingSyncAt) {
				return fmt.Sprintf("持有%.1fh后EV转弱 funding=%.4f", holdHours, pos.RealizedFundingUSDT)
			}
		}
	}
	return ""
}

func (e *SingleVenueSpotPerpExecutor) closePosition(ctx context.Context, pos SingleVenueSpotPerpPosition, live bool, reason string) error {
	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 卖现货 %.6f 原因:%s", pos.Symbol, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 买回永续 %.6f 原因:%s", pos.Symbol, pos.PerpLeg.Qty, reason)
		}
		appendDryRunEvent(e.recordDir, "svsp_close", map[string]any{"symbol": pos.Symbol, "reason": reason})
		return nil
	}
	fromStatus := pos.Status
	pos.Status = SVSPStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	pos.TransitionState = "close"
	_ = e.persistPosition(pos)

	v := e.venues[pos.Venue]
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok || v == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}
	sym := venue.Symbol(pos.Symbol)

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	if pos.NextFundingTime.IsZero() {
		e.syncFundingMeta(ctx, &pos)
	}
	spotQty, perpAmt, ferr := e.fetchLegState(ctx, v, sym, svspNegativeCarry(pos))
	closeSpotQty := 0.0
	closePerpQty := 0.0
	if ferr != nil {
		e.logger.Printf("[同所spot-perp] 平仓 %s 拉腿失败,跳过本轮(保留台账): %v", pos.Symbol, ferr)
	} else {
		if pos.SpotLeg.Qty > executionQtyTolerance && spotQty <= executionQtyTolerance {
			e.logger.Printf("[同所spot-perp] 平仓 %s 现货实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance && absQty(perpAmt) <= executionQtyTolerance {
			e.logger.Printf("[同所spot-perp] 平仓 %s 永续实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		closeSpotQty = ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
		closePerpQty = ledgerCappedCloseQty(pos.PerpLeg.Qty, absQty(perpAmt))
	}
	if closeSpotQty > executionQtyTolerance {
		id := svspLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeSVSPCloseLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, closeSpotQty, id, false, venue.MarginEffectNone, live, attempt); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所spot-perp] 平仓现货腿失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}
	if closePerpQty > executionQtyTolerance {
		id := svspLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placeSVSPCloseLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, closePerpQty, id, true, venue.MarginEffectNone, live, attempt); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所spot-perp] 平仓永续腿失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}

	spotQty, perpAmt, _ = e.fetchLegState(ctx, v, sym, svspNegativeCarry(pos))
	if spotQty <= executionQtyTolerance && absQty(perpAmt) <= executionQtyTolerance {
		if pos.SpotLeg.Qty <= executionQtyTolerance && pos.PerpLeg.Qty <= executionQtyTolerance {
			e.syncRealizedFunding(ctx, &pos)
			pos.Status = SVSPStatusClosed
			pos.UpdatedAt = e.nowFn()
			e.finalizeClosedPosition(pos, live)
			e.logger.Printf("[同所spot-perp] 平仓完成 %s 原因:%s", pos.Symbol, reason)
			return nil
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		return fmt.Errorf("平仓跳过: %s 实测为0,保留台账待下轮", pos.Symbol)
	}

	perpOpen := absQty(perpAmt) > executionQtyTolerance
	spotOpen := spotQty > executionQtyTolerance
	if dust := evaluatePositiveCarryOrphan(ctx, e.execCfg(), e.venues, pos.Symbol, pos.Venue, pos.Venue, spotOpen, perpOpen, spotQty, absQty(perpAmt), pos.SpotLeg.EntryPrice); dust.IsDustOrphan {
		if carryDeferDustOrphanDecision(e.nowFn(), pos.NextFundingTime, dust) {
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return fmt.Errorf("dust_orphan 推迟至结算后: %s", pos.Symbol)
		}
		if live && spotOpen {
			id := svspLegClientID(pos.IdemPrefix, "dust")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
			_, _ = e.placeSVSPCloseLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, id, false, venue.MarginEffectNone, live, attempt)
		}
		e.syncRealizedFunding(ctx, &pos)
		pos.Status = SVSPStatusClosed
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, reason), dust.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "同所spot-perp", pos.Symbol, dust.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return nil
	}

	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[同所spot-perp] 平仓未完成 %s(第%d轮): spot剩%.6f perp剩%.6f",
		pos.Symbol, attempt, spotQty, perpAmt)
	if attempt >= 3 {
		e.notifyCritical("同所spot-perp平仓持续失败",
			fmt.Sprintf("%s 已连续%d轮平不掉", pos.Symbol, attempt), pos.Symbol)
	}
	_ = fromStatus
	return fmt.Errorf("平仓未完成: %s", pos.Symbol)
}

func buildSVSPSettlement(pos SingleVenueSpotPerpPosition, closeTime time.Time) SingleVenueSpotPerpSettlement {
	attr := pos.SingleVenueSpotPerpAttribution
	totalFees := money.SumUSDT(attr.OpenFeesUSDT, attr.CloseFeesUSDT)
	tradePnL := 0.0
	if pos.SpotLeg.ClosedQty > executionQtyTolerance && pos.SpotLeg.ExitPrice > 0 {
		tradePnL += (pos.SpotLeg.ExitPrice - pos.SpotLeg.EntryPrice) * pos.SpotLeg.ClosedQty
	}
	if pos.PerpLeg.ClosedQty > executionQtyTolerance && pos.PerpLeg.ExitPrice > 0 {
		tradePnL += (pos.PerpLeg.EntryPrice - pos.PerpLeg.ExitPrice) * pos.PerpLeg.ClosedQty
	}
	netPnL := money.SumUSDT(pos.RealizedFundingUSDT, tradePnL, -totalFees, -attr.RollbackCostUSDT)
	return SingleVenueSpotPerpSettlement{
		SettlementID:    nonEmpty(pos.IdemPrefix, svspPositionKey(pos)),
		PositionKey:     svspPositionKey(pos),
		OpportunityID:   pos.OpportunityID,
		OpenTime:        pos.OpenedAt,
		CloseTime:       closeTime,
		Symbol:          pos.Symbol,
		Venue:           pos.Venue,
		NotionalUSDT:    pos.NotionalUSDT,
		RealizedFunding: pos.RealizedFundingUSDT,
		TradePnL:        tradePnL,
		OpenFees:        attr.OpenFeesUSDT,
		CloseFees:       attr.CloseFeesUSDT,
		RollbackCost:    attr.RollbackCostUSDT,
		TotalFees:       totalFees,
		NetPnL:          netPnL,
		CloseReason:     pos.CloseReason,
	}
}

func (e *SingleVenueSpotPerpExecutor) settlementsPath() string {
	return filepath.Join(singleVenueSpotPerpRecordDir(e.recordDir), svspSettlementsLogName)
}

func (e *SingleVenueSpotPerpExecutor) appendSettlement(pos SingleVenueSpotPerpPosition) {
	rec := buildSVSPSettlement(pos, e.nowFn())
	data, err := json.Marshal(rec)
	if err != nil {
		e.logger.Printf("[同所spot-perp结算] 序列化失败 %s: %v", pos.Symbol, err)
		return
	}
	e.settlementsMu.Lock()
	defer e.settlementsMu.Unlock()
	path := e.settlementsPath()
	if svspSettlementExists(path, rec.SettlementID) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
	e.logger.Printf("[同所spot-perp结算] %s net=%.4f funding=%.4f fees=%.4f",
		rec.Symbol, rec.NetPnL, rec.RealizedFunding, rec.TotalFees)
	appendAttributionRealizedBestEffort(e.recordDir, realizedRecordFromSVSPSettlement(rec))
}

func svspSettlementExists(path, settlementID string) bool {
	if settlementID == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec SingleVenueSpotPerpSettlement
		if json.Unmarshal([]byte(line), &rec) == nil && rec.SettlementID == settlementID {
			return true
		}
	}
	return false
}

func (e *SingleVenueSpotPerpExecutor) finalizeClosedPosition(pos SingleVenueSpotPerpPosition, live bool) {
	if !realLedgerWritesAllowed(live) {
		return
	}
	e.appendSettlement(pos)
	_ = e.removePosition(pos)
}

func (e *SingleVenueSpotPerpExecutor) notifyCritical(title, msg, symbol string) {
	if e.logger != nil {
		e.logger.Printf("[critical][同所spot-perp] %s: %s (%s)", title, msg, symbol)
	}
}

// OpeningsEnabled 返回运行时开仓开关;未登记材料级敞口 halt 后为 false。
func (e *SingleVenueSpotPerpExecutor) OpeningsEnabled() bool {
	if e == nil {
		return false
	}
	return e.openingsEnabled.Load()
}

func (e *SingleVenueSpotPerpExecutor) armDeadManForPerpVenue(ctx context.Context, venueName string, live bool) {
	armDeadMansForVenueNames(
		ctx, e.cfg, e.venues, e.logger, e.nowFn,
		&e.mu, e.deadManUnsupportedVenues, e.deadManLastArm,
		[]string{venueName}, live,
	)
}

// CloseOnePositionVerified 核对交易所真实持仓后平仓:无真实腿只清台账;有真实腿则 live 平仓。
func (e *SingleVenueSpotPerpExecutor) CloseOnePositionVerified(ctx context.Context, opportunityID, symbol, reason string) error {
	if e == nil {
		return fmt.Errorf("同所spot-perp 执行器未初始化")
	}
	pos, ok := findSingleVenueSpotPerpHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
	if !ok {
		return fmt.Errorf("同所spot-perp 持仓未找到 opp=%s", opportunityID)
	}
	v := e.venues[pos.Venue]
	if v == nil {
		return fmt.Errorf("%s@%s: venue 未就绪", pos.Symbol, pos.Venue)
	}
	spotQty, perpAmt, ferr := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol), svspNegativeCarry(pos))
	if ferr != nil {
		return fmt.Errorf("%s: %w", pos.Symbol, ferr)
	}
	neg := svspNegativeCarry(pos)
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpAmt > executionQtyTolerance
	if !neg {
		perpOpen = absQty(perpAmt) > executionQtyTolerance
	}
	if !spotOpen && !perpOpen {
		pos.Status = SVSPStatusClosed
		if pos.CloseReason == "" {
			pos.CloseReason = reason
		}
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos, true)
		return nil
	}
	return e.closePosition(ctx, pos, true, reason)
}

// CountRealHeldPositions 统计台账中有交易所真实敞口的持仓数。
func (e *SingleVenueSpotPerpExecutor) CountRealHeldPositions(ctx context.Context) int {
	if e == nil || e.store == nil {
		return 0
	}
	ps, err := e.store.load()
	if err != nil {
		return 0
	}
	count := 0
	for _, pos := range activeSingleVenueSpotPerpPositions(ps) {
		v := e.venues[pos.Venue]
		if v == nil {
			continue
		}
		spotQty, perpAmt, ferr := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol), svspNegativeCarry(pos))
		if ferr != nil {
			continue
		}
		if spotQty > executionQtyTolerance || absQty(perpAmt) > executionQtyTolerance {
			count++
		}
	}
	return count
}

// CloseAllPositions 一键全平:FetchBalances/FetchPositions 对账后只平真实腿;dry-run 合成记录仅清台账。
func (e *SingleVenueSpotPerpExecutor) CloseAllPositions(ctx context.Context, live bool) (closed int, errs []error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	ps, err := e.store.load()
	if err != nil {
		return 0, []error{err}
	}
	for _, pos := range activeSingleVenueSpotPerpPositions(ps) {
		v := e.venues[pos.Venue]
		if v == nil {
			errs = append(errs, fmt.Errorf("%s@%s: venue 未就绪", pos.Symbol, pos.Venue))
			continue
		}
		spotQty, perpAmt, ferr := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol), svspNegativeCarry(pos))
		if ferr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pos.Symbol, ferr))
			continue
		}
		spotOpen := spotQty > executionQtyTolerance
		perpOpen := absQty(perpAmt) > executionQtyTolerance
		if !spotOpen && !perpOpen {
			pos.Status = SVSPStatusClosed
			pos.CloseReason = "flatten_all_orphan"
			pos.UpdatedAt = e.nowFn()
			e.finalizeClosedPosition(pos, live)
			closed++
			continue
		}
		if cerr := e.closePosition(ctx, pos, live, "flatten_all"); cerr != nil {
			errs = append(errs, cerr)
		} else {
			closed++
		}
	}
	return closed, errs
}

// reconcileOnStart 启动时载入台账并对账;仅处理本策略 store 登记的持仓。
// 绝不触碰非本策略持仓(尤其 perp-perp 永续仓);未登记交易所敞口一律忽略、绝不下单。
func (e *SingleVenueSpotPerpExecutor) reconcileOnStart(ctx context.Context, live bool) {
	if e.store == nil {
		return
	}
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[同所spot-perp] reconcile 读台账失败: %v", err)
		return
	}
	active := activeSingleVenueSpotPerpPositions(ps)
	if len(active) > 0 {
		e.logger.Printf("[同所spot-perp] reconcile: %d 笔落盘持仓", len(active))
		for _, p := range active {
			v := e.venues[p.Venue]
			if v == nil {
				e.logger.Printf("[同所spot-perp] reconcile:%s venue 未就绪", p.Symbol)
				continue
			}
			e.reconcileTrackedPositionOnStart(ctx, p, v, live)
		}
	}
	e.reconcileUntrackedExposureOnStart(ctx, ps, live)
	if latest, lerr := e.store.load(); lerr == nil {
		ps = latest
	}
	e.setPositions(ps)
}

func (e *SingleVenueSpotPerpExecutor) reconcileUntrackedExposureOnStart(ctx context.Context, ps []SingleVenueSpotPerpPosition, live bool) {
	cfg := e.execCfg()
	untracked := carryScanUntrackedSVSPLegs(ctx, cfg, e.recordDir, e.logger, e.venues, ps)
	if len(untracked) == 0 {
		return
	}
	action := crossVenueUntrackedExposureAction(cfg)
	for _, leg := range untracked {
		e.logger.Printf("[同所spot-perp] reconcile: venue=%s 未登记 %s %s≈%.6f", leg.Venue, leg.Market, leg.Symbol, leg.Qty)
	}
	shouldHalt := true
	switch action {
	case crossVenueRecoveryActionFlatten:
		if !cfg.LiveTradingEnabled {
			e.notifyCritical("同所spot-perp冷启动", "检测到未登记真实持仓,flatten 需 live", "")
		} else {
			shouldHalt = false
			for _, leg := range untracked {
				e.flattenUntrackedLeg(ctx, leg, live)
			}
			e.notifyCritical("同所spot-perp冷启动 flatten", fmt.Sprintf("已对 %d 条未登记腿执行 flatten", len(untracked)), "")
		}
	default:
		e.notifyCritical("同所spot-perp冷启动", fmt.Sprintf("检测到 %d 条未登记真实持仓,已 halt 待人工确认", len(untracked)), "")
	}
	if shouldHalt {
		e.openingsEnabled.Store(false)
	}
}

func (e *SingleVenueSpotPerpExecutor) flattenUntrackedLeg(ctx context.Context, leg carryUntrackedLeg, live bool) {
	if !live || leg.Qty <= executionQtyTolerance {
		return
	}
	if carryShouldSkipUntrackedFlatten(e.recordDir, e.logger, leg) {
		return
	}
	v := e.venues[leg.Venue]
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok || tr == nil {
		return
	}
	id := fmt.Sprintf("svsp-reconcile-flat-%s-%d", leg.Symbol, e.nowFn().UnixNano())
	_, err := e.placeSVSPLegIOC(ctx, tr, leg.Market, venue.Symbol(leg.Symbol), leg.Side, leg.Qty, id, leg.ReduceOnly, venue.MarginEffectNone, live, svspTakerSlippageBps(e.execCfg()))
	carryAfterUntrackedFlatten(ctx, e.recordDir, e.logger, e.venues, leg, err == nil)
}

func (e *SingleVenueSpotPerpExecutor) reconcileTrackedPositionOnStart(ctx context.Context, pos SingleVenueSpotPerpPosition, v venue.Venue, live bool) {
	spotQty, perpAmt, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol), svspNegativeCarry(pos))
	if err != nil {
		e.logger.Printf("[同所spot-perp] reconcile %s 拉腿失败: %v", pos.Symbol, err)
		return
	}
	neg := svspNegativeCarry(pos)
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpAmt > executionQtyTolerance
	if !neg {
		perpOpen = absQty(perpAmt) > executionQtyTolerance
	}

	if svspIsOpeningStatus(pos.Status) {
		_, _ = e.manageOpeningContinuation(ctx, pos, spotOpen, perpOpen, spotQty, perpAmt, live)
		return
	}
	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpAmt)
		return
	}
	if !spotOpen && !perpOpen && pos.Status != SVSPStatusClosed {
		pos.Status = SVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos, live)
	}
}

// ReconcileOnStart 启动时载入台账并对账(统一轮动 runner 调用;live 由 runner.isLive() 传入)。
func (e *SingleVenueSpotPerpExecutor) ReconcileOnStart(ctx context.Context, live bool) {
	e.reconcileOnStart(ctx, live)
}

// ManageOpenPositions 管理所有在场持仓(统一轮动 runner 调用)。
func (e *SingleVenueSpotPerpExecutor) ManageOpenPositions(ctx context.Context, live bool) {
	e.manageOpenPositions(ctx, live)
}

// ListActivePositions 返回非 closed 状态的持仓(只读,供面板/API)。
func (e *SingleVenueSpotPerpExecutor) ListActivePositions() []SingleVenueSpotPerpPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		ps = e.snapshotPositions()
	}
	return activeSingleVenueSpotPerpPositions(ps)
}

// ListAllPositions 返回台账全部持仓(只读)。
func (e *SingleVenueSpotPerpExecutor) ListAllPositions() []SingleVenueSpotPerpPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		return e.snapshotPositions()
	}
	return ps
}

func (e *SingleVenueSpotPerpExecutor) listPositionsForAPI(statusFilter string) []SingleVenueSpotPerpPosition {
	switch strings.ToLower(strings.TrimSpace(statusFilter)) {
	case "closed":
		return nil
	case "all":
		return e.ListAllPositions()
	default:
		return e.ListActivePositions()
	}
}

// ListRecentSettlements 读取 settlements.jsonl 尾部记录(只读)。
func (e *SingleVenueSpotPerpExecutor) ListRecentSettlements(limit int) ([]SingleVenueSpotPerpSettlement, error) {
	if e == nil {
		return nil, nil
	}
	e.settlementsMu.Lock()
	defer e.settlementsMu.Unlock()
	return loadTailJSONLFile[SingleVenueSpotPerpSettlement](e.settlementsPath(), time.Time{}, limit)
}
