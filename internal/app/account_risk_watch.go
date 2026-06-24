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
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"lingxi/internal/money"
)

const (
	accountRiskWatchFileName = "account_risk_watch.json"
	accountRiskWatchInterval = 60 * time.Second

	// defaultMaxStrategyCumulativeLossUSDT 单策略累计净亏一级硬停默认(数百 U 账户;可被 RiskConfig 覆盖)。
	defaultMaxStrategyCumulativeLossUSDT = 35.0
	// defaultMaxAccountOpenNotionalUSDT 在场总名义一级硬停默认(略低于 MaxDailyOpenNotionalUSDT 1500)。
	defaultMaxAccountOpenNotionalUSDT = 1200.0
	// defaultAutoFlattenAccountDrawdownUSDT 二级自动全平默认:明显高于一级 MaxAccountDrawdownUSDT(60)。
	defaultAutoFlattenAccountDrawdownUSDT = 150.0
	// defaultAccountRiskMinFundFlowStepUSDT 充提检测默认最小步长(USDT)。
	defaultAccountRiskMinFundFlowStepUSDT = 5.0
	// defaultAccountRiskMaxEquityTickDropUSDT 单 tick 权益骤降跳过评估默认(USDT)。
	defaultAccountRiskMaxEquityTickDropUSDT = 50.0
)

// accountRiskSnapshot 单次观测的账户级风险视图。
type accountRiskSnapshot struct {
	ObservedAt                  time.Time          `json:"observed_at"`
	DayStartCST                 time.Time          `json:"day_start_cst"`
	DailyRealizedPnLUSDT        float64            `json:"daily_realized_pnl_usdt"`
	DailyEquityDeltaUSDT        float64            `json:"daily_equity_delta_usdt"`
	DailyFundFlowAdjustUSDT     float64            `json:"daily_fund_flow_adjust_usdt,omitempty"`
	DailyEquityDeltaForRiskUSDT float64            `json:"daily_equity_delta_for_risk_usdt"`
	VenueEquityDeltas           map[string]float64 `json:"venue_equity_deltas,omitempty"`
	EquityDataTrusted           bool               `json:"equity_data_trusted"`
	EquityDataUntrustedReason   string             `json:"equity_data_untrusted_reason,omitempty"`
	DailyLosingTradeCount       int                `json:"daily_losing_trade_count"`
	CumulativeRealizedPnL       float64            `json:"cumulative_realized_pnl_usdt"`
	RealizedPnLPeakUSDT         float64            `json:"realized_pnl_peak_usdt"`
	AccountDrawdownUSDT         float64            `json:"account_drawdown_usdt"`
	StrategyNetPnL              map[string]float64 `json:"strategy_net_pnl,omitempty"`
	RealEquityTotalUSDT         float64            `json:"real_equity_total_usdt"`
	RealEquityPeakUSDT          float64            `json:"real_equity_peak_usdt"`
	RealEquityDrawdownUSDT      float64            `json:"real_equity_drawdown_usdt"`
	// RealEquityDrawdownAdjustedUSDT 扣全时净充提后的交易口径回撤(自动全平/告警以此为准),
	// 提现不再被算成回撤;真实交易亏损仍如实计入。
	RealEquityDrawdownAdjustedUSDT float64        `json:"real_equity_drawdown_adjusted_usdt"`
	RealEquityQualityPassed        bool           `json:"real_equity_quality_passed"`
	RealEquityQualityFailReason    string         `json:"real_equity_quality_fail_reason,omitempty"`
	RealEquityPeakConfirmed        bool           `json:"real_equity_peak_confirmed"`
	OpenPositionCount              int            `json:"open_position_count"`
	OpenNotionalUSDT               float64        `json:"open_notional_usdt"`
	PositionsByStrategy            map[string]int `json:"positions_by_strategy,omitempty"`
}

// accountRiskThresholdResult 阈值判定结果(Stage A 告警 + Stage B 硬停输入)。
type accountRiskThresholdResult struct {
	DailyLossBreached bool   `json:"daily_loss_breached"`
	DailyLossReason   string `json:"daily_loss_reason,omitempty"`
	DrawdownBreached  bool   `json:"drawdown_breached"`
	DrawdownReason    string `json:"drawdown_reason,omitempty"`
	AnyBreached       bool   `json:"any_breached"`
}

// accountHardHaltIntent 一级硬停判定(纯函数输出,无副作用;非破坏性,仅停新开仓)。
type accountHardHaltIntent struct {
	ShouldHalt            bool     `json:"should_halt"`
	Reasons               []string `json:"reasons,omitempty"`
	DailyLossBreached     bool     `json:"daily_loss_breached"`
	DailyLosingTradesHit  bool     `json:"daily_losing_trades_hit"`
	SettlementDrawdownHit bool     `json:"settlement_drawdown_hit"`
	StrategyLossBreached  bool     `json:"strategy_loss_breached"`
	OpenNotionalBreached  bool     `json:"open_notional_breached"`
}

// accountAutoFlattenIntent 二级自动全平判定(纯函数;仅认 equity_snapshots 真实现金回撤)。
type accountAutoFlattenIntent struct {
	ShouldFlatten bool   `json:"should_flatten"`
	Reason        string `json:"reason,omitempty"`
}

// persistedAccountRiskWatch 观测快照持久化(records/account_risk_watch.json)。
type persistedAccountRiskWatch struct {
	Wired                  bool                       `json:"wired"`
	UpdatedAt              time.Time                  `json:"updated_at"`
	Snapshot               accountRiskSnapshot        `json:"snapshot"`
	Thresholds             accountRiskThresholdResult `json:"thresholds"`
	HardHalt               accountHardHaltIntent      `json:"hard_halt"`
	AutoFlatten            accountAutoFlattenIntent   `json:"auto_flatten"`
	MaxDailyLossUSDT       float64                    `json:"max_daily_loss_usdt"`
	MaxAccountDrawdownUSDT float64                    `json:"max_account_drawdown_usdt"`
}

// accountRiskWatcher 账户级风险 watcher:Stage B 一级硬停 + 二级 opt-in 自动全平。
type accountRiskWatcher struct {
	cfg       EngineConfig
	recordDir string
	alerts    *AlertDispatcher
	logger    *log.Logger
	nowFn     func() time.Time

	crossVenue *CrossVenueExecutor
	svsp       *SingleVenueSpotPerpExecutor
	cvsp       *CrossVenueSpotPerpExecutor
	svps       *SingleVenuePerpSpotExecutor
	cvps       *CrossVenuePerpSpotExecutor

	settlementFn        func() ([]strategySettlementRecord, error)
	equityFn            func(now time.Time) accountEquityObservation
	positionFn          func() accountRiskPositionSummary
	equityState         accountRiskEquityState
	realEquityFn        func(recordDir string) (current, peak, drawdown float64)
	realEquityQualityFn func(recordDir string) realEquityQualityResult
	notifyFn            func(title, detail string)
	fundFlowLookup      accountFundFlowLookup // 单测注入;nil 时从 crossVenue 解析

	// Stage B 执行依赖(生产由 newAccountRiskWatcher 绑定;单测注入 mock)。
	haltFn              func(reason string) error
	setRotationActiveFn func(on bool)
	flattenFn           func(ctx context.Context) error
	autoFlattenDone     atomic.Bool
}

type accountRiskPositionSummary struct {
	TotalCount   int
	OpenNotional float64
	ByStrategy   map[string]int
}

// accountEquityObservation 单次权益读取结果(含原始日 delta 与 venue 现金;可信性由 buildSnapshot 结合历史判定)。
type accountEquityObservation struct {
	TotalUSDT     float64
	ByVenue       map[string]float64
	VenueDeltas   map[string]float64
	RawDailyDelta float64
	ReadOK        bool
}

// accountRiskEquityState 跨 tick 权益状态:上次可信快照、日基准平移(充提)、交易上下文。
type accountRiskEquityState struct {
	DayStartCST             time.Time
	LastTrustedTotalUSDT    float64
	LastTrustedByVenue      map[string]float64
	HasLastTrusted          bool
	LastTrustedAt           time.Time
	DailyFundFlowAdjustUSDT float64
	// CumulativeFundFlowUSDT 全时净充提(deposit+/withdraw−),跨天累计、永不日重置。
	// 用于把「真实现金权益」换算成「交易口径权益」(扣掉本金进出),使提现/充值不污染回撤。
	CumulativeFundFlowUSDT float64
	// TradingEquityPeakUSDT 交易口径权益(realEquity − CumulativeFundFlow)的高水位,跨天保留。
	TradingEquityPeakUSDT float64
	LastDailyRealizedPnL  float64
	LastSettlementCount   int
	LastPositionCount     int
	fundFlowSourceLogged  bool
}

// updateTradingEquityDrawdown 把真实现金权益换算成交易口径(扣全时净充提),维护交易峰值并返回扣充提回撤。
//
// tradingEquity = realCurrent − CumulativeFundFlow。提现/充值同步改变 realCurrent 与 CumulativeFundFlow,
// 故 tradingEquity 连续不跳:充提既不制造也不掩盖回撤;只有真实交易盈亏移动它。
// 首次以历史确认峰值 realPeak 播种(假设峰值设定前无充提);盲区重锚时峰值压回当前(与原始回撤口径一致)。
func (st *accountRiskEquityState) updateTradingEquityDrawdown(realCurrent, realPeak float64, reanchored bool) float64 {
	if st == nil {
		return 0
	}
	tradingEquity := money.SumUSDT(realCurrent, -st.CumulativeFundFlowUSDT)
	if st.TradingEquityPeakUSDT == 0 && realPeak > 0 {
		st.TradingEquityPeakUSDT = realPeak
	}
	if reanchored {
		st.TradingEquityPeakUSDT = tradingEquity
	} else if tradingEquity > st.TradingEquityPeakUSDT {
		st.TradingEquityPeakUSDT = tradingEquity
	}
	dd := money.SumUSDT(st.TradingEquityPeakUSDT, -tradingEquity)
	if dd < 0 {
		dd = 0
	}
	return dd
}

func (st *accountRiskEquityState) resetForDay(dayStart time.Time) {
	if st == nil {
		return
	}
	st.DayStartCST = dayStart
	st.DailyFundFlowAdjustUSDT = 0
	st.LastDailyRealizedPnL = 0
	st.LastSettlementCount = 0
	st.LastPositionCount = 0
}

func copyVenueEquityMap(src map[string]float64) map[string]float64 {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func effectiveAccountRiskMinFundFlowStepUSDT(cfg RiskConfig) float64 {
	if cfg.AccountRiskMinFundFlowStepUSDT > 0 {
		return cfg.AccountRiskMinFundFlowStepUSDT
	}
	return defaultAccountRiskMinFundFlowStepUSDT
}

type accountRiskWatchConfig struct {
	Cfg            EngineConfig
	RecordDir      string
	Logger         *log.Logger
	ControlPlane   *ControlPlane
	RotationRunner *UnifiedRotationRunner
	CrossVenue     *CrossVenueExecutor
	SVSP           *SingleVenueSpotPerpExecutor
	CVSP           *CrossVenueSpotPerpExecutor
	SVPS           *SingleVenuePerpSpotExecutor
	CVPS           *CrossVenuePerpSpotExecutor
}

func newAccountRiskWatcher(c accountRiskWatchConfig) *accountRiskWatcher {
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	w := &accountRiskWatcher{
		cfg:        c.Cfg,
		recordDir:  strings.TrimSpace(c.RecordDir),
		alerts:     newAlertDispatcher(c.Cfg.AlertingConfig, logger),
		logger:     logger,
		nowFn:      time.Now,
		crossVenue: c.CrossVenue,
		svsp:       c.SVSP,
		cvsp:       c.CVSP,
		svps:       c.SVPS,
		cvps:       c.CVPS,
	}
	w.bindDefaultSources()
	w.bindDefaultActions(c)
	if st, ok := loadAccountRiskEquityState(w.recordDir); ok {
		w.equityState = st
	}
	return w
}

func (w *accountRiskWatcher) bindDefaultSources() {
	if w == nil {
		return
	}
	w.settlementFn = func() ([]strategySettlementRecord, error) {
		return loadStrategySettlementRecordsFromDir(w.recordDir)
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		if w.crossVenue != nil {
			deltas, currents, _, _, _ := w.crossVenue.TodayCSTEquityDeltas(now)
			var deltaTotal float64
			for _, d := range deltas {
				deltaTotal = money.SumUSDT(deltaTotal, d)
			}
			var currentTotal float64
			for _, c := range currents {
				currentTotal = money.SumUSDT(currentTotal, c)
			}
			return accountEquityObservation{
				TotalUSDT:     currentTotal,
				ByVenue:       currents,
				VenueDeltas:   deltas,
				RawDailyDelta: deltaTotal,
				ReadOK:        len(currents) > 0,
			}
		}
		return readEquitySnapshotObservation(w.recordDir, beijingCSTDayStart(now))
	}
	w.realEquityFn = computeRealEquityWaterMark
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		_, _, _, quality := computeRealEquityWaterMarkWithQuality(recordDir)
		return quality
	}
	w.positionFn = w.collectPositionSummary
	w.notifyFn = w.emitCriticalAlert
}

func (w *accountRiskWatcher) bindDefaultActions(c accountRiskWatchConfig) {
	if w == nil {
		return
	}
	if c.ControlPlane != nil {
		cp := c.ControlPlane
		w.haltFn = func(reason string) error {
			return cp.SetGlobalHalt(true)
		}
	}
	if c.RotationRunner != nil {
		runner := c.RotationRunner
		w.setRotationActiveFn = runner.SetActive
	}
	deps := strategyFlattenDeps{
		CrossVenue: c.CrossVenue,
		SVSP:       c.SVSP,
		CVSP:       c.CVSP,
		SVPS:       c.SVPS,
		CVPS:       c.CVPS,
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenAllStrategyPositions(ctx, deps)
		return nil
	}
}

func startAccountRiskWatcher(ctx context.Context, c accountRiskWatchConfig) {
	w := newAccountRiskWatcher(c)
	if w.logger != nil {
		w.logger.Printf("[账户风控] 已启动: interval=%s (Stage B:一级硬停默认启用,二级 flatten opt-in 默认关闭)",
			accountRiskWatchInterval)
	}
	w.tickOnce()
	ticker := time.NewTicker(accountRiskWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tickOnce()
		}
	}
}

func (w *accountRiskWatcher) tickOnce() {
	if w == nil {
		return
	}
	now := w.nowFn().UTC()
	snap := w.buildSnapshot(now)
	if !snap.EquityDataTrusted {
		if w.logger != nil {
			reason := snap.EquityDataUntrustedReason
			if strings.TrimSpace(reason) == "" {
				reason = "读取失败/0值"
			}
			w.logger.Printf("[账户风控] 权益数据不可靠(%s),本轮跳过风控评估", reason)
		}
		w.persistObservation(snap, accountRiskThresholdResult{}, accountHardHaltIntent{}, accountAutoFlattenIntent{})
		return
	}

	thresholds := evaluateAccountRiskThresholds(snap, w.cfg.RiskConfig)
	haltIntent := evaluateAccountHardHaltIntent(snap, w.cfg.RiskConfig)
	flattenIntent := evaluateAccountAutoFlattenIntent(snap, w.cfg.RiskConfig)
	w.persistObservation(snap, thresholds, haltIntent, flattenIntent)

	manualRecoveryHint := "需人工确认后通过面板 START 或清除 global_halt 解除(不自动解除)"

	if haltIntent.ShouldHalt {
		detail := strings.Join(haltIntent.Reasons, "; ") + " [Stage B:一级硬停,已停新开仓,在场仓继续 manage; " + manualRecoveryHint + "]"
		if w.haltFn != nil {
			if err := w.haltFn(detail); err != nil && w.logger != nil {
				w.logger.Printf("[账户风控] SetGlobalHalt 失败: %v", err)
			}
		}
		w.notifyFn("账户级硬停(一级)", detail)
	} else if thresholds.AnyBreached {
		for _, intent := range accountRiskAlertIntents(thresholds) {
			w.notifyFn(intent.Title, intent.Detail)
		}
	}

	if flattenIntent.ShouldFlatten && w.flattenFn != nil && !w.autoFlattenDone.Load() {
		detail := flattenIntent.Reason + " [Stage B:二级自动全平; " + manualRecoveryHint + "]"
		if w.haltFn != nil {
			if err := w.haltFn(detail); err != nil && w.logger != nil {
				w.logger.Printf("[账户风控] flatten 前 SetGlobalHalt 失败: %v", err)
			}
		}
		if w.setRotationActiveFn != nil {
			w.setRotationActiveFn(false)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := w.flattenFn(ctx); err != nil {
			if w.logger != nil {
				w.logger.Printf("[账户风控] 自动全平失败: %v", err)
			}
			w.notifyFn("账户级自动全平失败(二级)", detail+" err="+err.Error())
			return
		}
		w.autoFlattenDone.Store(true)
		if w.logger != nil {
			w.logger.Printf("[critical][账户风控] 二级自动全平已触发(幂等,后续 tick 不再重复 flatten)")
		}
		w.notifyFn("账户级自动全平(二级)", detail)
		return
	}

	if !haltIntent.ShouldHalt && !thresholds.AnyBreached && !flattenIntent.ShouldFlatten && w.logger != nil {
		w.logger.Printf("[账户风控] OK daily_equity_delta=%.4f daily_realized=%.4f settlement_dd=%.4f real_equity_dd=%.4f real_equity_dd_adj=%.4f cum_fund_flow=%.4f positions=%d notional=%.2f",
			snap.DailyEquityDeltaUSDT, snap.DailyRealizedPnLUSDT, snap.AccountDrawdownUSDT, snap.RealEquityDrawdownUSDT,
			snap.RealEquityDrawdownAdjustedUSDT, w.equityState.CumulativeFundFlowUSDT,
			snap.OpenPositionCount, snap.OpenNotionalUSDT)
	}
}

func (w *accountRiskWatcher) buildSnapshot(now time.Time) accountRiskSnapshot {
	dayStart := beijingCSTDayStart(now)
	if w.equityState.DayStartCST.IsZero() || !w.equityState.DayStartCST.Equal(dayStart) {
		w.equityState.resetForDay(dayStart)
	}

	records, _ := w.settlementFn()
	daily := aggregateStrategySettlementsInRange(records, dayStart, now)
	dailyLosingTrades := countLosingSettlementsInRange(records, dayStart, now)
	allTime := aggregateStrategySettlementsInRange(records, time.Time{}, now)
	obs := w.equityFn(now)
	pos := w.positionFn()
	cumulative, peak, drawdown := computeRealizedPnLWaterMark(records)
	realCurrent, realPeak, realDrawdown := w.realEquityFn(w.recordDir)
	realQuality := w.realEquityQualityFn(w.recordDir)

	trusted, untrustedReason := assessAccountEquityTrust(
		obs,
		realCurrent,
		pos,
		&w.equityState,
		w.cfg.RiskConfig,
		w.recordDir,
		now,
	)
	blindGap := w.equityState.HasLastTrusted && equityObservationHasBlindGap(w.equityState.LastTrustedAt, now)
	connectivityBlind := isEquitySamplerConnectivityBlind(w.recordDir)
	needsReanchor := blindGap || connectivityBlind
	reanchoredThisTick := false
	if trusted && needsReanchor {
		if w.logger != nil {
			w.logger.Printf("[账户风控] 因盲区重锚,不计入回撤(last_trusted_at=%s connectivity_blind=%v)",
				w.equityState.LastTrustedAt.UTC().Format(time.RFC3339), connectivityBlind)
		}
		w.equityState.reanchorTrustedObservation(obs, pos, daily.TotalNetPnL, len(records), now)
		_ = saveAccountRiskEquityState(w.recordDir, &w.equityState, now)
		reanchoredThisTick = true
	} else if trusted {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		lookup := w.fundFlowLookup
		if lookup == nil {
			lookup = crossVenueFundFlowLookup(w.crossVenue)
		}
		_, fundFlowStep := applyAccountFundFlowAdjustment(
			ctx,
			&w.equityState,
			obs,
			lookup,
			dayStart,
			obs.TotalUSDT,
			obs.RawDailyDelta,
			daily.TotalNetPnL,
			pos,
			len(records),
			effectiveAccountRiskMinFundFlowStepUSDT(w.cfg.RiskConfig),
			w.logger,
		)
		cancel()
		// 全时净充提累计:每 tick 把识别到的充提增量累加进去,跨天不重置(本金进出口径)。
		w.equityState.CumulativeFundFlowUSDT = money.SumUSDT(w.equityState.CumulativeFundFlowUSDT, fundFlowStep)
		w.equityState.rememberTrustedObservation(obs, pos, daily.TotalNetPnL, len(records), now)
		_ = saveAccountRiskEquityState(w.recordDir, &w.equityState, now)
	}
	dailyFundFlowAdjust := w.equityState.DailyFundFlowAdjustUSDT
	rawDailyDelta := obs.RawDailyDelta
	dailyEquityForRisk := money.SumUSDT(rawDailyDelta, -dailyFundFlowAdjust)
	if reanchoredThisTick {
		dailyEquityForRisk = 0
	}

	// 交易口径回撤:tradingEquity = 真实现金权益 − 全时净充提。提现同时压低 realCurrent 与
	// CumulativeFundFlow,tradingEquity 连续不跳 → 既不制造也不掩盖回撤;只有真实交易盈亏移动它。
	// 据此重锚的交易峰值供自动全平/告警用,杜绝把提现误判成回撤触发全平。
	realEquityDrawdownAdjusted := realDrawdown
	if realQuality.Passed && realCurrent > 0 {
		realEquityDrawdownAdjusted = w.equityState.updateTradingEquityDrawdown(realCurrent, realPeak, reanchoredThisTick)
		_ = saveAccountRiskEquityState(w.recordDir, &w.equityState, now)
	}

	return accountRiskSnapshot{
		ObservedAt:                     now,
		DayStartCST:                    dayStart,
		DailyRealizedPnLUSDT:           daily.TotalNetPnL,
		DailyEquityDeltaUSDT:           rawDailyDelta,
		DailyFundFlowAdjustUSDT:        dailyFundFlowAdjust,
		DailyEquityDeltaForRiskUSDT:    dailyEquityForRisk,
		DailyLosingTradeCount:          dailyLosingTrades,
		VenueEquityDeltas:              obs.VenueDeltas,
		EquityDataTrusted:              trusted,
		EquityDataUntrustedReason:      untrustedReason,
		CumulativeRealizedPnL:          cumulative,
		RealizedPnLPeakUSDT:            peak,
		AccountDrawdownUSDT:            drawdown,
		StrategyNetPnL:                 allTime.ByStrategy,
		RealEquityTotalUSDT:            realCurrent,
		RealEquityPeakUSDT:             realPeak,
		RealEquityDrawdownUSDT:         realDrawdown,
		RealEquityDrawdownAdjustedUSDT: realEquityDrawdownAdjusted,
		RealEquityQualityPassed:        realQuality.Passed,
		RealEquityQualityFailReason:    realQuality.Reason,
		RealEquityPeakConfirmed:        realQuality.PeakConfirmed,
		OpenPositionCount:              pos.TotalCount,
		OpenNotionalUSDT:               pos.OpenNotional,
		PositionsByStrategy:            pos.ByStrategy,
	}
}

// evaluateAccountRiskThresholds 纯函数:对比 RiskConfig 阈值,产出 Stage A 告警意图(无副作用)。
func evaluateAccountRiskThresholds(snap accountRiskSnapshot, cfg RiskConfig) accountRiskThresholdResult {
	if !snap.EquityDataTrusted {
		return accountRiskThresholdResult{}
	}
	var res accountRiskThresholdResult
	lossLimit := effectiveMaxDailyLossUSDT(cfg, snap.RealEquityTotalUSDT)
	dailyLossDelta := snap.DailyEquityDeltaForRiskUSDT
	if lossLimit > 0 && dailyLossDelta <= -lossLimit {
		res.DailyLossBreached = true
		res.DailyLossReason = fmt.Sprintf(
			"当日交易口径权益变化 %.4f USDT 已超过阈值 %.4f USDT(原始权益变化 %.4f,充提平移 %.4f,已实现 %.4f,持仓 %d 名义 %.2f)",
			dailyLossDelta, lossLimit, snap.DailyEquityDeltaUSDT, snap.DailyFundFlowAdjustUSDT, snap.DailyRealizedPnLUSDT,
			snap.OpenPositionCount, snap.OpenNotionalUSDT,
		)
	}
	drawdownLimit := effectiveMaxAccountDrawdownUSDT(cfg, snap.RealEquityPeakUSDT)
	if drawdownLimit > 0 && snap.AccountDrawdownUSDT >= drawdownLimit {
		res.DrawdownBreached = true
		res.DrawdownReason = fmt.Sprintf(
			"settlements 累计净利回撤 %.4f USDT 已达到阈值 %.4f USDT(自高水位 %.4f 累计 %.4f)",
			snap.AccountDrawdownUSDT, drawdownLimit, snap.RealizedPnLPeakUSDT, snap.CumulativeRealizedPnL,
		)
	}
	res.AnyBreached = res.DailyLossBreached || res.DrawdownBreached
	return res
}

// evaluateAccountHardHaltIntent 纯函数:一级硬停判定(非破坏性;任一条件触发即 ShouldHalt)。
func evaluateAccountHardHaltIntent(snap accountRiskSnapshot, cfg RiskConfig) accountHardHaltIntent {
	var intent accountHardHaltIntent
	if !snap.EquityDataTrusted {
		return intent
	}

	lossLimit := effectiveMaxDailyLossUSDT(cfg, snap.RealEquityTotalUSDT)
	dailyLossDelta := snap.DailyEquityDeltaForRiskUSDT
	if lossLimit > 0 && dailyLossDelta <= -lossLimit {
		intent.DailyLossBreached = true
		intent.Reasons = append(intent.Reasons, fmt.Sprintf(
			"日权益回撤 %.4f ≥ MaxDailyLossUSDT %.4f", -dailyLossDelta, lossLimit))
	}

	if cfg.DailyMaxLosingTrades > 0 && snap.DailyLosingTradeCount >= cfg.DailyMaxLosingTrades {
		intent.DailyLosingTradesHit = true
		intent.Reasons = append(intent.Reasons, fmt.Sprintf(
			"当日净亏笔数 %d ≥ DailyMaxLosingTrades %d(当日错次预算用尽)", snap.DailyLosingTradeCount, cfg.DailyMaxLosingTrades))
	}

	drawdownLimit := effectiveMaxAccountDrawdownUSDT(cfg, snap.RealEquityPeakUSDT)
	if drawdownLimit > 0 && snap.AccountDrawdownUSDT >= drawdownLimit {
		intent.SettlementDrawdownHit = true
		intent.Reasons = append(intent.Reasons, fmt.Sprintf(
			"settlements 回撤 %.4f ≥ MaxAccountDrawdownUSDT %.4f", snap.AccountDrawdownUSDT, drawdownLimit))
	}

	strategyLimit := effectiveMaxStrategyCumulativeLossUSDT(cfg, snap.RealEquityTotalUSDT)
	if strategyLimit > 0 {
		for strategy, pnl := range snap.StrategyNetPnL {
			if pnl <= -strategyLimit {
				intent.StrategyLossBreached = true
				intent.Reasons = append(intent.Reasons, fmt.Sprintf(
					"策略 %s 累计净利 %.4f ≤ -MaxStrategyCumulativeLossUSDT %.4f", strategy, pnl, strategyLimit))
			}
		}
	}

	notionalLimit := effectiveMaxAccountOpenNotionalUSDT(cfg, snap.RealEquityTotalUSDT)
	if notionalLimit > 0 && snap.OpenNotionalUSDT >= notionalLimit {
		intent.OpenNotionalBreached = true
		intent.Reasons = append(intent.Reasons, fmt.Sprintf(
			"在场名义 %.4f ≥ MaxAccountOpenNotionalUSDT %.4f", snap.OpenNotionalUSDT, notionalLimit))
	}

	intent.ShouldHalt = len(intent.Reasons) > 0
	return intent
}

// evaluateAccountAutoFlattenIntent 纯函数:二级自动全平(opt-in + 高阈值 + 仅认 equity_snapshots 真实现金回撤)。
func evaluateAccountAutoFlattenIntent(snap accountRiskSnapshot, cfg RiskConfig) accountAutoFlattenIntent {
	if !snap.EquityDataTrusted {
		return accountAutoFlattenIntent{}
	}
	if !snap.RealEquityQualityPassed {
		return accountAutoFlattenIntent{}
	}
	if !snap.RealEquityPeakConfirmed || snap.RealEquityPeakUSDT <= 0 {
		return accountAutoFlattenIntent{}
	}
	if realEquityDrawdownPrimarilyFundFlow(snap, cfg) {
		return accountAutoFlattenIntent{}
	}
	if !cfg.AutoFlattenOnHardDrawdownEnabled {
		return accountAutoFlattenIntent{}
	}
	limit := effectiveAutoFlattenAccountDrawdownUSDT(cfg, snap.RealEquityPeakUSDT)
	// 用扣全时净充提后的交易口径回撤判定:提现不再触发全平,真实交易亏损仍如实触发。
	if limit <= 0 || snap.RealEquityDrawdownAdjustedUSDT < limit {
		return accountAutoFlattenIntent{}
	}
	return accountAutoFlattenIntent{
		ShouldFlatten: true,
		Reason: fmt.Sprintf(
			"交易口径权益回撤 %.4f USDT ≥ AutoFlattenAccountDrawdownUSDT %.4f (原始回撤=%.4f current=%.4f peak=%.4f 全时净充提=%.4f;不用 settlements)",
			snap.RealEquityDrawdownAdjustedUSDT, limit, snap.RealEquityDrawdownUSDT, snap.RealEquityTotalUSDT, snap.RealEquityPeakUSDT, snap.DailyFundFlowAdjustUSDT),
	}
}

type accountRiskAlertIntent struct {
	Title  string
	Detail string
}

func accountRiskAlertIntents(result accountRiskThresholdResult) []accountRiskAlertIntent {
	if !result.AnyBreached {
		return nil
	}
	var out []accountRiskAlertIntent
	if result.DailyLossBreached {
		out = append(out, accountRiskAlertIntent{
			Title:  "账户日亏阈值(观测)",
			Detail: result.DailyLossReason + " [未绑定 haltFn 时仅告警]",
		})
	}
	if result.DrawdownBreached {
		out = append(out, accountRiskAlertIntent{
			Title:  "账户回撤阈值(观测)",
			Detail: result.DrawdownReason + " [未绑定 haltFn 时仅告警]",
		})
	}
	return out
}

// computeRealEquityWaterMark 从 equity_snapshots 回放各所 USDT 现金总和,得当前/峰值/回撤(真实现金口径)。
// 仅消费 complete 且 valid 的样本;peak 需连续 N 个样本确认后才抬升。
func computeRealEquityWaterMark(recordDir string) (current, peak, drawdown float64) {
	current, peak, drawdown, _ = computeRealEquityWaterMarkWithQuality(recordDir)
	return current, peak, drawdown
}

// countLosingSettlementsInRange 统计窗口内"可信且净亏(NetPnL<0)"的已结算笔数(当日错次预算用)。
// 仅计可信记录,避免 untrusted_legacy 幻觉污染错次计数;用极小 epsilon 把 ~0 的记录排除。
func countLosingSettlementsInRange(records []strategySettlementRecord, since, until time.Time) int {
	const lossEpsilonUSDT = 1e-6
	n := 0
	for _, rec := range records {
		if rec.CloseTime.IsZero() {
			continue
		}
		if !since.IsZero() && rec.CloseTime.Before(since) {
			continue
		}
		if !until.IsZero() && rec.CloseTime.After(until) {
			continue
		}
		if !isTrustedStrategySettlementRecord(rec) {
			continue
		}
		if rec.NetPnL < -lossEpsilonUSDT {
			n++
		}
	}
	return n
}

// computeRealizedPnLWaterMark 按 close_time 回放五策略已实现净利,得累计/高水位/回撤(settlements 口径,一级 halt 用)。
func computeRealizedPnLWaterMark(records []strategySettlementRecord) (cumulative, peak, drawdown float64) {
	if len(records) == 0 {
		return 0, 0, 0
	}
	sorted := append([]strategySettlementRecord(nil), records...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CloseTime.Equal(sorted[j].CloseTime) {
			return sorted[i].SettlementID < sorted[j].SettlementID
		}
		return sorted[i].CloseTime.Before(sorted[j].CloseTime)
	})
	for _, rec := range sorted {
		if rec.CloseTime.IsZero() || !isTrustedStrategySettlementRecord(rec) {
			// 排除 untrusted_legacy(如 HEI trade_pnl 幻觉),避免误触发 settlements 回撤一级 halt。
			continue
		}
		cumulative = money.SumUSDT(cumulative, rec.NetPnL)
		if cumulative > peak {
			peak = cumulative
		}
	}
	drawdown = money.SumUSDT(peak, -cumulative)
	if drawdown < 0 {
		drawdown = 0
	}
	return cumulative, peak, drawdown
}

func readEquitySnapshotTotalDelta(recordDir string, windowStart time.Time) (total float64, byVenue map[string]float64) {
	obs := readEquitySnapshotObservation(recordDir, windowStart)
	return obs.RawDailyDelta, obs.VenueDeltas
}

func readEquitySnapshotObservation(recordDir string, windowStart time.Time) accountEquityObservation {
	obs := accountEquityObservation{
		ByVenue:     make(map[string]float64),
		VenueDeltas: make(map[string]float64),
		ReadOK:      true,
	}
	path := filepath.Join(crossVenueRecordDir(recordDir), equitySnapshotsFileName)
	f, err := os.Open(path)
	if err != nil {
		obs.ReadOK = false
		return obs
	}
	defer f.Close()

	currents := make(map[string]float64)
	baselines := make(map[string]float64)
	latestTS := make(map[string]time.Time)
	baselineTS := make(map[string]time.Time)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			continue
		}
		venueName := rec.Venue
		if prev, ok := latestTS[venueName]; !ok || !ts.Before(prev) {
			currents[venueName] = rec.CashUSDT
			latestTS[venueName] = ts
		}
		if !ts.Before(windowStart) {
			if prev, ok := baselineTS[venueName]; !ok || ts.Before(prev) {
				baselines[venueName] = rec.CashUSDT
				baselineTS[venueName] = ts
			}
		}
	}
	if len(currents) == 0 {
		obs.ReadOK = false
		return obs
	}
	for venueName, current := range currents {
		obs.ByVenue[venueName] = current
		baseline, ok := baselines[venueName]
		if !ok {
			baseline = current
		}
		delta := current - baseline
		obs.VenueDeltas[venueName] = delta
		obs.RawDailyDelta = money.SumUSDT(obs.RawDailyDelta, delta)
	}
	var currentTotal float64
	for _, current := range currents {
		currentTotal = money.SumUSDT(currentTotal, current)
	}
	obs.TotalUSDT = currentTotal
	return obs
}

func detectAccountFundFlowStep(
	st *accountRiskEquityState,
	currentTotal float64,
	rawDailyDelta float64,
	dailyRealized float64,
	pos accountRiskPositionSummary,
	settlementCount int,
	minStep float64,
) float64 {
	if st == nil || minStep <= 0 {
		return 0
	}
	if st.HasLastTrusted {
		tickChange := money.SumUSDT(currentTotal, -st.LastTrustedTotalUSDT)
		if math.Abs(tickChange) < minStep {
			return 0
		}
		if pos.TotalCount != st.LastPositionCount {
			return 0
		}
		if settlementCount > st.LastSettlementCount {
			pnlDelta := money.SumUSDT(dailyRealized, -st.LastDailyRealizedPnL)
			if math.Abs(tickChange-pnlDelta) < minStep*0.5 {
				return 0
			}
		}
		st.DailyFundFlowAdjustUSDT = money.SumUSDT(st.DailyFundFlowAdjustUSDT, tickChange)
		return tickChange
	}
	unexplained := money.SumUSDT(rawDailyDelta, -dailyRealized)
	if math.Abs(unexplained) < minStep {
		return 0
	}
	if math.Abs(dailyRealized) >= minStep*0.5 && math.Abs(unexplained) < math.Abs(dailyRealized) {
		return 0
	}
	initialThreshold := minStep
	if pos.TotalCount > 0 {
		initialThreshold = math.Max(minStep*4, 25)
	}
	if math.Abs(unexplained) < initialThreshold {
		return 0
	}
	st.DailyFundFlowAdjustUSDT = money.SumUSDT(st.DailyFundFlowAdjustUSDT, unexplained)
	return unexplained
}

func assessAccountEquityTrust(
	obs accountEquityObservation,
	realCurrent float64,
	pos accountRiskPositionSummary,
	st *accountRiskEquityState,
	cfg RiskConfig,
	recordDir string,
	now time.Time,
) (trusted bool, reason string) {
	if isEquitySamplerConnectivityBlind(recordDir) {
		return false, "连通性盲区(全部 venue 采样失败)"
	}
	if !obs.ReadOK {
		return false, "权益读取失败"
	}
	for venue, cash := range obs.ByVenue {
		if cash <= 0 {
			if prev, ok := st.LastTrustedByVenue[venue]; ok && prev > 0 {
				return false, fmt.Sprintf("venue %s 现金读成 0", venue)
			}
		}
	}
	if obs.TotalUSDT <= 0 && st.HasLastTrusted && st.LastTrustedTotalUSDT > 0 {
		return false, "总权益读成 0"
	}
	if realCurrent <= 0 && st.HasLastTrusted && st.LastTrustedTotalUSDT > 0 {
		return false, "真实现金权益读成 0"
	}
	if st.HasLastTrusted && obs.TotalUSDT > 0 && obs.TotalUSDT < st.LastTrustedTotalUSDT {
		drop := money.SumUSDT(st.LastTrustedTotalUSDT, -obs.TotalUSDT)
		limit := effectiveAccountRiskMaxEquityTickDropUSDT(cfg, st.LastTrustedTotalUSDT)
		if drop > limit && pos.TotalCount >= st.LastPositionCount {
			if equityObservationHasBlindGap(st.LastTrustedAt, now) {
				return true, ""
			}
			return false, fmt.Sprintf("单 tick 权益骤降 %.4f > 阈值 %.4f 且无持仓减少", drop, limit)
		}
	}
	if st.HasLastTrusted && realCurrent > 0 && realCurrent < st.LastTrustedTotalUSDT {
		drop := money.SumUSDT(st.LastTrustedTotalUSDT, -realCurrent)
		limit := effectiveAccountRiskMaxEquityTickDropUSDT(cfg, st.LastTrustedTotalUSDT)
		if drop > limit && pos.TotalCount >= st.LastPositionCount {
			if equityObservationHasBlindGap(st.LastTrustedAt, now) {
				return true, ""
			}
			return false, fmt.Sprintf("真实现金单 tick 骤降 %.4f > 阈值 %.4f 且无持仓减少", drop, limit)
		}
	}
	return true, ""
}

func (st *accountRiskEquityState) rememberTrustedObservation(
	obs accountEquityObservation,
	pos accountRiskPositionSummary,
	dailyRealized float64,
	settlementCount int,
	observedAt time.Time,
) {
	if st == nil {
		return
	}
	st.LastTrustedTotalUSDT = obs.TotalUSDT
	st.LastTrustedByVenue = copyVenueEquityMap(obs.ByVenue)
	st.HasLastTrusted = true
	st.LastTrustedAt = observedAt.UTC()
	st.LastDailyRealizedPnL = dailyRealized
	st.LastSettlementCount = settlementCount
	st.LastPositionCount = pos.TotalCount
}

func (st *accountRiskEquityState) reanchorTrustedObservation(
	obs accountEquityObservation,
	pos accountRiskPositionSummary,
	dailyRealized float64,
	settlementCount int,
	observedAt time.Time,
) {
	if st == nil {
		return
	}
	st.LastTrustedTotalUSDT = obs.TotalUSDT
	st.LastTrustedByVenue = copyVenueEquityMap(obs.ByVenue)
	st.HasLastTrusted = true
	st.LastTrustedAt = observedAt.UTC()
	st.LastDailyRealizedPnL = dailyRealized
	st.LastSettlementCount = settlementCount
	st.LastPositionCount = pos.TotalCount
}

func loadStrategySettlementRecordsFromDir(recordDir string) ([]strategySettlementRecord, error) {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	var out []strategySettlementRecord

	perpRecs, err := loadTailJSONLFile[CrossVenueSettlement](filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range perpRecs {
		out = append(out, crossVenueSettlementToRecord(rec))
	}

	svspRecs, err := loadTailJSONLFile[SingleVenueSpotPerpSettlement](filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svspRecs {
		out = append(out, svspSettlementToRecord(rec))
	}

	cvspRecs, err := loadTailJSONLFile[CrossVenueSpotPerpSettlement](filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvspRecs {
		out = append(out, cvspSettlementToRecord(rec))
	}

	svpsRecs, err := loadTailJSONLFile[SingleVenuePerpSpotSettlement](filepath.Join(singleVenuePerpSpotRecordDir(dir), svpsSettlementsLogName), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range svpsRecs {
		out = append(out, svpsSettlementToRecord(rec))
	}

	cvpsRecs, err := loadTailJSONLFile[CrossVenuePerpSpotSettlement](filepath.Join(crossVenuePerpSpotRecordDir(dir), crossVenuePerpSpotSettlementsFile), time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	for _, rec := range cvpsRecs {
		out = append(out, cvpsSettlementToRecord(rec))
	}

	return out, nil
}

type strategyFlattenDeps struct {
	CrossVenue *CrossVenueExecutor
	SVSP       *SingleVenueSpotPerpExecutor
	CVSP       *CrossVenueSpotPerpExecutor
	SVPS       *SingleVenuePerpSpotExecutor
	CVPS       *CrossVenuePerpSpotExecutor
}

// flattenAllStrategyPositions 五策略 CloseAllPositions(ctx,true) 等价入口(供二级 flatten 与单测注入)。
func flattenAllStrategyPositions(ctx context.Context, d strategyFlattenDeps) int {
	return flattenStrategyPositionsReport(ctx, d, true).TotalClosed
}

// flattenStrategyPositionsReport 对五策略各调用 CloseAllPositions 并汇总结果(供 flatten CLI / Dashboard / 账户风控复用)。
func flattenStrategyPositionsReport(ctx context.Context, d strategyFlattenDeps, live bool) StrategyFlattenAllResponse {
	var results []StrategyFlattenResult
	totalClosed := 0

	if d.CrossVenue != nil {
		closed, errs := d.CrossVenue.CloseAllPositions(ctx, live)
		r := StrategyFlattenResult{Strategy: "cross_venue_perp_perp", Closed: closed}
		for _, e := range errs {
			r.Errors = append(r.Errors, e.Error())
		}
		if closed == 0 && len(errs) == 0 {
			r.Message = "无在场持仓"
		}
		results = append(results, r)
		totalClosed += closed
	} else {
		results = append(results, StrategyFlattenResult{Strategy: "cross_venue_perp_perp", Skipped: true, Message: "未启用"})
	}

	if d.SVSP != nil {
		closed, errs := d.SVSP.CloseAllPositions(ctx, live)
		r := StrategyFlattenResult{Strategy: "single_venue_spot_perp", Closed: closed}
		for _, e := range errs {
			r.Errors = append(r.Errors, e.Error())
		}
		if closed == 0 && len(errs) == 0 {
			r.Message = "无在场持仓"
		}
		results = append(results, r)
		totalClosed += closed
	} else {
		results = append(results, StrategyFlattenResult{Strategy: "single_venue_spot_perp", Skipped: true, Message: "未启用"})
	}

	if d.CVSP != nil {
		closed, errs := d.CVSP.CloseAllPositions(ctx, live)
		r := StrategyFlattenResult{Strategy: "cross_venue_spot_perp", Closed: closed}
		for _, e := range errs {
			r.Errors = append(r.Errors, e.Error())
		}
		if closed == 0 && len(errs) == 0 {
			r.Message = "无在场持仓"
		}
		results = append(results, r)
		totalClosed += closed
	} else {
		results = append(results, StrategyFlattenResult{Strategy: "cross_venue_spot_perp", Skipped: true, Message: "未启用"})
	}

	if d.SVPS != nil {
		closed, errs := d.SVPS.CloseAllPositions(ctx, live)
		r := StrategyFlattenResult{Strategy: "single_venue_perp_spot", Closed: closed}
		for _, e := range errs {
			r.Errors = append(r.Errors, e.Error())
		}
		if closed == 0 && len(errs) == 0 {
			r.Message = "无在场持仓"
		}
		results = append(results, r)
		totalClosed += closed
	} else {
		results = append(results, StrategyFlattenResult{Strategy: "single_venue_perp_spot", Skipped: true, Message: "未启用"})
	}

	if d.CVPS != nil {
		closed, errs := d.CVPS.CloseAllPositions(ctx, live)
		r := StrategyFlattenResult{Strategy: "cross_venue_perp_spot", Closed: closed}
		for _, e := range errs {
			r.Errors = append(r.Errors, e.Error())
		}
		if closed == 0 && len(errs) == 0 {
			r.Message = "无在场持仓"
		}
		results = append(results, r)
		totalClosed += closed
	} else {
		results = append(results, StrategyFlattenResult{Strategy: "cross_venue_perp_spot", Skipped: true, Message: "未启用"})
	}

	return StrategyFlattenAllResponse{Results: results, TotalClosed: totalClosed}
}

func (w *accountRiskWatcher) collectPositionSummary() accountRiskPositionSummary {
	var summary accountRiskPositionSummary
	summary.ByStrategy = make(map[string]int)
	if w == nil {
		return summary
	}
	if w.crossVenue != nil {
		snap := w.crossVenue.PositionsSnapshot()
		summary.ByStrategy["cross_venue_perp_perp"] = snap.ActivePositions
		summary.TotalCount += snap.ActivePositions
		for _, p := range activeCrossVenuePositions(snap.Positions) {
			summary.OpenNotional = money.SumUSDT(summary.OpenNotional, p.NotionalUSDT)
		}
	}
	if w.svsp != nil {
		active := w.svsp.ListActivePositions()
		summary.ByStrategy["single_venue_spot_perp"] = len(active)
		summary.TotalCount += len(active)
		for _, p := range active {
			summary.OpenNotional = money.SumUSDT(summary.OpenNotional, p.NotionalUSDT)
		}
	}
	if w.cvsp != nil {
		active := w.cvsp.ListActivePositions()
		summary.ByStrategy["cross_venue_spot_perp"] = len(active)
		summary.TotalCount += len(active)
		for _, p := range active {
			summary.OpenNotional = money.SumUSDT(summary.OpenNotional, p.NotionalUSDT)
		}
	}
	if w.svps != nil {
		active := w.svps.ListActivePositions()
		summary.ByStrategy["single_venue_perp_spot"] = len(active)
		summary.TotalCount += len(active)
		for _, p := range active {
			summary.OpenNotional = money.SumUSDT(summary.OpenNotional, p.NotionalUSDT)
		}
	}
	if w.cvps != nil {
		active := w.cvps.ListActivePositions()
		summary.ByStrategy["cross_venue_perp_spot"] = len(active)
		summary.TotalCount += len(active)
		for _, p := range active {
			summary.OpenNotional = money.SumUSDT(summary.OpenNotional, p.NotionalUSDT)
		}
	}
	return summary
}

func (w *accountRiskWatcher) emitCriticalAlert(title, detail string) {
	if w.logger != nil {
		w.logger.Printf("[critical][账户风控] %s: %s", title, detail)
	}
	if w.alerts != nil {
		w.alerts.Notify(Alert{
			Level:      EventLevelCritical,
			Title:      title,
			Detail:     detail,
			OccurredAt: w.nowFn().UTC(),
		})
	}
}

func (w *accountRiskWatcher) persistObservation(snap accountRiskSnapshot, thresholds accountRiskThresholdResult, halt accountHardHaltIntent, flatten accountAutoFlattenIntent) {
	dir := strings.TrimSpace(w.recordDir)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, accountRiskWatchFileName)
	payload := persistedAccountRiskWatch{
		Wired:                  true,
		UpdatedAt:              w.nowFn().UTC(),
		Snapshot:               snap,
		Thresholds:             thresholds,
		HardHalt:               halt,
		AutoFlatten:            flatten,
		MaxDailyLossUSDT:       w.cfg.RiskConfig.MaxDailyLossUSDT,
		MaxAccountDrawdownUSDT: w.cfg.RiskConfig.MaxAccountDrawdownUSDT,
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if w.logger != nil {
			w.logger.Printf("[账户风控] 创建目录失败: %v", err)
		}
		return
	}
	if err := writeJSONFile(path, payload); err != nil && w.logger != nil {
		w.logger.Printf("[账户风控] 写入观测快照失败: %v", err)
	}
}
