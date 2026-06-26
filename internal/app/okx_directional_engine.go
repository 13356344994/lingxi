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
	// 管理循环每轮回填:最新中价 + 本金口径浮盈%(供面板实时显示)。
	LastMid                  float64 `json:"last_mid,omitempty"`
	LastUnrealizedAccountPct float64 `json:"last_unrealized_account_pct,omitempty"`
	// 高阶仓位管理状态。
	InitialNotionalUSDT float64 `json:"initial_notional_usdt,omitempty"`  // 首次开仓名义(补仓上限/部分平仓基准)
	PeakAccountPct      float64 `json:"peak_account_pct,omitempty"`       // 本金口径浮盈高水位(移动止损用)
	StopFloorAccountPct float64 `json:"stop_floor_account_pct,omitempty"` // 动态止损地板(本金口径,单调上移)
	PartialTPDone       bool    `json:"partial_tp_done,omitempty"`        // 是否已部分止盈(后转保本+移动)
	AddCount            int     `json:"add_count,omitempty"`              // 已补仓次数
	RealizedPartialUSDT float64 `json:"realized_partial_usdt,omitempty"`  // 本仓已落袋的部分止盈
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
	OpenCount           int                          // 当前持仓数
	MaxConcurrent       int                          // 最大并发
	Positions           []OKXDirectionalPositionView // 多币持仓明细
}

// OKXDirectionalPositionView 单仓只读视图(面板多仓表)。
type OKXDirectionalPositionView struct {
	Symbol               string    `json:"symbol"`
	Side                 string    `json:"side"`
	EntryPrice           float64   `json:"entry_price"`
	Qty                  float64   `json:"qty"`
	NotionalUSDT         float64   `json:"notional_usdt"`
	Regime               string    `json:"regime"`
	OpenedAt             time.Time `json:"opened_at"`
	LastMid              float64   `json:"last_mid"`
	UnrealizedAccountPct float64   `json:"unrealized_account_pct"` // 本金口径浮盈%(已×杠杆)
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
	positions         map[string]*okxDirectionalPosition // 多币并发持仓(键=symbol)
	shortlist         []okxUniverseEntry                 // 每 ~30min 全场扫描产出的可交易币
	lastUniverseScan  time.Time
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
		positions:       make(map[string]*okxDirectionalPosition),
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
	for _, p := range e.loadPositions() {
		if p != nil && p.isOpen() {
			e.positions[p.Symbol] = p
		}
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
	// 每 ~30min 全场扫描刷新 shortlist(综合分:资费+波动+热度+BTC情境);90s 循环只在 shortlist 上动作。
	e.maybeRebuildUniverse(ctx)
	// 持仓始终管理(即便页面已停止,也继续看护止损/止盈/反转,绝不撒手不管)。先管理(可能腾出仓位),再补开。
	e.manageAll(ctx)
	// 开新仓须页面"开始"(control.Active);日熔断按用户要求默认关闭(DailyLossLimitUSDT=0 → 永不熔断)。
	ctrl := e.getControl()
	if ctrl.Active && !directionalDailyLossHalted(e.dailyRealizedUSDT, e.cfg.DailyLossLimitUSDT) && e.concurrencyAllows() {
		e.openFromShortlist(ctx)
	}
}

// maybeRebuildUniverse 到点(或 shortlist 空)就全场扫描重算 shortlist。失败保留旧 shortlist。
func (e *OKXDirectionalEngine) maybeRebuildUniverse(ctx context.Context) {
	interval := time.Duration(e.cfg.UniverseScanIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	e.mu.RLock()
	due := e.lastUniverseScan.IsZero() || e.nowFn().UTC().Sub(e.lastUniverseScan) >= interval || len(e.shortlist) == 0
	e.mu.RUnlock()
	if !due {
		return
	}
	snaps, err := e.okxV.FetchFundingSnapshot(ctx)
	if err != nil {
		e.logger.Printf("[OKX方向][选币] 拉资费快照失败,沿用旧 shortlist: %v", err)
		return
	}
	sl, err := scanOKXUniverse(ctx, e.okxA, e.okxV, snaps, e.cfg, e.logger)
	if err != nil {
		e.logger.Printf("[OKX方向][选币] 全场扫描失败,沿用旧 shortlist: %v", err)
		return
	}
	e.mu.Lock()
	e.shortlist = sl
	e.lastUniverseScan = e.nowFn().UTC()
	e.mu.Unlock()
	// 落盘 shortlist 供分析记录。
	_ = writeJSONFile(filepath.Join(e.recordDir, "universe.json"), map[string]any{"at": e.nowFn().UTC(), "shortlist": sl})
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

func (e *OKXDirectionalEngine) maxConcurrent() int {
	if e.cfg.MaxConcurrent > 0 {
		return e.cfg.MaxConcurrent
	}
	return 1
}

func (e *OKXDirectionalEngine) concurrencyAllows() bool {
	e.mu.RLock()
	n := len(e.positions)
	e.mu.RUnlock()
	return n < e.maxConcurrent()
}

// deployedMarginUSDT 当前所有持仓占用的保证金合计(各仓名义/杠杆)。
func (e *OKXDirectionalEngine) deployedMarginUSDT() float64 {
	lev := float64(e.leverage())
	if lev <= 0 {
		lev = 5
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	var m float64
	for _, p := range e.positions {
		if p != nil && p.isOpen() {
			m += p.NotionalUSDT / lev
		}
	}
	return m
}

// openPositionsSnapshot 返回当前持仓副本(并发安全)。
func (e *OKXDirectionalEngine) openPositionsSnapshot() []*okxDirectionalPosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*okxDirectionalPosition, 0, len(e.positions))
	for _, p := range e.positions {
		cp := *p
		out = append(out, &cp)
	}
	return out
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
		OpenCount:          len(e.positions),
		MaxConcurrent:      e.maxConcurrent(),
	}
	// 多仓明细 + 首仓回填单仓字段(向后兼容旧面板)。
	first := true
	for _, p := range e.positions {
		if !p.isOpen() {
			continue
		}
		st.Positions = append(st.Positions, OKXDirectionalPositionView{
			Symbol: p.Symbol, Side: p.Side, EntryPrice: p.EntryPrice, Qty: p.Qty,
			NotionalUSDT: p.NotionalUSDT, Regime: p.Regime, OpenedAt: p.OpenedAt,
			LastMid: p.LastMid, UnrealizedAccountPct: p.LastUnrealizedAccountPct,
		})
		if first {
			st.HoldingSymbol = p.Symbol
			st.Side = p.Side
			st.EntryPrice = p.EntryPrice
			st.Qty = p.Qty
			st.NotionalUSDT = p.NotionalUSDT
			st.OpenedAt = p.OpenedAt
			first = false
		}
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

// openFromShortlist 在 shortlist 上出信号,补开到 MaxConcurrent(跳过已持有/冷却),按信心降序优先,逐仓保证金预检。
func (e *OKXDirectionalEngine) openFromShortlist(ctx context.Context) {
	e.mu.RLock()
	sl := append([]okxUniverseEntry(nil), e.shortlist...)
	held := make(map[string]bool, len(e.positions))
	for s := range e.positions {
		held[s] = true
	}
	cooldown := make(map[string]time.Time, len(e.cooldownUntil))
	for k, v := range e.cooldownUntil {
		cooldown[k] = v
	}
	e.mu.RUnlock()

	slots := e.maxConcurrent() - len(held)
	if slots <= 0 || len(sl) == 0 {
		return
	}
	now := e.nowFn().UTC()
	costFloor := e.cfg.CostFloorBps
	if costFloor <= 0 {
		costFloor = 60
	}
	type pick struct {
		sym    string
		signal DirectionalSignal
		rate   float64
	}
	var picks []pick
	var nSignal, nBelowFloor, nInsufficient, nVeto, nNoSig int
	var floorEg, vetoEg, noSigEg string
	for _, ent := range sl {
		sym := ent.Symbol
		if held[sym] {
			continue
		}
		if until, ok := cooldown[sym]; ok && now.Before(until) {
			continue
		}
		closes, cerr := e.fetchCloses(ctx, venue.Symbol(sym))
		if cerr != nil {
			e.logger.Printf("[OKX方向] %s K线失败: %v", sym, cerr)
			continue
		}
		sig := computeDirectionalSignal(closes, ent.FundingRate, e.cfg)
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
		picks = append(picks, pick{sym: sym, signal: sig, rate: ent.FundingRate})
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].signal.Conviction > picks[j].signal.Conviction })
	if len(picks) == 0 {
		e.clearLastError()
		e.logger.Printf("[OKX方向][漏斗] shortlist=%d 空位=%d 有方向=0 | flat: 样本不足=%d 资费否决=%d 无信号=%d 过地板挡=%d(地板=%.0fbps) | 例[veto=%s][nosig=%s][floor=%s]",
			len(sl), slots, nInsufficient, nVeto, nNoSig, nBelowFloor, costFloor, vetoEg, noSigEg, floorEg)
		return
	}
	opened := 0
	for _, p := range picks {
		if opened >= slots {
			break
		}
		if e.openPosition(ctx, p.sym, p.signal, p.rate) {
			opened++
		}
	}
	_ = nSignal
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

// openPosition 开单仓:每仓本金 = 交易仓本金/MaxConcurrent,名义 = 每仓本金 × 杠杆;开仓前预检永续可用保证金。成功返回 true。
func (e *OKXDirectionalEngine) openPosition(ctx context.Context, sym string, sig DirectionalSignal, fundingRate float64) bool {
	book, err := e.okxV.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(sym))
	if err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 盘口失败: %v", sym, err)
		return false
	}
	mid := (book.BidPrice + book.AskPrice) / 2
	if mid <= 0 {
		e.setLastError("mid<=0")
		return false
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.okxV, venue.Symbol(sym), venue.MarketPerp)
	if !ok {
		e.setLastError("instrument 缺失")
		e.logger.Printf("[OKX方向] %s 无 instrument 元数据,跳过开仓", sym)
		return false
	}
	caps := e.okxV.Capabilities()
	// 每仓本金 = 交易仓本金 / 最大并发;名义 = 每仓本金 × 杠杆。总并发满仓总名义=本金×杠杆,总风险=本金(加仓不放大总敞口)。
	ctrl := e.getControl()
	perPosCapital := ctrl.TradingCapitalUSDT / float64(e.maxConcurrent())
	notional := perPosCapital * float64(e.leverage())
	if notional <= 0 {
		e.logger.Printf("[OKX方向] %s 每仓本金<=0,跳过开仓", sym)
		return false
	}
	qty := normalizeVenueQty(notional/mid, instr, caps.ContractBasedPerp)
	if !openLegTradableAfterQuantize(qty, mid, instr) {
		e.logger.Printf("[OKX方向] %s 量化后不可交易 qty=%.8f mid=%.6f notional=%.2f", sym, qty, mid, notional)
		return false
	}
	var side venue.Side
	switch sig.Side {
	case DirectionLong:
		side = venue.SideBuy
	case DirectionShort:
		side = venue.SideSell
	default:
		return false
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟开 %s %s regime=%s conviction=%.2f move=%.1fbps 名义=%.2f 原因=%s",
			sym, sig.Side.String(), sig.Regime.String(), sig.Conviction, sig.ExpectedMoveBps, notional, sig.Reason)
		return false
	}
	// 资本总上限:已部署保证金(各仓名义/杠杆)+ 本仓所需 ≤ 交易仓本金 M。
	// 即便交易所账户还有闲钱,也绝不超出用户设定的总风险(M)。这是"总风险=本金"的硬保证。
	deployed := e.deployedMarginUSDT()
	if deployed+perPosCapital > ctrl.TradingCapitalUSDT+1e-6 {
		e.logger.Printf("[OKX方向] %s 已部署保证金 %.2f + 本仓 %.2f > 交易仓本金 %.2f,跳过开仓", sym, deployed, perPosCapital, ctrl.TradingCapitalUSDT)
		return false
	}
	// 交易所侧可用保证金兜底预检。
	if avail, merr := fetchVenuePerpAvailableUSDT(ctx, e.okxV); merr == nil && avail+1e-9 < perPosCapital {
		e.logger.Printf("[OKX方向] %s 永续可用 %.2f < 每仓本金 %.2f,跳过开仓", sym, avail, perPosCapital)
		return false
	}
	if err := e.okxV.SetLeverage(ctx, venue.Symbol(sym), e.leverage()); err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 设杠杆失败: %v", sym, err)
		return false
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
		return false
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
	e.positions[sym] = pos
	e.mu.Unlock()
	e.persistPositions()
	e.clearLastError()
	e.logger.Printf("[OKX方向] 已开 %s %s qty=%.8f @%.6f 名义=%.2f regime=%s", sym, pos.Side, pos.Qty, pos.EntryPrice, notional, pos.Regime)
	return true
}

// manageAll 管理所有在场持仓(止损/止盈/信号反转/超时);逐仓独立。
func (e *OKXDirectionalEngine) manageAll(ctx context.Context) {
	for _, pos := range e.openPositionsSnapshot() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		e.manageOne(ctx, pos)
	}
}

func (e *OKXDirectionalEngine) manageOne(ctx context.Context, pos *okxDirectionalPosition) {
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
	// 回填实时浮盈到持仓(供面板显示)。
	e.mu.Lock()
	if p, ok := e.positions[pos.Symbol]; ok {
		p.LastMid = mid
		p.LastUnrealizedAccountPct = uPnLAccount
	}
	e.mu.Unlock()
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
	cfg := e.cfg
	stopPct := e.getControl().StopLossPct
	if stopPct <= 0 {
		stopPct = cfg.StopLossPct
	}
	// 更新本仓高阶状态(高水位 + 动态止损地板),全部本金口径。在锁内改真实指针。
	e.mu.Lock()
	real := e.positions[pos.Symbol]
	if real == nil {
		e.mu.Unlock()
		return
	}
	if real.InitialNotionalUSDT <= 0 {
		real.InitialNotionalUSDT = real.NotionalUSDT
	}
	if real.StopFloorAccountPct == 0 && !real.PartialTPDone {
		real.StopFloorAccountPct = -stopPct // 初始硬止损(本金口径)
	}
	if uPnLAccount > real.PeakAccountPct {
		real.PeakAccountPct = uPnLAccount
	}
	if real.PartialTPDone { // 部分止盈后:移动止损 = max(保本0, 高水位 − 间距),单调上移
		gap := cfg.TrailGapPct
		if gap <= 0 {
			gap = 0.15
		}
		cand := real.PeakAccountPct - gap
		if cand < 0 {
			cand = 0
		}
		if cand > real.StopFloorAccountPct {
			real.StopFloorAccountPct = cand
		}
	}
	floor := real.StopFloorAccountPct
	peak := real.PeakAccountPct
	partialDone := real.PartialTPDone
	addCount := real.AddCount
	initNotional := real.InitialNotionalUSDT
	e.mu.Unlock()
	e.persistPositions()

	// 数据驱动判定:由实时信心/动能/回撤决定本仓"补仓 / 部分止盈 / 持有 / 离场",而非到点机械执行三件套。
	act := decideDirectionalPositionAction(uPnLAccount, peak, floor, holdMin, side, fresh, partialDone, addCount, cfg)
	switch act.Kind {
	case dirActClose:
		e.closePosition(ctx, pos, mid, uPnLNotional, holdMin, act.Reason)
	case dirActPartialTP:
		frac := cfg.PartialTPFraction
		if frac <= 0 || frac >= 1 {
			frac = 0.5
		}
		if e.partialClose(ctx, pos, frac, mid, uPnLNotional, holdMin, act.Reason) {
			e.mu.Lock()
			if r := e.positions[pos.Symbol]; r != nil {
				r.PartialTPDone = true
				if r.StopFloorAccountPct < 0 {
					r.StopFloorAccountPct = 0 // 转保本
				}
			}
			e.mu.Unlock()
			e.persistPositions()
		}
	case dirActScaleIn:
		frac := cfg.ScaleInFraction
		if frac <= 0 {
			frac = 0.5
		}
		if e.scaleIn(ctx, pos, side, initNotional*frac, mid) {
			e.mu.Lock()
			if r := e.positions[pos.Symbol]; r != nil {
				r.AddCount++
			}
			e.mu.Unlock()
			e.persistPositions()
		}
	default:
		e.clearLastError()
	}
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
	coolMin := float64(e.cfg.SymbolCooldownMinutes)
	if coolMin <= 0 {
		coolMin = 30
	}
	// 防滚仓/亏损币快速复入:止损平仓后该币冷却拉长(LossCooldownMultiplier 倍)。
	if strings.Contains(reason, "止损") || realized < 0 {
		mult := e.cfg.LossCooldownMultiplier
		if mult < 1 {
			mult = 1
		}
		coolMin *= mult
	}
	e.mu.Lock()
	e.dailyRealizedUSDT += realized
	e.cooldownUntil[pos.Symbol] = e.nowFn().UTC().Add(time.Duration(coolMin * float64(time.Minute)))
	delete(e.positions, pos.Symbol)
	e.mu.Unlock()
	e.persistPositions()
	e.clearLastError()
	e.logger.Printf("[OKX方向] 已平 %s %s 原因=%s 实现≈%.4f USDT", pos.Symbol, pos.Side, reason, realized)
}

// partialClose 部分止盈:reduce-only 平掉 fraction 比例,记部分结算,缩减持仓 qty/名义,累加当日已实现。成功返回 true。
func (e *OKXDirectionalEngine) partialClose(ctx context.Context, pos *okxDirectionalPosition, fraction, mid, uPnLNotional float64, holdMin int, reason string) bool {
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟部分止盈 %s frac=%.2f", pos.Symbol, fraction)
		return false
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.okxV, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		return false
	}
	caps := e.okxV.Capabilities()
	var closeSide venue.Side
	switch pos.directionSide() {
	case DirectionLong:
		closeSide = venue.SideSell
	case DirectionShort:
		closeSide = venue.SideBuy
	default:
		return false
	}
	closeQty := normalizeVenueQty(pos.Qty*fraction, instr, caps.ContractBasedPerp)
	if closeQty <= 0 || !openLegTradableAfterQuantize(closeQty, mid, instr) {
		return false // 余量太小不可分,留给后续全平
	}
	if closeQty >= pos.Qty {
		closeQty = pos.Qty
	}
	clientID := okxDirectionalClientID(pos.Symbol, "ptp")
	res, cerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), closeSide, closeQty, clientID, true, instr, caps.ContractBasedPerp)
	if cerr != nil || res.ExecutedQty <= executionQtyTolerance {
		e.logger.Printf("[OKX方向] %s 部分止盈下单失败: %v", pos.Symbol, cerr)
		return false
	}
	exitPrice := mid
	if res.AvgPrice > 0 {
		exitPrice = res.AvgPrice
	}
	closedFrac := res.ExecutedQty / pos.Qty
	if closedFrac > 1 {
		closedFrac = 1
	}
	closedNotional := pos.NotionalUSDT * closedFrac
	realized := directionalUPnLPct(pos.directionSide(), pos.EntryPrice, exitPrice) * closedNotional
	settle := okxDirectionalSettlement{
		Time: e.nowFn().UTC(), Symbol: pos.Symbol, Side: pos.Side, Entry: pos.EntryPrice, Exit: exitPrice,
		NotionalUSDT: closedNotional, RealizedUSDT: realized, HoldMinutes: holdMin, Regime: pos.Regime,
		Reason: reason + "(部分)", FundingAtOpen: pos.FundingAtOpen,
	}
	if err := appendJSONLRecord(e.settlementsPath, settle); err != nil {
		e.logger.Printf("[OKX方向] 部分结算落盘失败: %v", err)
	}
	e.mu.Lock()
	if r := e.positions[pos.Symbol]; r != nil {
		r.Qty -= res.ExecutedQty
		if r.Qty < 0 {
			r.Qty = 0
		}
		r.NotionalUSDT -= closedNotional
		if r.NotionalUSDT < 0 {
			r.NotionalUSDT = 0
		}
		r.RealizedPartialUSDT += realized
	}
	e.dailyRealizedUSDT += realized
	e.mu.Unlock()
	e.logger.Printf("[OKX方向] 部分止盈 %s 平%.0f%% 实现≈%.4f USDT(转保本+移动止损)", pos.Symbol, closedFrac*100, realized)
	return true
}

// scaleIn 补仓(仅盈利仓):同向加 tranche 名义,受总资本上限约束,更新加权均价/qty/名义。成功返回 true。绝不摊亏(调用方已确保盈利+信号同向)。
func (e *OKXDirectionalEngine) scaleIn(ctx context.Context, pos *okxDirectionalPosition, side DirectionSide, tranche, mid float64) bool {
	if tranche <= 0 || mid <= 0 {
		return false
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟补仓 %s tranche=%.2f", pos.Symbol, tranche)
		return false
	}
	lev := float64(e.leverage())
	if e.deployedMarginUSDT()+tranche/lev > e.getControl().TradingCapitalUSDT+1e-6 {
		e.logger.Printf("[OKX方向] %s 补仓超总资本上限,跳过", pos.Symbol)
		return false
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.okxV, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		return false
	}
	caps := e.okxV.Capabilities()
	addSide := venue.SideBuy
	if side == DirectionShort {
		addSide = venue.SideSell
	}
	addQty := normalizeVenueQty(tranche/mid, instr, caps.ContractBasedPerp)
	if addQty <= 0 || !openLegTradableAfterQuantize(addQty, mid, instr) {
		return false
	}
	clientID := okxDirectionalClientID(pos.Symbol, "add")
	res, oerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), addSide, addQty, clientID, false, instr, caps.ContractBasedPerp)
	if oerr != nil || res.ExecutedQty <= executionQtyTolerance {
		e.logger.Printf("[OKX方向] %s 补仓失败: %v", pos.Symbol, oerr)
		return false
	}
	fillPrice := res.AvgPrice
	if fillPrice <= 0 {
		fillPrice = mid
	}
	addNotional := res.ExecutedQty * fillPrice
	var newAvg float64
	e.mu.Lock()
	if r := e.positions[pos.Symbol]; r != nil {
		newQty := r.Qty + res.ExecutedQty
		if newQty > 0 {
			r.EntryPrice = (r.EntryPrice*r.Qty + fillPrice*res.ExecutedQty) / newQty
		}
		r.Qty = newQty
		r.NotionalUSDT += addNotional
		newAvg = r.EntryPrice
	}
	e.mu.Unlock()
	e.logger.Printf("[OKX方向] 补仓 %s +qty%.6f @%.6f → 新均价%.6f(金字塔加盈利仓)", pos.Symbol, res.ExecutedQty, fillPrice, newAvg)
	return true
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

// loadPositions 读 positions.json:新格式为持仓列表;兼容旧单仓对象(自动迁移为单元素列表)。
func (e *OKXDirectionalEngine) loadPositions() []*okxDirectionalPosition {
	data, err := os.ReadFile(e.positionPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	// 先按列表解析。
	var list []*okxDirectionalPosition
	if err := json.Unmarshal(data, &list); err == nil {
		out := make([]*okxDirectionalPosition, 0, len(list))
		for _, p := range list {
			if p != nil && p.isOpen() {
				out = append(out, p)
			}
		}
		return out
	}
	// 回退:旧单仓对象。
	var one okxDirectionalPosition
	if err := json.Unmarshal(data, &one); err == nil && one.isOpen() {
		return []*okxDirectionalPosition{&one}
	}
	return nil
}

// persistPositions 把当前持仓 map 落盘为列表(原子写)。
func (e *OKXDirectionalEngine) persistPositions() {
	e.mu.RLock()
	list := make([]*okxDirectionalPosition, 0, len(e.positions))
	for _, p := range e.positions {
		if p != nil && p.isOpen() {
			list = append(list, p)
		}
	}
	e.mu.RUnlock()
	if err := writeJSONFile(e.positionPath, list); err != nil {
		e.logger.Printf("[OKX方向] 持仓落盘失败: %v", err)
	}
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
