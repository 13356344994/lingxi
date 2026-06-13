package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// venue_funding_runtime.go OKX/Gate 单所正费率执行器生命周期(B5 对齐 Bybit,选币来自扫描器快照)。
//
// TODO: 负费率借币路径(多永续+借币做空现货)待借币探测数据成熟后按所接入;本期仅正 carry。

const venueFundingExecTickInterval = 30 * time.Second

var venueFundingVenueFactory = buildVenueFundingAdapter

func buildVenueFundingAdapter(venueName, secretPath string) (venue.Venue, error) {
	return venueAdapterFactory(venueName, secretPath)
}

func (c *RuntimeController) isFundingExecutorVenue() bool {
	return isFundingExecutorVenue(c.venueName)
}

// SetCrossVenueScanner 注入跨所扫描器(选币读 FundingByVenue,绝不现场拉全量快照)。
func (c *RuntimeController) SetCrossVenueScanner(scanner *CrossVenueScanner) {
	c.crossVenueScanner = scanner
}

// runVenueFundingExecutor OKX/Gate 单所正费率主循环。
func (c *RuntimeController) runVenueFundingExecutor(ctx context.Context, secretPath string, cfg EngineConfig) (string, error) {
	vname := strings.ToLower(strings.TrimSpace(c.venueName))
	c.appendControllerLog("开始启动 %s 单所 funding 执行器(模式=%s)", vname, venueFundingModeLabel(cfg))

	lease, err := acquireSingleWriterLease(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return "", err
	}
	defer lease.Release()

	adapter, err := venueFundingVenueFactory(vname, secretPath)
	if err != nil {
		return "", err
	}
	store, err := newVenueFundingPositionStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return "", err
	}

	if ov, ok := loadVenueCapitalOverride(cfg.NetworkConfig.RecordDir); ok {
		cfg.AllocatedCapitalUSDT = ov
	}
	execCfg := venueFundingExecConfigFromEngine(cfg)
	logf := func(format string, args ...any) { c.appendControllerLog(format, args...) }
	planner := NewVenueFundingPlanner(vname, adapter, c.crossVenueScanner, execCfg, logf)
	slipBps := cfg.FundingExecutor.SlippageBudgetBps
	if slipBps <= 0 {
		slipBps = 20
	}
	liveExec := NewVenueFundingLiveExecutor(adapter, vname, slipBps, logf)
	live := cfg.FundingExecutor.LiveTradingEnabled
	haltPaths := emergencyStopPaths(cfg)

	c.venueFundingReconcileOnStart(ctx, adapter, store)

	readyMsg := fmt.Sprintf("模式=%s TopN=%d 最小费率=%.4f%% 提前=%ds 并发=%d 单标上限=%.0fU",
		venueFundingModeLabel(cfg), execCfg.TopN, execCfg.MinFundingRate*100,
		cfg.FundingExecutor.LeadSeconds, c.venueFundingMaxConcurrent(cfg), cfg.FundingExecutor.MaxPositionUSDT)
	c.appendControllerLog("%s funding 执行器已就绪: %s", vname, readyMsg)

	ticker := time.NewTicker(venueFundingExecTickInterval)
	defer ticker.Stop()
	for {
		haltActive := ensureEmergencyStopsClear(haltPaths) != nil
		c.writeVenueFundingHeartbeat(cfg)
		c.venueFundingManageCycle(ctx, cfg, store, planner, liveExec, adapter, live, haltActive)
		select {
		case <-ctx.Done():
			c.appendControllerLog("%s funding 执行器已停止(在场持仓由下次 Start reconcile 接管)", vname)
			return fmt.Sprintf("%s funding 执行器已停止", vname), ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *RuntimeController) venueFundingMaxConcurrent(cfg EngineConfig) int {
	n := cfg.FundingExecutor.MaxConcurrentPositions
	if n <= 0 {
		n = 1
	}
	return n
}

func (c *RuntimeController) writeVenueFundingHeartbeat(cfg EngineConfig) {
	dir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if dir == "" {
		return
	}
	now := time.Now().UTC()
	payload := heartbeatPayload{
		TS:    now.Unix(),
		Time:  now.Format(time.RFC3339),
		PID:   os.Getpid(),
		RunID: c.currentRunID(),
		Stage: "venue_funding_manage",
	}
	if err := writeJSONFile(filepath.Join(dir, heartbeatFileName), payload); err != nil {
		c.appendControllerLog("写 funding 心跳失败: %v", err)
	}
}

func (c *RuntimeController) venueFundingReconcileOnStart(ctx context.Context, adapter venue.Venue, store *venueFundingPositionStore) {
	open, err := store.load()
	if err != nil {
		c.appendControllerLog("funding reconcile 读台账失败: %v", err)
		return
	}
	positions, err := adapter.FetchPositions(ctx)
	if err != nil {
		c.appendControllerLog("funding reconcile 拉持仓失败,保留台账: %v", err)
		return
	}
	actualBySym := map[venue.Symbol]float64{}
	for _, p := range positions {
		amt := p.PositionAmt
		if amt < 0 {
			amt = -amt
		}
		actualBySym[p.Symbol] += amt
	}
	bals, _ := adapter.FetchBalances(ctx)
	for _, p := range open {
		if actualBySym[p.Symbol] <= executionQtyTolerance {
			baseBal := bybitSpotFreeBalance(bals, p.Symbol)
			if baseBal > executionQtyTolerance {
				c.appendControllerLog("funding reconcile:%s 永续已平但现货有余额≈%.6f,保留待平", p.Symbol, baseBal)
				continue
			}
			c.appendControllerLog("funding reconcile:%s 交易所无仓,清除台账", p.Symbol)
			_ = store.remove(p.Symbol)
			continue
		}
		c.appendControllerLog("funding reconcile:%s 恢复监控(永续≈%.6f)", p.Symbol, actualBySym[p.Symbol])
	}
}

func (c *RuntimeController) venueFundingManageCycle(
	ctx context.Context,
	cfg EngineConfig,
	store *venueFundingPositionStore,
	planner *VenueFundingPlanner,
	liveExec *VenueFundingLiveExecutor,
	adapter venue.Venue,
	live bool,
	haltActive bool,
) {
	vname := strings.ToLower(strings.TrimSpace(c.venueName))
	now := time.Now().UTC()
	holdAfter := time.Duration(cfg.FundingExecutor.HoldAfterSeconds) * time.Second
	lead := time.Duration(cfg.FundingExecutor.LeadSeconds) * time.Second
	maxConcurrent := c.venueFundingMaxConcurrent(cfg)
	rollEnabled := cfg.FundingExecutor.RollEnabled
	maxHold := cfg.FundingExecutor.MaxHoldSettlements
	if maxHold <= 0 {
		maxHold = 1
	}

	open, err := store.load()
	if err != nil {
		c.appendControllerLog("funding 读台账失败: %v", err)
		return
	}

	// 持仓期间续期 dead-man(okx/gate 原生支持)。
	if live && len(open) > 0 {
		c.venueFundingArmDeadMan(ctx, adapter, cfg)
	}

	// 1) 在场持仓管理(急停时照常管理,不停循环)。
	for _, p := range open {
		if p.NextFundingTime.IsZero() {
			continue
		}
		if rollEnabled && live && now.Before(p.NextFundingTime) && now.After(p.NextFundingTime.Add(-bybitPreSettleCheckLead)) {
			snap, ok := planner.FundingSnapshotFor(p.Symbol)
			if ok {
				if snap.LastFundingRate <= 0 {
					c.appendControllerLog("funding 结算前离场 %s:下期费率=%.4f%% 不利", p.Symbol, snap.LastFundingRate*100)
					c.venueFundingClosePosition(ctx, store, liveExec, p, p.SettlementsHeld, live, vname)
					continue
				}
			}
			continue
		}
		if !now.After(p.NextFundingTime.Add(holdAfter)) {
			continue
		}
		if rollEnabled && !p.ForceClose && p.SettlementsHeld+1 < maxHold && (!live || liveExec.PerpOpenQty(ctx, p.Symbol) > executionQtyTolerance) {
			snap, ok := planner.FundingSnapshotFor(p.Symbol)
			if ok && snap.NextFundingTime.After(now) {
				if !live {
					c.appendControllerLog("[影子][%s][拟续持] %s 下期费率=%.4f%%", vname, p.Symbol, snap.LastFundingRate*100)
					continue
				}
				rolled := p
				rolled.NextFundingTime = snap.NextFundingTime
				rolled.FundingRate = snap.LastFundingRate
				rolled.SettlementsHeld++
				_ = store.upsert(rolled)
				c.appendControllerLog("funding 续持记账 %s:已收 %d 次,翻进 %s", p.Symbol, rolled.SettlementsHeld, snap.NextFundingTime.Format("15:04:05Z"))
				continue
			}
		}
		if !live {
			c.appendControllerLog("[影子][%s][拟平仓] %s", vname, p.Symbol)
			continue
		}
		settle := p.SettlementsHeld + 1
		if p.ForceClose {
			settle = p.SettlementsHeld
		}
		c.venueFundingClosePosition(ctx, store, liveExec, p, settle, live, vname)
	}

	open, _ = store.load()
	held := map[venue.Symbol]bool{}
	for _, p := range open {
		held[p.Symbol] = true
	}
	if len(open) >= maxConcurrent {
		c.setVenueFundingStatus(open, fmt.Sprintf("在场 %d/%d", len(open), maxConcurrent), live)
		return
	}

	// 2) 开仓(急停时跳过)。
	if haltActive {
		c.appendControllerLog("funding 急停生效,跳过开仓(持仓管理照常)")
		c.setVenueFundingStatus(open, "急停:停开仓不停管理", live)
		return
	}

	availUSDT := mathInfPositive(adapter, ctx)
	perBudget := venueFundingPerCandidateBudget(availUSDT, cfg.FundingExecutor.MaxPositionUSDT)
	if perBudget <= 0 && availUSDT < math.Inf(1) {
		c.appendControllerLog("funding 可用 USDT %.2f 不足最小下单额,跳过开仓", availUSDT)
		c.setVenueFundingStatus(open, "资金不足", live)
		return
	}

	plan, err := planner.PlanCycle(ctx, maxConcurrent-len(open), perBudget)
	if err != nil {
		c.appendControllerLog("funding 规划失败: %v", err)
		return
	}

	baseRecordDir := c.cfg.NetworkConfig.RecordDir
	for _, p := range plan.Accepted {
		if len(held) >= maxConcurrent {
			break
		}
		if held[p.Symbol] {
			continue
		}
		if isCrossVenueSymbolHeld(baseRecordDir, p.Symbol) {
			c.appendControllerLog("funding 跳过 %s:跨所引擎已持有", p.Symbol)
			continue
		}
		if p.NextFundingTime.IsZero() || now.Before(p.NextFundingTime.Add(-lead)) || !now.Before(p.NextFundingTime) {
			continue
		}
		if !live {
			c.appendControllerLog("[影子][%s] 本应开仓 %s 费率=%.4f%% 数量=%.6f 净利率=%.4f%% 结算=%s",
				vname, p.Symbol, p.FundingRate*100, p.BaseQty, p.EstNetProfitRate*100, p.NextFundingTime.Format(time.RFC3339))
			continue
		}
		idem := venueFundingIdemPrefix(p.Symbol, p.NextFundingTime)
		pos, openErr := liveExec.OpenDeltaNeutral(ctx, p, idem, c.currentRunID())
		if openErr != nil {
			if pos.BaseQty > executionQtyTolerance {
				pos.ForceClose = true
				pos.NextFundingTime = now.Add(-time.Hour)
				_ = store.upsert(pos)
			}
			c.appendControllerLog("funding 开仓失败 %s: %v", p.Symbol, openErr)
			continue
		}
		_ = store.upsert(pos)
		c.appendControllerLog("funding 已开仓 %s %.6f 费率=%.4f%%", pos.Symbol, pos.BaseQty, p.FundingRate*100)
		held[pos.Symbol] = true
		open = append(open, pos)
	}
	c.setVenueFundingStatus(open, fmt.Sprintf("在场 %d/%d", len(open), maxConcurrent), live)
}

func (c *RuntimeController) venueFundingClosePosition(
	ctx context.Context,
	store *venueFundingPositionStore,
	liveExec *VenueFundingLiveExecutor,
	p VenueFundingPosition,
	settlements int,
	live bool,
	vname string,
) {
	if !live {
		return
	}
	if err := liveExec.CloseDeltaNeutral(ctx, p); err != nil {
		c.appendControllerLog("funding 平仓失败 %s: %v", p.Symbol, err)
		return
	}
	_ = store.remove(p.Symbol)
	c.appendControllerLog("funding 已平仓 %s(持有 %d 次结算)", p.Symbol, settlements)
}

func (c *RuntimeController) venueFundingArmDeadMan(ctx context.Context, adapter venue.Venue, cfg EngineConfig) {
	caps := adapter.Capabilities()
	if !caps.HasNativeDeadMan {
		return
	}
	cd := 60 * time.Second
	if err := adapter.ArmDeadMansSwitch(ctx, cd); err != nil {
		c.appendControllerLog("funding dead-man 续期失败: %v", err)
	}
}

func (c *RuntimeController) setVenueFundingStatus(open []VenueFundingPosition, message string, live bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.status.Running {
		return
	}
	syms := make([]string, 0, len(open))
	for _, p := range open {
		syms = append(syms, string(p.Symbol))
	}
	c.status.CurrentPositions = syms
	c.status.CurrentTargets = len(syms)
	modeNote := "影子"
	if live {
		modeNote = "实盘"
	}
	if strings.TrimSpace(message) != "" {
		c.status.LastMessage = fmt.Sprintf("[%s/%s] %s", strings.ToLower(c.venueName), modeNote, message)
	}
}

func mathInfPositive(adapter venue.Venue, ctx context.Context) float64 {
	bals, err := adapter.FetchBalances(ctx)
	if err != nil {
		return math.Inf(1)
	}
	if usdt, ok := bals.ByAsset["USDT"]; ok {
		return usdt.Free
	}
	return 0
}
