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

// cross_venue_perp_spot_executor.go 跨所 perp-spot 负费率执行器(A 所借币空现货 + B 所多永续, shadow 起步, live 闸透传)。

const (
	cvpsConfirmTimeout = 8 * time.Second
	cvpsConfirmPoll    = 400 * time.Millisecond
)

type cvpsTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

// CrossVenuePerpSpotExecutor 跨所 perp-spot 负 carry 执行引擎。
type CrossVenuePerpSpotExecutor struct {
	cfg       EngineConfig
	venues    map[string]venue.Venue
	store     *crossVenuePerpSpotPositionStore
	logger    *log.Logger
	recordDir string

	mu        sync.RWMutex
	positions []CrossVenuePerpSpotPosition
	nowFn     func() time.Time

	rollbackBlockedMu sync.Mutex
	rollbackBlocked   map[string]time.Time
}

// NewCrossVenuePerpSpotExecutor 构造执行引擎;默认 shadow,不下真单。
func NewCrossVenuePerpSpotExecutor(cfg EngineConfig, venues map[string]venue.Venue, recordDir string, logger *log.Logger) *CrossVenuePerpSpotExecutor {
	if logger == nil {
		logger = log.Default()
	}
	if recordDir == "" {
		recordDir = strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	}
	if recordDir == "" {
		recordDir = "records"
	}
	store, err := newCrossVenuePerpSpotPositionStore(recordDir)
	if err != nil && logger != nil {
		logger.Printf("[跨所perp-spot] 持仓 store 初始化失败: %v", err)
	}
	e := &CrossVenuePerpSpotExecutor{
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

func cvpsIdemPrefix(sym venue.Symbol, spotVenue string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("cvps-%s-%s-%d", base, spotVenue, time.Now().UTC().Unix())
}

func cvpsLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

func cvpsTakerSlippageBps(cfg core.CrossVenueExecutorConfig) float64 {
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

func parseCrossVenuePerpSpotLegs(opp UnifiedOpportunity) (spotVenue, perpVenue string, qty, notional float64, err error) {
	if opp.StrategyType != "" && opp.StrategyType != UnifiedStrategyCrossVenuePerpSpot {
		return "", "", 0, 0, fmt.Errorf("strategy_type=%s 非 cross_venue_perp_spot", opp.StrategyType)
	}
	for _, leg := range opp.Legs {
		market := strings.ToLower(strings.TrimSpace(leg.Market))
		side := strings.ToLower(strings.TrimSpace(leg.Side))
		vname := strings.TrimSpace(leg.Venue)
		switch {
		case market == "spot" && (side == "short" || side == "sell"):
			spotVenue = vname
			if leg.Qty > qty {
				qty = leg.Qty
			}
			if leg.Notional > notional {
				notional = leg.Notional
			}
		case market == "perp" && (side == "long" || side == "buy"):
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
		return "", "", 0, 0, fmt.Errorf("缺少 spot short 或 perp long 腿")
	}
	if qty <= executionQtyTolerance {
		return "", "", 0, 0, fmt.Errorf("qty 无效")
	}
	return spotVenue, perpVenue, qty, notional, nil
}

// checkNegativeCarrySupported 借币可得性闸:现货所在 venue 必须 SupportsNegativeCarry。
func (e *CrossVenuePerpSpotExecutor) checkNegativeCarrySupported(v venue.Venue, venueName string) bool {
	if v == nil {
		e.logger.Printf("[跨所perp-spot] venue %s 未就绪,放弃机会", venueName)
		return false
	}
	if !v.Capabilities().SupportsNegativeCarry {
		e.logger.Printf("[跨所perp-spot] %s 不支持借币做空(SupportsNegativeCarry=false),放弃机会", venueName)
		return false
	}
	return true
}

func (e *CrossVenuePerpSpotExecutor) persistPosition(pos CrossVenuePerpSpotPosition) error {
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
		if cvpsSamePosition(e.positions[i], pos) {
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

func (e *CrossVenuePerpSpotExecutor) removePosition(pos CrossVenuePerpSpotPosition) error {
	if e.store == nil {
		return nil
	}
	key := cvpsPositionKey(pos)
	if err := e.store.remove(key); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]CrossVenuePerpSpotPosition, 0, len(e.positions))
	for _, p := range e.positions {
		if cvpsPositionKey(p) != key {
			out = append(out, p)
		}
	}
	e.positions = out
	return nil
}

func (e *CrossVenuePerpSpotExecutor) snapshotPositions() []CrossVenuePerpSpotPosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]CrossVenuePerpSpotPosition, len(e.positions))
	copy(out, e.positions)
	return out
}

func (e *CrossVenuePerpSpotExecutor) notifyCritical(title, msg, symbol string) {
	if e.logger != nil {
		e.logger.Printf("[CRITICAL][跨所perp-spot] %s: %s (%s)", title, msg, symbol)
	}
}

func (e *CrossVenuePerpSpotExecutor) isSymbolRollbackBlocked(symbol string) bool {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	until, ok := e.rollbackBlocked[symbol]
	return ok && e.nowFn().Before(until)
}

func (e *CrossVenuePerpSpotExecutor) blockSymbolNewOpen(symbol string) {
	e.rollbackBlockedMu.Lock()
	defer e.rollbackBlockedMu.Unlock()
	e.rollbackBlocked[symbol] = e.nowFn().Add(rollbackVerifyBackoff)
}

func (e *CrossVenuePerpSpotExecutor) confirmFill(ctx context.Context, tr cvpsTrader, mkt venue.Market, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.nowFn().Add(cvpsConfirmTimeout)
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
		case <-time.After(cvpsConfirmPoll):
		}
	}
}

// placePerpSpotIOC 统一下单入口;!live 时返回合成成交,绝不 PlaceOrder/BorrowRepay。
func (e *CrossVenuePerpSpotExecutor) placePerpSpotIOC(
	ctx context.Context,
	tr cvpsTrader,
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

func (e *CrossVenuePerpSpotExecutor) placePerpSpotLegExec(
	ctx context.Context,
	tr cvpsTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	marginEffect venue.MarginEffect,
	live bool,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, cvpsTakerSlippageBps(e.cfg.CrossVenueExecutor))
	return e.placePerpSpotIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live)
}

// executeOpen 先开 A 所借币空现货,再开 B 所多永续;第二腿失败回滚第一腿(买回+还币)。
func (e *CrossVenuePerpSpotExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (CrossVenuePerpSpotPosition, error) {
	spotVenue, perpVenue, qty, notional, err := parseCrossVenuePerpSpotLegs(opp)
	if err != nil {
		return CrossVenuePerpSpotPosition{}, err
	}
	if e.isSymbolRollbackBlocked(opp.Symbol) {
		return CrossVenuePerpSpotPosition{}, fmt.Errorf("%s 回滚未卸净,暂禁新开", opp.Symbol)
	}
	spotV := e.venues[spotVenue]
	perpV := e.venues[perpVenue]
	if !e.checkNegativeCarrySupported(spotV, spotVenue) {
		return CrossVenuePerpSpotPosition{}, fmt.Errorf("spot venue %s 不支持负 carry 借币", spotVenue)
	}
	spotTrader, _ := spotV.(cvpsTrader)
	perpTrader, _ := perpV.(cvpsTrader)
	if spotV == nil || perpV == nil || spotTrader == nil || perpTrader == nil {
		return CrossVenuePerpSpotPosition{}, fmt.Errorf("venue 不满足交易接口")
	}
	sym := venue.Symbol(opp.Symbol)
	idem := cvpsIdemPrefix(sym, spotVenue)
	now := e.nowFn()

	pos := CrossVenuePerpSpotPosition{
		Symbol:              opp.Symbol,
		SpotVenue:           spotVenue,
		PerpVenue:           perpVenue,
		OpportunityID:       opp.ID,
		ExpectedFundingUSDT: opp.ExpectedFundingUSDT,
		ExpectedNetEVUSDT:   opp.ExpectedNetEVUSDT,
		ExpectedHoldHours:   opp.ExpectedHoldHours,
		Status:              CVPSStatusOpeningFirstLeg,
		TransitionState:     "open",
		OpenedAt:            now,
		UpdatedAt:           now,
		IdemPrefix:          idem,
		NotionalUSDT:        notional,
		SpotLeg:             CrossVenuePerpSpotLegState{Venue: spotVenue, Market: "spot", Side: "short"},
		PerpLeg:             CrossVenuePerpSpotLegState{Venue: perpVenue, Market: "perp", Side: "long"},
	}

	if !live {
		e.logger.Printf("[影子] 本应开仓 %s: A所(%s)借币空现货/B所(%s)多永续 各≈%.6f 名义≈%.2fU EV≈%.4f",
			opp.Symbol, spotVenue, perpVenue, qty, notional, opp.ExpectedNetEVUSDT)
		bookSpot, _ := spotTrader.FetchBookTicker(ctx, venue.MarketSpot, sym)
		bookPerp, _ := perpTrader.FetchBookTicker(ctx, venue.MarketPerp, sym)
		pos.Status = CVPSStatusOpen
		pos.TransitionState = "hold"
		pos.SpotLeg.Qty = qty
		pos.PerpLeg.Qty = qty
		if bookSpot.BidPrice > 0 {
			pos.SpotLeg.EntryPrice = bookSpot.BidPrice
		}
		if bookPerp.AskPrice > 0 {
			pos.PerpLeg.EntryPrice = bookPerp.AskPrice
		}
		if pos.NotionalUSDT <= 0 && pos.SpotLeg.EntryPrice > 0 {
			pos.NotionalUSDT = qty * pos.SpotLeg.EntryPrice
		}
		pos.UpdatedAt = e.nowFn()
		return pos, nil
	}

	base := baseAssetFromCanonicalSymbol(opp.Symbol)
	maxBorrow, merr := spotV.MaxBorrowable(ctx, base)
	if merr != nil {
		e.logger.Printf("[跨所perp-spot] %s MaxBorrowable 查询失败: %v,放弃", opp.Symbol, merr)
		return pos, fmt.Errorf("借币额度查询失败: %w", merr)
	}
	if maxBorrow+executionQtyTolerance < qty {
		e.logger.Printf("[跨所perp-spot] %s 借币额度不足 max=%.6f need=%.6f,放弃机会", opp.Symbol, maxBorrow, qty)
		return pos, fmt.Errorf("借币额度不足: max=%.6f need=%.6f", maxBorrow, qty)
	}

	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}

	spotID := cvpsLegClientID(idem, "s1")
	spotRes, err := e.placePerpSpotLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideSell, qty, spotID, false, venue.MarginEffectBorrow, live)
	if orderStatusIsUnknown(spotRes) {
		e.notifyCritical("跨所perp-spot查单未知", fmt.Sprintf("%s 现货借币腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("现货腿状态未知,禁止自动回滚: %w", err)
	}
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live)
		}
		return pos, fmt.Errorf("第一腿(借币空现货)失败: %w", err)
	}
	pos.SpotLeg.Qty = spotRes.ExecutedQty
	pos.SpotLeg.EntryPrice = spotRes.AvgPrice
	pos.SpotLeg.OrderIDs = []string{spotID}
	pos.Status = CVPSStatusOpeningSecondLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	if _, err := perpTrader.FetchBookTicker(ctx, venue.MarketPerp, sym); err != nil {
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, pos.SpotLeg.Qty, live)
		return pos, fmt.Errorf("第二腿预检失败: %w", err)
	}

	perpID := cvpsLegClientID(idem, "p1")
	hedgeQty := spotRes.ExecutedQty
	perpRes, perpErr := e.placePerpSpotLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideBuy, hedgeQty, perpID, false, venue.MarginEffectNone, live)
	if orderStatusIsUnknown(perpRes) {
		e.notifyCritical("跨所perp-spot查单未知", fmt.Sprintf("%s 永续腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("永续腿状态未知,禁止自动回滚: %w", perpErr)
	}
	if perpErr != nil || !hedgeLegFullyFilled(perpRes.ExecutedQty, hedgeQty) {
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live)
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackPerpLeg(ctx, perpTrader, sym, perpRes.ExecutedQty, live)
		}
		return pos, fmt.Errorf("第二腿(多永续)未足额成交: %w", perpErr)
	}
	if crossVenueLegMismatch(spotRes.ExecutedQty, perpRes.ExecutedQty) > executionQtyTolerance {
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live)
		return pos, fmt.Errorf("跨所腿量不匹配 spot=%.6f perp=%.6f", spotRes.ExecutedQty, perpRes.ExecutedQty)
	}

	pos.PerpLeg.Qty = perpRes.ExecutedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
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
	pos.Status = CVPSStatusOpen
	pos.TransitionState = "hold"
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	e.logger.Printf("[跨所perp-spot] 开仓完成 %s: 现货%s/永续%s 各≈%.6f", opp.Symbol, spotVenue, perpVenue, bookQty)
	return pos, nil
}

func (e *CrossVenuePerpSpotExecutor) rollbackFirstLeg(ctx context.Context, pos CrossVenuePerpSpotPosition, spotV venue.Venue, tr cvpsTrader, sym venue.Symbol, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	id := cvpsLegClientID(pos.IdemPrefix, "rb")
	res, err := e.placePerpSpotLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live)
	if err != nil {
		e.logger.Printf("[跨所perp-spot] 回滚第一腿失败 %s: %v", sym, err)
		e.notifyCritical("跨所perp-spot回滚失败", fmt.Sprintf("%s 现货买回还币回滚失败: %v", sym, err), string(sym))
	} else if res.ExecutedQty > executionQtyTolerance {
		pos.RollbackCostUSDT += res.AvgPrice * res.ExecutedQty * 0.001
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
	}
	e.verifyRollbackCleared(ctx, spotV, nil, sym, pos.Symbol)
}

func (e *CrossVenuePerpSpotExecutor) rollbackPerpLeg(ctx context.Context, tr cvpsTrader, sym venue.Symbol, qty float64, live bool) {
	if qty <= executionQtyTolerance {
		return
	}
	_, _ = e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, qty, "cvps-rbp", true, venue.MarginEffectNone, live)
}

func (e *CrossVenuePerpSpotExecutor) verifyRollbackCleared(ctx context.Context, spotV, perpV venue.Venue, sym venue.Symbol, symbol string) {
	spotShort := 0.0
	perpLong := 0.0
	if spotV != nil {
		if bal, err := spotV.FetchBalances(ctx); err == nil {
			spotShort = cvpsSpotShortQty(bal, symbol)
		}
	}
	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpLong = svpsPerpLongQty(pp, sym)
		}
	}
	if spotShort <= executionQtyTolerance && perpLong <= executionQtyTolerance {
		return
	}
	e.notifyCritical("跨所perp-spot回滚未卸净",
		fmt.Sprintf("%s spot空=%.6f perp多=%.6f,阻塞新开并退避重试", symbol, spotShort, perpLong), symbol)
	e.blockSymbolNewOpen(symbol)
}

func (e *CrossVenuePerpSpotExecutor) closePosition(ctx context.Context, pos CrossVenuePerpSpotPosition, live bool, reason string) error {
	pos.Status = CVPSStatusClosing
	if pos.CloseReason == "" {
		pos.CloseReason = reason
	}
	pos.UpdatedAt = e.nowFn()
	pos.TransitionState = "close"
	_ = e.persistPosition(pos)

	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	spotTrader, _ := spotV.(cvpsTrader)
	perpTrader, _ := perpV.(cvpsTrader)
	if spotTrader == nil || perpTrader == nil {
		return fmt.Errorf("venue 交易接口缺失")
	}

	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 现货腿 %s 买回还币 %.6f 原因:%s", pos.Symbol, pos.SpotVenue, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[影子] 本应平仓 %s 永续腿 %s 平多 %.6f 原因:%s", pos.Symbol, pos.PerpVenue, pos.PerpLeg.Qty, reason)
		}
		pos.Status = CVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos)
		return nil
	}

	pos.CloseAttempts++
	attempt := pos.CloseAttempts

	spotShort := 0.0
	perpLong := 0.0
	spotFetchErr := error(nil)
	perpFetchErr := error(nil)
	if spotV != nil {
		if bal, err := spotV.FetchBalances(ctx); err == nil {
			spotShort = cvpsSpotShortQty(bal, pos.Symbol)
		} else {
			spotFetchErr = err
		}
	}
	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpLong = svpsPerpLongQty(pp, sym)
		} else {
			perpFetchErr = err
		}
	}
	closeSpotQty := 0.0
	closePerpQty := 0.0
	if spotFetchErr != nil || perpFetchErr != nil {
		e.logger.Printf("[跨所perp-spot] 平仓 %s 拉腿失败,跳过本轮(保留台账): spot=%v perp=%v", pos.Symbol, spotFetchErr, perpFetchErr)
	} else {
		if pos.SpotLeg.Qty > executionQtyTolerance && spotShort <= executionQtyTolerance {
			e.logger.Printf("[跨所perp-spot] 平仓 %s 现货实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance && perpLong <= executionQtyTolerance {
			e.logger.Printf("[跨所perp-spot] 平仓 %s 永续实测为0,跳过本轮(保留台账)", pos.Symbol)
		}
		closeSpotQty = ledgerCappedCloseQty(pos.SpotLeg.Qty, spotShort)
		closePerpQty = ledgerCappedCloseQty(pos.PerpLeg.Qty, perpLong)
	}
	// 优先平现货腿(空现货逼空风险更大)。
	if closeSpotQty > executionQtyTolerance {
		id := cvpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placePerpSpotLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideBuy, closeSpotQty, id, false, venue.MarginEffectRepay, live); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
		}
	}
	if closePerpQty > executionQtyTolerance {
		id := cvpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placePerpSpotLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideSell, closePerpQty, id, true, venue.MarginEffectNone, live); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
		}
	}

	if spotV != nil {
		if bal, err := spotV.FetchBalances(ctx); err == nil {
			spotShort = cvpsSpotShortQty(bal, pos.Symbol)
		}
	}
	if perpV != nil {
		if pp, err := perpV.FetchPositions(ctx); err == nil {
			perpLong = svpsPerpLongQty(pp, sym)
		}
	}

	if spotShort <= executionQtyTolerance && perpLong <= executionQtyTolerance {
		if pos.SpotLeg.Qty <= executionQtyTolerance && pos.PerpLeg.Qty <= executionQtyTolerance {
			pos.Status = CVPSStatusClosed
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

func (e *CrossVenuePerpSpotExecutor) finalizeClosedPosition(pos CrossVenuePerpSpotPosition) {
	if e.store != nil {
		rec := buildCrossVenuePerpSpotSettlement(pos, e.nowFn())
		_ = e.store.appendSettlement(rec)
	}
	_ = e.removePosition(pos)
}

func cvpsSpotShortQty(bal venue.Balances, symbol string) float64 {
	base := baseAssetFromCanonicalSymbol(symbol)
	return marginShortQtyFromBalance(bal, base)
}

func (e *CrossVenuePerpSpotExecutor) shouldClose(pos *CrossVenuePerpSpotPosition) string {
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

func (e *CrossVenuePerpSpotExecutor) managePosition(ctx context.Context, pos CrossVenuePerpSpotPosition, live bool) (CrossVenuePerpSpotPosition, bool) {
	if pos.Status == CVPSStatusClosing {
		err := e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, err == nil
	}

	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		return pos, false
	}

	spotShort := 0.0
	perpLong := 0.0
	if bal, err := spotV.FetchBalances(ctx); err == nil {
		spotShort = cvpsSpotShortQty(bal, pos.Symbol)
	}
	if pp, err := perpV.FetchPositions(ctx); err == nil {
		perpLong = svpsPerpLongQty(pp, sym)
	}
	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance

	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotShort, perpLong)
		return pos, true
	}
	if !spotOpen && !perpOpen {
		pos.Status = CVPSStatusClosed
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

func (e *CrossVenuePerpSpotExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeCrossVenuePerpSpotPositions(e.snapshotPositions()) {
		updated, _ := e.managePosition(ctx, pos, live)
		_ = e.persistPosition(updated)
	}
}

func (e *CrossVenuePerpSpotExecutor) estimateFundingAccrual(pos *CrossVenuePerpSpotPosition) float64 {
	if pos == nil {
		return 0
	}
	now := e.nowFn()
	last := pos.LastAccrualAt
	if last.IsZero() {
		last = pos.OpenedAt
	}
	elapsedHours := now.Sub(last).Hours()
	if elapsedHours <= 0 || pos.PerpFundingRate >= 0 {
		return pos.EstFundingUSDT
	}
	notional := pos.NotionalUSDT
	if notional <= 0 {
		notional = pos.PerpLeg.Qty * pos.PerpLeg.EntryPrice
	}
	delta := notional * (-pos.PerpFundingRate) * elapsedHours / 8
	pos.LastAccrualAt = now
	return pos.EstFundingUSDT + delta
}

func (e *CrossVenuePerpSpotExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenuePerpSpotPosition, spotOpen, perpOpen, live bool, spotShort, perpLong float64) {
	pos.Status = CVPSStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	sym := venue.Symbol(pos.Symbol)
	if perpOpen && !spotOpen {
		if tr, ok := e.venues[pos.PerpVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "op")
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, perpLong, id, true, venue.MarginEffectNone, live); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	} else if spotOpen && !perpOpen {
		if tr, ok := e.venues[pos.SpotVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "os")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotShort)
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	}
	e.notifyCritical("跨所perp-spot孤腿", fmt.Sprintf("%s 检测到孤腿,已尝试平掉剩余腿", pos.Symbol), pos.Symbol)
}

// CloseOnePositionVerified 核对交易所真实持仓后平仓:无真实腿只清台账;有真实腿则 live 平仓。
func (e *CrossVenuePerpSpotExecutor) CloseOnePositionVerified(ctx context.Context, opportunityID, symbol, reason string) error {
	if e == nil {
		return fmt.Errorf("跨所perp-spot 执行器未初始化")
	}
	pos, ok := findCrossVenuePerpSpotHeldByOpportunityID(e.snapshotPositions(), opportunityID, symbol)
	if !ok {
		return fmt.Errorf("跨所perp-spot 持仓未找到 opp=%s", opportunityID)
	}
	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		return fmt.Errorf("%s: venue 未就绪", pos.Symbol)
	}
	spotShort := 0.0
	perpLong := 0.0
	if bal, berr := spotV.FetchBalances(ctx); berr == nil {
		spotShort = cvpsSpotShortQty(bal, pos.Symbol)
	}
	if pp, perr := perpV.FetchPositions(ctx); perr == nil {
		perpLong = svpsPerpLongQty(pp, sym)
	}
	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance
	if !spotOpen && !perpOpen {
		pos.Status = CVPSStatusClosed
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
func (e *CrossVenuePerpSpotExecutor) CountRealHeldPositions(ctx context.Context) int {
	if e == nil || e.store == nil {
		return 0
	}
	ps, err := e.store.load()
	if err != nil {
		return 0
	}
	count := 0
	for _, pos := range activeCrossVenuePerpSpotPositions(ps) {
		sym := venue.Symbol(pos.Symbol)
		spotV := e.venues[pos.SpotVenue]
		perpV := e.venues[pos.PerpVenue]
		if spotV == nil || perpV == nil {
			continue
		}
		spotShort := 0.0
		perpLong := 0.0
		if bal, berr := spotV.FetchBalances(ctx); berr == nil {
			spotShort = cvpsSpotShortQty(bal, pos.Symbol)
		}
		if pp, perr := perpV.FetchPositions(ctx); perr == nil {
			perpLong = svpsPerpLongQty(pp, sym)
		}
		if spotShort > executionQtyTolerance || perpLong > executionQtyTolerance {
			count++
		}
	}
	return count
}

// CloseAllPositions 一键全平:跨所 FetchBalances/FetchPositions 对账后只平真实腿;合成记录仅清台账。
func (e *CrossVenuePerpSpotExecutor) CloseAllPositions(ctx context.Context, live bool) (closed int, errs []error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	ps, err := e.store.load()
	if err != nil {
		return 0, []error{err}
	}
	for _, pos := range activeCrossVenuePerpSpotPositions(ps) {
		sym := venue.Symbol(pos.Symbol)
		spotV := e.venues[pos.SpotVenue]
		perpV := e.venues[pos.PerpVenue]
		if spotV == nil || perpV == nil {
			errs = append(errs, fmt.Errorf("%s: venue 未就绪", pos.Symbol))
			continue
		}
		spotShort := 0.0
		perpLong := 0.0
		if bal, berr := spotV.FetchBalances(ctx); berr == nil {
			spotShort = cvpsSpotShortQty(bal, pos.Symbol)
		}
		if pp, perr := perpV.FetchPositions(ctx); perr == nil {
			perpLong = svpsPerpLongQty(pp, sym)
		}
		spotOpen := spotShort > executionQtyTolerance
		perpOpen := perpLong > executionQtyTolerance
		if !spotOpen && !perpOpen {
			pos.Status = CVPSStatusClosed
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
func (e *CrossVenuePerpSpotExecutor) reconcileOnStart(ctx context.Context, live bool) {
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[跨所perp-spot] reconcile 读台账失败: %v", err)
		return
	}
	active := activeCrossVenuePerpSpotPositions(ps)
	if len(active) > 0 {
		e.logger.Printf("[跨所perp-spot] reconcile: %d 笔落盘持仓", len(active))
		for _, p := range active {
			e.reconcileTrackedPosition(ctx, p, live)
		}
	}

	for _, p := range activeCrossVenuePerpSpotPositions(ps) {
		if p.Status == CVPSStatusOpeningFirstLeg || p.Status == CVPSStatusOpeningSecondLeg {
			e.logger.Printf("[跨所perp-spot] reconcile: %s 处于中间态 %s,尝试恢复", p.Symbol, p.Status)
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

func (e *CrossVenuePerpSpotExecutor) reconcileTrackedPosition(ctx context.Context, pos CrossVenuePerpSpotPosition, live bool) {
	sym := venue.Symbol(pos.Symbol)
	spotV := e.venues[pos.SpotVenue]
	perpV := e.venues[pos.PerpVenue]
	if spotV == nil || perpV == nil {
		e.logger.Printf("[跨所perp-spot] reconcile:%s venue 未就绪,保留台账", pos.Symbol)
		return
	}
	spotShort := 0.0
	perpLong := 0.0
	if bal, err := spotV.FetchBalances(ctx); err == nil {
		spotShort = cvpsSpotShortQty(bal, pos.Symbol)
	}
	if pp, err := perpV.FetchPositions(ctx); err == nil {
		perpLong = svpsPerpLongQty(pp, sym)
	}
	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance
	if spotOpen != perpOpen {
		e.handleOrphanLeg(ctx, pos, spotOpen, perpOpen, live, spotShort, perpLong)
	}
}

// Positions 返回当前持仓快照(测试/面板用)。
func (e *CrossVenuePerpSpotExecutor) Positions() []CrossVenuePerpSpotPosition {
	return e.snapshotPositions()
}

// SettlementsPath 返回结算文件路径(测试用)。
func (e *CrossVenuePerpSpotExecutor) SettlementsPath() string {
	if e.store == nil {
		return ""
	}
	return filepath.Join(e.store.dir, crossVenuePerpSpotSettlementsFile)
}

// ReconcileOnStart 启动时载入台账并对账(统一轮动 runner 调用;live 由 runner.isLive() 传入)。
func (e *CrossVenuePerpSpotExecutor) ReconcileOnStart(ctx context.Context, live bool) {
	e.reconcileOnStart(ctx, live)
}

// ManageOpenPositions 管理所有在场持仓(统一轮动 runner 调用)。
func (e *CrossVenuePerpSpotExecutor) ManageOpenPositions(ctx context.Context, live bool) {
	e.manageOpenPositions(ctx, live)
}
