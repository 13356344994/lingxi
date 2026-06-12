package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// cross_venue_executor.go P1 跨所资金费对冲执行引擎:消费扫描器结果,空高费所/多低费所 perp-perp 对冲。
// 默认影子模式(enabled=false / live_trading_enabled=false),绝不在未显式开启 live 时下单。

const crossVenueTradeableVenueList = "okx,gate,bybit"

var crossVenueExecutorVenues = []string{okx.VenueName, gate.VenueName, bybit.VenueName}

var crossVenueExecutorFactory = buildCrossVenueExecutorAdapters

// CrossVenueExecutor 跨所 perp-perp 对冲执行引擎。
type CrossVenueExecutor struct {
	cfg       EngineConfig
	scanner   *CrossVenueScanner
	venues    map[string]venue.Venue
	orch      *Orchestrator
	store     *crossVenuePositionStore
	alerts    *AlertDispatcher
	logger    *log.Logger
	haltPaths []string

	mu             sync.RWMutex
	positions      []CrossVenuePosition
	symbolCooldown map[string]time.Time
	nowFn          func() time.Time
}

// CrossVenuePositionsAPIResponse 供面板 API 返回。
type CrossVenuePositionsAPIResponse struct {
	Enabled   bool                 `json:"enabled"`
	Live      bool                 `json:"live"`
	Mode      string               `json:"mode"`
	Positions []CrossVenuePosition `json:"positions"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// NewCrossVenueExecutor 构造执行引擎。
func NewCrossVenueExecutor(cfg EngineConfig, orch *Orchestrator, scanner *CrossVenueScanner, secretPath string, logger *log.Logger) (*CrossVenueExecutor, error) {
	if logger == nil {
		logger = log.Default()
	}
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	store, err := newCrossVenuePositionStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, err
	}
	venues := crossVenueExecutorFactory(orch, cfg, secretPath, logger)
	e := &CrossVenueExecutor{
		cfg:            cfg,
		scanner:        scanner,
		venues:         venues,
		orch:           orch,
		store:          store,
		alerts:         newCrossVenueAlertDispatcher(cfg.CrossVenueScanner, secretPath, logger),
		logger:         logger,
		haltPaths:      emergencyStopPaths(cfg),
		symbolCooldown: make(map[string]time.Time),
		nowFn:          time.Now,
	}
	if ps, lerr := store.load(); lerr == nil {
		e.positions = ps
	}
	return e, nil
}

func buildCrossVenueExecutorAdapters(orch *Orchestrator, cfg EngineConfig, secretPath string, logger *log.Logger) map[string]venue.Venue {
	out := make(map[string]venue.Venue, len(crossVenueExecutorVenues))
	for _, name := range crossVenueExecutorVenues {
		if adp := crossVenueAdapterFromOrchestrator(orch, name); adp != nil {
			out[name] = adp
			continue
		}
		adp, err := buildCrossVenueReadOnlyAdapter(name, cfg, secretPath)
		if err != nil {
			if logger != nil {
				logger.Printf("[跨所执行] venue=%s 适配器构造失败,跳过: %v", name, err)
			}
			continue
		}
		out[name] = adp
	}
	return out
}

func crossVenueExecutorModeLabel(cfg EngineConfig) string {
	if cfg.CrossVenueExecutor.LiveTradingEnabled {
		return "实盘下单"
	}
	return "影子(shadow,不下单)"
}

// Run 启动主循环;ctx 取消即退出。
func (e *CrossVenueExecutor) Run(ctx context.Context) {
	if e == nil || !e.cfg.CrossVenueExecutor.Enabled {
		return
	}
	interval := time.Duration(e.cfg.CrossVenueExecutor.LoopIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	live := e.cfg.CrossVenueExecutor.LiveTradingEnabled
	e.logger.Printf("[跨所执行] ========== 模式=%s enabled=true live=%v ==========", crossVenueExecutorModeLabel(e.cfg), live)
	e.logger.Printf("[跨所执行] 已启动: interval=%s open_spread=%.0f%% max_concurrent=%d max_position=%.0fU",
		interval, e.cfg.CrossVenueExecutor.OpenSpreadAPR*100, e.cfg.CrossVenueExecutor.MaxConcurrent, e.cfg.CrossVenueExecutor.MaxPositionUSDT)

	e.reconcileOnStart(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	e.tick(ctx, live)
	for {
		select {
		case <-ctx.Done():
			e.logger.Printf("[跨所执行] 已停止")
			return
		case <-ticker.C:
			e.tick(ctx, live)
		}
	}
}

func (e *CrossVenueExecutor) tick(ctx context.Context, live bool) {
	haltActive := e.isHaltActive()
	if haltActive {
		e.logger.Printf("[跨所执行] 急停生效,跳过开新仓(在场持仓仍管理)")
	}
	e.manageOpenPositions(ctx, live)
	if !haltActive {
		e.evaluateNewOpens(ctx, live)
	}
}

func (e *CrossVenueExecutor) isHaltActive() bool {
	return ensureEmergencyStopsClear(e.haltPaths) != nil
}

func (e *CrossVenueExecutor) manageOpenPositions(ctx context.Context, live bool) {
	active := activeCrossVenuePositions(e.snapshotPositions())
	for i := range active {
		_, _ = e.manageCrossVenuePosition(ctx, active[i], live)
	}
}

func (e *CrossVenueExecutor) evaluateNewOpens(ctx context.Context, live bool) {
	cfg := e.cfg.CrossVenueExecutor
	active := activeCrossVenuePositions(e.snapshotPositions())
	if len(active) >= cfg.MaxConcurrent {
		return
	}
	if e.scanner == nil {
		return
	}
	snap := e.scanner.Snapshot()
	deps := e.gateDeps()
	now := e.nowFn()
	// 拒绝原因汇总(每轮一行,最多列前 5 条):影子观察期必须能看到「为什么不开」,否则空跑无法验证决策质量。
	var rejected []string
	for _, opp := range snap.Opportunities {
		if len(activeCrossVenuePositions(e.snapshotPositions())) >= cfg.MaxConcurrent {
			break
		}
		gateRes := evaluateCrossVenueOpenGate(ctx, cfg, deps, e.venues, crossVenueGateInput{
			Opp:           opp,
			Positions:     e.snapshotPositions(),
			BybitHeld:     func(sym venue.Symbol) bool { return isBybitSymbolHeld(e.cfg.NetworkConfig.RecordDir, sym) },
			HaltActive:    false,
			SymbolCooling: e.isSymbolCooling,
			Now:           now,
		})
		if !gateRes.OK {
			if len(rejected) < 5 {
				rejected = append(rejected, fmt.Sprintf("%s(%s)", opp.Symbol, gateRes.Reason))
			}
			continue
		}
		highCaps := e.venues[opp.HighVenue].Capabilities()
		lowCaps := e.venues[opp.LowVenue].Capabilities()
		refPrice := (gateRes.HighMid + gateRes.LowMid) / 2
		qty := alignCrossVenueQty(gateRes.NotionalUSDT, refPrice, gateRes.HighInstr, gateRes.LowInstr, highCaps, lowCaps)
		if !crossVenueQtyTradable(qty, refPrice, gateRes.HighInstr) {
			continue
		}
		plan := crossVenueOpenPlan{
			Opp:        opp,
			Gate:       gateRes,
			Qty:        qty,
			RefPrice:   refPrice,
			IdemPrefix: crossVenueIdemPrefix(venue.Symbol(opp.Symbol), opp.HighVenue),
		}
		pos, err := e.executeCrossVenueOpen(ctx, plan, live)
		if err != nil {
			e.logger.Printf("[跨所执行] 开仓失败 %s: %v", opp.Symbol, err)
			continue
		}
		// 影子模式返回空持仓(只记日志不建仓),不能把零值记录灌进内存台账/面板。
		if pos.Symbol == "" {
			continue
		}
		e.mu.Lock()
		e.positions = upsertCrossVenuePosition(e.positions, pos)
		e.mu.Unlock()
		active = append(active, pos)
	}
	if len(rejected) > 0 {
		e.logger.Printf("[跨所执行] 本轮闸门拒绝 %d 例(前5): %s", len(rejected), strings.Join(rejected, "; "))
	}
}

func upsertCrossVenuePosition(ps []CrossVenuePosition, p CrossVenuePosition) []CrossVenuePosition {
	for i := range ps {
		if ps[i].Symbol == p.Symbol {
			ps[i] = p
			return ps
		}
	}
	return append(ps, p)
}

func (e *CrossVenueExecutor) gateDeps() crossVenueGateDeps {
	return crossVenueGateDeps{
		lookupInstrument: lookupVenueInstrument,
		fetchBookTicker: func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error) {
			return v.FetchBookTicker(ctx, venue.MarketPerp, sym)
		},
		fetchDepth: func(ctx context.Context, v venue.Venue, sym venue.Symbol, limit int) (venue.Depth, error) {
			return v.FetchDepth(ctx, venue.MarketPerp, sym, limit)
		},
		fetchFunding:     fetchVenueFundingForSymbol,
		fetchBalanceUSDT: fetchVenueUSDTBalance,
		venueCaps:        func(v venue.Venue) venue.Capabilities { return v.Capabilities() },
		allocatedUSDT:    e.allocatedUSDTForVenue,
	}
}

func (e *CrossVenueExecutor) allocatedUSDTForVenue(venueName string) float64 {
	if e.orch != nil {
		if rt, ok := e.orch.Get(venueName); ok && rt != nil && rt.allocatedCapitalUSDT > 0 {
			return rt.allocatedCapitalUSDT
		}
	}
	dir := recordDirForVenue(e.cfg, venueName)
	if ov, ok := loadVenueCapitalOverride(dir); ok {
		return ov
	}
	return e.cfg.AllocatedCapitalUSDT
}

func (e *CrossVenueExecutor) reconcileOnStart(ctx context.Context) {
	ps, err := e.store.load()
	if err != nil {
		e.logger.Printf("[跨所执行] reconcile 读台账失败: %v", err)
		return
	}
	active := activeCrossVenuePositions(ps)
	if len(active) == 0 {
		return
	}
	e.logger.Printf("[跨所执行] reconcile: %d 笔落盘持仓", len(active))
	for _, p := range active {
		highV := e.venues[p.HighVenue]
		lowV := e.venues[p.LowVenue]
		if highV == nil || lowV == nil {
			e.logger.Printf("[跨所执行] reconcile:%s venue 未就绪,保留台账", p.Symbol)
			continue
		}
		highPos, errH := highV.FetchPositions(ctx)
		lowPos, errL := lowV.FetchPositions(ctx)
		if errH != nil || errL != nil {
			e.logger.Printf("[跨所执行] reconcile:%s 拉持仓失败,保留台账", p.Symbol)
			continue
		}
		sym := venue.Symbol(p.Symbol)
		hAmt := perpPositionQty(highPos, sym)
		lAmt := perpPositionQty(lowPos, sym)
		hOpen := absQty(hAmt) > executionQtyTolerance
		lOpen := absQty(lAmt) > executionQtyTolerance
		switch {
		case !hOpen && !lOpen:
			e.logger.Printf("[跨所执行] reconcile:%s 两所均无持仓,orphan 清除台账", p.Symbol)
			p.Status = CrossVenueStatusOrphanSingleLeg
			_ = e.store.remove(p.Symbol)
			e.notifyWarn("跨所 orphan", fmt.Sprintf("%s 台账有但两所均无持仓", p.Symbol), p.Symbol)
		case hOpen != lOpen:
			p.Status = CrossVenueStatusOrphanSingleLeg
			_ = e.persistPosition(p)
			e.notifyCritical("跨所 reconcile 孤腿", fmt.Sprintf("%s 台账与所内不一致", p.Symbol), p.Symbol)
		default:
			e.logger.Printf("[跨所执行] reconcile:%s 恢复监控", p.Symbol)
		}
	}
	// 所内有但台账没有 → 只告警,绝不自动收编
	for _, name := range crossVenueExecutorVenues {
		v := e.venues[name]
		if v == nil {
			continue
		}
		positions, err := v.FetchPositions(ctx)
		if err != nil {
			continue
		}
		for _, ep := range positions {
			if absQty(ep.PositionAmt) <= executionQtyTolerance {
				continue
			}
			if _, tracked := crossVenuePositionBySymbol(ps, ep.Symbol); tracked {
				continue
			}
			// 可能是 bybit 单所或其他,仅当 symbol 在跨所机会对时才告警
			e.logger.Printf("[跨所执行] reconcile: venue=%s 未登记持仓 %s≈%.6f,请人工核对", name, ep.Symbol, ep.PositionAmt)
		}
	}
	e.mu.Lock()
	e.positions = ps
	e.mu.Unlock()
}

func (e *CrossVenueExecutor) persistPosition(p CrossVenuePosition) error {
	if err := e.store.upsert(p); err != nil {
		e.logger.Printf("[跨所执行] 落盘失败 %s: %v", p.Symbol, err)
		return err
	}
	e.mu.Lock()
	e.positions = upsertCrossVenuePosition(e.positions, p)
	e.mu.Unlock()
	return nil
}

func (e *CrossVenueExecutor) snapshotPositions() []CrossVenuePosition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]CrossVenuePosition, len(e.positions))
	copy(out, e.positions)
	return out
}

// PositionsSnapshot 返回台账+模式(供 HTTP API)。
func (e *CrossVenueExecutor) PositionsSnapshot() CrossVenuePositionsAPIResponse {
	ps, _ := e.store.load()
	if ps == nil {
		ps = e.snapshotPositions()
	}
	return CrossVenuePositionsAPIResponse{
		Enabled:   e.cfg.CrossVenueExecutor.Enabled,
		Live:      e.cfg.CrossVenueExecutor.LiveTradingEnabled,
		Mode:      crossVenueExecutorModeLabel(e.cfg),
		Positions: ps,
		UpdatedAt: e.nowFn(),
	}
}

func (e *CrossVenueExecutor) setSymbolCooldown(sym string) {
	hours := e.cfg.CrossVenueExecutor.SymbolCooldownHours
	if hours <= 0 {
		hours = 1
	}
	e.mu.Lock()
	e.symbolCooldown[sym] = e.nowFn().Add(time.Duration(hours) * time.Hour)
	e.mu.Unlock()
}

// fundingFromScanner 从扫描器缓存查某所某 symbol 的费率明细(快照最旧 10 分钟,对平仓判断/记账精度足够)。
// 缓存缺失(扫描器未注入/该所本轮失败/symbol 不在快照)返回 not-ok,**绝不现场拉全量快照**——
// OKX 全量逐币串行拉一次 20–46s,管理循环现场拉会拖死 60s 主循环并打爆限频。
func (e *CrossVenueExecutor) fundingFromScanner(venueName string, sym venue.Symbol) (CrossVenueFundingEntry, bool) {
	if e.scanner == nil {
		return CrossVenueFundingEntry{}, false
	}
	byVenue := e.scanner.Snapshot().FundingByVenue
	if byVenue == nil {
		return CrossVenueFundingEntry{}, false
	}
	entry, ok := byVenue[venueName][string(sym)]
	return entry, ok
}

func (e *CrossVenueExecutor) isSymbolCooling(sym string) bool {
	e.mu.RLock()
	until, ok := e.symbolCooldown[sym]
	e.mu.RUnlock()
	return ok && e.nowFn().Before(until)
}

func (e *CrossVenueExecutor) notifyTrade(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.Notify(Alert{Level: EventLevelInfo, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

func (e *CrossVenueExecutor) notifyWarn(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.Notify(Alert{Level: EventLevelWarning, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

func (e *CrossVenueExecutor) notifyCritical(title, detail, sym string) {
	if e.alerts == nil {
		return
	}
	e.alerts.Notify(Alert{Level: EventLevelCritical, Title: title, Detail: detail, Symbol: sym, OccurredAt: e.nowFn()})
}

// crossVenueExecutorRecordDir 返回跨所台账目录(测试/文档用)。
func crossVenueExecutorRecordDir(base string) string {
	return filepath.Join(strings.TrimSpace(base), "crossvenue")
}
