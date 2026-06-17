package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

// cross_venue_spot_perp_executor.go 跨所 spot-perp 正费率执行器(shadow 起步, live 闸透传)。

const (
	cvspConfirmTimeout = 8 * time.Second
	cvspConfirmPoll    = 400 * time.Millisecond
)

type cvspTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

// CrossVenueSpotPerpExecutor 跨所 spot-perp 执行引擎(A 所买现货 + B 所空永续)。
type CrossVenueSpotPerpExecutor struct {
	cfg       EngineConfig
	venues    map[string]venue.Venue
	store     *crossVenueSpotPerpPositionStore
	logger    *log.Logger
	recordDir string

	mu        sync.RWMutex
	positions []CrossVenueSpotPerpPosition
	nowFn     func() time.Time

	rollbackBlockedMu sync.Mutex
	rollbackBlocked   map[string]time.Time
}

// NewCrossVenueSpotPerpExecutor 构造执行引擎;默认 shadow,不下真单。
func NewCrossVenueSpotPerpExecutor(cfg EngineConfig, venues map[string]venue.Venue, recordDir string, logger *log.Logger) *CrossVenueSpotPerpExecutor {
	if logger == nil {
		logger = log.Default()
	}
	if recordDir == "" {
		recordDir = strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	}
	if recordDir == "" {
		recordDir = "records"
	}
	store, err := newCrossVenueSpotPerpPositionStore(recordDir)
	if err != nil && logger != nil {
		logger.Printf("[跨所spot-perp] 持仓 store 初始化失败: %v", err)
	}
	e := &CrossVenueSpotPerpExecutor{
		cfg:             cfg,
		venues:          venues,
		store:           store,
		logger:          logger,
		recordDir:       recordDir,
		nowFn:           time.Now,
		rollbackBlocked: make(map[string]time.Time),
	}
	if store != nil {
		if ps, lerr := store.load(); lerr == nil {
			e.positions = ps
		}
	}
	return e
}

func cvspIdemPrefix(sym venue.Symbol, perpVenue string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("cvsp-%s-%s-%d", base, perpVenue, time.Now().UTC().Unix())
}

func cvspLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

func cvspTakerSlippageBps(cfg core.CrossVenueExecutorConfig) float64 {
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

func parseCrossVenueSpotPerpLegs(opp UnifiedOpportunity) (spotVenue, perpVenue string, qty, notional float64, err error) {
	if opp.StrategyType != "" && opp.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
		return "", "", 0, 0, fmt.Errorf("strategy_type=%s 非 cross_venue_spot_perp", opp.StrategyType)
	}
	for _, leg := range opp.Legs {
		market := strings.ToLower(strings.TrimSpace(leg.Market))
		side := strings.ToLower(strings.TrimSpace(leg.Side))
		vname := strings.TrimSpace(leg.Venue)
		switch {
		case market == "spot" && side == "buy":
			spotVenue = vname
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		case market == "perp" && (side == "short" || side == "sell"):
			perpVenue = vname
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		}
	}
	if spotVenue == "" || perpVenue == "" {
		return "", "", 0, 0, fmt.Errorf("缺少 spot buy 或 perp short 腿")
	}
	if qty <= executionQtyTolerance {
		return "", "", 0, 0, fmt.Errorf("qty 无效")
	}
	return spotVenue, perpVenue, qty, notional, nil
}

func (e *CrossVenueSpotPerpExecutor) persistPosition(pos CrossVenueSpotPerpPosition) error {
	if e.store == nil {
		return fmt.Errorf("store 未初始化")
	}
	if err := e.store.upsert(pos); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	found := false
	for i := range e.positions {
		if cvspSamePosition(e.positions[i], pos) {
			e.positions[i] = pos
			found = true
			break
		}
	}
	if !found {
		e.positions = append(e.positions, pos)
	}
	return nil
}

func (e *CrossVenueSpotPerpExecutor) removePosition(pos CrossVenueSpotPerpPosition) error {
	if e.store == nil {
		return nil
	}
	key := cvspPositionKey(pos)
	if err := e.store.remove(key); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]CrossVenueSpotPerpPosition, 0, len(e.positions))
	for _, p := range e.positions {
		if cvspPositionKey(p) != key {
			out = append(out, p)
		}
	}
	e.positions = out
	return nil
}

func (e *CrossVenueSpotPerpExecutor) snapshotPositions() []CrossVenueSpotPerpPosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]CrossVenueSpotPerpPosition, len(e.positions))
	copy(out, e.positions)
	return out
}

func (e *CrossVenueSpotPerpExecutor) notifyCritical(title, msg, symbol string) {
	if e.logger != nil {
		e.logger.Printf("[CRITICAL][跨所spot-perp] %s: %s (%s)", title, msg, symbol)
	}
}

func (e *CrossVenueSpotPerpExecutor) isSymbolRollbackBlocked(symbol string) bool {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	until, ok := e.rollbackBlocked[symbol]
	return ok && e.nowFn().Before(until)
}

func (e *CrossVenueSpotPerpExecutor) blockSymbolNewOpen(symbol string) {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	e.rollbackBlocked[symbol] = e.nowFn().Add(rollbackVerifyBackoff)
}

func (e *CrossVenueSpotPerpExecutor) confirmFill(ctx context.Context, tr cvspTrader, mkt venue.Market, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(cvspConfirmTimeout)
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
		case <-time.After(cvspConfirmPoll):
		}
	}
}

// placeSpotPerpIOC 统一下单入口;!live 时返回合成成交,绝不 PlaceOrder。
func (e *CrossVenueSpotPerpExecutor) placeSpotPerpIOC(
	ctx context.Context,
	tr cvspTrader,
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

func (e *CrossVenueSpotPerpExecutor) placeSpotPerpLegExec(
	ctx context.Context,
	tr cvspTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	live bool,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, cvspTakerSlippageBps(e.cfg.CrossVenueExecutor))
	return e.placeSpotPerpIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, live)
}

// executeOpen 先开 B 所空永续,再开 A 所买现货;第二腿失败回滚第一腿。
func (e *CrossVenueSpotPerpExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (CrossVenueSpotPerpPosition, error) {
	spotVenue, perpVenue, qty, notional, err := parseCrossVenueSpotPerpLegs(opp)
	if err != nil {
		return CrossVenueSpotPerpPosition{}, err
	}
	if e.isSymbolRollbackBlocked(opp.Symbol) {
		return CrossVenueSpotPerpPosition{}, fmt.Errorf("%s 回滚未卸净,暂禁新开", opp.Symbol)
	}
	spotV := e.venues[spotVenue]
	perpV := e.venues[perpVenue]
	spotTrader, _ := spotV.(cvspTrader)
	perpTrader, _ := perpV.(cvspTrader)
	if spotV == nil || perpV == nil || spotTrader == nil || perpTrader == nil {
		return CrossVenueSpotPerpPosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	sym := venue.Symbol(opp.Symbol)
	idem := cvspIdemPrefix(sym, perpVenue)
	now := e.nowFn()

	pos := CrossVenueSpotPerpPosition{
		Symbol:              opp.Symbol,
		SpotVenue:           spotVenue,
		PerpVenue:           perpVenue,
		OpportunityID:       opp.ID,
		ExpectedFundingUSDT: opp.ExpectedFundingUSDT,
		ExpectedNetEVUSDT:   opp.ExpectedNetEVUSDT,
		ExpectedHoldHours:   opp.ExpectedHoldHours,
		Status:              CVSPStatusOpeningFirstLeg,
		TransitionState:     "open",
		OpenedAt:            now,
		UpdatedAt:           now,
		IdemPrefix:          idem,
		NotionalUSDT:        notional,
		SpotLeg:             CrossVenueSpotPerpLegState{Venue: spotVenue, Market: "spot", Side: "buy"},
		PerpLeg:             CrossVenueSpotPerpLegState{Venue: perpVenue, Market: "perp", Side: "short"},
	}

	if !live {
		e.logger.Printf("[影子] 本应开仓 %s: A所(%s)买现货/B所(%s)空永续 各≈%.6f 名义≈%.2fU EV≈%.4f",
			opp.Symbol, spotVenue, perpVenue, qty, notional, opp.ExpectedNetEVUSDT)
		bookSpot, _ := spotTrader.FetchBookTicker(ctx, venue.MarketSpot, sym)
		bookPerp, _ := perpTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
		pos.Status = CVSPStatusOpen
		pos.TransitionState = "hold"
		pos.SpotLeg.Qty = qty
		pos.PerpLeg.Qty = qty
		if bookSpot.AskPrice > 0 {
			pos.SpotLeg.EntryPrice = bookSpot.AskPrice
		}
		if bookPerp.BidPrice > 0 {
			pos.PerpLeg.EntryPrice = bookPerp.BidPrice
		}
		if pos.NotionalUSDT <= 0 && pos.SpotLeg.EntryPrice > 0 {
			pos.NotionalUSDT = qty * pos.SpotLeg.EntryPrice
		}
		pos.UpdatedAt = e.nowFn()
		return pos, nil
	}

	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}

	perpID := cvspLegClientID(idem, "p1")
	perpRes, err := e.placeSpotPerpLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideSell, qty, perpID, false, live)
	if err != nil || perpRes.ExecutedQty <= executionQtyTolerance {
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live)
		}
		return pos, fmt.Errorf("第一腿(空永续)失败: %w", err)
	}
	pos.PerpLeg.Qty = perpRes.ExecutedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
	pos.Status = CVSPStatusOpeningSecondLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	if _, err := spotTrader.FetchBookTicker(ctx, venue.MarketSpot, sym); err != nil {
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, pos.PerpLeg.Qty, live)
		return pos, fmt.Errorf("第二腿预检失败: %w", err)
	}

	spotID := cvspLegClientID(idem, "s1")
	hedgeQty := perpRes.ExecutedQty
	spotRes, spotErr := e.placeSpotPerpLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideBuy, hedgeQty, spotID, false, live)
	if orderStatusIsUnknown(spotRes) {
		e.notifyCritical("跨所spot-perp查单未知", fmt.Sprintf("%s 现货腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("现货腿状态未知,禁止自动回滚: %w", spotErr)
	}
	if spotErr != nil || !hedgeLegFullyFilled(spotRes.ExecutedQty, hedgeQty) {
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live)
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, spotTrader, sym, spotRes.ExecutedQty, live)
		}
		return pos, fmt.Errorf("第二腿(买现货)未足额成交: %w", spotErr)
	}
	if crossVenueLegMismatch(spotRes.ExecutedQty, perpRes.ExecutedQty) > executionQtyTolerance {
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live)
		return pos, fmt.Errorf("跨所腿量不匹配 spot=%.6f perp=%.6f", spotRes.ExecutedQty, perpRes.ExecutedQty)
	}

	pos.SpotLeg.Qty = spotRes.ExecutedQty
	pos.SpotLeg.EntryPrice = spotRes.AvgPrice
	pos.SpotLeg.OrderIDs = []string{spotID}
	bookQty := spotRes.ExecutedQty
	pos.SpotLeg.Qty = bookQty
	pos.PerpLeg.Qty = bookQty
	if pos.NotionalUSDT <= 0 {
		ref := spotRes.AvgPrice
		if ref <= 0 {
			ref = perpRes.AvgPrice
		}
		pos.NotionalUSDT = bookQty * ref
	}
	pos.Status = CVSPStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[跨所spot-perp] 开仓完成 %s: 现货%s/永续%s 各≈%.6f", opp.Symbol, spotVenue, perpVenue, bookQty)
	return pos, nil
}

func (e *CrossVenueSpotPerpExecutor) rollbackFirstLeg(ctx context.Context, pos CrossVenueSpotPerpPosition, perpV venue.Venue, tr cvspTrader, sym venue.Symbol, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	id := cvspLegClientID(pos.IdemPrefix, "rb")
	res, err := e.placeSpotPerpLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, id, true, live)
	if err != nil {
		e.logger.Printf("[跨所spot-perp] 回滚第一腿失败 %s: %v", sym, err)
		e.notifyCritical("跨所spot-perp回滚失败", fmt.Sprintf("%s 永续 reduce-only 回滚失败: %v", sym, err), string(sym))
	} else if res.ExecutedQty > executionQtyTolerance {
		pos.RollbackCostUSDT += res.AvgPrice * res.ExecutedQty * 0.001
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
	}
	e.verifyRollbackCleared(ctx, perpV, sym, pos.Symbol)
}

func (e *CrossVenueSpotPerpExecutor) rollbackSpotLeg(ctx context.Context, tr cvspTrader, sym venue.Symbol, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	_, _ = e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, "cvsp-rbs", false, live)
}

func (e *CrossVenueSpotPerpExecutor) verifyRollbackCleared(ctx context.Context, perpV venue.Venue, sym venue.Symbol, symbol string) {
	perpQty := 0.0
	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpQty = absQty(perpPositionQty(pp, sym))
		}
	}
	if perpQty <= executionQtyTolerance {
		return
	}
	e.notifyCritical("跨所spot-perp回滚未卸净",
		fmt.Sprintf("%s perp=%.6f,阻塞新开并退避重试", symbol, perpQty), symbol)
	e.blockSymbolNewOpen(symbol)
}

func (e *CrossVenueSpotPerpExecutor) closePosition(ctx context.Context, pos CrossVenueSpotPerpPosition, live bool, reason string) error {
	pos.Status = CVSPStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	pos.TransitionState = "close"
	_ = e.persistPosition(pos)

	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	spotTrader, _ := spotV.(cvspTrader)
	perpTrader, _ := perpV.(cvspTrader)
	if spotTrader == nil || perpTrader == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}

	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 现货腿 %s 卖出 %.6f 原因:%s", pos.Symbol, pos.SpotVenue, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 永续腿 %s 买回 %.6f 原因:%s", pos.Symbol, pos.PerpVenue, pos.PerpLeg.Qty, reason)
		}
		pos.Status = CVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return nil
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts

	spotQty := 0.0
	perpQty := 0.0
	spotFetchErr := error(nil)
	perpFetchErr := error(nil)
	if spotV != nil {
		if bal, err := spotV.FetchBalances(ctx); err == nil {
			spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
		} else {
			spotFetchErr = err
		}
	}
	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpQty = absQty(perpPositionQty(pp, sym))
		} else {
			perpFetchErr = err
		}
	}
	closePerpQty := 0.0
	closeSpotQty := 0.0
	if spotFetchErr != nil || perpFetchErr != nil {
		e.logger.Printf("[跨所spot-perp] 平仓 %s 拉腿失败,跳过本轮(保留台账): spot=%v perp=%v", pos.Symbol, spotFetchErr, perpFetchErr)
	} else {
		if pos.SpotLeg.Qty > executionQtyTolerance && spotQty <= executionQtyTolerance {
			e.logger.Printf("[跨所spot-perp] 平仓 %s 现货实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance && perpQty <= executionQtyTolerance {
			e.logger.Printf("[跨所spot-perp] 平仓 %s 永续实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		closePerpQty = ledgerCappedCloseQty(pos.PerpLeg.Qty, perpQty)
		closeSpotQty = ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
	}
	if closePerpQty > executionQtyTolerance {
		id := cvspLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placeSpotPerpLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideBuy, closePerpQty, id, true, live); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
		}
	}
	if closeSpotQty > executionQtyTolerance {
		id := cvspLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeSpotPerpLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideSell, closeSpotQty, id, false, live); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
		}
	}

	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpQty = absQty(perpPositionQty(pp, sym))
		}
	}
	if spotV != nil {
		if bal, err := spotV.FetchBalances(ctx); err == nil {
			spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
		}
	}

	if spotQty <= executionQtyTolerance && perpQty <= executionQtyTolerance {
		if pos.SpotLeg.Qty <= executionQtyTolerance && pos.PerpLeg.Qty <= executionQtyTolerance {
			pos.Status = CVSPStatusClosed
			pos.UpdatedAt = e.nowFn()
			e.finalizeClosedPosition(pos)
			return nil
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		return fmt.Errorf("平仓跳过: %s 实测为0,保留台账待下轮", pos.Symbol)
	}

	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	return fmt.Errorf("平仓未完成: %s 第 %d 轮仍有剩余腿", pos.Symbol, attempt)
}

func (e *CrossVenueSpotPerpExecutor) finalizeClosedPosition(pos CrossVenueSpotPerpPosition) {
	if e.store != nil {
		rec := buildCrossVenueSpotPerpSettlement(pos, e.nowFn())
		_ = e.store.appendSettlement(rec)
	}
	_ = e.removePosition(pos)
}

func cvspSpotHoldingQty(bal venue.Balances, symbol string) float64 {
	base := baseAssetFromCanonicalSymbol(symbol)
	return spotLongQtyFromBalance(bal, base)
}

func (e *CrossVenueSpotPerpExecutor) shouldClose(pos *CrossVenueSpotPerpPosition) string {
	if pos == nil {
		return ""
	}
	cfg := e.cfg.CrossVenueExecutor
	now := e.nowFn()
	maxHold := time.Duration(cfg.MaxHoldHours) * time.Hour
	if maxHold <= 0 {
		maxHold = 72 * time.Hour
	}
	if now.Sub(pos.OpenedAt) > maxHold {
		return fmt.Sprintf("持仓超过 %dh", cfg.MaxHoldHours)
	}
	holdHours := now.Sub(pos.OpenedAt).Hours()
	if pos.ExpectedHoldHours > 0 && holdHours >= pos.ExpectedHoldHours && pos.ExpectedNetEVUSDT <= 0 {
		return fmt.Sprintf("持有期满且 EV<=0(%.4f)", pos.ExpectedNetEVUSDT)
	}
	return ""
}

func (e *CrossVenueSpotPerpExecutor) managePosition(ctx context.Context, pos CrossVenueSpotPerpPosition, live bool) (CrossVenueSpotPerpPosition, bool) {
	if pos.Status == CVSPStatusClosing {
		err := e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, err == nil
	}

	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		return pos, false
	}

	spotQty := 0.0
	perpQty := 0.0
	if bal, err := spotV.FetchBalances(ctx); err == nil {
		spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
	}
	if pp, err := perpV.FetchPositions(ctx); err == nil {
		perpQty = absQty(perpPositionQty(pp, sym))
	}
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpQty > executionQtyTolerance

	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpQty)
		return pos, true
	}
	if !spotOpen && !perpOpen {
		pos.Status = CVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return pos, true
	}

	pos.EstFundingUSDT = e.estimateFundingAccrual(&pos)
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	if reason := e.shouldClose(&pos); reason != "" {
		_ = e.closePosition(ctx, pos, live, reason)
		return pos, true
	}
	return pos, false
}

func (e *CrossVenueSpotPerpExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeCrossVenueSpotPerpPositions(e.snapshotPositions()) {
		updated, _ := e.managePosition(ctx, pos, live)
		_ = e.persistPosition(updated)
	}
}

func (e *CrossVenueSpotPerpExecutor) estimateFundingAccrual(pos *CrossVenueSpotPerpPosition) float64 {
	if pos == nil {
		return 0
	}
	now := e.nowFn()
	last := pos.LastAccrualAt
	if last.IsZero() {
		last = pos.OpenedAt
	}
	elapsedHours := now.Sub(last).Hours()
	if elapsedHours <= 0 || pos.PerpFundingRate <= 0 {
		return pos.EstFundingUSDT
	}
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	delta := notional * pos.PerpFundingRate * elapsedHours / 8
	pos.LastAccrualAt = now
	return pos.EstFundingUSDT + delta
}

func (e *CrossVenueSpotPerpExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenueSpotPerpPosition, spotOpen, perpOpen, live bool, spotQty, perpQty float64) {
	pos.Status = CVSPStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	sym := venue.Symbol(pos.Symbol)
	if perpOpen && !spotOpen {
		if tr, ok := e.venues[pos.PerpVenue].(cvspTrader); ok && live {
			id := cvspLegClientID(pos.IdemPrefix, "op")
			if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, absQty(perpQty), id, true, live); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	} else if spotOpen && !perpOpen {
		if tr, ok := e.venues[pos.SpotVenue].(cvspTrader); ok && live {
			id := cvspLegClientID(pos.IdemPrefix, "os")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
			if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, id, false, live); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	}
	e.notifyCritical("跨所spot-perp孤腿", fmt.Sprintf("%s 检测到孤腿,已尝试平掉剩余腿", pos.Symbol), pos.Symbol)
}

// CloseOnePositionVerified 核对交易所真实持仓后平仓:无真实腿只清台账;有真实腿则 live 平仓。
func (e *CrossVenueSpotPerpExecutor) CloseOnePositionVerified(ctx context.Context, opportunityID, symbol, reason string) error {
	if e == nil {
		return fmt.Errorf("跨所spot-perp 执行器未初始化")
	}
	pos, ok := findCrossVenueSpotPerpHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
	if !ok {
		return fmt.Errorf("跨所spot-perp 持仓未找到 opp=%s", opportunityID)
	}
	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		return fmt.Errorf("%s: venue 未就绪", pos.Symbol)
	}
	spotQty := 0.0
	perpQty := 0.0
	if bal, berr := spotV.FetchBalances(ctx); berr == nil {
		spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
	}
	if pp, perr := perpV.FetchPositions(ctx); perr == nil {
		perpQty = absQty(perpPositionQty(pp, sym))
	}
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpQty > executionQtyTolerance
	if !spotOpen && !perpOpen {
		pos.Status = CVSPStatusClosed
		if pos.CloseReason == "" {
			pos.CloseReason = reason
		}
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return nil
	}
	return e.closePosition(ctx, pos, true, reason)
}

// CountRealHeldPositions 统计台账中有交易所真实敞口的持仓数。
func (e *CrossVenueSpotPerpExecutor) CountRealHeldPositions(ctx context.Context) int {
	if e == nil || e.store == nil {
		return 0
	}
	ps, err := e.store.load()
	if err != nil {
		return 0
	}
	count := 0
	for _, pos := range activeCrossVenueSpotPerpPositions(ps) {
		sym := venue.Symbol(pos.Symbol)
		spotV := e.venues[pos.SpotVenue]
		perpV := e.venues[pos.PerpVenue]
		if spotV == nil || perpV == nil {
			continue
		}
		spotQty := 0.0
		perpQty := 0.0
		if bal, berr := spotV.FetchBalances(ctx); berr == nil {
			spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
		}
		if pp, perr := perpV.FetchPositions(ctx); perr == nil {
			perpQty = absQty(perpPositionQty(pp, sym))
		}
		if spotQty > executionQtyTolerance || perpQty > executionQtyTolerance {
			count++
		}
	}
	return count
}

// CloseAllPositions 一键全平:跨所 FetchBalances/FetchPositions 对账后只平真实腿;合成记录仅清台账。
func (e *CrossVenueSpotPerpExecutor) CloseAllPositions(ctx context.Context, live bool) (closed int, errs []error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	ps, err := e.store.load()
	if err != nil {
		return 0, []error{err}
	}
	for _, pos := range activeCrossVenueSpotPerpPositions(ps) {
		sym := venue.Symbol(pos.Symbol)
		spotV := e.venues[pos.SpotVenue]
		perpV := e.venues[pos.PerpVenue]
		if spotV == nil || perpV == nil {
			errs = append(errs, fmt.Errorf("%s: venue 未就绪", pos.Symbol))
			continue
		}
		spotQty := 0.0
		perpQty := 0.0
		if bal, berr := spotV.FetchBalances(ctx); berr == nil {
			spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
		}
		if pp, perr := perpV.FetchPositions(ctx); perr == nil {
			perpQty = absQty(perpPositionQty(pp, sym))
		}
		spotOpen := spotQty > executionQtyTolerance
		perpOpen := perpQty > executionQtyTolerance
		if !spotOpen && !perpOpen {
			pos.Status = CVSPStatusClosed
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

// reconcileOnStart 载入持仓、跨两所 FetchPositions/FetchBalances 对账;仅处理本策略 store 登记的持仓。
// 绝不触碰非本策略持仓(尤其 perp-perp 永续仓);未登记交易所敞口一律忽略、绝不下单。
func (e *CrossVenueSpotPerpExecutor) reconcileOnStart(ctx context.Context, live bool) {
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[跨所spot-perp] reconcile 读台账失败: %v", err)
		return
	}
	active := activeCrossVenueSpotPerpPositions(ps)
	if len(active) > 0 {
		e.logger.Printf("[跨所spot-perp] reconcile: %d 笔落盘持仓", len(active))
		for _, p := range active {
			e.reconcileTrackedPosition(ctx, p, live)
		}
	}

	for _, p := range activeCrossVenueSpotPerpPositions(ps) {
		if p.Status == CVSPStatusOpeningFirstLeg || p.Status == CVSPStatusOpeningSecondLeg {
			e.logger.Printf("[跨所spot-perp] reconcile: %s 处于中间态 %s,尝试恢复", p.Symbol, p.Status)
			if live {
				_, _ = e.managePosition(ctx, p, live)
			}
		}
	}

	if latest, lerr := e.store.load(); lerr == nil {
		ps = latest
	}
	e.mu.Lock()
	e.positions = ps
	e.mu.Unlock()
}

func (e *CrossVenueSpotPerpExecutor) reconcileTrackedPosition(ctx context.Context, pos CrossVenueSpotPerpPosition, live bool) {
	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		e.logger.Printf("[跨所spot-perp] reconcile:%s venue 未就绪,保留台账", pos.Symbol)
		return
	}
	spotQty := 0.0
	perpQty := 0.0
	if bal, err := spotV.FetchBalances(ctx); err == nil {
		spotQty = cvspSpotHoldingQty(bal, pos.Symbol)
	}
	if pp, err := perpV.FetchPositions(ctx); err == nil {
		perpQty = absQty(perpPositionQty(pp, sym))
	}
	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpQty > executionQtyTolerance
	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotQty, perpQty)
	}
}

// Positions 返回当前持仓快照(测试/面板用)。
func (e *CrossVenueSpotPerpExecutor) Positions() []CrossVenueSpotPerpPosition {
	return e.snapshotPositions()
}

// SettlementsPath 返回结算文件路径(测试用)。
func (e *CrossVenueSpotPerpExecutor) SettlementsPath() string {
	if e.store == nil {
		return ""
	}
	return filepath.Join(e.store.dir, crossVenueSpotPerpSettlementsFile)
}

// ReconcileOnStart 启动时载入台账并对账(统一轮动 runner 调用;live 由 runner.isLive() 传入)。
func (e *CrossVenueSpotPerpExecutor) ReconcileOnStart(ctx context.Context, live bool) {
	e.reconcileOnStart(ctx, live)
}

// ManageOpenPositions 管理所有在场持仓(统一轮动 runner 调用)。
func (e *CrossVenueSpotPerpExecutor) ManageOpenPositions(ctx context.Context, live bool) {
	e.manageOpenPositions(ctx, live)
}
