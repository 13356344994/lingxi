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
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

// single_venue_perp_spot_executor.go 同所 perp-spot 负费率执行器(多永续+借币空现货, shadow 起步, live 闸透传)。
// 强平缓冲:空现货在逼空/强平场景下亏损可能穿仓,执行器保守杠杆并优先平现货腿。

const (
	svpsConfirmTimeout     = 8 * time.Second
	svpsConfirmPoll        = 400 * time.Millisecond
	svpsSettlementsLogName = "settlements.jsonl"
)

type singleVenuePerpSpotTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

// SingleVenuePerpSpotExecutor 同所多永续+借币空现货负 carry 执行引擎。
type SingleVenuePerpSpotExecutor struct {
	cfg    EngineConfig
	venues map[string]venue.Venue
	store  *singleVenuePerpSpotPositionStore
	logger *log.Logger

	mu            sync.RWMutex
	positions     []SingleVenuePerpSpotPosition
	settlementsMu sync.Mutex
	recordDir     string
	nowFn         func() time.Time
}

// NewSingleVenuePerpSpotExecutor 构造执行器;venues 由调用方注入便于单测。
func NewSingleVenuePerpSpotExecutor(cfg EngineConfig, venues map[string]venue.Venue, recordDir string, logger *log.Logger) *SingleVenuePerpSpotExecutor {
	if logger == nil {
		logger = log.Default()
	}
	if venues == nil {
		venues = make(map[string]venue.Venue)
	}
	store, err := newSingleVenuePerpSpotPositionStore(recordDir)
	if err != nil && logger != nil {
		logger.Printf("[同所perp-spot] 持仓台账初始化失败: %v", err)
	}
	e := &SingleVenuePerpSpotExecutor{
		cfg:       cfg,
		venues:    venues,
		store:     store,
		logger:    logger,
		recordDir: strings.TrimSpace(recordDir),
		nowFn:     time.Now,
	}
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

func svpsIdemPrefix(sym venue.Symbol, venueName string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("svps-%s-%s-%d", base, venueName, time.Now().UTC().Unix())
}

func svpsLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

func svpsTakerSlippageBps(cfg CrossVenueExecutorConfig) float64 {
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

func (e *SingleVenuePerpSpotExecutor) execCfg() CrossVenueExecutorConfig {
	return e.cfg.CrossVenueExecutor
}

func (e *SingleVenuePerpSpotExecutor) snapshotPositions() []SingleVenuePerpSpotPosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]SingleVenuePerpSpotPosition, len(e.positions))
	copy(out, e.positions)
	return out
}

func (e *SingleVenuePerpSpotExecutor) setPositions(ps []SingleVenuePerpSpotPosition) {
	e.mu.Lock()
	e.positions = ps
	e.mu.Unlock()
}

func (e *SingleVenuePerpSpotExecutor) persistPosition(pos SingleVenuePerpSpotPosition) error {
	if e.store == nil {
		return fmt.Errorf("持仓台账未初始化")
	}
	if err := e.store.upsert(pos); err != nil {
		return err
	}
	e.mu.Lock()
	found := false
	for i := range e.positions {
		if svpsSamePosition(e.positions[i], pos) {
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

func (e *SingleVenuePerpSpotExecutor) removePosition(pos SingleVenuePerpSpotPosition) error {
	if e.store == nil {
		return nil
	}
	key := svpsPositionKey(pos)
	if err := e.store.remove(key); err != nil {
		return err
	}
	e.mu.Lock()
	out := make([]SingleVenuePerpSpotPosition, 0, len(e.positions))
	for _, p := range e.positions {
		if svpsPositionKey(p) != key {
			out = append(out, p)
		}
	}
	e.positions = out
	e.mu.Unlock()
	return nil
}

func parseSingleVenuePerpSpotOpportunity(opp UnifiedOpportunity) (venueName string, qty, notional float64, err error) {
	if opp.StrategyType != "" && opp.StrategyType != UnifiedStrategySingleVenuePerpSpot {
		return "", 0, 0, fmt.Errorf("strategy_type=%s", opp.StrategyType)
	}
	if len(opp.Legs) < 2 {
		return "", 0, 0, fmt.Errorf("legs<2")
	}
	var spotVenue, perpVenue string
	for _, leg := range opp.Legs {
		mkt := strings.ToLower(strings.TrimSpace(leg.Market))
		side := strings.ToLower(strings.TrimSpace(leg.Side))
		v := strings.TrimSpace(leg.Venue)
		switch mkt {
		case "spot":
			if side != "short" && side != "sell" {
				return "", 0, 0, fmt.Errorf("spot leg side=%s", leg.Side)
			}
			spotVenue = v
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		case "perp":
			if side != "long" && side != "buy" {
				return "", 0, 0, fmt.Errorf("perp leg side=%s", leg.Side)
			}
			perpVenue = v
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		default:
			return "", 0, 0, fmt.Errorf("unknown market=%s", leg.Market)
		}
	}
	if spotVenue == "" || perpVenue == "" {
		return "", 0, 0, fmt.Errorf("missing spot/perp leg")
	}
	if !strings.EqualFold(spotVenue, perpVenue) {
		return "", 0, 0, fmt.Errorf("legs on different venues: %s vs %s", spotVenue, perpVenue)
	}
	if qty <= executionQtyTolerance {
		return "", 0, 0, fmt.Errorf("qty<=0")
	}
	return spotVenue, qty, notional, nil
}

// checkNegativeCarrySupported 借币可得性闸:不支持借币做空的所直接放弃,绝不尝试借币。
func (e *SingleVenuePerpSpotExecutor) checkNegativeCarrySupported(v venue.Venue, venueName string) bool {
	if v == nil {
		e.logger.Printf("[同所perp-spot] venue %s 未就绪,放弃机会", venueName)
		return false
	}
	if !v.Capabilities().SupportsNegativeCarry {
		e.logger.Printf("[同所perp-spot] %s 不支持借币做空(SupportsNegativeCarry=false),放弃机会", venueName)
		return false
	}
	return true
}

func (e *SingleVenuePerpSpotExecutor) confirmFill(
	ctx context.Context,
	tr singleVenuePerpSpotTrader,
	mkt venue.Market,
	sym venue.Symbol,
	clientID string,
) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(svpsConfirmTimeout)
	var last venue.OrderResult
	for {
		res, err := tr.QueryOrder(ctx, mkt, sym, clientID)
		if err == nil {
			last = res
			st := strings.ToLower(res.Status)
			if st == "filled" {
				return res, nil
			}
			if st == "canceled" || st == "rejected" {
				return res, fmt.Errorf("订单终态=%s", res.Status)
			}
		}
		if e.nowFn().After(deadline) {
			if last.ExecutedQty > executionQtyTolerance {
				return last, nil
			}
			return last, fmt.Errorf("确认成交超时")
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(svpsConfirmPoll):
		}
	}
}

// placeSVPSIOC IOC 下单;!live 时返回合成成交且绝不调用 PlaceOrder/BorrowRepay。
func (e *SingleVenuePerpSpotExecutor) placeSVPSIOC(
	ctx context.Context,
	tr singleVenuePerpSpotTrader,
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

func (e *SingleVenuePerpSpotExecutor) placeSVPSLegIOC(
	ctx context.Context,
	tr singleVenuePerpSpotTrader,
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
	return e.placeSVPSIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live)
}

// executeOpen 腿1借币空现货→腿2多永续;腿2失败回滚腿1(买回现货+还币)。
func (e *SingleVenuePerpSpotExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (SingleVenuePerpSpotPosition, error) {
	venueName, qty, notional, err := parseSingleVenuePerpSpotOpportunity(opp)
	if err != nil {
		return SingleVenuePerpSpotPosition{}, err
	}
	v := e.venues[venueName]
	if !e.checkNegativeCarrySupported(v, venueName) {
		return SingleVenuePerpSpotPosition{}, fmt.Errorf("venue %s 不支持负 carry 借币", venueName)
	}
	tr, ok := v.(singleVenuePerpSpotTrader)
	if !ok {
		return SingleVenuePerpSpotPosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	sym := venue.Symbol(opp.Symbol)
	cfg := e.execCfg()
	idem := svpsIdemPrefix(sym, venueName)
	now := e.nowFn()

	pos := SingleVenuePerpSpotPosition{
		Symbol:            opp.Symbol,
		Venue:             venueName,
		OpportunityID:     opp.ID,
		OpenExpectedNetEV: opp.ExpectedNetEVUSDT,
		ExpectedHoldHours: opp.ExpectedHoldHours,
		NotionalUSDT:      notional,
		Status:            SVPSStatusOpeningSpot,
		TransitionState:   "open",
		OpenedAt:          now,
		UpdatedAt:         now,
		IdemPrefix:        idem,
		SpotLeg:           SingleVenuePerpSpotLegState{Market: "spot", Side: "short"},
		PerpLeg:           SingleVenuePerpSpotLegState{Market: "perp", Side: "long"},
	}
	if pos.ExpectedHoldHours <= 0 {
		pos.ExpectedHoldHours = 8
	}
	if pos.NotionalUSDT <= 0 {
		pos.NotionalUSDT = qty * 100
	}

	if !live {
		e.logger.Printf("[影子] 本应开仓 %s@%s: 借币空现货/多永续 各≈%.6f 名义≈%.2fU EV≈%.4f",
			opp.Symbol, venueName, qty, pos.NotionalUSDT, opp.ExpectedNetEVUSDT)
		pos.Status = SVPSStatusOpen
		pos.SpotLeg.Qty = qty
		pos.SpotLeg.EntryPrice = 100
		pos.PerpLeg.Qty = qty
		pos.PerpLeg.EntryPrice = 100
		pos.TransitionState = "hold"
		return pos, nil
	}

	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}

	takerBps := svpsTakerSlippageBps(cfg)
	spotID := svpsLegClientID(idem, "s1")
	spotRes, err := e.placeSVPSLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, spotID, false, venue.MarginEffectBorrow, live, takerBps)
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, tr, sym, &pos, spotRes.ExecutedQty, live, idem)
		}
		return pos, fmt.Errorf("现货借币腿失败: %w", err)
	}
	pos.SpotLeg.Qty = spotRes.ExecutedQty
	pos.SpotLeg.EntryPrice = spotRes.AvgPrice
	pos.SpotLeg.OrderIDs = []string{spotID}
	pos.Status = SVPSStatusOpeningPerp
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	hedgeQty := spotRes.ExecutedQty
	perpID := svpsLegClientID(idem, "p1")
	perpRes, perpErr := e.placeSVPSLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, hedgeQty, perpID, false, venue.MarginEffectNone, live, takerBps)
	if perpErr != nil || perpRes.ExecutedQty <= executionQtyTolerance {
		e.rollbackSpotLeg(ctx, tr, sym, &pos, pos.SpotLeg.Qty, live, idem)
		e.logger.Printf("[同所perp-spot] %s 永续腿失败,已回滚现货腿(买回+还币)", opp.Symbol)
		return pos, fmt.Errorf("永续腿失败: %w", perpErr)
	}
	pos.PerpLeg.Qty = perpRes.ExecutedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
	pos.NotionalUSDT = hedgeQty * spotRes.AvgPrice
	pos.Status = SVPSStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[同所perp-spot] 开仓 %s@%s 借币空现货%.6f/多永续%.6f 名义≈%.2fU",
		opp.Symbol, venueName, pos.SpotLeg.Qty, pos.PerpLeg.Qty, pos.NotionalUSDT)
	return pos, nil
}

func (e *SingleVenuePerpSpotExecutor) rollbackSpotLeg(
	ctx context.Context,
	tr singleVenuePerpSpotTrader,
	sym venue.Symbol,
	pos *SingleVenuePerpSpotPosition,
	qty float64,
	live bool,
	idemPrefix string,
) {
	if qty <= executionQtyTolerance {
		return
	}
	id := svpsLegClientID(idemPrefix, "rb")
	res, err := e.placeSVPSLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live, svpsTakerSlippageBps(e.execCfg()))
	if err != nil {
		e.logger.Printf("[同所perp-spot] 回滚现货腿失败 %s: %v", sym, err)
		e.notifyCritical("同所perp-spot回滚失败", fmt.Sprintf("%s 现货买回还币回滚失败: %v", sym, err), string(sym))
		return
	}
	if pos != nil {
		pos.RollbackCostUSDT += res.Fee
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(*pos)
	}
}

func svpsSpotShortQty(bals venue.Balances, baseAsset string) float64 {
	if bals.ByAsset == nil {
		return 0
	}
	b := bals.ByAsset[strings.ToUpper(strings.TrimSpace(baseAsset))]
	net := b.Free + b.Locked
	if net < 0 {
		return -net
	}
	return 0
}

func svpsPerpLongQty(positions []venue.Position, sym venue.Symbol) float64 {
	amt := perpPositionQty(positions, sym)
	if amt > executionQtyTolerance {
		return amt
	}
	return 0
}

func (e *SingleVenuePerpSpotExecutor) fetchLegState(ctx context.Context, v venue.Venue, sym venue.Symbol) (spotShortQty, perpLongQty float64, err error) {
	base := baseAssetFromCanonicalSymbol(string(sym))
	bals, berr := v.FetchBalances(ctx)
	if berr == nil {
		spotShortQty = svpsSpotShortQty(bals, base)
	}
	positions, perr := v.FetchPositions(ctx)
	if perr == nil {
		perpLongQty = svpsPerpLongQty(positions, sym)
	}
	if berr != nil && perr != nil {
		return 0, 0, fmt.Errorf("balances=%v positions=%v", berr, perr)
	}
	return spotShortQty, perpLongQty, nil
}

func (e *SingleVenuePerpSpotExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeSingleVenuePerpSpotPositions(e.snapshotPositions()) {
		updated, _ := e.managePosition(ctx, pos, live)
		_ = e.persistPosition(updated)
	}
}

func (e *SingleVenuePerpSpotExecutor) managePosition(ctx context.Context, pos SingleVenuePerpSpotPosition, live bool) (SingleVenuePerpSpotPosition, bool) {
	v := e.venues[pos.Venue]
	if v == nil {
		return pos, false
	}

	if pos.Status == SVPSStatusClosing {
		err := e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, err == nil
	}

	spotShort, perpLong, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
	if err != nil {
		e.logger.Printf("[同所perp-spot] 拉腿状态失败 %s: %v", pos.Symbol, err)
		return pos, false
	}
	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance

	if svpsIsOpeningStatus(pos.Status) {
		return e.manageOpeningContinuation(ctx, pos, spotOpen, perpOpen, spotShort, perpLong, live)
	}

	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotShort, perpLong)
		return pos, true
	}
	if !spotOpen && !perpOpen {
		pos.Status = SVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return pos, true
	}

	e.syncRealizedFunding(ctx, &pos)
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	if reason := e.shouldClosePosition(ctx, &pos); reason != "" {
		_ = e.closePosition(ctx, pos, live, reason)
		return pos, true
	}
	return pos, false
}

func (e *SingleVenuePerpSpotExecutor) manageOpeningContinuation(
	ctx context.Context,
	pos SingleVenuePerpSpotPosition,
	spotOpen, perpOpen bool,
	spotShort, perpLong float64,
	live bool,
) (SingleVenuePerpSpotPosition, bool) {
	now := e.nowFn()
	v := e.venues[pos.Venue]
	tr, _ := v.(singleVenuePerpSpotTrader)
	sym := venue.Symbol(pos.Symbol)

	switch {
	case !spotOpen && !perpOpen:
		pos.Status = SVPSStatusClosed
		pos.CloseReason = "open_retry"
		pos.UpdatedAt = now
		e.finalizeClosedPosition(pos)
		return pos, true
	case spotOpen && !perpOpen && pos.Status == SVPSStatusOpeningPerp:
		if tr == nil {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotShort, perpLong)
			return pos, true
		}
		hedgeQty := spotShort
		if pos.SpotLeg.Qty > executionQtyTolerance {
			hedgeQty = pos.SpotLeg.Qty
		}
		perpID := svpsLegClientID(pos.IdemPrefix, "p2")
		perpRes, err := e.placeSVPSLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, hedgeQty, perpID, false, venue.MarginEffectNone, live, svpsTakerSlippageBps(e.execCfg()))
		if err != nil || perpRes.ExecutedQty <= executionQtyTolerance {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotShort, perpLong)
			return pos, true
		}
		pos.PerpLeg.Qty = perpRes.ExecutedQty
		pos.PerpLeg.EntryPrice = perpRes.AvgPrice
		pos.PerpLeg.OrderIDs = append(pos.PerpLeg.OrderIDs, perpID)
		pos.Status = SVPSStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, true
	case spotOpen && perpOpen:
		pos.Status = SVPSStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, true
	default:
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotShort, perpLong)
		return pos, true
	}
}

func (e *SingleVenuePerpSpotExecutor) handleOrphanLeg(
	ctx context.Context,
	pos SingleVenuePerpSpotPosition,
	spotOpen, perpOpen, live bool,
	spotShort, perpLong float64,
) {
	pos.Status = SVPSStatusOrphan
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	v := e.venues[pos.Venue]
	tr, _ := v.(singleVenuePerpSpotTrader)
	if tr == nil {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	takerBps := svpsTakerSlippageBps(e.execCfg())
	if spotOpen && !perpOpen {
		qty := spotShort
		if pos.SpotLeg.Qty > executionQtyTolerance {
			qty = pos.SpotLeg.Qty
		}
		if live {
			_, _ = e.placeSVPSLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, svpsLegClientID(pos.IdemPrefix, "os"), false, venue.MarginEffectRepay, live, takerBps)
		}
	} else if perpOpen && !spotOpen {
		qty := perpLong
		if live {
			_, _ = e.placeSVPSLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, qty, svpsLegClientID(pos.IdemPrefix, "op"), true, venue.MarginEffectNone, live, takerBps)
		}
	}
	e.notifyCritical("同所perp-spot孤腿", fmt.Sprintf("%s@%s 检测到孤腿,已平掉剩余腿", pos.Symbol, pos.Venue), pos.Symbol)
}

func (e *SingleVenuePerpSpotExecutor) syncRealizedFunding(ctx context.Context, pos *SingleVenuePerpSpotPosition) {
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

func (e *SingleVenuePerpSpotExecutor) shouldClosePosition(_ context.Context, pos *SingleVenuePerpSpotPosition) string {
	if pos == nil {
		return ""
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
			return fmt.Sprintf("持有%.1fh后EV转弱 funding=%.4f", holdHours, pos.RealizedFundingUSDT)
		}
	}
	return ""
}

func (e *SingleVenuePerpSpotExecutor) closePosition(ctx context.Context, pos SingleVenuePerpSpotPosition, live bool, reason string) error {
	fromStatus := pos.Status
	pos.Status = SVPSStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	pos.TransitionState = "close"
	_ = e.persistPosition(pos)

	v := e.venues[pos.Venue]
	tr, ok := v.(singleVenuePerpSpotTrader)
	if !ok || v == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}
	sym := venue.Symbol(pos.Symbol)

	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 买回现货还币 %.6f 原因:%s", pos.Symbol, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 平多永续 %.6f 原因:%s", pos.Symbol, pos.PerpLeg.Qty, reason)
		}
		e.syncRealizedFunding(ctx, &pos)
		pos.Status = SVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return nil
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	takerBps := svpsTakerSlippageBps(e.execCfg())

	spotShort, perpLong, _ := e.fetchLegState(ctx, v, sym)
	// 优先平现货腿(空现货逼空风险更大)。
	if spotShort > executionQtyTolerance {
		id := svpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeSVPSLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, spotShort, id, false, venue.MarginEffectRepay, live, takerBps); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所perp-spot] 平仓现货腿(还币)失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}
	if perpLong > executionQtyTolerance {
		id := svpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placeSVPSLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, perpLong, id, true, venue.MarginEffectNone, live, takerBps); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所perp-spot] 平仓永续腿失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}

	spotShort, perpLong, _ = e.fetchLegState(ctx, v, sym)
	if spotShort <= executionQtyTolerance && perpLong <= executionQtyTolerance {
		e.syncRealizedFunding(ctx, &pos)
		pos.Status = SVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		e.logger.Printf("[同所perp-spot] 平仓完成 %s 原因:%s", pos.Symbol, reason)
		return nil
	}

	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[同所perp-spot] 平仓未完成 %s(第%d轮): spot空剩%.6f perp多剩%.6f",
		pos.Symbol, attempt, spotShort, perpLong)
	if attempt >= 3 {
		e.notifyCritical("同所perp-spot平仓持续失败",
			fmt.Sprintf("%s 已连续%d轮平不掉", pos.Symbol, attempt), pos.Symbol)
	}
	_ = fromStatus
	return fmt.Errorf("平仓未完成: %s", pos.Symbol)
}

func buildSVPSSettlement(pos SingleVenuePerpSpotPosition, closeTime time.Time) SingleVenuePerpSpotSettlement {
	attr := pos.SingleVenuePerpSpotAttribution
	totalFees := money.SumUSDT(attr.OpenFeesUSDT, attr.CloseFeesUSDT)
	tradePnL := 0.0
	if pos.SpotLeg.ClosedQty > executionQtyTolerance && pos.SpotLeg.ExitPrice > 0 {
		tradePnL += (pos.SpotLeg.EntryPrice - pos.SpotLeg.ExitPrice) * pos.SpotLeg.ClosedQty
	}
	if pos.PerpLeg.ClosedQty > executionQtyTolerance && pos.PerpLeg.ExitPrice > 0 {
		tradePnL += (pos.PerpLeg.ExitPrice - pos.PerpLeg.EntryPrice) * pos.PerpLeg.ClosedQty
	}
	netPnL := money.SumUSDT(pos.RealizedFundingUSDT, tradePnL, -totalFees, -attr.RollbackCostUSDT, -attr.BorrowCostUSDT)
	return SingleVenuePerpSpotSettlement{
		SettlementID:    nonEmpty(pos.IdemPrefix, svpsPositionKey(pos)),
		PositionKey:     svpsPositionKey(pos),
		OpportunityID:   pos.OpportunityID,
		OpenTime:        pos.OpenedAt,
		CloseTime:       closeTime,
		Symbol:          pos.Symbol,
		Venue:           pos.Venue,
		NotionalUSDT:    pos.NotionalUSDT,
		RealizedFunding: pos.RealizedFundingUSDT,
		TradePnL:        tradePnL,
		BorrowCost:      attr.BorrowCostUSDT,
		OpenFees:        attr.OpenFeesUSDT,
		CloseFees:       attr.CloseFeesUSDT,
		RollbackCost:    attr.RollbackCostUSDT,
		TotalFees:       totalFees,
		NetPnL:          netPnL,
		CloseReason:     pos.CloseReason,
	}
}

func (e *SingleVenuePerpSpotExecutor) settlementsPath() string {
	return filepath.Join(singleVenuePerpSpotRecordDir(e.recordDir), svpsSettlementsLogName)
}

func (e *SingleVenuePerpSpotExecutor) appendSettlement(pos SingleVenuePerpSpotPosition) {
	rec := buildSVPSSettlement(pos, e.nowFn())
	data, err := json.Marshal(rec)
	if err != nil {
		e.logger.Printf("[同所perp-spot结算] 序列化失败 %s: %v", pos.Symbol, err)
		return
	}
	e.settlementsMu.Lock()
	defer e.settlementsMu.Unlock()
	path := e.settlementsPath()
	if svpsSettlementExists(path, rec.SettlementID) {
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
	e.logger.Printf("[同所perp-spot结算] %s net=%.4f funding=%.4f borrow=%.4f fees=%.4f",
		rec.Symbol, rec.NetPnL, rec.RealizedFunding, rec.BorrowCost, rec.TotalFees)
}

func svpsSettlementExists(path, settlementID string) bool {
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
		var rec SingleVenuePerpSpotSettlement
		if json.Unmarshal([]byte(line), &rec) == nil && rec.SettlementID == settlementID {
			return true
		}
	}
	return false
}

func (e *SingleVenuePerpSpotExecutor) finalizeClosedPosition(pos SingleVenuePerpSpotPosition) {
	e.appendSettlement(pos)
	_ = e.removePosition(pos)
}

func (e *SingleVenuePerpSpotExecutor) notifyCritical(title, msg, symbol string) {
	if e.logger != nil {
		e.logger.Printf("[critical][同所perp-spot] %s: %s (%s)", title, msg, symbol)
	}
}

// CloseAllPositions 一键全平:FetchBalances/FetchPositions 对账后只平真实腿;shadow 合成记录仅清台账。
func (e *SingleVenuePerpSpotExecutor) CloseAllPositions(ctx context.Context, live bool) (closed int, errs []error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	ps, err := e.store.load()
	if err != nil {
		return 0, []error{err}
	}
	for _, pos := range activeSingleVenuePerpSpotPositions(ps) {
		v := e.venues[pos.Venue]
		if v == nil {
			errs = append(errs, fmt.Errorf("%s@%s: venue 未就绪", pos.Symbol, pos.Venue))
			continue
		}
		spotShort, perpLong, ferr := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
		if ferr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pos.Symbol, ferr))
			continue
		}
		spotOpen := spotShort > executionQtyTolerance
		perpOpen := perpLong > executionQtyTolerance
		if !spotOpen && !perpOpen {
			pos.Status = SVPSStatusClosed
			pos.CloseReason = "flatten_all_orphan"
			pos.UpdatedAt = e.nowFn()
			e.finalizeClosedPosition(pos)
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

// reconcileOnStart 启动时载入台账并对账;孤腿平掉。
func (e *SingleVenuePerpSpotExecutor) reconcileOnStart(ctx context.Context) {
	if e.store == nil {
		return
	}
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[同所perp-spot] reconcile 读台账失败: %v", err)
		return
	}
	active := activeSingleVenuePerpSpotPositions(ps)
	if len(active) > 0 {
		e.logger.Printf("[同所perp-spot] reconcile: %d 笔落盘持仓", len(active))
		for _, p := range active {
			v := e.venues[p.Venue]
			if v == nil {
				e.logger.Printf("[同所perp-spot] reconcile:%s venue 未就绪", p.Symbol)
				continue
			}
			e.reconcileTrackedPositionOnStart(ctx, p, v)
		}
	}
	live := e.execCfg().LiveTradingEnabled
	for venueName, v := range e.venues {
		if v == nil {
			continue
		}
		spotShorts, perpPositions, _ := e.fetchVenueExposure(ctx, v)
		for base, qty := range spotShorts {
			sym := base + "-USDT"
			if qty > executionQtyTolerance && !svpsHasTrackedSymbolVenue(ps, venueName, sym) {
				e.logger.Printf("[同所perp-spot] reconcile: venue=%s 未登记现货空 %s≈%.6f", venueName, sym, qty)
				e.flattenUntrackedOrphan(ctx, venueName, sym, qty, 0, live)
			}
		}
		for _, ep := range perpPositions {
			if ep.PositionAmt <= executionQtyTolerance {
				continue
			}
			sym := string(ep.Symbol)
			if svpsHasTrackedSymbolVenue(ps, venueName, sym) {
				continue
			}
			e.logger.Printf("[同所perp-spot] reconcile: venue=%s 未登记永续多 %s≈%.6f", venueName, sym, ep.PositionAmt)
			base := baseAssetFromCanonicalSymbol(sym)
			spotShort := spotShorts[base]
			e.flattenUntrackedOrphan(ctx, venueName, sym, spotShort, ep.PositionAmt, live)
		}
	}
	if latest, lerr := e.store.load(); lerr == nil {
		ps = latest
	}
	e.setPositions(ps)
}

func (e *SingleVenuePerpSpotExecutor) fetchVenueExposure(ctx context.Context, v venue.Venue) (map[string]float64, []venue.Position, error) {
	spotShorts := make(map[string]float64)
	bals, berr := v.FetchBalances(ctx)
	if berr == nil {
		for asset, bal := range bals.ByAsset {
			net := bal.Free + bal.Locked
			if net < -executionQtyTolerance && !strings.EqualFold(asset, "USDT") {
				spotShorts[strings.ToUpper(asset)] = -net
			}
		}
	}
	positions, perr := v.FetchPositions(ctx)
	if berr != nil && perr != nil {
		return nil, nil, fmt.Errorf("balances=%v positions=%v", berr, perr)
	}
	return spotShorts, positions, nil
}

func (e *SingleVenuePerpSpotExecutor) reconcileTrackedPositionOnStart(ctx context.Context, pos SingleVenuePerpSpotPosition, v venue.Venue) {
	spotShort, perpLong, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
	if err != nil {
		e.logger.Printf("[同所perp-spot] reconcile %s 拉腿失败: %v", pos.Symbol, err)
		return
	}
	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance
	live := e.execCfg().LiveTradingEnabled

	if svpsIsOpeningStatus(pos.Status) {
		_, _ = e.manageOpeningContinuation(ctx, pos, spotOpen, perpOpen, spotShort, perpLong, live)
		return
	}
	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotShort, perpLong)
		return
	}
	if !spotOpen && !perpOpen && pos.Status != SVPSStatusClosed {
		pos.Status = SVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
	}
}

func (e *SingleVenuePerpSpotExecutor) flattenUntrackedOrphan(
	ctx context.Context,
	venueName, symbol string,
	spotShort, perpLong float64,
	live bool,
) {
	v := e.venues[venueName]
	tr, ok := v.(singleVenuePerpSpotTrader)
	if !ok || v == nil {
		return
	}
	sym := venue.Symbol(symbol)
	takerBps := svpsTakerSlippageBps(e.execCfg())
	if spotShort > executionQtyTolerance && perpLong <= executionQtyTolerance {
		if live {
			_, _ = e.placeSVPSLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, spotShort, svpsLegClientID("orph", "s"), false, venue.MarginEffectRepay, live, takerBps)
		}
	}
	if perpLong > executionQtyTolerance && spotShort <= executionQtyTolerance {
		if live {
			_, _ = e.placeSVPSLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, perpLong, svpsLegClientID("orph", "p"), true, venue.MarginEffectNone, live, takerBps)
		}
	}
}
