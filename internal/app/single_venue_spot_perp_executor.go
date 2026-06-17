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

// single_venue_spot_perp_executor.go 同所 spot-perp 正费率执行器(shadow 起步, live 闸透传)。

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

func parseSingleVenueSpotPerpOpportunity(opp UnifiedOpportunity) (venueName string, qty, notional float64, err error) {
	if opp.StrategyType != "" && opp.StrategyType != UnifiedStrategySingleVenueSpotPerp {
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
			if side != "buy" {
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
			if side != "short" && side != "sell" {
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

func (e *SingleVenueSpotPerpExecutor) confirmFill(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	mkt venue.Market,
	sym venue.Symbol,
	clientID string,
) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(svspConfirmTimeout)
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
		case <-time.After(svspConfirmPoll):
		}
	}
}

// placeSVSPOC IOC 下单;!live 时返回合成成交且绝不调用 PlaceOrder(照 cross_venue IOC shadow 分支)。
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
	live bool,
	slippageBps float64,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, slippageBps)
	return e.placeSVSPOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, live)
}

// executeOpen 腿1买现货→腿2空永续;腿2失败回滚腿1(卖回现货)。
func (e *SingleVenueSpotPerpExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (SingleVenueSpotPerpPosition, error) {
	venueName, qty, notional, err := parseSingleVenueSpotPerpOpportunity(opp)
	if err != nil {
		return SingleVenueSpotPerpPosition{}, err
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
	if pos.ExpectedHoldHours <= 0 {
		pos.ExpectedHoldHours = 8
	}
	if pos.NotionalUSDT <= 0 {
		pos.NotionalUSDT = qty * 100
	}

	if !live {
		e.logger.Printf("[影子] 本应开仓 %s@%s: 买现货/空永续 各≈%.6f 名义≈%.2fU EV≈%.4f",
			opp.Symbol, venueName, qty, pos.NotionalUSDT, opp.ExpectedNetEVUSDT)
		pos.Status = SVSPStatusOpen
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

	takerBps := svspTakerSlippageBps(cfg)
	spotID := svspLegClientID(idem, "s1")
	spotRes, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, spotID, false, live, takerBps)
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, tr, sym, &pos, spotRes.ExecutedQty, live, idem)
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
	perpRes, perpErr := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, hedgeQty, perpID, false, live, takerBps)
	if perpErr != nil || perpRes.ExecutedQty <= executionQtyTolerance {
		e.rollbackSpotLeg(ctx, tr, sym, &pos, pos.SpotLeg.Qty, live, idem)
		e.logger.Printf("[同所spot-perp] %s 永续腿失败,已回滚现货腿", opp.Symbol)
		return pos, fmt.Errorf("永续腿失败: %w", perpErr)
	}
	pos.PerpLeg.Qty = perpRes.ExecutedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
	pos.NotionalUSDT = hedgeQty * spotRes.AvgPrice
	pos.Status = SVSPStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[同所spot-perp] 开仓 %s@%s 买现货%.6f/空永续%.6f 名义≈%.2fU",
		opp.Symbol, venueName, pos.SpotLeg.Qty, pos.PerpLeg.Qty, pos.NotionalUSDT)
	return pos, nil
}

func (e *SingleVenueSpotPerpExecutor) rollbackSpotLeg(
	ctx context.Context,
	tr singleVenueSpotPerpTrader,
	sym venue.Symbol,
	pos *SingleVenueSpotPerpPosition,
	qty float64,
	live bool,
	idemPrefix string,
) {
	if qty <= executionQtyTolerance {
		return
	}
	id := svspLegClientID(idemPrefix, "rb")
	res, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, id, false, live, svspTakerSlippageBps(e.execCfg()))
	if err != nil {
		e.logger.Printf("[同所spot-perp] 回滚现货腿失败 %s: %v", sym, err)
		e.notifyCritical("同所spot-perp回滚失败", fmt.Sprintf("%s 现货腿回滚失败: %v", sym, err), string(sym))
		return
	}
	if pos != nil {
		pos.RollbackCostUSDT += res.Fee
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(*pos)
	}
}

func spotBalanceQty(bals venue.Balances, baseAsset string) float64 {
	if bals.ByAsset == nil {
		return 0
	}
	b := bals.ByAsset[strings.ToUpper(strings.TrimSpace(baseAsset))]
	return b.Free + b.Locked
}

func (e *SingleVenueSpotPerpExecutor) fetchLegState(ctx context.Context, v venue.Venue, sym venue.Symbol) (spotQty, perpAmt float64, err error) {
	base := baseAssetFromCanonicalSymbol(string(sym))
	bals, berr := v.FetchBalances(ctx)
	if berr == nil {
		spotQty = spotBalanceQty(bals, base)
	}
	positions, perr := v.FetchPositions(ctx)
	if perr == nil {
		perpAmt = perpPositionQty(positions, sym)
	}
	if berr != nil && perr != nil {
		return 0, 0, fmt.Errorf("balances=%v positions=%v", berr, perr)
	}
	return spotQty, perpAmt, nil
}

// manageOpenPositions 管理所有在场持仓。
func (e *SingleVenueSpotPerpExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeSingleVenueSpotPerpPositions(e.snapshotPositions()) {
		updated, _ := e.managePosition(ctx, pos, live)
		_ = e.persistPosition(updated)
	}
}

func (e *SingleVenueSpotPerpExecutor) managePosition(ctx context.Context, pos SingleVenueSpotPerpPosition, live bool) (SingleVenueSpotPerpPosition, bool) {
	v := e.venues[pos.Venue]
	if v == nil {
		return pos, false
	}

	if pos.Status == SVSPStatusClosing {
		err := e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, err == nil
	}

	spotQty, perpAmt, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
	if err != nil {
		e.logger.Printf("[同所spot-perp] 拉腿状态失败 %s: %v", pos.Symbol, err)
		return pos, false
	}
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := absQty(perpAmt) > executionQtyTolerance

	if svspIsOpeningStatus(pos.Status) {
		return e.manageOpeningContinuation(ctx, pos, spotOpen, perpOpen, spotQty, perpAmt, live)
	}

	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpAmt)
		return pos, true
	}
	if !spotOpen && !perpOpen {
		pos.Status = SVSPStatusClosed
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
		e.finalizeClosedPosition(pos)
		return pos, true
	case spotOpen && !perpOpen && pos.Status == SVSPStatusOpeningPerp:
		if tr == nil {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotQty, perpAmt)
			return pos, true
		}
		hedgeQty := spotQty
		if pos.SpotLeg.Qty > executionQtyTolerance {
			hedgeQty = pos.SpotLeg.Qty
		}
		perpID := svspLegClientID(pos.IdemPrefix, "p2")
		perpRes, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideSell, hedgeQty, perpID, false, live, svspTakerSlippageBps(e.execCfg()))
		if err != nil || perpRes.ExecutedQty <= executionQtyTolerance {
			e.handleOrphanLeg(ctx, pos, true, false, live, spotQty, perpAmt)
			return pos, true
		}
		pos.PerpLeg.Qty = perpRes.ExecutedQty
		pos.PerpLeg.EntryPrice = perpRes.AvgPrice
		pos.PerpLeg.OrderIDs = append(pos.PerpLeg.OrderIDs, perpID)
		pos.Status = SVSPStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, true
	case spotOpen && perpOpen:
		pos.Status = SVSPStatusOpen
		pos.TransitionState = "hold"
		pos.UpdatedAt = now
		_ = e.persistPosition(pos)
		return pos, true
	default:
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpAmt)
		return pos, true
	}
}

func (e *SingleVenueSpotPerpExecutor) handleOrphanLeg(
	ctx context.Context,
	pos SingleVenueSpotPerpPosition,
	spotOpen, perpOpen, live bool,
	spotQty, perpAmt float64,
) {
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
		qty := spotQty
		if pos.SpotLeg.Qty > executionQtyTolerance {
			qty = pos.SpotLeg.Qty
		}
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, svspLegClientID(pos.IdemPrefix, "os"), false, live, takerBps)
		}
	} else if perpOpen && !spotOpen {
		qty := absQty(perpAmt)
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, svspLegClientID(pos.IdemPrefix, "op"), true, live, takerBps)
		}
	}
	e.notifyCritical("同所spot-perp孤腿", fmt.Sprintf("%s@%s 检测到孤腿,已平掉剩余腿", pos.Symbol, pos.Venue), pos.Symbol)
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

func (e *SingleVenueSpotPerpExecutor) shouldClosePosition(_ context.Context, pos *SingleVenueSpotPerpPosition) string {
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

func (e *SingleVenueSpotPerpExecutor) closePosition(ctx context.Context, pos SingleVenueSpotPerpPosition, live bool, reason string) error {
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

	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 卖现货 %.6f 原因:%s", pos.Symbol, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 买回永续 %.6f 原因:%s", pos.Symbol, pos.PerpLeg.Qty, reason)
		}
		e.syncRealizedFunding(ctx, &pos)
		pos.Status = SVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return nil
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	takerBps := svspTakerSlippageBps(e.execCfg())

	spotQty, perpAmt, _ := e.fetchLegState(ctx, v, sym)
	if spotQty > executionQtyTolerance {
		id := svspLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, spotQty, id, false, live, takerBps); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所spot-perp] 平仓现货腿失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}
	if absQty(perpAmt) > executionQtyTolerance {
		id := svspLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		qty := absQty(perpAmt)
		if res, err := e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, id, true, live, takerBps); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
			pos.CloseFeesUSDT += res.Fee
		} else {
			e.logger.Printf("[同所spot-perp] 平仓永续腿失败 %s(第%d轮): %v", pos.Symbol, attempt, err)
		}
	}

	spotQty, perpAmt, _ = e.fetchLegState(ctx, v, sym)
	if spotQty <= executionQtyTolerance && absQty(perpAmt) <= executionQtyTolerance {
		e.syncRealizedFunding(ctx, &pos)
		pos.Status = SVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		e.logger.Printf("[同所spot-perp] 平仓完成 %s 原因:%s", pos.Symbol, reason)
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

func (e *SingleVenueSpotPerpExecutor) finalizeClosedPosition(pos SingleVenueSpotPerpPosition) {
	e.appendSettlement(pos)
	_ = e.removePosition(pos)
}

func (e *SingleVenueSpotPerpExecutor) notifyCritical(title, msg, symbol string) {
	if e.logger != nil {
		e.logger.Printf("[critical][同所spot-perp] %s: %s (%s)", title, msg, symbol)
	}
}

// CloseAllPositions 一键全平:FetchBalances/FetchPositions 对账后只平真实腿;shadow 合成记录仅清台账。
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
		spotQty, perpAmt, ferr := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
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
func (e *SingleVenueSpotPerpExecutor) reconcileOnStart(ctx context.Context) {
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
			e.reconcileTrackedPositionOnStart(ctx, p, v)
		}
	}
	live := e.execCfg().LiveTradingEnabled
	for venueName, v := range e.venues {
		if v == nil {
			continue
		}
		spotQtys, perpPositions, _ := e.fetchVenueExposure(ctx, v)
		sym := ""
		for s, qty := range spotQtys {
			if qty > executionQtyTolerance {
				sym = s
				if !svspHasTrackedSymbolVenue(ps, venueName, sym) {
					e.logger.Printf("[同所spot-perp] reconcile: venue=%s 未登记现货 %s≈%.6f", venueName, sym, qty)
					e.flattenUntrackedOrphan(ctx, venueName, sym, qty, 0, live)
				}
			}
		}
		for _, ep := range perpPositions {
			if absQty(ep.PositionAmt) <= executionQtyTolerance {
				continue
			}
			sym = string(ep.Symbol)
			if svspHasTrackedSymbolVenue(ps, venueName, sym) {
				continue
			}
			e.logger.Printf("[同所spot-perp] reconcile: venue=%s 未登记永续 %s≈%.6f", venueName, sym, ep.PositionAmt)
			base := baseAssetFromCanonicalSymbol(sym)
			spotQty := spotQtys[base]
			e.flattenUntrackedOrphan(ctx, venueName, sym, spotQty, ep.PositionAmt, live)
		}
	}
	if latest, lerr := e.store.load(); lerr == nil {
		ps = latest
	}
	e.setPositions(ps)
}

func (e *SingleVenueSpotPerpExecutor) fetchVenueExposure(ctx context.Context, v venue.Venue) (map[string]float64, []venue.Position, error) {
	spotQtys := make(map[string]float64)
	bals, berr := v.FetchBalances(ctx)
	if berr == nil {
		for asset, bal := range bals.ByAsset {
			qty := bal.Free + bal.Locked
			if qty > executionQtyTolerance && !strings.EqualFold(asset, "USDT") {
				spotQtys[strings.ToUpper(asset)] = qty
			}
		}
	}
	positions, perr := v.FetchPositions(ctx)
	if berr != nil && perr != nil {
		return nil, nil, fmt.Errorf("balances=%v positions=%v", berr, perr)
	}
	return spotQtys, positions, nil
}

func (e *SingleVenueSpotPerpExecutor) reconcileTrackedPositionOnStart(ctx context.Context, pos SingleVenueSpotPerpPosition, v venue.Venue) {
	spotQty, perpAmt, err := e.fetchLegState(ctx, v, venue.Symbol(pos.Symbol))
	if err != nil {
		e.logger.Printf("[同所spot-perp] reconcile %s 拉腿失败: %v", pos.Symbol, err)
		return
	}
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := absQty(perpAmt) > executionQtyTolerance
	live := e.execCfg().LiveTradingEnabled

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
		e.finalizeClosedPosition(pos)
	}
}

func (e *SingleVenueSpotPerpExecutor) flattenUntrackedOrphan(
	ctx context.Context,
	venueName, symbol string,
	spotQty, perpAmt float64,
	live bool,
) {
	v := e.venues[venueName]
	tr, ok := v.(singleVenueSpotPerpTrader)
	if !ok || v == nil {
		return
	}
	sym := venue.Symbol(symbol)
	takerBps := svspTakerSlippageBps(e.execCfg())
	if spotQty > executionQtyTolerance && absQty(perpAmt) <= executionQtyTolerance {
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketSpot, sym, venue.SideSell, spotQty, svspLegClientID("orph", "s"), false, live, takerBps)
		}
	}
	if absQty(perpAmt) > executionQtyTolerance && spotQty <= executionQtyTolerance {
		if live {
			_, _ = e.placeSVSPLegIOC(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, absQty(perpAmt), svspLegClientID("orph", "p"), true, live, takerBps)
		}
	}
}
