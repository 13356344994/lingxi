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

// cross_venue_spot_perp_executor.go 跨所 spot-perp 正费率执行器(OFF 起步, live 闸透传)。

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

	deadManUnsupportedVenues map[string]struct{}  // dead-man 声明支持但实际 ErrUnsupported 时缓存
	deadManLastArm           map[string]time.Time // 各所上次成功续期 dead-man 的时刻(账户级,按所节流)

	rollbackBlockedMu sync.Mutex
	rollbackBlocked   map[string]time.Time

	openQuarantine *openFailureQuarantine

	openingsEnabled atomic.Bool // 未登记材料级敞口 halt 时禁止新开
}

// NewCrossVenueSpotPerpExecutor 构造执行引擎;默认 plan,不下真单。
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

// OpeningsEnabled 返回运行时开仓开关;未登记材料级敞口 halt 后为 false。
func (e *CrossVenueSpotPerpExecutor) OpeningsEnabled() bool {
	if e == nil {
		return false
	}
	return e.openingsEnabled.Load()
}

func (e *CrossVenueSpotPerpExecutor) armDeadManForPerpVenue(ctx context.Context, venueName string, live bool) {
	armDeadMansForVenueNames(
		ctx, e.cfg, e.venues, e.logger, e.nowFn,
		&e.mu, e.deadManUnsupportedVenues, e.deadManLastArm,
		[]string{venueName}, live,
	)
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

func (e *CrossVenueSpotPerpExecutor) OpenBlockedForSymbol(symbol string) bool {
	if e == nil {
		return false
	}
	return e.isSymbolRollbackBlocked(symbol) || e.openQuarantine.isQuarantined(symbol)
}

func (e *CrossVenueSpotPerpExecutor) recordOpenOutcome(symbol string, success bool) {
	if success {
		e.openQuarantine.recordOpenSuccess(symbol)
		return
	}
	e.openQuarantine.recordOpenFailure(symbol)
}

func (e *CrossVenueSpotPerpExecutor) lookupLegInstrument(ctx context.Context, venueName string, sym venue.Symbol, mkt venue.Market) venue.Instrument {
	v := e.venues[venueName]
	if v == nil {
		return venue.Instrument{}
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, v, sym, mkt)
	if !ok {
		if e.logger != nil {
			e.logger.Printf("[跨所spot-perp] %s %s 缺 instrument 元数据,订单未量化", venueName, sym)
		}
	}
	return instr
}

func spotQuoteAvailableUSDT(bal venue.Balances) float64 {
	if b, ok := bal.ByAsset["USDT"]; ok {
		return math.Max(b.Free, 0)
	}
	return 0
}

func cvspAffordableSpotQty(spotAvail, askPrice, step, slippageBps float64) float64 {
	if spotAvail <= 0 || askPrice <= 0 {
		return 0
	}
	budget := spotAvail / (1 + slippageBps/10000)
	return quantizeQtyToStep(budget/askPrice, step)
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
	instr venue.Instrument,
	contractBased bool,
) (venue.OrderResult, error) {
	book, err := tr.FetchBookTicker(ctx, mkt, sym)
	if err != nil {
		return venue.OrderResult{}, err
	}
	price := iocLimitPrice(side, book, cvspTakerSlippageBps(e.cfg.CrossVenueExecutor))
	return e.placeSpotPerpIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, live, instr, contractBased)
}

// placeCVSPCloseLegExec 平仓专用腿:按 CloseAttempts 阶梯放宽滑点,≥阈值后穿价兜底。
func (e *CrossVenueSpotPerpExecutor) placeCVSPCloseLegExec(
	ctx context.Context,
	tr cvspTrader,
	mkt venue.Market,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
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
	return e.placeSpotPerpIOC(ctx, tr, mkt, sym, side, qty, price, clientID, reduceOnly, live, instr, contractBased)
}

// executeOpen 先开 B 所空永续,再开 A 所买现货;第二腿失败回滚第一腿。
func (e *CrossVenueSpotPerpExecutor) executeOpen(ctx context.Context, opp UnifiedOpportunity, live bool) (CrossVenueSpotPerpPosition, error) {
	if !e.OpeningsEnabled() {
		return CrossVenueSpotPerpPosition{}, fmt.Errorf("跨所spot-perp 未登记敞口 halt,禁止新开")
	}
	spotVenue, perpVenue, qty, notional, err := parseCrossVenueSpotPerpLegs(opp)
	if err != nil {
		return CrossVenueSpotPerpPosition{}, err
	}
	if e.isSymbolRollbackBlocked(opp.Symbol) || e.OpenBlockedForSymbol(opp.Symbol) {
		return CrossVenueSpotPerpPosition{}, fmt.Errorf("%s 回滚未卸净或开仓隔离中,暂禁新开", opp.Symbol)
	}
	spotV := e.venues[spotVenue]
	perpV := e.venues[perpVenue]
	spotTrader, _ := spotV.(cvspTrader)
	perpTrader, _ := perpV.(cvspTrader)
	if spotV == nil || perpV == nil || spotTrader == nil || perpTrader == nil {
		return CrossVenueSpotPerpPosition{}, fmt.Errorf("venue 不满足交易接口")
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
			carryLogPreflightSkip(e.logger, "跨所spot-perp", opp.Symbol, alignErr.Error())
		}
		return CrossVenueSpotPerpPosition{}, alignErr
	}
	qty = openQty
	if refPrice > 0 {
		notional = qty * refPrice
	}
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
		logDryRunOpen(e.logger, "跨所spot-perp", opp.Symbol,
			fmt.Sprintf("A所(%s)买现货/B所(%s)空永续 各≈%.6f 名义≈%.2fU EV≈%.4f", spotVenue, perpVenue, qty, notional, opp.ExpectedNetEVUSDT))
		appendDryRunEvent(e.recordDir, "cvsp_open", map[string]any{
			"symbol": opp.Symbol, "spot_venue": spotVenue, "perp_venue": perpVenue, "qty": qty,
		})
		return CrossVenueSpotPerpPosition{}, nil
	}

	slippageBps := cvspTakerSlippageBps(e.cfg.CrossVenueExecutor)
	perpMargin := rotationPerpMarginWithBuffer(e.cfg.CrossVenueExecutor, notional)
	spotNeed := notional * (1 + slippageBps/10000)
	if err := carryPreflightSpotPerpOpen(ctx, e.venues, spotVenue, perpVenue, spotNeed, perpMargin); err != nil {
		carryLogPreflightSkip(e.logger, "跨所spot-perp", opp.Symbol, err.Error())
		return CrossVenueSpotPerpPosition{}, err
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

	perpID := cvspLegClientID(idem, "p1")
	perpRes, err := e.placeSpotPerpLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideSell, qty, perpID, false, live, perpInstr, perpContract)
	if orderStatusIsUnknown(perpRes) || isUnknownOrderStateError(err) {
		if perpRes.ExecutedQty > executionQtyTolerance {
			pos.PerpLeg.Qty = perpRes.ExecutedQty
			pos.PerpLeg.EntryPrice = perpRes.AvgPrice
			pos.PerpLeg.OrderIDs = []string{perpID}
			pos.Status = CVSPStatusOpeningSecondLeg
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		e.notifyCritical("跨所spot-perp查单未知", fmt.Sprintf("%s 第一腿(永续)查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("第一腿状态未知,禁止自动回滚: %w", err)
	}
	if err != nil || perpRes.ExecutedQty <= executionQtyTolerance {
		openFailed = true
		if perpRes.ExecutedQty > executionQtyTolerance {
			e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live, perpInstr, perpContract)
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
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, pos.PerpLeg.Qty, live, perpInstr, perpContract)
		return pos, fmt.Errorf("第二腿预检失败: %w", err)
	}

	spotID := cvspLegClientID(idem, "s1")
	hedgeQty := crossVenueLegExecutedCoinQty(perpCaps, perpInstr, perpRes, 0)
	if bal, err := spotV.FetchBalances(ctx); err == nil {
		spotBook, _ := spotTrader.FetchBookTicker(ctx, venue.MarketSpot, sym)
		if affordable := cvspAffordableSpotQty(spotQuoteAvailableUSDT(bal), spotBook.AskPrice, spotInstr.StepSize, slippageBps); affordable+executionQtyTolerance < hedgeQty {
			hedgeQty = affordable
		}
	}
	if !openLegTradableAfterQuantize(hedgeQty, refPrice, spotInstr) {
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, pos.PerpLeg.Qty, live, perpInstr, perpContract)
		return pos, fmt.Errorf("现货腿量化后数量不可交易 qty=%.8f", hedgeQty)
	}
	spotRes, spotErr := e.placeSpotPerpLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideBuy, hedgeQty, spotID, false, live, spotInstr, false)
	if orderStatusIsUnknown(spotRes) {
		e.notifyCritical("跨所spot-perp查单未知", fmt.Sprintf("%s 现货腿查单失败,禁止自动回滚", opp.Symbol), opp.Symbol)
		return pos, fmt.Errorf("现货腿状态未知,禁止自动回滚: %w", spotErr)
	}
	if spotErr != nil || spotRes.ExecutedQty <= executionQtyTolerance {
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live, perpInstr, perpContract)
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, spotV, spotTrader, sym, spotRes.ExecutedQty, live, spotInstr)
		}
		return pos, fmt.Errorf("第二腿(买现货)未足额成交: %w", spotErr)
	}
	match := carryMatchTwoLegOpensNormalized(perpRes, spotRes, perpInstr, spotInstr, perpCaps, spotCaps, carryMinTradableQty(refPrice, spotInstr))
	if !match.OK {
		openFailed = true
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, perpRes.ExecutedQty, live, perpInstr, perpContract)
		if spotRes.ExecutedQty > executionQtyTolerance {
			e.rollbackSpotLeg(ctx, spotV, spotTrader, sym, spotRes.ExecutedQty, live, spotInstr)
		}
		return pos, fmt.Errorf("第二腿(买现货)未足额成交: spot=%.6f perp=%.6f", spotRes.ExecutedQty, perpRes.ExecutedQty)
	}
	if match.ExcessLeg1 > executionQtyTolerance {
		e.rollbackFirstLeg(ctx, pos, perpV, perpTrader, sym, match.ExcessLeg1, live, perpInstr, perpContract)
	}
	if match.ExcessLeg2 > executionQtyTolerance {
		e.rollbackSpotLeg(ctx, spotV, spotTrader, sym, match.ExcessLeg2, live, spotInstr)
	}

	pos.SpotLeg.Qty = match.MatchedQty
	pos.SpotLeg.EntryPrice = spotRes.AvgPrice
	pos.SpotLeg.OrderIDs = []string{spotID}
	bookQty := match.MatchedQty
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
	e.recordOpenOutcome(opp.Symbol, true)
	e.logger.Printf("[跨所spot-perp] 开仓完成 %s: 现货%s/永续%s 各≈%.6f", opp.Symbol, spotVenue, perpVenue, bookQty)
	return pos, nil
}

func (e *CrossVenueSpotPerpExecutor) rollbackFirstLeg(ctx context.Context, pos CrossVenueSpotPerpPosition, perpV venue.Venue, tr cvspTrader, sym venue.Symbol, qty float64, live bool, instr venue.Instrument, contractBased bool) {
	if qty <= executionQtyTolerance {
		return
	}
	id := cvspLegClientID(pos.IdemPrefix, "rb")
	res, err := e.placeSpotPerpLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, qty, id, true, live, instr, contractBased)
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

func (e *CrossVenueSpotPerpExecutor) rollbackSpotLeg(ctx context.Context, spotV venue.Venue, tr cvspTrader, sym venue.Symbol, qty float64, live bool, instr venue.Instrument) {
	if qty <= executionQtyTolerance {
		return
	}
	sellQty := carryResolveSpotSellQty(ctx, spotV, sym, qty, instr.StepSize)
	if sellQty <= executionQtyTolerance {
		if e.logger != nil {
			e.logger.Printf("[跨所spot-perp] 回滚现货 %s 可卖量=0(意图=%.6f),跳过", sym, qty)
		}
		return
	}
	_, _ = e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, sellQty, "cvsp-rbs", false, live, instr, false)
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
	if !live {
		if pos.SpotLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 现货腿 %s 卖出 %.6f 原因:%s", pos.Symbol, pos.SpotVenue, pos.SpotLeg.Qty, reason)
		}
		if pos.PerpLeg.Qty > executionQtyTolerance {
			e.logger.Printf("[dry-run] 本应平仓 %s 永续腿 %s 买回 %.6f 原因:%s", pos.Symbol, pos.PerpVenue, pos.PerpLeg.Qty, reason)
		}
		appendDryRunEvent(e.recordDir, "cvsp_close", map[string]any{"symbol": pos.Symbol, "reason": reason})
		return nil
	}
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

	pos.CloseAttempts++
	attempt := pos.CloseAttempts
	if pos.PerpNextFundingTime.IsZero() {
		e.syncPerpFundingMeta(ctx, &pos)
	}
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
	spotInstr := e.lookupLegInstrument(ctx, pos.SpotVenue, sym, venue.MarketSpot)
	perpInstr := e.lookupLegInstrument(ctx, pos.PerpVenue, sym, venue.MarketPerp)
	perpContract := perpV != nil && perpV.Capabilities().ContractBasedPerp
	if closePerpQty > executionQtyTolerance {
		id := cvspLegClientID(pos.IdemPrefix, fmt.Sprintf("cp%d", attempt))
		if res, err := e.placeCVSPCloseLegExec(ctx, perpTrader, venue.MarketPerp, sym, venue.SideBuy, closePerpQty, id, true, live, attempt, perpInstr, perpContract); err == nil {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
		}
	}
	if closeSpotQty > executionQtyTolerance {
		id := cvspLegClientID(pos.IdemPrefix, fmt.Sprintf("cs%d", attempt))
		if res, err := e.placeCVSPCloseLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideSell, closeSpotQty, id, false, live, attempt, spotInstr, false); err == nil {
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
			e.finalizeClosedPosition(pos, live)
			return nil
		}
		pos.UpdatedAt = e.nowFn()
		_ = e.persistPosition(pos)
		return fmt.Errorf("平仓跳过: %s 实测为0,保留台账待下轮", pos.Symbol)
	}

	spotOpen := spotQty > executionQtyTolerance
	perpOpen := perpQty > executionQtyTolerance
	if dust := evaluatePositiveCarryOrphan(ctx, e.cfg.CrossVenueExecutor, e.venues, pos.Symbol, pos.SpotVenue, pos.PerpVenue, spotOpen, perpOpen, spotQty, perpQty, pos.SpotLeg.EntryPrice); dust.IsDustOrphan {
		if carryShouldDeferDustOrphanClose(e.nowFn(), pos.PerpNextFundingTime) {
			pos.UpdatedAt = e.nowFn()
			_ = e.persistPosition(pos)
			return fmt.Errorf("dust_orphan 未到 funding 结算,保留持仓待下轮")
		}
		if live && spotOpen {
			id := cvspLegClientID(pos.IdemPrefix, "dust")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
			_, _ = e.placeCVSPCloseLegExec(ctx, spotTrader, venue.MarketSpot, sym, venue.SideSell, qty, id, false, live, attempt, spotInstr, false)
		}
		pos.Status = CVSPStatusClosed
		pos.DustResidualCostUSDT = applyCarryDustResidualCost(dust.RemainingNotionalUSDT)
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, reason), dust.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "跨所spot-perp", pos.Symbol, dust.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return nil
	}

	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	return fmt.Errorf("平仓未完成: %s 第 %d 轮仍有剩余腿", pos.Symbol, attempt)
}

func (e *CrossVenueSpotPerpExecutor) finalizeClosedPosition(pos CrossVenueSpotPerpPosition, live bool) {
	if !realLedgerWritesAllowed(live) {
		return
	}
	if e.store != nil {
		rec := buildCrossVenueSpotPerpSettlement(pos, e.nowFn())
		_ = e.store.appendSettlement(rec)
		appendAttributionRealizedBestEffort(e.recordDir, realizedRecordFromCVSPSettlement(rec))
	}
	_ = e.removePosition(pos)
}

func cvspSpotHoldingQty(bal venue.Balances, symbol string) float64 {
	base := baseAssetFromCanonicalSymbol(symbol)
	return spotLongQtyFromBalance(bal, base)
}

func (e *CrossVenueSpotPerpExecutor) shouldClose(ctx context.Context, pos *CrossVenueSpotPerpPosition) string {
	if pos == nil {
		return ""
	}
	if reason := carryHardStopCloseReason(ctx, e.venues, true, pos.Symbol, pos.SpotVenue, pos.PerpVenue, pos.SpotLeg.EntryPrice, pos.PerpLeg.EntryPrice); reason != "" {
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

func (e *CrossVenueSpotPerpExecutor) managePosition(ctx context.Context, pos CrossVenueSpotPerpPosition, live bool) (CrossVenueSpotPerpPosition, bool) {
	if !live {
		if reason := e.shouldClose(ctx, &pos); reason != "" {
			logDryRunClose(e.logger, "跨所spot-perp", pos.Symbol, reason)
		}
		return pos, false
	}
	if pos.Status == CVSPStatusClosing {
		_ = e.closePosition(ctx, pos, live, nonEmpty(pos.CloseReason, "继续平仓"))
		return pos, false
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
		return pos, false
	}
	if !spotOpen && !perpOpen {
		pos.Status = CVSPStatusClosed
		pos.UpdatedAt = e.nowFn()
		e.finalizeClosedPosition(pos, live)
		return pos, false
	}

	e.syncPerpFundingMeta(ctx, &pos)
	dev := qtyDeviation(spotQty, perpQty)
	if carryShouldRebalanceHedge(dev, pos.LastHedgeRebalanceAt, e.nowFn()) {
		e.rebalanceCVSPHedge(ctx, &pos, spotQty, perpQty, live)
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

func (e *CrossVenueSpotPerpExecutor) manageOpenPositions(ctx context.Context, live bool) {
	for _, pos := range activeCrossVenueSpotPerpPositions(e.snapshotPositions()) {
		_, shouldPersist := e.managePosition(ctx, pos, live)
		// managePosition 子路径已自行 persist 或 finalize;不可再 upsert 入参快照,否则复活已平仓仓。
		if shouldPersist {
			_ = e.persistPosition(pos)
		}
	}
}

func (e *CrossVenueSpotPerpExecutor) syncPerpFundingMeta(ctx context.Context, pos *CrossVenueSpotPerpPosition) {
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
	interval := pos.PerpIntervalHours
	if interval <= 0 {
		interval = 8
	}
	hourlyRate := pos.PerpFundingRate / float64(interval)
	delta := notional * hourlyRate * elapsedHours
	pos.LastAccrualAt = now
	return pos.EstFundingUSDT + delta
}

func (e *CrossVenueSpotPerpExecutor) rebalanceCVSPHedge(ctx context.Context, pos *CrossVenueSpotPerpPosition, spotQty, perpQty float64, live bool) {
	if pos == nil {
		return
	}
	reduceSpot, delta := carryPositiveRebalancePlan(spotQty, perpQty)
	if delta <= executionQtyTolerance {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	now := e.nowFn()
	spotInstr := e.lookupLegInstrument(ctx, pos.SpotVenue, sym, venue.MarketSpot)
	perpInstr := e.lookupLegInstrument(ctx, pos.PerpVenue, sym, venue.MarketPerp)
	perpContract := e.venues[pos.PerpVenue] != nil && e.venues[pos.PerpVenue].Capabilities().ContractBasedPerp
	if reduceSpot {
		if tr, ok := e.venues[pos.SpotVenue].(cvspTrader); ok && live {
			id := cvspLegClientID(pos.IdemPrefix, "rb-s")
			if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, delta, id, false, live, spotInstr, false); res.ExecutedQty > executionQtyTolerance {
				pos.SpotLeg.Qty = math.Max(pos.SpotLeg.Qty-res.ExecutedQty, 0)
			}
		}
	} else if tr, ok := e.venues[pos.PerpVenue].(cvspTrader); ok && live {
		id := cvspLegClientID(pos.IdemPrefix, "rb-p")
		if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, delta, id, true, live, perpInstr, perpContract); res.ExecutedQty > executionQtyTolerance {
			pos.PerpLeg.Qty = math.Max(pos.PerpLeg.Qty-res.ExecutedQty, 0)
		}
	}
	pos.LastHedgeRebalanceAt = now
	pos.UpdatedAt = now
	_ = e.persistPosition(*pos)
	if e.logger != nil {
		e.logger.Printf("[跨所spot-perp] %s 对冲再平衡:偏差%.1f%% 减%s %.6f",
			pos.Symbol, qtyDeviation(spotQty, perpQty)*100, map[bool]string{true: "现货", false: "永续"}[reduceSpot], delta)
	}
}

func (e *CrossVenueSpotPerpExecutor) handleOrphanLeg(ctx context.Context, pos CrossVenueSpotPerpPosition, spotOpen, perpOpen, live bool, spotQty, perpQty float64) {
	decision := evaluatePositiveCarryOrphan(ctx, e.cfg.CrossVenueExecutor, e.venues, pos.Symbol, pos.SpotVenue, pos.PerpVenue, spotOpen, perpOpen, spotQty, perpQty, pos.SpotLeg.EntryPrice)
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
		if live && spotOpen {
			if tr, ok := e.venues[pos.SpotVenue].(cvspTrader); ok {
				id := cvspLegClientID(pos.IdemPrefix, "dust")
				qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
				_, _ = e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, id, false, live, spotInstr, false)
			}
		}
		pos.Status = CVSPStatusClosed
		pos.DustResidualCostUSDT = applyCarryDustResidualCost(decision.RemainingNotionalUSDT)
		pos.CloseReason = appendDustCloseReason(nonEmpty(pos.CloseReason, "dust_orphan"), decision.RemainingNotionalUSDT)
		pos.UpdatedAt = e.nowFn()
		logDustOrphanClosed(e.logger, "跨所spot-perp", pos.Symbol, decision.RemainingNotionalUSDT)
		e.finalizeClosedPosition(pos, live)
		return
	}
	pos.Status = CVSPStatusOrphanSingleLeg
	pos.UpdatedAt = e.nowFn()
	_ = e.persistPosition(pos)
	if perpOpen && !spotOpen {
		if tr, ok := e.venues[pos.PerpVenue].(cvspTrader); ok && live {
			id := cvspLegClientID(pos.IdemPrefix, "op")
			if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketPerp, sym, venue.SideBuy, absQty(perpQty), id, true, live, perpInstr, perpContract); res.ExecutedQty > executionQtyTolerance {
				_ = e.persistPosition(pos)
			}
		}
	} else if spotOpen && !perpOpen {
		if tr, ok := e.venues[pos.SpotVenue].(cvspTrader); ok && live {
			id := cvspLegClientID(pos.IdemPrefix, "os")
			qty := ledgerCappedCloseQty(pos.SpotLeg.Qty, spotQty)
			if res, _ := e.placeSpotPerpLegExec(ctx, tr, venue.MarketSpot, sym, venue.SideSell, qty, id, false, live, spotInstr, false); res.ExecutedQty > executionQtyTolerance {
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
		e.finalizeClosedPosition(pos, true)
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

	e.reconcileUntrackedExposureOnStart(ctx, ps, live)

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

func (e *CrossVenueSpotPerpExecutor) reconcileUntrackedExposureOnStart(ctx context.Context, ps []CrossVenueSpotPerpPosition, live bool) {
	cfg := e.cfg.CrossVenueExecutor
	untracked := carryScanUntrackedCVSPLegs(ctx, cfg, e.recordDir, e.logger, e.venues, ps)
	if len(untracked) == 0 {
		return
	}
	action := crossVenueUntrackedExposureAction(cfg)
	for _, leg := range untracked {
		e.logger.Printf("[跨所spot-perp] reconcile: venue=%s 未登记 %s %s≈%.6f", leg.Venue, leg.Market, leg.Symbol, leg.Qty)
	}
	shouldHalt := true
	switch action {
	case crossVenueRecoveryActionFlatten:
		if !cfg.LiveTradingEnabled {
			e.notifyCritical("跨所spot-perp冷启动", "检测到未登记真实持仓,flatten 需 live", "")
		} else {
			shouldHalt = false
			for _, leg := range untracked {
				e.flattenUntrackedLeg(ctx, leg, live)
			}
			e.notifyCritical("跨所spot-perp冷启动 flatten", fmt.Sprintf("已对 %d 条未登记腿执行 flatten", len(untracked)), "")
		}
	default:
		e.notifyCritical("跨所spot-perp冷启动", fmt.Sprintf("检测到 %d 条未登记真实持仓,已 halt 待人工确认", len(untracked)), "")
	}
	if shouldHalt {
		e.openingsEnabled.Store(false)
	}
}

func (e *CrossVenueSpotPerpExecutor) flattenUntrackedLeg(ctx context.Context, leg carryUntrackedLeg, live bool) {
	if !live || leg.Qty <= executionQtyTolerance {
		return
	}
	if carryShouldSkipUntrackedFlatten(e.recordDir, e.logger, leg) {
		return
	}
	v := e.venues[leg.Venue]
	tr, ok := v.(cvspTrader)
	if !ok || tr == nil {
		return
	}
	sym := venue.Symbol(leg.Symbol)
	instr := e.lookupLegInstrument(ctx, leg.Venue, sym, leg.Market)
	contractBased := leg.Market == venue.MarketPerp && v.Capabilities().ContractBasedPerp
	id := fmt.Sprintf("cvsp-reconcile-flat-%s-%d", leg.Symbol, e.nowFn().UnixNano())
	res, err := e.placeSpotPerpLegExec(ctx, tr, leg.Market, sym, leg.Side, leg.Qty, id, leg.ReduceOnly, live, instr, contractBased)
	carryAfterUntrackedFlatten(ctx, e.recordDir, e.logger, e.venues, leg, res, err)
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
		return
	}
	if spotOpen && perpOpen {
		if carryReconcileAdoptPositiveLegQtys(&pos, spotQty, perpQty, e.nowFn(), e.logger, "跨所spot-perp") {
			_ = e.persistPosition(pos)
		}
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

// ListActivePositions 返回非 closed 状态的持仓(只读,供面板/API)。
func (e *CrossVenueSpotPerpExecutor) ListActivePositions() []CrossVenueSpotPerpPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		ps = e.snapshotPositions()
	}
	return activeCrossVenueSpotPerpPositions(ps)
}

// ListAllPositions 返回台账全部持仓(只读)。
func (e *CrossVenueSpotPerpExecutor) ListAllPositions() []CrossVenueSpotPerpPosition {
	if e == nil {
		return nil
	}
	ps, err := e.store.load()
	if err != nil || ps == nil {
		return e.snapshotPositions()
	}
	return ps
}

func (e *CrossVenueSpotPerpExecutor) listPositionsForAPI(statusFilter string) []CrossVenueSpotPerpPosition {
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
func (e *CrossVenueSpotPerpExecutor) ListRecentSettlements(limit int) ([]CrossVenueSpotPerpSettlement, error) {
	if e == nil {
		return nil, nil
	}
	return loadTailJSONLFile[CrossVenueSpotPerpSettlement](e.SettlementsPath(), time.Time{}, limit)
}
