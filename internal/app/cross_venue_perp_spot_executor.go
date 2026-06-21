package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

// cross_venue_perp_spot_executor.go 跨所 perp-spot 负费率执行器(A 所借币空现货 + B 所多永续, OFF 起步, live 闸透传)。

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

	deadManUnsupportedVenues map[string]struct{}  // dead-man 声明支持但实际 ErrUnsupported 时缓存
	deadManLastArm           map[string]time.Time // 各所上次成功续期 dead-man 的时刻(账户级,按所节流)

	rollbackBlockedMu sync.Mutex
	rollbackBlocked   map[string]time.Time

	openQuarantine *openFailureQuarantine

	openingsEnabled atomic.Bool // 未登记材料级敞口 halt 时禁止新开
}

// NewCrossVenuePerpSpotExecutor 构造执行引擎;默认 plan,不下真单。
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
		cfg:                      cfg,
		venues:                   venues,
		store:                    store,
		logger:                   logger,
		recordDir:                recordDir,
		nowFn:                    time.Now,
		deadManUnsupportedVenues: make(map[string]struct{}),
		deadManLastArm:           make(map[string]time.Time),
		rollbackBlocked:          make(map[string]time.Time),
		openQuarantine:           newOpenFailureQuarantine(time.Now),
	}
	e.openingsEnabled.Store(true)
	if store != nil {
		if ps, lerr := store.load(); lerr == nil {
			e.positions = ps
		}
	}
	e.openQuarantine.nowFn = e.nowFn
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

// OpeningsEnabled 返回运行时开仓开关;未登记材料级敞口 halt 后为 false。
func (e *CrossVenuePerpSpotExecutor) OpeningsEnabled() bool {
	if e == nil {
		return false
	}
	return e.openingsEnabled.Load()
}

func (e *CrossVenuePerpSpotExecutor) armDeadManForPerpVenue(ctx context.Context, venueName string, live bool) {
	armDeadMansForVenueNames(
		ctx, e.cfg, e.venues, e.logger, e.nowFn,
		&e.mu, e.deadManUnsupportedVenues, e.deadManLastArm,
		[]string{venueName}, live,
	)
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

func (e *CrossVenuePerpSpotExecutor) OpenBlockedForSymbol(symbol string) bool {
	if e == nil {
		return false
	}
	return e.isSymbolRollbackBlocked(symbol) || e.openQuarantine.isQuarantined(symbol)
}

func (e *CrossVenuePerpSpotExecutor) recordOpenOutcome(symbol string, success bool) {
	if success {
		e.openQuarantine.recordOpenSuccess(symbol)
		return
	}
	e.openQuarantine.recordOpenFailure(symbol)
}

func (e *CrossVenuePerpSpotExecutor) lookupLegInstrument(ctx context.Context, venueName string, sym venue.Symbol, mkt venue.Market) venue.Instrument {
	v := e.venues[venueName]
	if v == nil {
		return venue.Instrument{}
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, v, sym, mkt)
	if !ok && e.logger != nil {
		e.logger.Printf("[跨所perp-spot] %s %s 缺 instrument 元数据,订单未量化", venueName, sym)
	}
	return instr
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
	instr venue.Instrument,
	contractBased bool,
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
	limitPrice, qty = quantizeOrderPriceQty(limitPrice, qty, side, instr, contractBased, e.logger, string(sym))
	logLiveOrderLeg(e.logger, traderVenueName(tr), mkt, string(sym), side, limitPrice, instr.TickSize, qty, instr.StepSize)
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
	instr venue.Instrument,
	contractBased bool,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, cvpsTakerSlippageBps(e.cfg.CrossVenueExecutor))
	return e.placePerpSpotIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live, instr, contractBased)
}

// placeCVPSCloseLegExec 平仓专用腿:按 CloseAttempts 阶梯放宽滑点,≥阈值后穿价兜底。
func (e *CrossVenuePerpSpotExecutor) placeCVPSCloseLegExec(
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
	closeAttempts int,
	instr venue.Instrument,
	contractBased bool,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price, _ := carryCloseIocLimitPrice(e.cfg.CrossVenueExecutor, side, book, closeAttempts)
	return e.placePerpSpotIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, marginEffect, live, instr, contractBased)
}

// executeOpen 先开 A 所借币空现货,再开 B 所多永续;第二腿失败回滚第一腿(买回+还币)。
func (e *CrossVenuePerpSpotExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (CrossVenuePerpSpotPosition, error) {
	if !e.OpeningsEnabled() {
		return CrossVenuePerpSpotPosition{}, fmt.Errorf("跨所perp-spot 未登记敞口 halt,禁止新开")
	}
	spotVenue, perpVenue, qty, notional, err := parseCrossVenuePerpSpotLegs(opp)
	if err != nil {
		return CrossVenuePerpSpotPosition{}, err
	}
	if e.isSymbolRollbackBlocked(opp.Symbol) || e.OpenBlockedForSymbol(opp.Symbol) {
		return CrossVenuePerpSpotPosition{}, fmt.Errorf("%s 回滚未卸净或开仓隔离中,暂禁新开", opp.Symbol)
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
	spotInstr := opportunityLegInstrument(opp, spotVenue, "spot")
	perpInstr := opportunityLegInstrument(opp, perpVenue, "perp")
	perpContract := perpV.Capabilities().ContractBasedPerp
	spotCaps := spotV.Capabilities()
	perpCaps := perpV.Capabilities()
	if spotInstr.TickSize <= 0 || spotInstr.StepSize <= 0 {
		spotInstr = e.lookupLegInstrument(ctx, spotVenue, sym, venue.MarketSpot)
	}
	if perpCaps.ContractBasedPerp && perpInstr.ContractSize <= 0 {
		perpInstr = carryMergeContractPerpInstrument(perpInstr, e.lookupLegInstrument(ctx, perpVenue, sym, venue.MarketPerp))
	} else if perpInstr.TickSize <= 0 || perpInstr.StepSize <= 0 {
		perpInstr = e.lookupLegInstrument(ctx, perpVenue, sym, venue.MarketPerp)
	}
	refPrice := 0.0
	if perpBook, err := perpTrader.FetchBookTicker(ctx, venue.MarketPerp, sym); err == nil {
		refPrice = perpBook.AskPrice
	}
	openQty, alignErr := carryPreflightSpotPerpOpenAlign(qty, refPrice, spotInstr, perpInstr, spotCaps, perpCaps)
	if alignErr != nil {
		if live {
			carryLogPreflightSkip(e.logger, "跨所perp-spot", opp.Symbol, alignErr.Error())
		}
		return CrossVenuePerpSpotPosition{}, alignErr
	}
	qty = openQty
	if refPrice > 0 {
		notional = qty * refPrice
	}
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
		logDryRunOpen(e.logger, "跨所perp-spot", opp.Symbol,
			fmt.Sprintf("A所(%s)借币空现货/B所(%s)多永续 各≈%.6f 名义≈%.2fU EV≈%.4f", spotVenue, perpVenue, qty, notional, opp.ExpectedNetEVUSDT))
		appendDryRunEvent(e.recordDir, "cvps_open", map[string]any{
			"symbol": opp.Symbol, "spot_venue": spotVenue, "perp_venue": perpVenue, "qty": qty,
		})
		return CrossVenuePerpSpotPosition{}, nil
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
	perpMargin := rotationPerpMarginWithBuffer(e.cfg.CrossVenueExecutor, notional)
	if err := carryPreflightSpotPerpOpen(ctx, e.venues, spotVenue, perpVenue, 0, perpMargin); err != nil {
		carryLogPreflightSkip(e.logger, "跨所perp-spot", opp.Symbol, err.Error())
		return CrossVenuePerpSpotPosition{}, err
	}

	if err := e.persistPosition(pos); err != nil {
		return pos, err
	}

	openFailed := false
	defer func() {
		if openFailed {
			e.recordOpenOutcome(opp.Symbol, false)
		}
	}()

	spotID := cvpsLegClientID(idem, "s1")
	spotRes, err := e.placePerpSpotLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideSell, qty, spotID, false, venue.MarginEffectBorrow, live, spotInstr, false)
	if orderStatusIsUnknown(spotRes) || isUnknownOrderStateError(err) {
		if spotRes.ExecutedQty > executionQtyTolerance {
			pos.SpotLeg.Qty = spotRes.ExecutedQty
			pos.SpotLeg.EntryPrice = spotRes.AvgPrice
			pos.SpotLeg.OrderIDs = []string{spotID}
			pos.Status = CVPSStatusOpeningSecondLeg
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所perp-spot查单未知", fmt.Sprintf("%s 现货借币腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("现货腿状态未知,禁止自动回滚: %w", err)
	}
	if err != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		openFailed = true
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live, spotInstr)
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
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, pos.SpotLeg.Qty, live, spotInstr)
		return pos, fmt.Errorf("第二腿预检失败: %w", err)
	}

	perpID := cvpsLegClientID(idem, "p1")
	hedgeQty := spotRes.ExecutedQty
	perpRes, perpErr := e.placePerpSpotLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideBuy, hedgeQty, perpID, false, venue.MarginEffectNone, live, perpInstr, perpContract)
	if orderStatusIsUnknown(perpRes) {
		e.notifyCritical("跨所perp-spot查单未知", fmt.Sprintf("%s 永续腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("永续腿状态未知,禁止自动回滚: %w", perpErr)
	}
	if perpErr != nil || perpRes.ExecutedQty <= executionQtyTolerance {
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live, spotInstr)
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackPerpLeg(ctx, perpTrader, sym, perpRes.ExecutedQty, live, perpInstr, perpContract)
		}
		return pos, fmt.Errorf("第二腿(多永续)未足额成交: %w", perpErr)
	}
	match := carryMatchTwoLegOpensNormalized(spotRes, perpRes, spotInstr, perpInstr, spotCaps, perpCaps, carryMinTradableQty(refPrice, perpInstr))
	if !match.OK {
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, spotRes.ExecutedQty, live, spotInstr)
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackPerpLeg(ctx, perpTrader, sym, perpRes.ExecutedQty, live, perpInstr, perpContract)
		}
		return pos, fmt.Errorf("第二腿(多永续)未足额成交: spot=%.6f perp=%.6f", spotRes.ExecutedQty, perpRes.ExecutedQty)
	}
	if match.ExcessLeg1 > executionQtyTolerance {
		e.rollbackFirstLeg(ctx, pos, spotV, spotTrader, sym, match.ExcessLeg1, live, spotInstr)
	}
	if match.ExcessLeg2 > executionQtyTolerance {
		e.rollbackPerpLeg(ctx, perpTrader, sym, match.ExcessLeg2, live, perpInstr, perpContract)
	}

	pos.PerpLeg.Qty = match.MatchedQty
	pos.PerpLeg.EntryPrice = perpRes.AvgPrice
	pos.PerpLeg.OrderIDs = []string{perpID}
	bookQty := match.MatchedQty
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
	e.recordOpenOutcome(opp.Symbol, true)
	e.logger.Printf("[跨所perp-spot] 开仓完成 %s: 现货%s/永续%s 各≈%.6f", opp.Symbol, spotVenue, perpVenue, bookQty)
	return pos, nil
}

func (e *CrossVenuePerpSpotExecutor) rollbackFirstLeg(ctx context.Context, pos CrossVenuePerpSpotPosition, spotV venue.Venue, tr cvpsTrader, sym venue.Symbol, qty float64, live bool, instr venue.Instrument) {
	if qty <= executionQtyTolerance {
		return
	}
	id := cvpsLegClientID(pos.IdemPrefix, "rb")
	res, err := e.placePerpSpotLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live, instr, false)
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

func (e *CrossVenuePerpSpotExecutor) rollbackPerpLeg(ctx context.Context, tr cvpsTrader, sym venue.Symbol, qty float64, live bool, instr venue.Instrument, contractBased bool) {
	if qty <= executionQtyTolerance {
		return
	}
	_, _ = e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, qty, "cvps-rbp", true, venue.MarginEffectNone, live, instr, contractBased)
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
	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 现货腿 %s 买回还币 %.6f 原因:%s", pos.Symbol, pos.SpotVenue, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 永续腿 %s 平多 %.6f 原因:%s", pos.Symbol, pos.PerpVenue, pos.PerpLeg.Qty, reason)
		}
		appendDryRunEvent(e.recordDir, "cvps_close", map[string]any{"symbol": pos.Symbol, "reason": reason})
		return nil
	}
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

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	if pos.PerpNextFundingTime.IsZero() {
		e.syncPerpFundingMeta(ctx, &pos)
	}
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
	spotInstr := e.lookupLegInstrument(ctx, pos.SpotVenue, sym, venue.MarketSpot)
	perpInstr := e.lookupLegInstrument(ctx, pos.PerpVenue, sym, venue.MarketPerp)
	perpContract := perpV != nil && perpV.Capabilities().ContractBasedPerp
	// 优先平现货腿(空现货逼空风险更大)。
	var spotCloseErr error
	if closeSpotQty > executionQtyTolerance {
		id := cvpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeCVPSCloseLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideBuy, closeSpotQty, id, false, venue.MarginEffectRepay, live, attempt, spotInstr, false); err == nil {
			pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
		} else {
			spotCloseErr = err
		}
	}
	if closePerpQty > executionQtyTolerance {
		id := cvpsLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placeCVPSCloseLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideSell, closePerpQty, id, true, venue.MarginEffectNone, live, attempt, perpInstr, perpContract); err == nil {
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
			e.finalizeClosedPosition(pos, live)
			return nil
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		return fmt.Errorf("平仓跳过: %s 实测为0,保留台账待下轮", pos.Symbol)
	}

	spotOpen := spotShort > executionQtyTolerance
	perpOpen := perpLong > executionQtyTolerance
	dust := evaluateNegativeCarryOrphan(ctx, e.cfg.CrossVenueExecutor, e.venues, pos.Symbol, pos.SpotVenue, pos.PerpVenue, spotOpen, perpOpen, spotShort, perpLong, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice)
	if !dust.IsDustOrphan && spotOpen && !perpOpen && isNotionalFilterError(spotCloseErr) {
		dust = negativeCarryDustFromNotionalReject(ctx, e.cfg.CrossVenueExecutor, e.venues, pos.Symbol, pos.SpotVenue, pos.PerpVenue, spotOpen, perpOpen, spotShort, perpLong, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice)
	}
	if dust.IsDustOrphan {
		if carryShouldDeferDustOrphanClose(e.nowFn(), pos.PerpNextFundingTime) {
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return fmt.Errorf("dust_orphan 未到 funding 结算,保留持仓待下轮")
		}
		if live {
			if spotOpen && !perpOpen {
				bestEffortRepayMarginBorrow(ctx, spotV, pos.Symbol, spotShort)
			} else if perpOpen {
				id := cvpsLegClientID(pos.IdemPrefix, "dust")
				qty := ledgerCappedCloseQty(pos.PerpLeg.Qty, perpLong)
				_, _ = e.placeCVPSCloseLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideSell, qty, id, true, venue.MarginEffectNone, live, attempt, perpInstr, perpContract)
			}
		}
		dustQty := negativeCarryDustResidualQty(spotOpen, perpOpen, spotShort, perpLong)
		appendCarryDustLedgerBestEffort(e.recordDir, "cvps_dust_close", pos.Symbol, baseAssetFromCanonicalSymbol(pos.Symbol), dustQty, dust.RemainingNotionalUSDT, nonEmpty(reason, "dust_close"))
		pos.Status = CVPSStatusClosed
		pos.DustResidualCostUSDT = applyCarryDustResidualCost(dust.RemainingNotionalUSDT)
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, reason), dust.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "跨所perp-spot", pos.Symbol, dust.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return nil
	}

	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	return fmt.Errorf("平仓未完成: %s 第 %d 轮仍有剩余腿", pos.Symbol, attempt)
}

func (e *CrossVenuePerpSpotExecutor) finalizeClosedPosition(pos CrossVenuePerpSpotPosition, live bool) {
	if !realLedgerWritesAllowed(live) {
		return
	}
	if e.store != nil {
		rec := buildCrossVenuePerpSpotSettlement(pos, e.nowFn())
		_ = e.store.appendSettlement(rec)
		appendAttributionRealizedBestEffort(e.recordDir, realizedRecordFromCVPSSettlement(rec))
	}
	_ = e.removePosition(pos)
}

func cvpsSpotShortQty(bal venue.Balances, symbol string) float64 {
	base := baseAssetFromCanonicalSymbol(symbol)
	return marginShortQtyFromBalance(bal, base)
}

func (e *CrossVenuePerpSpotExecutor) shouldClose(ctx context.Context, pos *CrossVenuePerpSpotPosition) string {
	if pos == nil {
		return ""
	}
	if reason := carryHardStopCloseReason(ctx, e.venues, false, pos.Symbol, pos.SpotVenue, pos.PerpVenue, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice); reason != "" {
		return reason
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
	if !live {
		if reason := e.shouldClose(ctx, &pos); reason != "" {
			logDryRunClose(e.logger, "跨所perp-spot", pos.Symbol, reason)
		}
		return pos, false
	}
	if pos.Status == CVPSStatusClosing {
		_ = e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, false
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
		return pos, false
	}
	if !spotOpen && !perpOpen {
		pos.Status = CVPSStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos, live)
		return pos, false
	}

	e.syncPerpFundingMeta(ctx, &pos)
	dev := qtyDeviation(spotShort, perpLong)
	if carryShouldRebalanceHedge(dev, pos.LastHedgeRebalanceAt, e.nowFn()) {
		e.rebalanceCVPSHedge(ctx, &pos, spotShort, perpLong, live)
	}
	pos.EstFundingUSDT = e.estimateFundingAccrual(&pos)
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)

	// dead-man 续期(永续腿所在所, okx/gate 等)
	e.armDeadManForPerpVenue(ctx, pos.PerpVenue, live)

	if reason := e.shouldClose(ctx, &pos); reason != "" {
		if carryIsHardStopCloseReason(reason) || !carryShouldDeferEVWeakClose(e.nowFn(), pos.PerpNextFundingTime, reason) {
			_ = e.closePosition(ctx, pos, live, reason)
		}
		return pos, false
	}
	return pos, false
}

func (e *CrossVenuePerpSpotExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeCrossVenuePerpSpotPositions(e.snapshotPositions()) {
		_, shouldPersist := e.managePosition(ctx, pos, live)
		// managePosition 子路径已自行 persist 或 finalize;不可再 upsert 入参快照,否则复活已平仓仓。
		if shouldPersist {
			_ = e.persistPosition(pos)
		}
	}
}

func (e *CrossVenuePerpSpotExecutor) syncPerpFundingMeta(ctx context.Context, pos *CrossVenuePerpSpotPosition) {
	if pos == nil {
		return
	}
	if pos.PerpIntervalHours <= 0 {
		pos.PerpIntervalHours = 8
	}
	perpV := e.venues[pos.PerpVenue]
	if perpV == nil {
		return
	}
	snaps, err := perpV.FetchFundingSnapshot(ctx)
	if err != nil {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	for _, snap := range snaps {
		if snap.Symbol != sym {
			continue
		}
		pos.PerpFundingRate = snap.LastFundingRate
		if snap.IntervalHours > 0 {
			pos.PerpIntervalHours = snap.IntervalHours
		}
		if !snap.NextFundingTime.IsZero() {
			pos.PerpNextFundingTime = snap.NextFundingTime
		}
		return
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
	interval := pos.PerpIntervalHours
	if interval <= 0 {
		interval = 8
	}
	hourlyRate := (-pos.PerpFundingRate) / float64(interval)
	delta := notional * hourlyRate * elapsedHours
	pos.LastAccrualAt = now
	return pos.EstFundingUSDT + delta
}

func (e *CrossVenuePerpSpotExecutor) rebalanceCVPSHedge(ctx context.Context, pos *CrossVenuePerpSpotPosition, spotShort, perpLong float64, live bool) {
	if pos == nil {
		return
	}
	reduceSpot, reducePerp, delta := carryNegativeRebalancePlan(spotShort, perpLong)
	if delta <= executionQtyTolerance {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	now := e.nowFn()
	spotInstr := e.lookupLegInstrument(ctx, pos.SpotVenue, sym, venue.MarketSpot)
	perpInstr := e.lookupLegInstrument(ctx, pos.PerpVenue, sym, venue.MarketPerp)
	perpContract := e.venues[pos.PerpVenue] != nil && e.venues[pos.PerpVenue].Capabilities().ContractBasedPerp
	if reduceSpot {
		if tr, ok := e.venues[pos.SpotVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "rb-s")
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, delta, id, false, venue.MarginEffectRepay, live, spotInstr, false); res.ExecutedQty > executionQtyTolerance {
				pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			}
		}
	} else if reducePerp {
		if tr, ok := e.venues[pos.PerpVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "rb-p")
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, delta, id, true, venue.MarginEffectNone, live, perpInstr, perpContract); res.ExecutedQty > executionQtyTolerance {
				pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
			}
		}
	}
	pos.LastHedgeRebalanceAt = now
	pos.UpdatedAt = now
	_ = e.persistPosition(*pos)
	if e.logger != nil {
		leg := "永续"
		if reduceSpot {
			leg = "现货"
		}
		e.logger.Printf("[跨所perp-spot] %s 对冲再平衡:偏差%.1f%% 减%s %.6f",
			pos.Symbol, qtyDeviation(spotShort, perpLong)*100, leg, delta)
	}
}

func (e *CrossVenuePerpSpotExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenuePerpSpotPosition, spotOpen, perpOpen, live bool, spotShort, perpLong float64) {
	decision := evaluateNegativeCarryOrphan(ctx, e.cfg.CrossVenueExecutor, e.venues, pos.Symbol, pos.SpotVenue, pos.PerpVenue, spotOpen, perpOpen, spotShort, perpLong, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice)
	if pos.PerpNextFundingTime.IsZero() {
		e.syncPerpFundingMeta(ctx, &pos)
	}
	sym := venue.Symbol(pos.Symbol)
	spotInstr := e.lookupLegInstrument(ctx, pos.SpotVenue, sym, venue.MarketSpot)
	perpInstr := e.lookupLegInstrument(ctx, pos.PerpVenue, sym, venue.MarketPerp)
	perpContract := e.venues[pos.PerpVenue] != nil && e.venues[pos.PerpVenue].Capabilities().ContractBasedPerp
	if decision.IsDustOrphan {
		if carryShouldDeferDustOrphanClose(e.nowFn(), pos.PerpNextFundingTime) {
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return
		}
		if live {
			if spotOpen && !perpOpen {
				bestEffortRepayMarginBorrow(ctx, e.venues[pos.SpotVenue], pos.Symbol, spotShort)
			} else if perpOpen {
				if tr, ok := e.venues[pos.PerpVenue].(cvpsTrader); ok {
					id := cvpsLegClientID(pos.IdemPrefix, "dust")
					qty := ledgerCappedCloseQty(pos.PerpLeg.Qty, perpLong)
					_, _ = e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, qty, id, true, venue.MarginEffectNone, live, perpInstr, perpContract)
				}
			}
		}
		dustQty := negativeCarryDustResidualQty(spotOpen, perpOpen, spotShort, perpLong)
		appendCarryDustLedgerBestEffort(e.recordDir, "cvps_dust_orphan", pos.Symbol, baseAssetFromCanonicalSymbol(pos.Symbol), dustQty, decision.RemainingNotionalUSDT, "dust_orphan")
		pos.Status = CVPSStatusClosed
		pos.DustResidualCostUSDT = applyCarryDustResidualCost(decision.RemainingNotionalUSDT)
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, "dust_orphan"), decision.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "跨所perp-spot", pos.Symbol, decision.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return
	}
	pos.Status = CVPSStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	if perpOpen && !spotOpen {
		if tr, ok := e.venues[pos.PerpVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "op")
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideSell, perpLong, id, true, venue.MarginEffectNone, live, perpInstr, perpContract); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	} else if spotOpen && !perpOpen {
		if tr, ok := e.venues[pos.SpotVenue].(cvpsTrader); ok && live {
			id := cvpsLegClientID(pos.IdemPrefix, "os")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotShort)
			if res, _ := e.placePerpSpotLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideBuy, qty, id, false, venue.MarginEffectRepay, live, spotInstr, false); res.ExecutedQty > executionQtyTolerance {
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
		e.finalizeClosedPosition(pos, true)
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

	e.reconcileUntrackedExposureOnStart(ctx, ps, live)

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

func (e *CrossVenuePerpSpotExecutor) reconcileUntrackedExposureOnStart(ctx context.Context, ps []CrossVenuePerpSpotPosition, live bool) {
	cfg := e.cfg.CrossVenueExecutor
	untracked := carryScanUntrackedCVPSLegs(ctx, cfg, e.recordDir, e.logger, e.venues, ps)
	if len(untracked) == 0 {
		return
	}
	action := crossVenueUntrackedExposureAction(cfg)
	for _, leg := range untracked {
		e.logger.Printf("[跨所perp-spot] reconcile: venue=%s 未登记 %s %s≈%.6f", leg.Venue, leg.Market, leg.Symbol, leg.Qty)
	}
	shouldHalt := true
	switch action {
	case crossVenueRecoveryActionFlatten:
		if !cfg.LiveTradingEnabled {
			e.notifyCritical("跨所perp-spot冷启动", "检测到未登记真实持仓,flatten 需 live", "")
		} else {
			shouldHalt = false
			for _, leg := range untracked {
				e.flattenUntrackedLeg(ctx, leg, live)
			}
			e.notifyCritical("跨所perp-spot冷启动 flatten", fmt.Sprintf("已对 %d 条未登记腿执行 flatten", len(untracked)), "")
		}
	default:
		e.notifyCritical("跨所perp-spot冷启动", fmt.Sprintf("检测到 %d 条未登记真实持仓,已 halt 待人工确认", len(untracked)), "")
	}
	if shouldHalt {
		e.openingsEnabled.Store(false)
	}
}

func (e *CrossVenuePerpSpotExecutor) flattenUntrackedLeg(ctx context.Context, leg carryUntrackedLeg, live bool) {
	if !live || leg.Qty <= executionQtyTolerance {
		return
	}
	if carryShouldSkipUntrackedFlatten(e.recordDir, e.logger, leg) {
		return
	}
	v := e.venues[leg.Venue]
	tr, ok := v.(cvpsTrader)
	if !ok || tr == nil {
		return
	}
	sym := venue.Symbol(leg.Symbol)
	instr := e.lookupLegInstrument(ctx, leg.Venue, sym, leg.Market)
	contractBased := leg.Market == venue.MarketPerp && v.Capabilities().ContractBasedPerp
	margin := venue.MarginEffectNone
	if leg.Market == venue.MarketSpot && leg.Side == venue.SideBuy {
		margin = venue.MarginEffectRepay
	}
	id := fmt.Sprintf("cvps-reconcile-flat-%s-%d", leg.Symbol, e.nowFn().UnixNano())
	res, err := e.placePerpSpotLegExec(ctx, tr, leg.Market, sym, leg.Side, leg.Qty, id, leg.ReduceOnly, margin, live, instr, contractBased)
	carryAfterUntrackedFlatten(ctx, e.recordDir, e.logger, e.venues, leg, res, err)
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
		return
	}
	if spotOpen && perpOpen {
		if carryReconcileAdoptNegativeLegQtys(&pos, spotShort, perpLong, e.nowFn(), e.logger, "跨所perp-spot") {
			_ = e.persistPosition(pos)
		}
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

// ListActivePositions 返回非 closed 状态的持仓(只读,供面板/API)。
func (e *CrossVenuePerpSpotExecutor) ListActivePositions() []CrossVenuePerpSpotPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		ps = e.snapshotPositions()
	}
	return activeCrossVenuePerpSpotPositions(ps)
}

// ListAllPositions 返回台账全部持仓(只读)。
func (e *CrossVenuePerpSpotExecutor) ListAllPositions() []CrossVenuePerpSpotPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		return e.snapshotPositions()
	}
	return ps
}

func (e *CrossVenuePerpSpotExecutor) listPositionsForAPI(statusFilter string) []CrossVenuePerpSpotPosition {
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
func (e *CrossVenuePerpSpotExecutor) ListRecentSettlements(limit int) ([]CrossVenuePerpSpotSettlement, error) {
	if e == nil {
		return nil, nil
	}
	return loadTailJSONLFile[CrossVenuePerpSpotSettlement](e.SettlementsPath(), time.Time{}, limit)
}
