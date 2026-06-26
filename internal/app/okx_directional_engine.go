package app

// okx_directional_engine.go OKX 方向交易引擎 plumbing:拉资费/K线、调用纯信号、下单与落盘。
// 纯信号/风控见 okx_directional_signal.go;本文件不做 regime/资费闸重算。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/okx"
)

const (
	okxDirectionalPositionFile     = "position.json"
	okxDirectionalSettlementsFile  = "settlements.jsonl"
	okxDirectionalTakerSlippageBps = 40.0
	okxDirectionalMakerRounds      = 3
	okxDirectionalMakerInterval    = 2 * time.Second
	okxDirectionalMakerDeadline    = 12 * time.Second
	okxDirectionalMakerTakerBuf    = 3 * time.Second
)

// okxDirectionalPosition 是当前唯一持仓(内存 + position.json)。
type okxDirectionalPosition struct {
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"` // long / short
	EntryPrice    float64   `json:"entry_price"`
	Qty           float64   `json:"qty"`
	NotionalUSDT  float64   `json:"notional_usdt"`
	OpenedAt      time.Time `json:"opened_at"`
	Regime        string    `json:"regime"`
	FundingAtOpen float64   `json:"funding_at_open"`
}

func (p *okxDirectionalPosition) directionSide() DirectionSide {
	if p == nil {
		return DirectionFlat
	}
	switch strings.ToLower(strings.TrimSpace(p.Side)) {
	case "long":
		return DirectionLong
	case "short":
		return DirectionShort
	default:
		return DirectionFlat
	}
}

func (p *okxDirectionalPosition) isOpen() bool {
	return p != nil && strings.TrimSpace(p.Symbol) != "" && p.directionSide() != DirectionFlat
}

// okxDirectionalSettlement 平仓结算一行(settlements.jsonl)。
type okxDirectionalSettlement struct {
	Time          time.Time `json:"time"`
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	Entry         float64   `json:"entry"`
	Exit          float64   `json:"exit"`
	NotionalUSDT  float64   `json:"notional"`
	RealizedUSDT  float64   `json:"realized_usdt"`
	HoldMinutes   int       `json:"hold_minutes"`
	Regime        string    `json:"regime"`
	Reason        string    `json:"reason"`
	FundingAtOpen float64   `json:"funding_at_open"`
}

// OKXDirectionalStatus 只读快照(面板/监控用)。
type OKXDirectionalStatus struct {
	Enabled             bool
	Live                bool
	Active              bool    // 页面"开始/停止"
	TradingCapitalUSDT  float64 // 页面设的交易仓本金
	StopLossPct         float64 // 页面设的止损%(本金基准)
	Leverage            int
	HoldingSymbol       string
	Side                string
	EntryPrice          float64
	Qty                 float64
	NotionalUSDT        float64
	UnrealizedPctApprox float64 // 本金口径(已×杠杆)
	OpenedAt            time.Time
	DailyRealizedUSDT   float64
	DailyLossLimitUSDT  float64
	HaltedByDailyLoss   bool
	LastError           string
}

// OKXDirectionalEngine OKX 方向引擎(独立于 carry)。
type OKXDirectionalEngine struct {
	cfg       OKXDirectionalConfig
	netCfg    EngineConfig
	okxV      venue.Venue
	okxA      *okx.Adapter
	logger    *log.Logger
	recordDir string
	nowFn     func() time.Time

	mu                sync.RWMutex
	pos               *okxDirectionalPosition
	dailyRealizedUSDT float64
	dayStartCST       time.Time
	cooldownUntil     map[string]time.Time
	lastError         string
	positionPath      string
	settlementsPath   string
	controlPath       string
	control           okxDirectionalControl
}

// okxDirectionalControl 页面可控运行态(records/okx_directional/control.json):
// 用户在页面只设"交易仓本金 + 止损% + 开始/停止";杠杆固定 5X、无日熔断。
type okxDirectionalControl struct {
	Active             bool      `json:"active"`
	TradingCapitalUSDT float64   `json:"trading_capital_usdt"`
	StopLossPct        float64   `json:"stop_loss_pct"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// NewOKXDirectionalEngine 从 Orchestrator 解析 OKX 适配器并加载持久化持仓。
func NewOKXDirectionalEngine(cfg EngineConfig, orch *Orchestrator, recordDir string, logger *log.Logger) (*OKXDirectionalEngine, error) {
	v := crossVenueAdapterFromOrchestrator(orch, okx.VenueName)
	if v == nil {
		return nil, fmt.Errorf("okx 适配器未注册")
	}
	okxA, ok := v.(*okx.Adapter)
	if !ok {
		return nil, fmt.Errorf("okx 适配器类型断言失败(期望 *okx.Adapter)")
	}
	if logger == nil {
		logger = log.Default()
	}
	dir := okxDirectionalRecordDir(recordDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 okx_directional 目录失败: %w", err)
	}
	e := &OKXDirectionalEngine{
		cfg:             cfg.OKXDirectional,
		netCfg:          cfg,
		okxV:            v,
		okxA:            okxA,
		logger:          logger,
		recordDir:       dir,
		nowFn:           time.Now,
		cooldownUntil:   make(map[string]time.Time),
		positionPath:    filepath.Join(dir, okxDirectionalPositionFile),
		settlementsPath: filepath.Join(dir, okxDirectionalSettlementsFile),
		controlPath:     filepath.Join(dir, "control.json"),
	}
	// 控制态:优先读持久化(页面设过的本金/止损/启停);无则用 config 默认且 active=false(必须页面点开始)。
	e.control = okxDirectionalControl{Active: false, TradingCapitalUSDT: cfg.OKXDirectional.TradingCapitalUSDT, StopLossPct: cfg.OKXDirectional.StopLossPct}
	if c, ok := e.loadControl(); ok {
		e.control = c
	}
	if pos, err := e.loadPosition(); err != nil {
		return nil, err
	} else if pos != nil && pos.isOpen() {
		e.pos = pos
	}
	return e, nil
}

func okxDirectionalRecordDir(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, "okx_directional")
}

// Run 主循环:日界刷新 → 持仓管理 / 扫描开仓。
func (e *OKXDirectionalEngine) Run(ctx context.Context) {
	interval := time.Duration(e.cfg.LoopIntervalSeconds) * time.Second
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	e.logger.Printf("[OKX方向] 引擎启动 live=%v 周期=%s", e.cfg.LiveTradingEnabled, interval)
	for {
		e.runOnce(ctx)
		select {
		case <-ctx.Done():
			e.logger.Printf("[OKX方向] 引擎退出: %v", ctx.Err())
			return
		case <-time.After(interval):
		}
	}
}

func (e *OKXDirectionalEngine) runOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.setLastError(fmt.Sprintf("panic: %v", r))
			e.logger.Printf("[OKX方向] 循环 panic 已恢复: %v", r)
		}
	}()
	e.refreshCSTDay()
	// 持仓始终管理(即便页面已停止,也继续看护止损/止盈/反转,绝不撒手不管)。
	if e.holding() {
		e.manage(ctx)
		return
	}
	// 开新仓须页面"开始"(control.Active);日熔断按用户要求默认关闭(DailyLossLimitUSDT=0 → 永不熔断)。
	ctrl := e.getControl()
	if ctrl.Active && !directionalDailyLossHalted(e.dailyRealizedUSDT, e.cfg.DailyLossLimitUSDT) && e.concurrencyAllows() {
		e.scanAndOpen(ctx)
	}
}

func (e *OKXDirectionalEngine) refreshCSTDay() {
	now := e.nowFn().UTC()
	dayStart := beijingCSTDayStart(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dayStartCST.IsZero() || !e.dayStartCST.Equal(dayStart) {
		e.dayStartCST = dayStart
		e.dailyRealizedUSDT = 0
	}
}

func (e *OKXDirectionalEngine) holding() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.pos != nil && e.pos.isOpen()
}

func (e *OKXDirectionalEngine) concurrencyAllows() bool {
	maxC := e.cfg.MaxConcurrent
	if maxC <= 0 {
		maxC = 1
	}
	e.mu.RLock()
	open := 0
	if e.pos != nil && e.pos.isOpen() {
		open = 1
	}
	e.mu.RUnlock()
	return open < maxC
}

func (e *OKXDirectionalEngine) setLastError(msg string) {
	e.mu.Lock()
	e.lastError = msg
	e.mu.Unlock()
}

func (e *OKXDirectionalEngine) clearLastError() {
	e.mu.Lock()
	e.lastError = ""
	e.mu.Unlock()
}

// OKXDirectionalStatus 返回并发安全的只读快照。
func (e *OKXDirectionalEngine) OKXDirectionalStatus() OKXDirectionalStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	capUSDT := e.control.TradingCapitalUSDT
	if capUSDT <= 0 {
		capUSDT = e.cfg.TradingCapitalUSDT
	}
	stop := e.control.StopLossPct
	if stop <= 0 {
		stop = e.cfg.StopLossPct
	}
	st := OKXDirectionalStatus{
		Enabled:            e.cfg.Enabled,
		Live:               e.cfg.LiveTradingEnabled,
		Active:             e.control.Active,
		TradingCapitalUSDT: capUSDT,
		StopLossPct:        stop,
		Leverage:           e.leverage(),
		DailyRealizedUSDT:  e.dailyRealizedUSDT,
		DailyLossLimitUSDT: e.cfg.DailyLossLimitUSDT,
		HaltedByDailyLoss:  directionalDailyLossHalted(e.dailyRealizedUSDT, e.cfg.DailyLossLimitUSDT),
		LastError:          e.lastError,
	}
	if e.pos != nil && e.pos.isOpen() {
		st.HoldingSymbol = e.pos.Symbol
		st.Side = e.pos.Side
		st.EntryPrice = e.pos.EntryPrice
		st.Qty = e.pos.Qty
		st.NotionalUSDT = e.pos.NotionalUSDT
		st.OpenedAt = e.pos.OpenedAt
	}
	return st
}

// selectDirectionalUniverse 纯函数:资费阈值 + 冷却过滤,按 |费率| 降序取 TopN。
func selectDirectionalUniverse(snaps []venue.FundingSnapshot, cooldown map[string]time.Time, now time.Time, cfg OKXDirectionalConfig) []string {
	minAbs := cfg.MinFundingAbsPerInterval
	if minAbs < 0 {
		minAbs = 0
	}
	topN := cfg.UniverseTopN
	if topN <= 0 {
		topN = 12
	}
	type item struct {
		sym  string
		absR float64
	}
	var cands []item
	for _, s := range snaps {
		sym := string(s.Symbol)
		if sym == "" || !isUSDTPerpSymbol(sym) {
			continue
		}
		if math.Abs(s.LastFundingRate) < minAbs {
			continue
		}
		if until, ok := cooldown[sym]; ok && now.Before(until) {
			continue
		}
		cands = append(cands, item{sym: sym, absR: math.Abs(s.LastFundingRate)})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].absR == cands[j].absR {
			return cands[i].sym < cands[j].sym
		}
		return cands[i].absR > cands[j].absR
	})
	if len(cands) > topN {
		cands = cands[:topN]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.sym
	}
	return out
}

func isUSDTPerpSymbol(sym string) bool {
	sym = strings.ToUpper(strings.TrimSpace(sym))
	return strings.HasSuffix(sym, "-USDT") || strings.HasSuffix(sym, "USDT")
}

// directionalDailyLossHalted 当日累计已实现亏损达熔断线。
func directionalDailyLossHalted(dailyRealized, limitUSDT float64) bool {
	if limitUSDT <= 0 {
		return false
	}
	return dailyRealized <= -limitUSDT
}

// directionalUPnLPct 未实现盈亏占名义(含方向符号,正=盈)。
func directionalUPnLPct(side DirectionSide, entry, mid float64) float64 {
	if entry <= 0 || mid <= 0 {
		return 0
	}
	switch side {
	case DirectionLong:
		return (mid - entry) / entry
	case DirectionShort:
		return (entry - mid) / entry
	default:
		return 0
	}
}

func (e *OKXDirectionalEngine) scanAndOpen(ctx context.Context) {
	snaps, err := e.okxV.FetchFundingSnapshot(ctx)
	if err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] 拉资费快照失败: %v", err)
		return
	}
	e.mu.RLock()
	cooldown := make(map[string]time.Time, len(e.cooldownUntil))
	for k, v := range e.cooldownUntil {
		cooldown[k] = v
	}
	e.mu.RUnlock()

	now := e.nowFn().UTC()
	universe := selectDirectionalUniverse(snaps, cooldown, now, e.cfg)
	if len(universe) == 0 {
		e.clearLastError()
		return
	}
	rateBySym := make(map[string]float64, len(snaps))
	for _, s := range snaps {
		rateBySym[string(s.Symbol)] = s.LastFundingRate
	}

	type pick struct {
		sym    string
		signal DirectionalSignal
		rate   float64
	}
	var best *pick
	costFloor := e.cfg.CostFloorBps
	if costFloor <= 0 {
		costFloor = 60
	}
	// 漏斗诊断:统计每轮"宇宙→有方向→过成本地板"各级数量,并把 flat 原因分类(样本不足/资费否决/无信号),便于精准优化。
	var nSignal, nBelowFloor int
	var nInsufficient, nVeto, nNoSig int
	var floorEg, vetoEg, noSigEg string
	for _, sym := range universe {
		closes, cerr := e.fetchCloses(ctx, venue.Symbol(sym))
		if cerr != nil {
			e.logger.Printf("[OKX方向] %s K线失败: %v", sym, cerr)
			continue
		}
		rate := rateBySym[sym]
		sig := computeDirectionalSignal(closes, rate, e.cfg)
		if sig.Side == DirectionFlat {
			switch {
			case strings.Contains(sig.Reason, "样本不足"):
				nInsufficient++
			case strings.Contains(sig.Reason, "资费逆"):
				nVeto++
				vetoEg = sym + ":" + sig.Reason
			default:
				nNoSig++
				if noSigEg == "" {
					noSigEg = sym + ":" + sig.Reason
				}
			}
			continue
		}
		nSignal++
		if sig.ExpectedMoveBps < costFloor {
			nBelowFloor++
			floorEg = fmt.Sprintf("%s %s move=%.0fbps<%.0f", sym, sig.Side.String(), sig.ExpectedMoveBps, costFloor)
			continue
		}
		if best == nil || sig.Conviction > best.signal.Conviction {
			best = &pick{sym: sym, signal: sig, rate: rate}
		}
	}
	if best == nil {
		e.clearLastError()
		e.logger.Printf("[OKX方向][漏斗] 宇宙=%d 有方向=%d 过地板=0 | flat分解: 样本不足=%d 资费否决=%d 无信号=%d | 被成本地板挡=%d(地板=%.0fbps) | 例[veto=%s][nosig=%s][floor=%s]",
			len(universe), nSignal, nInsufficient, nVeto, nNoSig, nBelowFloor, costFloor, vetoEg, noSigEg, floorEg)
		return
	}
	e.openPosition(ctx, best.sym, best.signal, best.rate)
}

func (e *OKXDirectionalEngine) fetchCloses(ctx context.Context, sym venue.Symbol) ([]float64, error) {
	bar := strings.TrimSpace(e.cfg.CandleBar)
	if bar == "" {
		bar = "5m"
	}
	limit := e.cfg.CandleLimit
	if limit <= 0 {
		limit = 120
	}
	candles, err := e.okxA.FetchCandles(ctx, venue.MarketPerp, sym, bar, limit)
	if err != nil {
		return nil, err
	}
	closes := make([]float64, 0, len(candles))
	for _, c := range candles {
		if c.Close > 0 {
			closes = append(closes, c.Close)
		}
	}
	return closes, nil
}

func (e *OKXDirectionalEngine) openPosition(ctx context.Context, sym string, sig DirectionalSignal, fundingRate float64) {
	book, err := e.okxV.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(sym))
	if err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 盘口失败: %v", sym, err)
		return
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	if mid <= 0 {
		e.setLastError("mid<=0")
		return
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.okxV, venue.Symbol(sym), venue.MarketPerp)
	if !ok {
		e.setLastError("instrument 缺失")
		e.logger.Printf("[OKX方向] %s 无 instrument 元数据,跳过开仓", sym)
		return
	}
	caps := e.okxV.Capabilities()
	// 名义 = 交易仓本金(页面设) × 杠杆(5X)。用户用本金大小控风险。
	ctrl := e.getControl()
	notional := ctrl.TradingCapitalUSDT * float64(e.leverage())
	if notional <= 0 {
		e.logger.Printf("[OKX方向] %s 交易仓本金<=0,跳过开仓", sym)
		return
	}
	qty := normalizeVenueQty(notional/mid, instr, caps.ContractBasedPerp)
	if !openLegTradableAfterQuantize(qty, mid, instr) {
		e.logger.Printf("[OKX方向] %s 量化后不可交易 qty=%.8f mid=%.6f notional=%.2f", sym, qty, mid, notional)
		return
	}
	var side venue.Side
	switch sig.Side {
	case DirectionLong:
		side = venue.SideBuy
	case DirectionShort:
		side = venue.SideSell
	default:
		return
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟开 %s %s regime=%s conviction=%.2f move=%.1fbps 原因=%s",
			sym, sig.Side.String(), sig.Regime.String(), sig.Conviction, sig.ExpectedMoveBps, sig.Reason)
		return
	}
	if err := e.okxV.SetLeverage(ctx, venue.Symbol(sym), e.leverage()); err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 设杠杆失败: %v", sym, err)
		return
	}
	clientID := okxDirectionalClientID(sym, "open")
	res, oerr := e.placeDirectionalLeg(ctx, venue.Symbol(sym), side, qty, clientID, false, instr, caps.ContractBasedPerp)
	if oerr != nil || res.ExecutedQty <= executionQtyTolerance {
		msg := "开仓未成交"
		if oerr != nil {
			msg = oerr.Error()
		}
		e.setLastError(msg)
		e.logger.Printf("[OKX方向] %s 开仓失败: %v", sym, oerr)
		return
	}
	fillPrice := res.AvgPrice
	if fillPrice <= 0 {
		fillPrice = mid
	}
	pos := &okxDirectionalPosition{
		Symbol:        sym,
		Side:          sig.Side.String(),
		EntryPrice:    fillPrice,
		Qty:           res.ExecutedQty,
		NotionalUSDT:  notional,
		OpenedAt:      e.nowFn().UTC(),
		Regime:        sig.Regime.String(),
		FundingAtOpen: fundingRate,
	}
	e.mu.Lock()
	e.pos = pos
	e.mu.Unlock()
	if err := e.savePosition(pos); err != nil {
		e.logger.Printf("[OKX方向] 持仓落盘失败: %v", err)
	}
	e.clearLastError()
	e.logger.Printf("[OKX方向] 已开 %s %s qty=%.8f @%.6f regime=%s", sym, pos.Side, pos.Qty, pos.EntryPrice, pos.Regime)
}

func (e *OKXDirectionalEngine) manage(ctx context.Context) {
	e.mu.RLock()
	pos := e.pos
	e.mu.RUnlock()
	if pos == nil || !pos.isOpen() {
		return
	}
	sym := venue.Symbol(pos.Symbol)
	book, err := e.okxV.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 管理盘口失败: %v", pos.Symbol, err)
		return
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	if mid <= 0 {
		return
	}
	side := pos.directionSide()
	uPnLNotional := directionalUPnLPct(side, pos.EntryPrice, mid) // 价格移动口径(占名义)
	lev := float64(e.leverage())
	uPnLAccount := uPnLNotional * lev // 交易仓本金口径:止损/止盈按本金 %(5X 下 ×5)
	holdMin := int(e.nowFn().UTC().Sub(pos.OpenedAt).Minutes())
	if holdMin < 0 {
		holdMin = 0
	}
	closes, cerr := e.fetchCloses(ctx, sym)
	if cerr != nil {
		e.setLastError(cerr.Error())
		e.logger.Printf("[OKX方向] %s 管理K线失败: %v", pos.Symbol, cerr)
		return
	}
	rate := pos.FundingAtOpen
	if snaps, serr := e.okxV.FetchFundingSnapshot(ctx); serr == nil {
		for _, s := range snaps {
			if string(s.Symbol) == pos.Symbol {
				rate = s.LastFundingRate
				break
			}
		}
	}
	fresh := computeDirectionalSignal(closes, rate, e.cfg)
	// 止损/止盈以"交易仓本金"为基准(页面设的止损%);故传本金口径 uPnL + 页面止损%。
	effCfg := e.cfg
	effCfg.StopLossPct = e.getControl().StopLossPct
	shouldClose, reason := directionalShouldClose(uPnLAccount, holdMin, side, fresh, effCfg)
	if !shouldClose {
		e.clearLastError()
		return
	}
	e.closePosition(ctx, pos, mid, uPnLNotional, holdMin, reason)
}

func (e *OKXDirectionalEngine) closePosition(ctx context.Context, pos *okxDirectionalPosition, mid, uPnLPct float64, holdMin int, reason string) {
	var closeSide venue.Side
	switch pos.directionSide() {
	case DirectionLong:
		closeSide = venue.SideSell
	case DirectionShort:
		closeSide = venue.SideBuy
	default:
		return
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟平 %s %s 原因=%s uPnL=%.3f%% hold=%dm",
			pos.Symbol, pos.Side, reason, uPnLPct*100, holdMin)
		return
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.okxV, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		e.setLastError("instrument 缺失")
		return
	}
	caps := e.okxV.Capabilities()
	clientID := okxDirectionalClientID(pos.Symbol, "close")
	res, cerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), closeSide, pos.Qty, clientID, true, instr, caps.ContractBasedPerp)
	exitPrice := mid
	if res.AvgPrice > 0 {
		exitPrice = res.AvgPrice
	}
	if cerr != nil || res.ExecutedQty <= executionQtyTolerance {
		msg := "平仓未成交"
		if cerr != nil {
			msg = cerr.Error()
		}
		e.setLastError(msg)
		e.logger.Printf("[OKX方向] %s 平仓失败: %v", pos.Symbol, cerr)
		return
	}
	realized := uPnLPct * pos.NotionalUSDT
	if exitPrice > 0 && pos.EntryPrice > 0 {
		realized = directionalUPnLPct(pos.directionSide(), pos.EntryPrice, exitPrice) * pos.NotionalUSDT
	}
	settle := okxDirectionalSettlement{
		Time:          e.nowFn().UTC(),
		Symbol:        pos.Symbol,
		Side:          pos.Side,
		Entry:         pos.EntryPrice,
		Exit:          exitPrice,
		NotionalUSDT:  pos.NotionalUSDT,
		RealizedUSDT:  realized,
		HoldMinutes:   holdMin,
		Regime:        pos.Regime,
		Reason:        reason,
		FundingAtOpen: pos.FundingAtOpen,
	}
	if err := appendJSONLRecord(e.settlementsPath, settle); err != nil {
		e.logger.Printf("[OKX方向] 结算落盘失败: %v", err)
	}
	coolMin := e.cfg.SymbolCooldownMinutes
	if coolMin <= 0 {
		coolMin = 30
	}
	e.mu.Lock()
	e.dailyRealizedUSDT += realized
	e.cooldownUntil[pos.Symbol] = e.nowFn().UTC().Add(time.Duration(coolMin) * time.Minute)
	e.pos = nil
	e.mu.Unlock()
	if err := e.savePosition(nil); err != nil {
		e.logger.Printf("[OKX方向] 清空持仓落盘失败: %v", err)
	}
	e.clearLastError()
	e.logger.Printf("[OKX方向] 已平 %s %s 原因=%s 实现≈%.4f USDT", pos.Symbol, pos.Side, reason, realized)
}

func (e *OKXDirectionalEngine) leverage() int {
	if e.cfg.Leverage > 0 {
		return e.cfg.Leverage
	}
	return 5
}

func okxDirectionalClientID(sym, leg string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(sym, "-", ""), "_", "")
	id := fmt.Sprintf("okxdir-%s-%s-%d", base, leg, time.Now().UTC().Unix())
	if len(id) > 36 {
		id = id[len(id)-36:]
	}
	return id
}

// placeDirectionalLeg maker-first 追价,未成交则小滑点 IOC 兜底。
func (e *OKXDirectionalEngine) placeDirectionalLeg(
	ctx context.Context,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	instr venue.Instrument,
	contractBased bool,
) (venue.OrderResult, error) {
	deadline := e.nowFn().UTC().Add(okxDirectionalMakerDeadline)
	var acc venue.OrderResult
	remaining := qty
	live := e.cfg.LiveTradingEnabled

	for attempt := 1; attempt <= okxDirectionalMakerRounds && remaining > executionQtyTolerance; attempt++ {
		now := e.nowFn().UTC()
		if !now.Add(okxDirectionalMakerInterval + okxDirectionalMakerTakerBuf).Before(deadline) {
			break
		}
		book, berr := e.okxV.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
			break
		}
		price := makerPassivePrice(side, book)
		price, remaining = quantizeOrderPriceQty(price, remaining, side, instr, contractBased, e.logger, string(sym))
		makerID := crossVenueLegClientID(clientID, fmt.Sprintf("m%d", attempt))
		logLiveOrderLeg(e.logger, okx.VenueName, venue.MarketPerp, string(sym), side, price, instr.TickSize, remaining, instr.StepSize)
		placed, perr := e.okxV.PlaceOrder(ctx, venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        sym,
			Side:          side,
			Type:          venue.OrderTypePostOnly,
			Qty:           remaining,
			Price:         price,
			ClientOrderID: makerID,
			ReduceOnly:    reduceOnly,
		})
		if perr != nil {
			e.logger.Printf("[OKX方向][maker] %s 第%d轮失败: %v", sym, attempt, perr)
			break
		}
		if placed.ExecutedQty > executionQtyTolerance && strings.EqualFold(placed.Status, "filled") {
			acc = mergeCrossVenueOrderResults(acc, placed)
			remaining = qty - acc.ExecutedQty
			if remaining <= executionQtyTolerance {
				return acc, nil
			}
		}
		waitUntil := e.nowFn().UTC().Add(okxDirectionalMakerInterval)
		for e.nowFn().UTC().Before(waitUntil) {
			res, qerr := e.okxV.QueryOrder(ctx, venue.MarketPerp, sym, makerID)
			if qerr == nil {
				switch strings.ToLower(res.Status) {
				case "filled":
					acc = mergeCrossVenueOrderResults(acc, res)
					remaining = qty - acc.ExecutedQty
					if remaining <= executionQtyTolerance {
						return acc, nil
					}
				case "canceled", "rejected":
					goto nextMakerRound
				}
			}
			select {
			case <-ctx.Done():
				_ = e.okxV.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
				return acc, ctx.Err()
			case <-time.After(crossVenueConfirmPoll):
			}
		}
	nextMakerRound:
		_ = e.okxV.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
		if res, qerr := e.okxV.QueryOrder(ctx, venue.MarketPerp, sym, makerID); qerr == nil && res.ExecutedQty > executionQtyTolerance {
			acc = mergeCrossVenueOrderResults(acc, res)
			remaining = qty - acc.ExecutedQty
		}
		if remaining <= executionQtyTolerance {
			return acc, nil
		}
	}

	if remaining <= executionQtyTolerance && acc.ExecutedQty > executionQtyTolerance {
		return acc, nil
	}
	if remaining <= executionQtyTolerance {
		remaining = qty - acc.ExecutedQty
	}
	book, err := e.okxV.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		if acc.ExecutedQty > executionQtyTolerance {
			return acc, err
		}
		return acc, err
	}
	takerPrice := iocLimitPrice(side, book, okxDirectionalTakerSlippageBps)
	takerPrice, remaining = quantizeOrderPriceQty(takerPrice, remaining, side, instr, contractBased, e.logger, string(sym))
	takerID := crossVenueLegClientID(clientID, "tk")
	logLiveOrderLeg(e.logger, okx.VenueName, venue.MarketPerp, string(sym), side, takerPrice, instr.TickSize, remaining, instr.StepSize)
	takerRes, terr := e.okxV.PlaceOrder(ctx, venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        sym,
		Side:          side,
		Type:          venue.OrderTypeIOC,
		Qty:           remaining,
		Price:         takerPrice,
		ClientOrderID: takerID,
		ReduceOnly:    reduceOnly,
	})
	if terr != nil {
		if acc.ExecutedQty > executionQtyTolerance {
			return acc, terr
		}
		return takerRes, terr
	}
	if takerRes.ExecutedQty <= executionQtyTolerance && live {
		confirmed, qerr := e.confirmDirectionalFill(ctx, sym, takerID)
		if qerr == nil && confirmed.ExecutedQty > executionQtyTolerance {
			takerRes = confirmed
		}
	}
	return mergeCrossVenueOrderResults(acc, takerRes), nil
}

func (e *OKXDirectionalEngine) confirmDirectionalFill(ctx context.Context, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.nowFn().UTC().Add(crossVenueConfirmTimeout)
	var last venue.OrderResult
	for {
		res, err := e.okxV.QueryOrder(ctx, venue.MarketPerp, sym, clientID)
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
		if e.nowFn().UTC().After(deadline) {
			if last.ExecutedQty > executionQtyTolerance {
				return last, nil
			}
			return last, fmt.Errorf("确认成交超时")
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(crossVenueConfirmPoll):
		}
	}
}

func (e *OKXDirectionalEngine) loadPosition() (*okxDirectionalPosition, error) {
	data, err := os.ReadFile(e.positionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var pos okxDirectionalPosition
	if err := json.Unmarshal(data, &pos); err != nil {
		return nil, err
	}
	if !pos.isOpen() {
		return nil, nil
	}
	return &pos, nil
}

func (e *OKXDirectionalEngine) savePosition(pos *okxDirectionalPosition) error {
	if pos == nil || !pos.isOpen() {
		return writeJSONFile(e.positionPath, struct{}{})
	}
	return writeJSONFile(e.positionPath, pos)
}

// getControl 返回当前控制态副本(本金/止损% 带 config 回退)。
func (e *OKXDirectionalEngine) getControl() okxDirectionalControl {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c := e.control
	if c.TradingCapitalUSDT <= 0 {
		c.TradingCapitalUSDT = e.cfg.TradingCapitalUSDT
	}
	if c.StopLossPct <= 0 {
		c.StopLossPct = e.cfg.StopLossPct
	}
	return c
}

// SetOKXDirectionalControl 供面板设置:开始/停止 + 交易仓本金 + 止损%(本金基准,如 0.5=50%)。持久化。
// stopPct/capitalUSDT ≤0 表示沿用现值。仅校验合法区间,不下单。
func (e *OKXDirectionalEngine) SetOKXDirectionalControl(active bool, capitalUSDT, stopPct float64) error {
	e.mu.Lock()
	c := e.control
	c.Active = active
	if capitalUSDT > 0 {
		c.TradingCapitalUSDT = capitalUSDT
	}
	if stopPct > 0 {
		if stopPct > 1 {
			// 容错:页面传 50(百分数)而非 0.5。
			stopPct = stopPct / 100
		}
		if stopPct > 0.95 {
			stopPct = 0.95
		}
		c.StopLossPct = stopPct
	}
	c.UpdatedAt = e.nowFn().UTC()
	e.control = c
	e.mu.Unlock()
	if err := e.saveControl(c); err != nil {
		return err
	}
	e.logger.Printf("[OKX方向][控制] active=%v 交易仓=%.2fU 止损=%.0f%%", c.Active, c.TradingCapitalUSDT, c.StopLossPct*100)
	return nil
}

func (e *OKXDirectionalEngine) loadControl() (okxDirectionalControl, bool) {
	data, err := os.ReadFile(e.controlPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return okxDirectionalControl{}, false
	}
	var c okxDirectionalControl
	if err := json.Unmarshal(data, &c); err != nil {
		return okxDirectionalControl{}, false
	}
	return c, true
}

func (e *OKXDirectionalEngine) saveControl(c okxDirectionalControl) error {
	return writeJSONFile(e.controlPath, c)
}
