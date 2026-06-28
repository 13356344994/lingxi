package app

// okx_directional_engine.go OKX 方向交易引擎 plumbing:拉资费/K线、调用纯信号、下单与落盘。
// 纯信号/风控见 okx_directional_signal.go;本文件不做 regime/资费闸重算。

import (
	"context"
	"encoding/json"
	"errors"
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
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

const (
	okxDirectionalPositionFile      = "position.json"
	okxDirectionalSettlementsFile   = "settlements.jsonl"
	okxDirectionalCooldownFile      = "cooldowns.json"
	okxDirectionalTakerSlippageBps  = 40.0
	okxDirectionalUrgentSlippageBps = 150.0 // 风控急平 IOC 放宽滑点(薄盘也能成交)
	okxDirectionalMakerRounds       = 3
	okxDirectionalMakerInterval     = 2 * time.Second
	okxDirectionalMakerDeadline     = 12 * time.Second
	okxDirectionalMakerTakerBuf     = 3 * time.Second
	okxDirectionalRiskInterval      = 20 * time.Second // 快速风控子循环周期(止损发现 ~20s)
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
	// 仓位唯一 ID(用于多条 settlement 聚合回单仓,格式 "symbol-nanosecond")。
	PositionID string `json:"position_id,omitempty"`
	// 管理循环每轮回填:最新中价 + 本金口径浮盈% + 浮盈USDT(供面板实时显示)。
	LastMid                  float64 `json:"last_mid,omitempty"`
	LastUnrealizedAccountPct float64 `json:"last_unrealized_account_pct,omitempty"`
	LastUnrealizedUSDT       float64 `json:"last_unrealized_usdt,omitempty"`
	// 高阶仓位管理状态。
	InitialNotionalUSDT float64 `json:"initial_notional_usdt,omitempty"`  // 首次开仓名义(补仓上限/部分平仓基准)
	PeakAccountPct      float64 `json:"peak_account_pct,omitempty"`       // 本金口径浮盈高水位(移动止损用)
	StopFloorAccountPct float64 `json:"stop_floor_account_pct,omitempty"` // 动态止损地板(本金口径,单调上移)
	PartialTPDone       bool    `json:"partial_tp_done,omitempty"`        // 是否已部分止盈(后转保本+移动)
	AddCount            int     `json:"add_count,omitempty"`              // 已补仓次数
	RealizedPartialUSDT float64 `json:"realized_partial_usdt,omitempty"`  // 本仓已落袋的部分止盈
	// 执行质量/成交后行为仪表化(阶段A:量化 maker 逆选择 + 秒级退出能否把 PF 拉过 1;只记录,不改交易行为)。
	EntryMid          float64 `json:"entry_mid,omitempty"`           // 开仓决策时刻中价(算入场滑点基准)
	EntrySlipBps      float64 `json:"entry_slip_bps,omitempty"`      // 入场滑点(bps,正=比中价更差)
	OpenViaTaker      bool    `json:"open_via_taker,omitempty"`      // 开仓是否走了 IOC 吃单兜底(maker 没挂上)
	OpenFeeUSDT       float64 `json:"open_fee_usdt,omitempty"`       // 开仓+补仓累计实扣手续费(交易所真实口径)
	MFEAccountPct     float64 `json:"mfe_account_pct,omitempty"`     // 成交后最大浮盈(本金口径高水位,运行态)
	MAEAccountPct     float64 `json:"mae_account_pct,omitempty"`     // 成交后最大浮亏(本金口径,负值,运行态)
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
	// 仓位 ID:与 okxDirectionalPosition.PositionID 对应,用于聚合分批止盈。
	PositionID string `json:"position_id,omitempty"`
	// 执行质量/成交后仪表化(阶段A,仅记录供离线归因:maker 逆选择有多严重 / 秒级退出能否把 PF 拉过 1)。
	HoldSeconds   int     `json:"hold_seconds,omitempty"`    // 持有秒数(比 hold_minutes 更细,供秒级退出分析)
	EntrySlipBps  float64 `json:"entry_slip_bps,omitempty"`  // 入场滑点(bps,正=更差)
	ExitSlipBps   float64 `json:"exit_slip_bps,omitempty"`   // 出场滑点(bps,正=更差)
	OpenViaTaker  bool    `json:"open_via_taker,omitempty"`  // 开仓是否走 IOC 吃单
	CloseViaTaker bool    `json:"close_via_taker,omitempty"` // 平仓是否走 IOC 吃单
	FeeUSDT       float64 `json:"fee_usdt,omitempty"`        // 本次结算归属的实扣手续费(开仓侧按比例 + 平仓侧)
	MFEAccountPct float64 `json:"mfe_account_pct,omitempty"` // 成交后最大浮盈(本金口径)
	MAEAccountPct float64 `json:"mae_account_pct,omitempty"` // 成交后最大浮亏(本金口径,负值)
}

// OKXDirectionalStatus 只读快照(面板/监控用)。
type OKXDirectionalStatus struct {
	Venue               string `json:"venue"`
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
	// 收益(以交易仓本金 M 为基数)。
	TotalUnrealizedUSDT float64 // 在场持仓合计浮盈
	TotalPnLTodayUSDT   float64 // 当日已实现 + 当前浮盈
	PnLPctOfCapital     float64 // TotalPnLTodayUSDT / 交易仓本金
	DeployedMarginUSDT  float64 // 已部署保证金合计
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
	UnrealizedUSDT       float64   `json:"unrealized_usdt"`        // 浮盈(USDT)
}

// OKXDirectionalEngine OKX 方向引擎(独立于 carry)。
type OKXDirectionalEngine struct {
	cfg       OKXDirectionalConfig
	netCfg    EngineConfig
	v         DirectionalVenue
	venueName string
	peers     map[string]DirectionalVenue // G3 跨所;本阶段可为 nil
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
	coinLossCount     map[string]int // 单币亏损次数(跨日清零;累计≥2 → ban 到次日)
	lastError         string
	positionPath      string
	settlementsPath   string
	controlPath       string
	cooldownPath      string
	control           okxDirectionalControl
	lastProvisionAt   time.Time
	delistGuard       *DelistGuard
	annGuard          *okxAnnouncementGuard
	// 安全网 B2: 控制面接线(可为 nil,nil 时跳过 halt 检测)
	controlPlane   *ControlPlane
	lastHaltLogAt  time.Time
}

// okxDirectionalControl 页面可控运行态(records/okx_directional/control.json):
// 用户在页面只设"交易仓本金 + 止损% + 开始/停止";杠杆固定 5X、无日熔断。
type okxDirectionalControl struct {
	Active             bool      `json:"active"`
	TradingCapitalUSDT float64   `json:"trading_capital_usdt"`
	StopLossPct        float64   `json:"stop_loss_pct"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// NewDirectionalEngine 通用方向引擎构造:注入 venue 适配器并加载持久化持仓。
func NewDirectionalEngine(venueName string, v DirectionalVenue, peers map[string]DirectionalVenue, cfg EngineConfig, recordDir string, logger *log.Logger) (*OKXDirectionalEngine, error) {
	if v == nil {
		return nil, fmt.Errorf("%s 适配器未注册", venueName)
	}
	if logger == nil {
		logger = log.Default()
	}
	// 多所方向引擎共享 "[OKX方向]" 消息前缀:非 okx 所再套一层 "[venue]" 以便按所区分/检索;
	// okx 保持原样(不打断既有监控)。beijingLogWriter 互斥写,子 logger 共享写入器安全。
	if venueName != okx.VenueName {
		logger = log.New(logger.Writer(), logger.Prefix()+"["+venueName+"]", logger.Flags())
	}
	dir := directionalRecordDir(recordDir, venueName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 %s_directional 目录失败: %w", venueName, err)
	}
	e := &OKXDirectionalEngine{
		cfg:             cfg.OKXDirectional,
		netCfg:          cfg,
		v:               v,
		venueName:       venueName,
		peers:           peers,
		logger:          logger,
		recordDir:       dir,
		nowFn:           time.Now,
		positions:       make(map[string]*okxDirectionalPosition),
		cooldownUntil:   make(map[string]time.Time),
		coinLossCount:   make(map[string]int),
		positionPath:    filepath.Join(dir, okxDirectionalPositionFile),
		settlementsPath: filepath.Join(dir, okxDirectionalSettlementsFile),
		controlPath:     filepath.Join(dir, "control.json"),
		cooldownPath:    filepath.Join(dir, okxDirectionalCooldownFile),
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
	// 单币冷却/亏损计数跨重启恢复(根治「重启清零→止损后当日 ban 从未生效」)。
	e.restoreCooldowns()
	venues := map[string]venue.Venue{venueName: v}
	for name, pv := range peers {
		venues[name] = pv
	}
	e.delistGuard = NewDelistGuard(venues, DefaultDelistGuardConfig(), logger)
	if venueName == okx.VenueName {
		e.annGuard = newOKXAnnouncementGuard(logger)
	}
	return e, nil
}

func buildDirectionalPeers(orch *Orchestrator, self string, logger *log.Logger) map[string]DirectionalVenue {
	peers := make(map[string]DirectionalVenue)
	for _, name := range []string{okx.VenueName, binance.VenueName, bybit.VenueName, gate.VenueName} {
		if name == self {
			continue
		}
		adp := crossVenueAdapterFromOrchestrator(orch, name)
		if adp == nil {
			continue
		}
		if dv, ok := adp.(DirectionalVenue); ok {
			peers[name] = dv
		} else if logger != nil {
			logger.Printf("[方向][跨所] %s 适配器未实现 DirectionalVenue,跳过", name)
		}
	}
	return peers
}

// NewDirectionalEngineForVenue 从 Orchestrator 按所名构造方向引擎。
func NewDirectionalEngineForVenue(venueName string, cfg EngineConfig, orch *Orchestrator, recordDir string, logger *log.Logger) (*OKXDirectionalEngine, error) {
	v := crossVenueAdapterFromOrchestrator(orch, venueName)
	if v == nil {
		return nil, fmt.Errorf("%s 适配器未注册", venueName)
	}
	dv, ok := v.(DirectionalVenue)
	if !ok {
		return nil, fmt.Errorf("%s 适配器未实现 DirectionalVenue", venueName)
	}
	peers := buildDirectionalPeers(orch, venueName, logger)
	return NewDirectionalEngine(venueName, dv, peers, cfg, recordDir, logger)
}

// NewOKXDirectionalEngine 从 Orchestrator 解析 OKX 适配器并委托 NewDirectionalEngineForVenue。
func NewOKXDirectionalEngine(cfg EngineConfig, orch *Orchestrator, recordDir string, logger *log.Logger) (*OKXDirectionalEngine, error) {
	return NewDirectionalEngineForVenue(okx.VenueName, cfg, orch, recordDir, logger)
}

// Venue 返回引擎所属交易所名。
func (e *OKXDirectionalEngine) Venue() string { return e.venueName }

// SettlementsPath 返回结算记录文件路径。
func (e *OKXDirectionalEngine) SettlementsPath() string { return e.settlementsPath }

// SetControlPlane 接入统一控制面(安全网 B2)。nil 表示不检测。
func (e *OKXDirectionalEngine) SetControlPlane(cp *ControlPlane) { e.controlPlane = cp }

func directionalRecordDir(base, venueName string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "records"
	}
	return filepath.Join(base, venueName+"_directional")
}

func okxDirectionalRecordDir(base string) string {
	return directionalRecordDir(base, "okx")
}

// Run 主循环:full-ticker(90s)→ runOnce(选币/开仓/完整管理);risk-ticker(20s)→ manageRiskFast(快速止损)。
func (e *OKXDirectionalEngine) Run(ctx context.Context) {
	interval := time.Duration(e.cfg.LoopIntervalSeconds) * time.Second
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	e.logger.Printf("[OKX方向] 引擎启动 live=%v 周期=%s 风控=%s", e.cfg.LiveTradingEnabled, interval, okxDirectionalRiskInterval)
	e.logEffectiveParams()
	full := time.NewTicker(interval)
	risk := time.NewTicker(okxDirectionalRiskInterval)
	defer full.Stop()
	defer risk.Stop()
	// 启动后立即执行一次完整循环。
	e.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			e.logger.Printf("[OKX方向] 引擎退出: %v", ctx.Err())
			return
		case <-full.C:
			e.runOnce(ctx)
		case <-risk.C:
			e.manageRiskFast(ctx)
		}
	}
}

// logEffectiveParams 启动时打印「最终生效」的关键交易参数(已含 control.json 覆盖)。
// 根治配置透明度问题:config.yaml / defaults.go / control.json 三处可能不一致,
// 启动日志钉死实际跑的值,避免「改了 config 却没生效」的误判。
func (e *OKXDirectionalEngine) logEffectiveParams() {
	c := e.getControl()
	cfg := e.cfg
	// 前缀:非 okx 所的 logger 已自带 "[venue]" 前缀,故此处不重复加 venueName(避免 "[bybit][bybit]")。
	e.logger.Printf("[OKX方向][生效参数] 本金=%.2fU 止损=%.0f%%(本金) 杠杆=%dX 并发上限=%d 每仓名义=%.2fU active=%v",
		c.TradingCapitalUSDT, c.StopLossPct*100, e.leverage(), e.maxConcurrent(), cfg.PerPositionNotionalUSDT, c.Active)
	e.logger.Printf("[OKX方向][生效参数] 入场: minConv=%.2f zEntry=%.2f costFloor=%.0fbps takerFallback=%.0fbps universeTopN=%d 选币周期=%dmin maxSpread=%.0fbps",
		cfg.MinOpenConviction, cfg.ZScoreEntry, cfg.CostFloorBps, cfg.OpenTakerFallbackBps, cfg.UniverseTopN, cfg.UniverseScanIntervalMinutes, cfg.MaxOpenSpreadBps)
	e.logger.Printf("[OKX方向][生效参数] 出场: quickTP=%.0f%% partialTP=%.0f%%×%.0f%% trailGap=%.0f%% trailLock=%.0f%% staleCut=%dm maxHold=%dm absMax=%dm 反转最小持有=%dm",
		cfg.QuickTPPct*100, cfg.PartialTPTriggerPct*100, cfg.PartialTPFraction*100, cfg.TrailGapPct*100, cfg.TrailLockFraction*100,
		cfg.StaleCutMinutes, cfg.MaxHoldMinutes, cfg.AbsMaxHoldMinutes, cfg.ReversalMinHoldMinutes)
	e.logger.Printf("[OKX方向][生效参数] regime: 趋势态交易=%v 资费对齐闸=%v 跨所印证=%v(否决需≥%d反向) BTC追势过滤=%v",
		cfg.TradeTrendRegime, cfg.RequireFundingAlignment, cfg.CrossVenueCorroborationEnabled, cfg.CrossVenueVetoMinPeers, cfg.AvoidBTCChase)
}

// manageRiskFast 快速风控子循环(~20s):仅拉盘口(廉价),发现触止损立即平仓。
// 不拉K线/资费,不开新仓,只做硬止损守门。并发安全;book 失败容错跳过。
func (e *OKXDirectionalEngine) manageRiskFast(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Printf("[OKX方向][快速风控] panic 已恢复: %v", r)
		}
	}()
	ctrl := e.getControl()
	for _, pos := range e.openPositionsSnapshot() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !pos.isOpen() {
			continue
		}
		book, err := e.v.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(pos.Symbol))
		if err != nil {
			continue // 廉价失败容错:跳过本仓,等下轮
		}
		mid := (book.BidPrice + book.AskPrice) / 2
		if mid <= 0 {
			continue
		}
		side := pos.directionSide()
		uPnLNotional := directionalUPnLPct(side, pos.EntryPrice, mid)
		lev := float64(e.leverage())
		uPnLAccount := uPnLNotional * lev
		// 回填实时浮盈(与 manageOne 保持同口径)+ 运行态 MFE/MAE(成交后最大浮盈/浮亏,本金口径)。
		e.mu.Lock()
		if p, ok := e.positions[pos.Symbol]; ok {
			p.LastMid = mid
			p.LastUnrealizedAccountPct = uPnLAccount
			p.LastUnrealizedUSDT = uPnLNotional * p.NotionalUSDT
			if uPnLAccount > p.MFEAccountPct {
				p.MFEAccountPct = uPnLAccount
			}
			if uPnLAccount < p.MAEAccountPct {
				p.MAEAccountPct = uPnLAccount
			}
		}
		e.mu.Unlock()
		// 取动态止损地板;为 0 时退回 -StopLossPct 作硬止损地板。
		e.mu.RLock()
		var floor float64
		if p, ok := e.positions[pos.Symbol]; ok {
			floor = p.StopFloorAccountPct
		}
		e.mu.RUnlock()
		if floor == 0 {
			stopPct := ctrl.StopLossPct
			if stopPct <= 0 {
				stopPct = e.cfg.StopLossPct
			}
			floor = -stopPct
		}
		if uPnLAccount <= floor {
			holdMin := int(e.nowFn().UTC().Sub(pos.OpenedAt).Minutes())
			if holdMin < 0 {
				holdMin = 0
			}
			e.logger.Printf("[OKX方向][快速风控] %s 浮盈%.2f%%≤止损地板%.2f%%,立即硬止损", pos.Symbol, uPnLAccount*100, floor*100)
			e.closePosition(ctx, pos, mid, uPnLNotional, holdMin, "硬止损(快速)")
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
	if e.annGuard != nil {
		e.annGuard.Refresh(ctx)
	}
	if e.delistGuard != nil {
		e.delistGuard.Refresh(ctx)
	}
	// 持仓始终管理(即便页面已停止,也继续看护止损/止盈/反转,绝不撒手不管)。先管理(可能腾出仓位),再补开。
	e.manageAll(ctx)
	// 开新仓须页面"开始"(control.Active);日熔断按用户要求默认关闭(DailyLossLimitUSDT=0 → 永不熔断)。
	ctrl := e.getControl()
	if ctrl.Active {
		// 把 OKX 统一交易账户可动用 USDT 向交易仓本金 M 靠拢(空仓双向调平;持仓时单向从资金账户补进闲置资金,使剩余空位能开出)。
		e.provisionCapital(ctx)
		if !directionalDailyLossHalted(e.dailyRealizedUSDT, e.cfg.DailyLossLimitUSDT) && e.concurrencyAllows() {
			e.openFromShortlist(ctx)
		}
	}
}

// provisionCapital 把统一交易账户可动用 USDT 向交易仓本金 M 靠拢:
//   - 空仓:双向调平到 M(超出回资金账户、不足从资金账户补);
//   - 持仓:ExecuteRebalance 只搬「自由资金」、绝不动已锁定保证金,故持仓期间也安全运行——
//     把资金账户里闲置的 USDT 单向补进交易账户,让剩余空位也能开出(修「持仓后开不出新仓」)。
// 节流 5min。
func (e *OKXDirectionalEngine) provisionCapital(ctx context.Context) {
	e.mu.RLock()
	last := e.lastProvisionAt
	e.mu.RUnlock()
	if !last.IsZero() && e.nowFn().UTC().Sub(last) < 5*time.Minute {
		return
	}
	m := e.getControl().TradingCapitalUSDT
	if m <= 0 {
		return
	}
	caps := e.v.Capabilities()
	if caps.UnifiedWallet {
		// 统一账户所(如 Bybit/Gate):无所内 funding↔trading 划转概念,可用即交易本金,不折腾。
		// OKX 虽 UnifiedWallet=true(现货/永续同池),CapitalWallets 仍含 "trading" 分仓,继续走下方逻辑。
		hasTradingWallet := false
		for _, w := range caps.CapitalWallets {
			if w == venue.Wallet("trading") {
				hasTradingWallet = true
				break
			}
		}
		if !hasTradingWallet {
			return
		}
	}
	snaps, totalMovable, err := e.v.CaptureMovableSnapshot(ctx)
	if err != nil {
		// 该所不支持所内钱包划转(如 binance 只读适配器)→ 静默跳过补仓,不刷错误噪声;直接用账户现有可用交易。
		if !errors.Is(err, venue.ErrUnsupported) {
			e.logger.Printf("[OKX方向][资金] 读取账户快照失败: %v", err)
		}
		return
	}
	var tradingAvail float64
	for _, s := range snaps {
		if s.Wallet == venue.Wallet("trading") {
			tradingAvail = s.FreeUSDT
		}
	}
	e.mu.Lock()
	e.lastProvisionAt = e.nowFn().UTC()
	e.mu.Unlock()
	// 目标「交易账户自由可用」= 交易仓本金 − 已占保证金(已占部分被锁定、不在可调内);
	// 再夹到可调总额(trading+funding 自由)以内——避免持仓时把目标错设成全额 M 导致 target>可调 报错。
	// 空仓时 deployedMargin=0 → target=min(M, 可调总额),与旧「调到 M」等价。
	target := m - e.deployedMarginUSDT()
	if target < 0 {
		target = 0
	}
	if target > totalMovable {
		target = totalMovable
	}
	if math.Abs(tradingAvail-target) <= 2.0 {
		return // 已接近目标自由可用,不折腾
	}
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][资金][OFF] 拟把交易账户可用 %.2f 调到 %.2f(本金%.2f−已占保证金,夹于可调%.2f)", tradingAvail, target, m, totalMovable)
		return
	}
	plan := e.v.PlanAllocation(target, caps)
	trs, rerr := e.v.ExecuteRebalance(ctx, plan)
	if rerr != nil {
		e.logger.Printf("[OKX方向][资金] 调账户到 %.2f 失败: %v", target, rerr)
		return
	}
	if len(trs) == 0 {
		return // 资金账户无可补余额 / 已达目标:不打无效日志
	}
	e.logger.Printf("[OKX方向][资金] 交易账户可用 %.2f→目标 %.2f(本金%.2f−已占保证金),划转 %d 笔", tradingAvail, target, m, len(trs))
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
	snaps, err := e.v.FetchFundingSnapshot(ctx)
	if err != nil {
		e.logger.Printf("[OKX方向][选币] 拉资费快照失败,沿用旧 shortlist: %v", err)
		return
	}
	sl, err := scanOKXUniverse(ctx, e.v, snaps, e.cfg, e.logger)
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
	rolled := false
	if e.dayStartCST.IsZero() || !e.dayStartCST.Equal(dayStart) {
		e.dayStartCST = dayStart
		e.dailyRealizedUSDT = 0
		e.coinLossCount = make(map[string]int) // 跨日清空单币亏损计数
		rolled = true
	}
	e.mu.Unlock()
	if rolled {
		e.persistCooldowns() // 跨日清零后落盘,避免重启又把昨日计数读回来
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
		Venue:              e.venueName,
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
			UnrealizedUSDT: p.LastUnrealizedUSDT,
		})
		st.TotalUnrealizedUSDT += p.LastUnrealizedUSDT
		if lev := float64(e.leverage()); lev > 0 {
			st.DeployedMarginUSDT += p.NotionalUSDT / lev
		}
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
	// 收益(以交易仓本金 M 为基数):当日已实现 + 当前浮盈,及其占 M 的比例。
	st.TotalPnLTodayUSDT = e.dailyRealizedUSDT + st.TotalUnrealizedUSDT
	if capUSDT > 0 {
		st.PnLPctOfCapital = st.TotalPnLTodayUSDT / capUSDT
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

// directionalSlipBps 成交滑点(bps,正=比中价更差):买单成交价高于中价为差,卖单成交价低于中价为差。
// mid 或 fill 无效时返回 0。供执行质量归因(maker 逆选择/吃单成本)。
func directionalSlipBps(side venue.Side, mid, fill float64) float64 {
	if mid <= 0 || fill <= 0 {
		return 0
	}
	if side == venue.SideBuy {
		return (fill - mid) / mid * 10000
	}
	return (mid - fill) / mid * 10000
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
	// 安全网 B2(探索档语义):仅人工紧急急停(.halt-live 文件)时跳过新开仓;
	// 账户回撤型持久 global_halt 不锁死微仓探索档(探索档不因普通风控停机,只为人工急停/系统级故障停)。
	// 在场持仓管理/平仓任何时候都不受影响。
	if e.controlPlane != nil && e.controlPlane.IsEmergencyHaltActive() {
		now := e.nowFn()
		if now.Sub(e.lastHaltLogAt) > 5*time.Minute {
			e.lastHaltLogAt = now
			e.logger.Printf("[%s][OKX方向] 人工紧急急停(.halt-live)生效,跳过新开仓(在场持仓照常管理)", e.venueName)
		}
		return
	}
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
		if e.delistGuard != nil {
			if nonTrading, known := e.delistGuard.IsNonTrading(e.venueName, sym, venue.MarketPerp); nonTrading && known {
				e.logger.Printf("[OKX方向][下架守卫] %s 非交易/下架状态,跳过开仓", sym)
				continue
			}
		}
		if e.annGuard != nil {
			if risky, reason := e.annGuard.IsRisky(sym); risky {
				e.logger.Printf("[OKX方向][公告] %s 命中风险公告,跳过开仓: %s", sym, reason)
				continue
			}
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
	// BTC 近期方向(用于追势过滤):每周期取一次。1=涨 -1=跌 0=横盘/未知。
	btcDir := 0
	if e.cfg.AvoidBTCChase {
		look := e.cfg.BTCChaseLookbackBars
		if look <= 0 {
			look = 6
		}
		minMove := e.cfg.BTCChaseMinMovePct
		if minMove <= 0 {
			minMove = 0.0008
		}
		if bc, berr := e.fetchCloses(ctx, venue.Symbol("BTC-USDT")); berr == nil && len(bc) > look {
			ref := bc[len(bc)-1-look]
			if ref > 0 {
				m := (bc[len(bc)-1] - ref) / ref
				if m > minMove {
					btcDir = 1
				} else if m < -minMove {
					btcDir = -1
				}
			}
		}
	}
	opened := 0
	for _, p := range picks {
		if opened >= slots {
			break
		}
		// BTC 追势过滤:与 BTC 近期方向同向=追势(负 edge),跳过;逆 BTC/横盘 才开。
		if btcDir != 0 {
			sigDir := 1
			if p.signal.Side == DirectionShort {
				sigDir = -1
			}
			if sigDir == btcDir {
				e.logger.Printf("[OKX方向] %s %s 与BTC近期方向同向(追势,负edge),跳过", p.sym, p.signal.Side.String())
				continue
			}
		}
		peersDir := e.gatherPeerDirections(ctx, p.sym)
		convDelta, veto, creason := crossVenueCorroboration(p.signal.Side, peersDir, e.cfg)
		if veto {
			e.logger.Printf("[OKX方向][跨所] 否决 %s %s: %s", p.sym, p.signal.Side.String(), creason)
			continue
		}
		if convDelta != 0 {
			p.signal.Conviction = clamp01(p.signal.Conviction + convDelta)
		}
		if creason != "" && creason != "无跨所数据" {
			e.logger.Printf("[OKX方向][跨所] %s %s %s 信心→%.2f", p.sym, p.signal.Side.String(), creason, p.signal.Conviction)
		}
		// 最低开仓信心闸:只做高信心档(回测证实仅高信心档扣费后正 edge,低信心档确定性亏损)。
		if mc := e.cfg.MinOpenConviction; mc > 0 && p.signal.Conviction < mc {
			e.logger.Printf("[OKX方向] %s 信心%.3f < 最低开仓信心%.3f,跳过(只做高信心档)", p.sym, p.signal.Conviction, mc)
			continue
		}
		// 单币跨所集中度上限:同币+同方向已有 ≥cap 个所持仓则跳过,避免跨所印证把四所推成同一笔相关下注(错则齐亏)。
		if cap := e.cfg.MaxVenuesPerSymbolSide; cap > 0 {
			if held := e.peerHoldersOfSymbol(p.sym, p.signal.Side); held >= cap {
				e.logger.Printf("[OKX方向] %s 已有%d个所持同向仓(≥上限%d),跳过(降跨所集中度)", p.sym, held, cap)
				continue
			}
		}
		if e.openPosition(ctx, p.sym, p.signal, p.rate) {
			opened++
		}
	}
	_ = nSignal
}

// peerHoldersOfSymbol 统计有多少其他所当前持有 sym 的同方向仓(读 peer 的 position.json,跨所共享文件系统)。
// 用于单币跨所集中度上限,避免四所对同一币同向同开。读文件失败的 peer 不计入(best-effort)。
func (e *OKXDirectionalEngine) peerHoldersOfSymbol(sym string, side DirectionSide) int {
	base := filepath.Dir(e.recordDir)
	count := 0
	for peerName := range e.peers {
		pp := filepath.Join(base, peerName+"_directional", okxDirectionalPositionFile)
		data, err := os.ReadFile(pp)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		var list []*okxDirectionalPosition
		if json.Unmarshal(data, &list) != nil {
			continue
		}
		for _, p := range list {
			if p != nil && p.isOpen() && p.Symbol == sym && p.directionSide() == side {
				count++
				break // 计所数,不计仓数
			}
		}
	}
	return count
}

// gatherPeerDirections best-effort 拉其他所同币近窗 K 线并判方向;该所无此币/失败则跳过(不计入)。
func (e *OKXDirectionalEngine) gatherPeerDirections(ctx context.Context, sym string) []peerDirState {
	if len(e.peers) == 0 {
		return nil
	}
	bar := strings.TrimSpace(e.cfg.CandleBar)
	if bar == "" {
		bar = "5m"
	}
	limit := e.cfg.CandleLimit
	if limit <= 0 {
		limit = 120
	}
	win := e.cfg.SignalWindowBars
	if win <= 0 {
		win = 24
	}
	deadband := e.cfg.CrossVenueDeadbandPct
	if deadband <= 0 {
		deadband = 0.003
	}
	out := make([]peerDirState, 0, len(e.peers))
	for name, pv := range e.peers {
		if pv == nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		candles, err := pv.FetchCandles(cctx, venue.MarketPerp, venue.Symbol(sym), bar, limit)
		cancel()
		if err != nil || len(candles) < 2 {
			continue
		}
		closes := make([]float64, 0, len(candles))
		for _, c := range candles {
			if c.Close > 0 {
				closes = append(closes, c.Close)
			}
		}
		out = append(out, peerDirState{Venue: name, Side: recentDirection(closes, win, deadband)})
	}
	return out
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
	candles, err := e.v.FetchCandles(ctx, venue.MarketPerp, sym, bar, limit)
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
	book, err := e.v.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(sym))
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
	// 修复B:开仓流动性闸——盘口价差过大(薄盘)直接跳过开仓。
	if e.cfg.MaxOpenSpreadBps > 0 && book.BidPrice > 0 && book.AskPrice > 0 {
		spreadBps := (book.AskPrice - book.BidPrice) / mid * 10000
		if spreadBps > e.cfg.MaxOpenSpreadBps {
			e.logger.Printf("[OKX方向][流动性] %s 盘口价差 %.0fbps > %.0fbps,薄盘跳过开仓", sym, spreadBps, e.cfg.MaxOpenSpreadBps)
			return false
		}
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(sym), venue.MarketPerp)
	if !ok {
		e.setLastError("instrument 缺失")
		e.logger.Printf("[OKX方向] %s 无 instrument 元数据,跳过开仓", sym)
		return false
	}
	caps := e.v.Capabilities()
	ctrl := e.getControl()
	// 仓位大小两种模型:
	//   ① PerPositionNotionalUSDT>0(广撒网档):每仓固定名义,持仓数量由「资金/每仓保证金」决定、上限 MaxConcurrent。
	//      即"上限设30,资金不够就按资金可开的最大数量"——下方已部署保证金闸自动封顶。
	//   ② 否则(旧模型):每仓本金 = 交易仓本金 / 最大并发,满仓总名义=本金×杠杆。
	var perPosCapital float64
	if e.cfg.PerPositionNotionalUSDT > 0 {
		perPosCapital = e.cfg.PerPositionNotionalUSDT / float64(e.leverage())
	} else {
		perPosCapital = ctrl.TradingCapitalUSDT / float64(e.maxConcurrent())
	}
	// 趋势单减半仓:趋势态胜率低(复盘 25% vs range 53%),少冒险,资金偏向 range 反转单。
	if sig.Regime == RegimeTrend {
		f := e.cfg.TrendSizeFraction
		if f <= 0 || f > 1 {
			f = 1
		}
		perPosCapital *= f
	}
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
	avail, merr := fetchVenuePerpAvailableUSDT(ctx, e.v)
	if merr == nil && avail+1e-9 < perPosCapital {
		// 永续/合约钱包可用不足时,先从闲置钱包(现货/全仓杠杆)把 USDT 补进合约钱包再复检。
		// 根治 binance「钱在现货、合约钱包可用=0、永远开不出仓」——provisionCapital 走的 CaptureMovableSnapshot
		// 对 binance 返回 ErrUnsupported 被跳过,但 binance 实现了 TopUpPerpMarginUSDT,此处显式调用补足。
		// 绝不动资金账户(用户红线),TopUp 仅从现货/全仓杠杆搬;不支持的所(统一钱包)best-effort 跳过。
		if tp, ok := e.v.(venue.PerpMarginProvider); ok {
			// 目标=整笔交易仓本金(把现货/全仓/资金钱包资金一次补够,而非只补一仓),让多仓都能开出。
			if moved, terr := tp.TopUpPerpMarginUSDT(ctx, ctrl.TradingCapitalUSDT); terr == nil && moved > 0 {
				e.logger.Printf("[OKX方向] %s 合约钱包可用不足,从闲置钱包补 %.2f USDT 进合约钱包(目标交易仓本金%.2f)后复检", sym, moved, ctrl.TradingCapitalUSDT)
				avail, merr = fetchVenuePerpAvailableUSDT(ctx, e.v)
			}
		}
	}
	if merr == nil && avail+1e-9 < perPosCapital {
		e.logger.Printf("[OKX方向] %s 永续可用 %.2f < 每仓本金 %.2f,跳过开仓", sym, avail, perPosCapital)
		return false
	}
	if err := e.v.SetLeverage(ctx, venue.Symbol(sym), e.leverage()); err != nil {
		e.setLastError(err.Error())
		e.logger.Printf("[OKX方向] %s 设杠杆失败: %v", sym, err)
		return false
	}
	clientID := okxDirectionalClientID(sym, "open")
	res, tookTaker, oerr := e.placeDirectionalLeg(ctx, venue.Symbol(sym), side, qty, clientID, false, instr, caps.ContractBasedPerp, false)
	if oerr != nil {
		e.setLastError(oerr.Error())
		e.logger.Printf("[OKX方向] %s 开仓失败: %v", sym, oerr)
		return false
	}
	if res.ExecutedQty <= executionQtyTolerance {
		// maker-only 未成交属正常流程(不吃单设计);不写 last_error 避免污染面板。
		e.logger.Printf("[OKX方向] %s maker 未成交,放弃本轮开仓(maker-only,不吃单;executed=%.8f)", sym, res.ExecutedQty)
		return false
	}
	fillPrice, trusted := sanitizeFillPrice(res.AvgPrice, mid, e.cfg.FillPriceMaxDevPct)
	if !trusted {
		e.logger.Printf("[OKX方向][记账] %s 开仓成交价 %.6f 偏离 mid %.6f 过大或无效,记账回退 mid", sym, res.AvgPrice, mid)
	}
	openedAt := e.nowFn().UTC()
	pos := &okxDirectionalPosition{
		Symbol:        sym,
		Side:          sig.Side.String(),
		EntryPrice:    fillPrice,
		Qty:           res.ExecutedQty,
		NotionalUSDT:  notional,
		OpenedAt:      openedAt,
		Regime:        sig.Regime.String(),
		FundingAtOpen: fundingRate,
		PositionID:    fmt.Sprintf("%s-%d", sym, openedAt.UnixNano()),
		EntryMid:      mid,
		EntrySlipBps:  directionalSlipBps(side, mid, fillPrice),
		OpenViaTaker:  tookTaker,
		OpenFeeUSDT:   math.Abs(res.Fee),
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
	book, err := e.v.FetchBookTicker(ctx, venue.MarketPerp, sym)
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
	// 回填实时浮盈到持仓(供面板显示)+ 运行态 MFE/MAE(成交后最大浮盈/浮亏,本金口径)。
	e.mu.Lock()
	if p, ok := e.positions[pos.Symbol]; ok {
		p.LastMid = mid
		p.LastUnrealizedAccountPct = uPnLAccount
		p.LastUnrealizedUSDT = uPnLNotional * p.NotionalUSDT // 实时浮盈(USDT)
		if uPnLAccount > p.MFEAccountPct {
			p.MFEAccountPct = uPnLAccount
		}
		if uPnLAccount < p.MAEAccountPct {
			p.MAEAccountPct = uPnLAccount
		}
	}
	e.mu.Unlock()
	holdMin := int(e.nowFn().UTC().Sub(pos.OpenedAt).Minutes())
	if holdMin < 0 {
		holdMin = 0
	}
	if e.delistGuard != nil {
		if nonTrading, known := e.delistGuard.IsNonTrading(e.venueName, pos.Symbol, venue.MarketPerp); nonTrading && known {
			e.logger.Printf("[OKX方向][下架守卫] %s 非交易/下架,主动平仓", pos.Symbol)
			e.closePosition(ctx, pos, mid, uPnLNotional, holdMin, "下架/非交易主动平仓")
			return
		}
	}
	closes, cerr := e.fetchCloses(ctx, sym)
	if cerr != nil {
		e.setLastError(cerr.Error())
		e.logger.Printf("[OKX方向] %s 管理K线失败: %v", pos.Symbol, cerr)
		return
	}
	rate := pos.FundingAtOpen
	if snaps, serr := e.v.FetchFundingSnapshot(ctx); serr == nil {
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
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		e.setLastError("instrument 缺失")
		return
	}
	caps := e.v.Capabilities()
	clientID := okxDirectionalClientID(pos.Symbol, "close")
	// 风控/不利信号 → urgent=true 跳过 maker 直接 IOC 吃单,且使用放宽滑点防薄盘卡单。
	urgentExit := strings.Contains(reason, "止损") || strings.Contains(reason, "反转") ||
		strings.Contains(reason, "保盈") || strings.Contains(reason, "停滞") ||
		strings.Contains(reason, "下架")
	res, closeTookTaker, cerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), closeSide, pos.Qty, clientID, true, instr, caps.ContractBasedPerp, urgentExit)
	exitPrice, exitTrusted := sanitizeFillPrice(res.AvgPrice, mid, e.cfg.FillPriceMaxDevPct)
	if !exitTrusted {
		e.logger.Printf("[OKX方向][记账] %s 平仓成交价 %.6f 偏离 mid %.6f 过大或无效,记账回退 mid", pos.Symbol, res.AvgPrice, mid)
	}
	if cerr != nil || res.ExecutedQty <= executionQtyTolerance {
		msg := "平仓未成交"
		if cerr != nil {
			msg = cerr.Error()
		}
		// 交易所报「无持仓/空仓」=该仓在所上已不存在(手动平过/幽灵持仓):移除跟踪,不再每周期空转重试。
		if cerr != nil {
			le := strings.ToLower(cerr.Error())
			if strings.Contains(le, "empty position") || strings.Contains(le, "position not found") ||
				strings.Contains(le, "no position") || strings.Contains(le, "no open position") {
				e.mu.Lock()
				delete(e.positions, pos.Symbol)
				e.mu.Unlock()
				e.persistPositions()
				e.logger.Printf("[OKX方向] %s 交易所已无持仓(幽灵),移除跟踪不再重试: %v", pos.Symbol, cerr)
				return
			}
		}
		e.setLastError(msg)
		e.logger.Printf("[OKX方向] %s 平仓失败: %v", pos.Symbol, cerr)
		return
	}
	// 记账口径:默认盘口(平仓时刻真实 mark-to-market);仅当成交价可信且不是「回读成持仓均价」的伪值时才用真实成交价。
	// 根治 Gate 假 0:某些 venue 平仓 AvgPrice 被回读成持仓均价(exit≈entry),用成交价算就记成假 0 污染复盘。
	// 因此 exit≈entry(到 1e-6)一律判为回读伪值 → 用盘口口径(realized 已是盘口默认值)并把 Exit 记成盘口。
	realized := uPnLPct * pos.NotionalUSDT
	fillIsArtifact := exitPrice <= 0 || (pos.EntryPrice > 0 && math.Abs(exitPrice-pos.EntryPrice) < pos.EntryPrice*1e-6)
	if exitTrusted && exitPrice > 0 && pos.EntryPrice > 0 && !fillIsArtifact {
		realized = directionalUPnLPct(pos.directionSide(), pos.EntryPrice, exitPrice) * pos.NotionalUSDT
	} else {
		if fillIsArtifact && exitPrice > 0 && math.Abs(uPnLPct) > 0.002 {
			e.logger.Printf("[OKX方向][记账] %s 平仓成交价(%.8f)≈持仓均价但实时浮盈%.2f%%,疑成交价回读异常,改用盘口口径记账", pos.Symbol, exitPrice, uPnLPct*100)
		}
		exitPrice = mid // 回读伪值/无效 → Exit 记盘口,避免落假 0
	}
	holdSec := int(e.nowFn().UTC().Sub(pos.OpenedAt).Seconds())
	if holdSec < 0 {
		holdSec = 0
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
		PositionID:    pos.PositionID,
		HoldSeconds:   holdSec,
		EntrySlipBps:  pos.EntrySlipBps,
		ExitSlipBps:   directionalSlipBps(closeSide, mid, exitPrice),
		OpenViaTaker:  pos.OpenViaTaker,
		CloseViaTaker: closeTookTaker,
		FeeUSDT:       pos.OpenFeeUSDT + math.Abs(res.Fee), // 剩余开仓侧实扣费(分批已按比例扣减)+ 本次平仓侧实扣费
		MFEAccountPct: pos.MFEAccountPct,
		MAEAccountPct: pos.MAEAccountPct,
	}
	if err := appendJSONLRecord(e.settlementsPath, settle); err != nil {
		e.logger.Printf("[OKX方向] 结算落盘失败: %v", err)
	}
	coolMin := float64(e.cfg.SymbolCooldownMinutes)
	if coolMin <= 0 {
		coolMin = 30
	}
	now := e.nowFn().UTC()
	isStopLoss := strings.Contains(reason, "止损")
	// 单币亏损记忆(ban/冷却)增强:
	//   - reason 含 "止损" → ban 到次日 CST 0 点(彻底晾一天)
	//   - realized<0 且非止损标签:coinLossCount[sym]++;≥2 → 同样 ban 到次日;否则 cooldown×mult 至少 120min
	//   - realized≥0:正常冷却,不增计数
	var coolUntil time.Time
	if isStopLoss {
		coolUntil = beijingCSTDayStart(now).Add(24 * time.Hour)
	} else if realized < 0 {
		e.mu.Lock()
		e.coinLossCount[pos.Symbol]++
		count := e.coinLossCount[pos.Symbol]
		e.mu.Unlock()
		if count >= 2 {
			coolUntil = beijingCSTDayStart(now).Add(24 * time.Hour)
		} else {
			mult := e.cfg.LossCooldownMultiplier
			if mult < 1 {
				mult = 1
			}
			cm := coolMin * mult
			if cm < 120 { // 至少 2 小时
				cm = 120
			}
			coolUntil = now.Add(time.Duration(cm * float64(time.Minute)))
		}
	} else {
		coolUntil = now.Add(time.Duration(coolMin * float64(time.Minute)))
	}
	e.mu.Lock()
	e.dailyRealizedUSDT += realized
	e.cooldownUntil[pos.Symbol] = coolUntil
	delete(e.positions, pos.Symbol)
	e.mu.Unlock()
	e.persistPositions()
	e.persistCooldowns() // 冷却/亏损计数落盘,重启可恢复(止损 ban 不再因重启失效)
	e.clearLastError()
	e.logger.Printf("[OKX方向] 已平 %s %s 原因=%s 实现≈%.4f USDT", pos.Symbol, pos.Side, reason, realized)
}

// partialClose 部分止盈:reduce-only 平掉 fraction 比例,记部分结算,缩减持仓 qty/名义,累加当日已实现。成功返回 true。
func (e *OKXDirectionalEngine) partialClose(ctx context.Context, pos *okxDirectionalPosition, fraction, mid, uPnLNotional float64, holdMin int, reason string) bool {
	if !e.cfg.LiveTradingEnabled {
		e.logger.Printf("[OKX方向][OFF] 拟部分止盈 %s frac=%.2f", pos.Symbol, fraction)
		return false
	}
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		return false
	}
	caps := e.v.Capabilities()
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
	res, partialTookTaker, cerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), closeSide, closeQty, clientID, true, instr, caps.ContractBasedPerp, false)
	if cerr != nil || res.ExecutedQty <= executionQtyTolerance {
		e.logger.Printf("[OKX方向] %s 部分止盈下单失败: %v", pos.Symbol, cerr)
		return false
	}
	exitPrice, exitTrusted := sanitizeFillPrice(res.AvgPrice, mid, e.cfg.FillPriceMaxDevPct)
	if !exitTrusted {
		e.logger.Printf("[OKX方向][记账] %s 部分平仓成交价 %.6f 偏离 mid %.6f 过大或无效,记账回退 mid", pos.Symbol, res.AvgPrice, mid)
	}
	closedFrac := res.ExecutedQty / pos.Qty
	if closedFrac > 1 {
		closedFrac = 1
	}
	closedNotional := pos.NotionalUSDT * closedFrac
	realized := directionalUPnLPct(pos.directionSide(), pos.EntryPrice, exitPrice) * closedNotional
	holdSec := int(e.nowFn().UTC().Sub(pos.OpenedAt).Seconds())
	if holdSec < 0 {
		holdSec = 0
	}
	proratedOpenFee := pos.OpenFeeUSDT * closedFrac // 开仓侧实扣费按平掉比例归属本次部分结算
	settle := okxDirectionalSettlement{
		Time: e.nowFn().UTC(), Symbol: pos.Symbol, Side: pos.Side, Entry: pos.EntryPrice, Exit: exitPrice,
		NotionalUSDT: closedNotional, RealizedUSDT: realized, HoldMinutes: holdMin, Regime: pos.Regime,
		Reason: reason + "(部分)", FundingAtOpen: pos.FundingAtOpen, PositionID: pos.PositionID,
		HoldSeconds: holdSec, EntrySlipBps: pos.EntrySlipBps, ExitSlipBps: directionalSlipBps(closeSide, mid, exitPrice),
		OpenViaTaker: pos.OpenViaTaker, CloseViaTaker: partialTookTaker,
		FeeUSDT: proratedOpenFee + math.Abs(res.Fee), MFEAccountPct: pos.MFEAccountPct, MAEAccountPct: pos.MAEAccountPct,
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
		r.OpenFeeUSDT -= proratedOpenFee // 已归属本次部分结算的开仓费扣减,余仓全平时只记剩余
		if r.OpenFeeUSDT < 0 {
			r.OpenFeeUSDT = 0
		}
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
	instr, ok := lookupVenueInstrumentForMarket(ctx, e.v, venue.Symbol(pos.Symbol), venue.MarketPerp)
	if !ok {
		return false
	}
	caps := e.v.Capabilities()
	addSide := venue.SideBuy
	if side == DirectionShort {
		addSide = venue.SideSell
	}
	addQty := normalizeVenueQty(tranche/mid, instr, caps.ContractBasedPerp)
	if addQty <= 0 || !openLegTradableAfterQuantize(addQty, mid, instr) {
		return false
	}
	clientID := okxDirectionalClientID(pos.Symbol, "add")
	res, addTookTaker, oerr := e.placeDirectionalLeg(ctx, venue.Symbol(pos.Symbol), addSide, addQty, clientID, false, instr, caps.ContractBasedPerp, false)
	if oerr != nil || res.ExecutedQty <= executionQtyTolerance {
		e.logger.Printf("[OKX方向] %s 补仓失败: %v", pos.Symbol, oerr)
		return false
	}
	fillPrice, fillTrusted := sanitizeFillPrice(res.AvgPrice, mid, e.cfg.FillPriceMaxDevPct)
	if !fillTrusted {
		e.logger.Printf("[OKX方向][记账] %s 补仓成交价 %.6f 偏离 mid %.6f 过大或无效,记账回退 mid", pos.Symbol, res.AvgPrice, mid)
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
		r.OpenFeeUSDT += math.Abs(res.Fee) // 补仓侧实扣费累计到开仓费(全平/分批按比例归属)
		if addTookTaker {
			r.OpenViaTaker = true
		}
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
// 返回 (合并成交结果, 是否走了 IOC 吃单兜底, error):tookTaker 供执行质量归因(maker 逆选择)。
func (e *OKXDirectionalEngine) placeDirectionalLeg(
	ctx context.Context,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	instr venue.Instrument,
	contractBased bool,
	urgent bool,
) (venue.OrderResult, bool, error) {
	deadline := e.nowFn().UTC().Add(okxDirectionalMakerDeadline)
	var acc venue.OrderResult
	remaining := qty
	live := e.cfg.LiveTradingEnabled

	// 紧急离场(硬止损/信号反转):跳过 maker 被动追价,直奔 IOC 吃单立即平仓——
	// 否则价格朝反方向狂奔时 maker 永不成交,拖到 IOC 时已冲穿止损(实测滑到 -36~-45% 账户)。
	for attempt := 1; !urgent && attempt <= okxDirectionalMakerRounds && remaining > executionQtyTolerance; attempt++ {
		now := e.nowFn().UTC()
		if !now.Add(okxDirectionalMakerInterval + okxDirectionalMakerTakerBuf).Before(deadline) {
			break
		}
		book, berr := e.v.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
			break
		}
		price := makerPassivePrice(side, book)
		price, remaining = quantizeOrderPriceQty(price, remaining, side, instr, contractBased, e.logger, string(sym))
		makerID := crossVenueLegClientID(clientID, fmt.Sprintf("m%d", attempt))
		logLiveOrderLeg(e.logger, e.venueName, venue.MarketPerp, string(sym), side, price, instr.TickSize, remaining, instr.StepSize)
		placed, perr := e.v.PlaceOrder(ctx, venue.OrderRequest{
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
				return acc, false, nil
			}
		}
		waitUntil := e.nowFn().UTC().Add(okxDirectionalMakerInterval)
		for e.nowFn().UTC().Before(waitUntil) {
			res, qerr := e.v.QueryOrder(ctx, venue.MarketPerp, sym, makerID)
			if qerr == nil {
				switch strings.ToLower(res.Status) {
				case "filled":
					acc = mergeCrossVenueOrderResults(acc, res)
					remaining = qty - acc.ExecutedQty
					if remaining <= executionQtyTolerance {
						return acc, false, nil
					}
				case "canceled", "rejected":
					goto nextMakerRound
				}
			}
			select {
			case <-ctx.Done():
				_ = e.v.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
				return acc, false, ctx.Err()
			case <-time.After(crossVenueConfirmPoll):
			}
		}
	nextMakerRound:
		_ = e.v.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
		if res, qerr := e.v.QueryOrder(ctx, venue.MarketPerp, sym, makerID); qerr == nil && res.ExecutedQty > executionQtyTolerance {
			acc = mergeCrossVenueOrderResults(acc, res)
			remaining = qty - acc.ExecutedQty
		}
		if remaining <= executionQtyTolerance {
			return acc, false, nil
		}
	}

	if remaining <= executionQtyTolerance && acc.ExecutedQty > executionQtyTolerance {
		return acc, false, nil
	}
	if remaining <= executionQtyTolerance {
		remaining = qty - acc.ExecutedQty
	}
	// 开仓/补仓腿(reduceOnly=false):默认只做 maker(maker 没挂上即放弃,保护薄 edge);
	// 探索高频档可配 OpenTakerFallbackBps>0,允许小滑点 IOC 吃单兜底以提高成交率/频率(代价多付吃单成本)。
	// 平仓腿(reduceOnly=true)始终保留下方 IOC 兜底,确保止损/超时能离场。
	openTakerSlip := e.cfg.OpenTakerFallbackBps
	if !reduceOnly && openTakerSlip <= 0 {
		return acc, false, nil
	}
	book, err := e.v.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		return acc, false, err
	}
	// 滑点选择:平仓默认 40bps;风控急平 150bps;开仓吃单兜底用 OpenTakerFallbackBps。
	slippageBps := okxDirectionalTakerSlippageBps
	if !reduceOnly {
		slippageBps = openTakerSlip
	}
	if urgent {
		slippageBps = okxDirectionalUrgentSlippageBps
	}
	takerPrice := iocLimitPrice(side, book, slippageBps)
	takerPrice, remaining = quantizeOrderPriceQty(takerPrice, remaining, side, instr, contractBased, e.logger, string(sym))
	takerID := crossVenueLegClientID(clientID, "tk")
	logLiveOrderLeg(e.logger, e.venueName, venue.MarketPerp, string(sym), side, takerPrice, instr.TickSize, remaining, instr.StepSize)
	takerRes, terr := e.v.PlaceOrder(ctx, venue.OrderRequest{
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
			return acc, false, terr
		}
		return takerRes, takerRes.ExecutedQty > executionQtyTolerance, terr
	}
	if takerRes.ExecutedQty <= executionQtyTolerance && live {
		confirmed, qerr := e.confirmDirectionalFill(ctx, sym, takerID)
		if qerr == nil && confirmed.ExecutedQty > executionQtyTolerance {
			takerRes = confirmed
		}
	}
	tookTaker := takerRes.ExecutedQty > executionQtyTolerance
	return mergeCrossVenueOrderResults(acc, takerRes), tookTaker, nil
}

func (e *OKXDirectionalEngine) confirmDirectionalFill(ctx context.Context, sym venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.nowFn().UTC().Add(crossVenueConfirmTimeout)
	var last venue.OrderResult
	for {
		res, err := e.v.QueryOrder(ctx, venue.MarketPerp, sym, clientID)
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

// okxDirectionalCooldownState 单币冷却/亏损计数的持久化快照(records/{venue}_directional/cooldowns.json)。
// 根治「冷却/ban 仅存内存,重启即清零」——本 session 重启频繁(日志已见上百次),导致
// 「硬止损→当日 ban」「单币亏 2 次→晾一天」实际从未生效,亏损币反复快速复入污染 PF。
type okxDirectionalCooldownState struct {
	CooldownUntil map[string]time.Time `json:"cooldown_until"`
	CoinLossCount map[string]int       `json:"coin_loss_count"`
	DayStartCST   time.Time            `json:"day_start_cst"`
	SavedAt       time.Time            `json:"saved_at"`
}

// restoreCooldowns 启动时从 cooldowns.json 恢复冷却/亏损计数。
//   - 同一 CST 交易日:恢复 cooldownUntil + coinLossCount + dayStartCST(令 refreshCSTDay 不误清当日计数);
//   - 跨日(保存的是旧交易日):只恢复尚未到期的 cooldownUntil(ban 到次日的仍有效),
//     丢弃 coinLossCount(本就跨日清零),dayStartCST 留零由 refreshCSTDay 正常初始化。
func (e *OKXDirectionalEngine) restoreCooldowns() {
	st, ok := e.loadCooldowns()
	if !ok {
		return
	}
	now := e.nowFn().UTC()
	today := beijingCSTDayStart(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cooldownUntil == nil {
		e.cooldownUntil = make(map[string]time.Time)
	}
	restored := 0
	for sym, until := range st.CooldownUntil {
		if now.Before(until) { // 只恢复仍在冷却期内的(过期的无意义)
			e.cooldownUntil[sym] = until
			restored++
		}
	}
	sameDay := !st.DayStartCST.IsZero() && st.DayStartCST.Equal(today)
	if sameDay {
		e.coinLossCount = make(map[string]int, len(st.CoinLossCount))
		for sym, c := range st.CoinLossCount {
			e.coinLossCount[sym] = c
		}
		e.dayStartCST = today // 防 refreshCSTDay 把当日已累计的亏损计数清零
		e.dailyRealizedUSDT = 0
	}
	e.logger.Printf("[OKX方向][冷却] 启动恢复:冷却币=%d 当日亏损计数=%d 同日=%v",
		restored, len(e.coinLossCount), sameDay)
}

func (e *OKXDirectionalEngine) loadCooldowns() (okxDirectionalCooldownState, bool) {
	data, err := os.ReadFile(e.cooldownPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return okxDirectionalCooldownState{}, false
	}
	var st okxDirectionalCooldownState
	if err := json.Unmarshal(data, &st); err != nil {
		return okxDirectionalCooldownState{}, false
	}
	return st, true
}

// persistCooldowns 落盘冷却/亏损计数(调用方持有或未持有锁均安全:内部自取读锁拷贝)。
// 落盘前剪掉已过期的 cooldownUntil 项,避免文件无界增长。
func (e *OKXDirectionalEngine) persistCooldowns() {
	now := e.nowFn().UTC()
	e.mu.RLock()
	cd := make(map[string]time.Time, len(e.cooldownUntil))
	for sym, until := range e.cooldownUntil {
		if now.Before(until) {
			cd[sym] = until
		}
	}
	lc := make(map[string]int, len(e.coinLossCount))
	for sym, c := range e.coinLossCount {
		if c > 0 {
			lc[sym] = c
		}
	}
	st := okxDirectionalCooldownState{
		CooldownUntil: cd,
		CoinLossCount: lc,
		DayStartCST:   e.dayStartCST,
		SavedAt:       now,
	}
	e.mu.RUnlock()
	if err := writeJSONFile(e.cooldownPath, st); err != nil {
		e.logger.Printf("[OKX方向][冷却] 落盘失败: %v", err)
	}
}
