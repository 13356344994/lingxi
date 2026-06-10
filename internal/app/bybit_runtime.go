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
	"lingxi/internal/venue/bybit"
)

// bybit_runtime.go 是 Bybit 执行器的 **生命周期编排(B5)**:把 shadow planner(B2)+ live executor(B3)+ 持仓
// 落盘/reconcile 接成一个受控的后台循环,挂到 per-venue RuntimeController 上。
//
// 安全模型(逐条对齐币安实盘):
//   - **单写租约**:records/bybit/.lingxi-live.lock,防同账户双开。
//   - **双级急停**:每 tick 检查全局 + 本所 .halt-live,任一存在即停。
//   - **主安全闸**:cfg.BybitExecutor.LiveTradingEnabled=false(默认)→ 只 shadow(规划+日志,绝不下单);
//       置 true 才真实下单。即便启动任务,默认也只是「影子运行」,零下单风险。
//   - **崩溃恢复**:启动先 reconcile 落盘持仓 vs 交易所真实仓位,绝不留裸腿、绝不误平。
//   - **窗口化开仓**:仅在结算前 LeadSeconds 窗口内开,结算后 HoldAfterSeconds 平,跨结算收一次资金费即走。

const bybitExecTickInterval = 30 * time.Second // 扫描/持仓管理 tick 间隔

// bybitFullVenue 是 live 执行器 + shadow planner 共同需要的读写能力(*bybit.Adapter 天然满足)。
type bybitFullVenue interface {
	bybitMarketReader
	bybitTrader
}

// bybitVenueFactory 可注入(测试替换 fake,杜绝触网/真实下单)。
var bybitVenueFactory = buildBybitVenue

func buildBybitVenue(secretPath string) (bybitFullVenue, error) {
	sec, err := LoadVenueSecretConfig(secretPath, bybit.VenueName)
	if err != nil {
		return nil, err
	}
	adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
	if err != nil {
		return nil, err
	}
	return adp, nil
}

func isBybitVenueName(name string) bool {
	return strings.ToLower(strings.TrimSpace(name)) == bybit.VenueName
}

func (c *RuntimeController) isBybitVenue() bool { return isBybitVenueName(c.venueName) }

// bybitExecConfigFromEngine 把引擎配置映射成精简执行器参数(分配资金 + 静态费率兜底,实拉费率在 PlanCycle 覆盖)。
func bybitExecConfigFromEngine(cfg EngineConfig) BybitExecConfig {
	bx := cfg.BybitExecutor
	topN := bx.TopN
	if topN <= 0 {
		topN = 1
	}
	return BybitExecConfig{
		AllocatedUSDT:       cfg.AllocatedCapitalUSDT,
		TopN:                topN,
		MinFundingRate:      bx.MinFundingRate,
		MinNetProfitRate:    bx.MinNetProfitRate,
		SpotTakerFee:        cfg.HistoricalConfig.SpotTakerFeeRate,
		PerpTakerFee:        cfg.HistoricalConfig.FuturesTakerFeeRate,
		EnableNegativeCarry: bx.EnableNegativeCarry,
	}
}

// bybitFundingStillFavorable 判定续持复核时下期费率是否仍对该方向有利(还能净收资金费)。
// 正 carry:费率仍 > 0;负 carry:费率仍 < 0。不利(含归零)即应平仓,避免继续持有倒贴。
func bybitFundingStillFavorable(rate float64, negative bool) bool {
	if negative {
		return rate < 0
	}
	return rate > 0
}

// bybitCarryLabel 返回方向中文标签(日志/通知用)。
func bybitCarryLabel(negative bool) string {
	if negative {
		return "负 carry"
	}
	return "正 carry"
}

func bybitModeLabel(cfg EngineConfig) string {
	if cfg.BybitExecutor.LiveTradingEnabled {
		return "实盘下单"
	}
	return "影子(shadow,不下单)"
}

// bybitIdemPrefix 由 (symbol, 结算时刻) 派生确定性幂等前缀,**不含 runID** → 跨重启稳定,重试不会重复成交。
func bybitIdemPrefix(sym venue.Symbol, funding time.Time) string {
	base := strings.ReplaceAll(strings.ReplaceAll(string(sym), "-", ""), "_", "")
	return fmt.Sprintf("lx-%s-%d", base, funding.Unix())
}

// runBybitExecutor 是 Bybit 执行器主循环(由 runTask 在 live 模式 + bybit venue 时调用)。
func (c *RuntimeController) runBybitExecutor(ctx context.Context, secretPath string, cfg EngineConfig) (string, error) {
	c.appendControllerLog("开始启动 Bybit 执行器(模式=%s)", bybitModeLabel(cfg))

	lease, err := acquireSingleWriterLease(cfg.NetworkConfig.RecordDir)
	if err != nil {
		c.appendControllerLog("Bybit 单写租约获取失败: %v", err)
		return "", err
	}
	defer func() {
		if rerr := lease.Release(); rerr != nil {
			c.appendControllerLog("释放 Bybit 单写租约失败: %v", rerr)
		}
	}()
	c.appendControllerLog("已获取 Bybit 单写租约,确保单实例独占写入")

	adapter, err := bybitVenueFactory(secretPath)
	if err != nil {
		c.appendControllerLog("构造 Bybit 适配器失败: %v", err)
		return "", err
	}
	store, err := newBybitPositionStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		c.appendControllerLog("初始化 Bybit 持仓台账失败: %v", err)
		return "", err
	}

	execCfg := bybitExecConfigFromEngine(cfg)
	logf := func(format string, args ...any) { c.appendControllerLog(format, args...) }
	planner := NewBybitShadowExecutor(adapter, execCfg, logf)
	executor := NewBybitLiveExecutor(adapter, logf, cfg.NetworkConfig.RecordDir)
	live := cfg.BybitExecutor.LiveTradingEnabled
	haltPaths := emergencyStopPaths(cfg)

	// 实时告警:用与币安同源的 secret.yaml Telegram 凭据构造分发器,让 Bybit 的开/平/启停/失败也推送到电报。
	// 未配置 Telegram 时分发器为 nil(所有 bybitNotify* 调用 nil-safe,零外部调用)。
	if token, chatID := loadAlertingSecretsOrEmpty(secretPath); token != "" && chatID != "" {
		ac := cfg.AlertingConfig
		ac.Enabled = true
		ac.TelegramBotToken = token
		ac.TelegramChatID = chatID
		if strings.TrimSpace(ac.MinSeverity) == "" {
			ac.MinSeverity = "warning"
		}
		c.bybitAlerts = newAlertDispatcher(ac, c.logger)
	}

	// 修 PROJECT_AUDIT C-5(preflight 校时):同步 Bybit 服务器时间偏移到签名时钟。此前签名用裸本地时钟,
	// VPS 漂移超 recvWindow(5s)→ 全部签名请求被拒且无自愈。失败不阻塞启动(网络瞬断时偏移保持 0,下轮重校)。
	lastTimeSync := time.Time{}
	syncBybitClock := func() {
		type timeSyncer interface {
			SyncServerTime(context.Context) (time.Duration, error)
		}
		ts, ok := adapter.(timeSyncer)
		if !ok {
			return
		}
		offset, serr := ts.SyncServerTime(ctx)
		if serr != nil {
			c.appendControllerLog("Bybit 服务器时间同步失败(签名沿用当前偏移,下轮重试): %v", serr)
			return
		}
		lastTimeSync = time.Now()
		if offset > 2*time.Second || offset < -2*time.Second {
			c.appendControllerLog("Bybit 时钟偏移较大: 服务器−本地=%s(已补偿进签名时间戳,建议检查 VPS NTP)", offset)
			c.bybitNotifyWarn("Bybit 时钟偏移", fmt.Sprintf("服务器−本地=%s,已自动补偿,建议检查 NTP", offset), "")
		} else {
			c.appendControllerLog("Bybit 服务器时间已同步: 偏移=%s", offset)
		}
	}
	syncBybitClock()

	c.bybitReconcileOnStart(ctx, adapter, store)

	readyMsg := fmt.Sprintf("模式=%s 分配资金=%.2f TopN=%d 最小费率=%.4f%% 提前=%ds 持有=%ds 并发上限=%d",
		bybitModeLabel(cfg), execCfg.AllocatedUSDT, execCfg.TopN, execCfg.MinFundingRate*100,
		cfg.BybitExecutor.LeadSeconds, cfg.BybitExecutor.HoldAfterSeconds, c.bybitMaxConcurrent(cfg))
	c.appendControllerLog("Bybit 执行器已就绪:%s", readyMsg)
	c.bybitNotifyTrade("Bybit 执行器已启动", readyMsg, "")

	ticker := time.NewTicker(bybitExecTickInterval)
	defer ticker.Stop()
	for {
		if haltErr := ensureEmergencyStopsClear(haltPaths); haltErr != nil {
			c.appendControllerLog("检测到急停,Bybit 执行器停止: %v", haltErr)
			c.bybitNotifyWarn("Bybit 执行器急停", haltErr.Error(), "")
			return "Bybit 执行器因急停停止", haltErr
		}
		// 修 PROJECT_AUDIT C-4:Bybit 执行器不经过币安引擎,此前从不写心跳 → 外部 watchdog
		// 双信号判活(心跳+/healthz)会把健康的 Bybit 单所进程当卡死连续误杀。每 tick 写本所心跳。
		c.writeBybitHeartbeat(cfg)
		if time.Since(lastTimeSync) > time.Hour {
			syncBybitClock()
		}
		c.bybitManageCycle(ctx, cfg, store, planner, executor, adapter, live)
		select {
		case <-ctx.Done():
			c.appendControllerLog("Bybit 执行器已停止")
			c.bybitNotifyTrade("Bybit 执行器已停止", "任务已手动停止", "")
			return "Bybit 执行器已停止", ctx.Err()
		case <-ticker.C:
		}
	}
}

// bybitPersistPosition 落盘持仓;失败时收进内存兜底队列(C-10),manage 每 tick 重试并保证失管为零。
// 返回是否真正写进磁盘。
func (c *RuntimeController) bybitPersistPosition(store *bybitPositionStore, pos BybitLivePosition, what string) bool {
	if err := store.upsert(pos); err != nil {
		if c.bybitPendingPersist == nil {
			c.bybitPendingPersist = make(map[venue.Symbol]BybitLivePosition)
		}
		c.bybitPendingPersist[pos.Symbol] = pos
		c.appendControllerLog("Bybit %s落盘失败 %s(已收进内存兜底,每轮重试落盘且照常管理平仓): %v", what, pos.Symbol, err)
		c.bybitNotifyWarn("Bybit 台账落盘失败", fmt.Sprintf("%s %s 落盘失败,已转内存兜底管理,请尽快检查磁盘: %v", what, pos.Symbol, err), string(pos.Symbol))
		return false
	}
	delete(c.bybitPendingPersist, pos.Symbol)
	return true
}

// bybitFlushPendingPersist 重试内存兜底队列落盘,并把仍未落盘的持仓并入管理视图(按 symbol 去重)。
func (c *RuntimeController) bybitFlushPendingPersist(store *bybitPositionStore, open []BybitLivePosition) []BybitLivePosition {
	if len(c.bybitPendingPersist) == 0 {
		return open
	}
	onDisk := make(map[venue.Symbol]bool, len(open))
	for _, p := range open {
		onDisk[p.Symbol] = true
	}
	for sym, pos := range c.bybitPendingPersist {
		if err := store.upsert(pos); err == nil {
			delete(c.bybitPendingPersist, sym)
			c.appendControllerLog("Bybit 内存兜底持仓 %s 已补落盘", sym)
			if !onDisk[sym] {
				open = append(open, pos)
				onDisk[sym] = true
			}
			continue
		}
		if !onDisk[sym] {
			open = append(open, pos)
			onDisk[sym] = true
		}
	}
	return open
}

// writeBybitHeartbeat 把 Bybit 执行循环的活性写进本所 records 目录的 heartbeat.json
// (tmp+fsync+rename 原子写)。best-effort:失败仅记日志。
func (c *RuntimeController) writeBybitHeartbeat(cfg EngineConfig) {
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
		Stage: "bybit_manage",
	}
	if err := writeJSONFile(filepath.Join(dir, heartbeatFileName), payload); err != nil {
		c.appendControllerLog("写 Bybit 心跳失败: %v", err)
	}
}

func (c *RuntimeController) bybitMaxConcurrent(cfg EngineConfig) int {
	n := cfg.BybitExecutor.MaxConcurrentPositions
	if n <= 0 {
		n = 1
	}
	return n
}

// bybitReconcileOnStart 启动时核对落盘持仓 vs 交易所真实永续仓:交易所已无仓 → 清记录(避免误平);
// 仍有仓 → 保留,交由 manage 循环按结算时间平仓。拉仓失败则保守保留落盘,待下轮。
func (c *RuntimeController) bybitReconcileOnStart(ctx context.Context, adapter bybitFullVenue, store *bybitPositionStore) {
	open, err := store.load()
	if err != nil {
		c.appendControllerLog("Bybit reconcile 读取落盘持仓失败: %v", err)
		return
	}
	positions, err := adapter.FetchPositions(ctx)
	if err != nil {
		c.appendControllerLog("Bybit reconcile 拉取真实持仓失败,保留落盘待下轮: %v", err)
		return
	}
	actualBySym := make(map[venue.Symbol]float64, len(positions))
	for _, p := range positions {
		amt := p.PositionAmt
		if amt < 0 {
			amt = -amt
		}
		actualBySym[p.Symbol] += amt
	}
	bals, _ := adapter.FetchBalances(ctx)
	tracked := make(map[venue.Symbol]bool, len(open))
	if len(open) > 0 {
		c.appendControllerLog("Bybit reconcile:发现 %d 笔落盘持仓,核对交易所真实仓位", len(open))
	}
	for _, p := range open {
		tracked[p.Symbol] = true
		if actualBySym[p.Symbol] <= executionQtyTolerance {
			// 永续已平。现货腿(base 资产)是否仍需收敛,按方向判定:
			baseBal := bybitSpotFreeBalance(bals, p.Symbol)
			if p.Negative {
				// 修 FIX_PLAN #4:负 carry 现货腿是「借币空头」,持有的 free base≈0,但可能仍欠借币本金(UTA walletBalance 记为负)。
				// 绝不能只看 free>0 就判定「无现货腿」而删台账——否则借币债务无人管 + 继续计息。base 余额为负(欠款未还清)→ 保留,
				// 交由 manage 用 CloseDeltaNeutral 买回还币。
				if baseBal < -executionQtyTolerance {
					c.appendControllerLog("Bybit reconcile:%s 永续已平但仍有借币欠款≈%.6f(未还清),保留持仓待 manage 买回还币", p.Symbol, -baseBal)
					continue
				}
				c.appendControllerLog("Bybit reconcile:%s 负 carry 永续已平且借币已还清,清除落盘记录", p.Symbol)
				_ = store.remove(p.Symbol)
				continue
			}
			// 正 carry:若现货 base 仍有余额(典型:历史平仓腿因「按名义卖出 > 真实余额」失败而残留),
			// 保留持仓 → 交由 manage 循环用修复后的 CloseDeltaNeutral 按真实余额把裸现货腿平掉,避免孤儿敞口。
			if baseBal > executionQtyTolerance {
				c.appendControllerLog("Bybit reconcile:%s 永续已平但现货仍有余额≈%.6f(裸现货腿),保留持仓待 manage 按真实余额平掉", p.Symbol, baseBal)
				continue
			}
			c.appendControllerLog("Bybit reconcile:%s 交易所无永续持仓且现货无余额,清除落盘记录", p.Symbol)
			_ = store.remove(p.Symbol)
			continue
		}
		c.appendControllerLog("Bybit reconcile:%s 恢复持仓监控(永续≈%.6f),将按结算时间平仓", p.Symbol, actualBySym[p.Symbol])
	}
	// 孤儿仓兜底:交易所有永续持仓但本地无台账(开仓成功但落盘失败后崩溃等)。修 PROJECT_AUDIT C-10:
	// 先用确定性 orderLinkId(lx-<SYM>-<结算时刻>-op)向交易所回查近 48 个整点,确认是本机所开则
	// 自动收编为「立即到期、只平不滚」的台账记录,交由 manage 循环按真实余额平掉;查不到归属
	// (人工持仓/超出回查窗口)仍只告警不动仓,绝不误平无关持仓。
	for _, ep := range positions {
		sym := ep.Symbol
		qty := actualBySym[sym]
		if qty <= executionQtyTolerance || tracked[sym] {
			continue
		}
		tracked[sym] = true // 同一 symbol 多条(双向持仓)只处理一次
		if adopted, ok := c.bybitProbeOrphanOwnership(ctx, adapter, ep); ok {
			if c.bybitPersistPosition(store, adopted, "孤儿仓收编") {
				c.appendControllerLog("Bybit reconcile:未登记持仓 %s(≈%.6f)确认为本机所开(orderLinkId 命中),已收编落盘待平", sym, qty)
			}
			c.bybitNotifyWarn("Bybit 孤儿仓已收编", fmt.Sprintf("%s 永续≈%.6f 确认为本机遗留,已自动收编待平", sym, qty), string(sym))
			continue
		}
		c.appendControllerLog("Bybit reconcile:发现未登记的永续持仓 %s(≈%.6f),回查未命中本机订单,请人工核对", sym, qty)
		c.bybitNotifyWarn("Bybit 发现未登记持仓", fmt.Sprintf("%s 永续≈%.6f 无本地台账且回查未命中本机订单,请人工核对", sym, qty), string(sym))
	}
}

// bybitOrphanProbeHours 是孤儿仓归属回查的整点窗口数(Bybit 结算时刻总在整点;realtime 接口可查近期已成交单)。
const bybitOrphanProbeHours = 48

// bybitProbeOrphanOwnership 回查某未登记永续持仓是否本机所开:用确定性幂等前缀重构开仓腿
// orderLinkId 逐整点探测,命中已成交单即返回可收编的「立即到期待平」持仓。
func (c *RuntimeController) bybitProbeOrphanOwnership(ctx context.Context, adapter bybitFullVenue, ep venue.Position) (BybitLivePosition, bool) {
	now := time.Now().UTC()
	hourTop := now.Truncate(time.Hour)
	for h := 0; h <= bybitOrphanProbeHours; h++ {
		funding := hourTop.Add(time.Duration(1-h) * time.Hour) // 从下一个整点往回探(开仓时记录的是未来的结算时刻)
		clientID := bybitLegClientID(bybitIdemPrefix(ep.Symbol, funding), "op")
		res, err := adapter.QueryOrder(ctx, venue.MarketPerp, ep.Symbol, clientID)
		if err != nil || res.Status != "filled" || res.ExecutedQty <= executionQtyTolerance {
			continue
		}
		qty := math.Abs(ep.PositionAmt)
		adopted := BybitLivePosition{
			Symbol:          ep.Symbol,
			BaseQty:         qty,
			SpotEntryPrice:  ep.EntryPrice,
			PerpEntryPrice:  ep.EntryPrice,
			SpotNotional:    qty * ep.EntryPrice,
			PerpNotional:    qty * ep.EntryPrice,
			NextFundingTime: now.Add(-time.Hour), // 立即到期
			OpenedAt:        funding.Add(-time.Hour),
			RunID:           c.currentRunID(),
			IdemPrefix:      bybitIdemPrefix(ep.Symbol, funding),
			Negative:        ep.PositionAmt > 0, // 永续多头 = 负 carry 持仓
			SettlementsHeld: 1 << 30,            // 不续持,直接平
		}
		return adopted, true
	}
	return BybitLivePosition{}, false
}

// appendBybitSettlement 把 Bybit 平仓结果写入与币安同 schema 的结算台账(settlements.json,per-venue
// 目录天然隔离)。修 PROJECT_AUDIT C-3:此前 Bybit 平仓盈亏只打日志/Telegram,无结构化记录,
// 两所成本口径不可比、学费数据只能从日志手工拼。写失败只告警(平仓事实已发生,不能因记账失败中断管理循环)。
func (c *RuntimeController) appendBybitSettlement(p BybitLivePosition, closeRes bybitCloseResult, settlements int, now time.Time) {
	if c.store == nil {
		return
	}
	estFunding := float64(settlements) * math.Abs(p.FundingRate) * p.PerpNotional
	fundingIncome := estFunding
	if closeRes.FundingOK {
		fundingIncome = closeRes.RealFunding
	}
	settlement := CycleSettlement{
		RunID:                 c.currentRunID(),
		CycleID:               p.IdemPrefix,
		Targets:               1,
		FundingIncome:         fundingIncome,
		ExpectedFundingIncome: estFunding,
		FundingIncomeDiff:     fundingIncome - estFunding,
		FundingIncomePending:  !closeRes.FundingOK,
		TradingPnL:            closeRes.PricePnL,
		SpotTradingPnL:        closeRes.SpotPricePnL,
		FuturesTradingPnL:     closeRes.PerpPricePnL,
		TotalFees:             closeRes.TotalFeesUSDT,
		NetPnL:                closeRes.RealizedPnL,
		SnapshotNetPnL:        closeRes.RealizedPnL,
		CloseExecutedAt:       now,
		FundingTimePoint:      p.NextFundingTime,
		CycleNotionalUSDT:     p.SpotNotional + p.PerpNotional,
	}
	if closeRes.FundingOK {
		settlement.FundingSettledAt = now
	}
	if err := c.store.AppendSettlement(settlement); err != nil {
		c.appendControllerLog("Bybit 结算记录落盘失败 %s(平仓已完成,仅记账缺失): %v", p.Symbol, err)
	}
}

// bybitSpotFreeBalance 返回某标的 base 资产的现货可用余额(查不到则 0)。
func bybitSpotFreeBalance(bals venue.Balances, sym venue.Symbol) float64 {
	if bals.ByAsset == nil {
		return 0
	}
	return bals.ByAsset[baseAssetOf(string(sym))].Free
}

// bybitMaxRollWait 是「续持待定」的安全上限:结算后若交易所 nextFundingTime 超过此时长仍未翻进下一个结算
// (异常,正常几秒内翻进),则放弃续持、转平仓,避免无限持有。资金费间隔通常 ≥1h,20min 足够容忍正常翻进延迟。
const bybitMaxRollWait = 20 * time.Minute

// bybitManageCycle 单轮管理:先平到期持仓,再(在结算前窗口内、未满并发上限时)扫描开新仓。
func (c *RuntimeController) bybitManageCycle(
	ctx context.Context,
	cfg EngineConfig,
	store *bybitPositionStore,
	planner *BybitShadowExecutor,
	executor *BybitLiveExecutor,
	adapter bybitFullVenue,
	live bool,
) {
	now := time.Now().UTC()
	holdAfter := time.Duration(cfg.BybitExecutor.HoldAfterSeconds) * time.Second
	lead := time.Duration(cfg.BybitExecutor.LeadSeconds) * time.Second
	maxConcurrent := c.bybitMaxConcurrent(cfg)

	open, err := store.load()
	if err != nil {
		c.appendControllerLog("Bybit 读取持仓失败: %v", err)
		return
	}
	open = c.bybitFlushPendingPersist(store, open)

	// 1) 到期持仓(结算后 holdAfter):滚动持有模式下先复核下期费率决定续持/等待/平仓;否则直接平(单次收割)。
	rollEnabled := cfg.BybitExecutor.RollEnabled
	maxHold := cfg.BybitExecutor.MaxHoldSettlements
	if maxHold <= 0 {
		maxHold = 1
	}
	for _, p := range open {
		if p.NextFundingTime.IsZero() || !now.After(p.NextFundingTime.Add(holdAfter)) {
			continue
		}
		// 滚动持有:已收满本次结算的资金费;若开启滚动且未达持有上限,复核下期费率决定续持/等待/平仓。
		// 安全闸:只在永续对冲腿确认在场(仓位健康对冲)时才考虑续持,绝不滚动裸仓。
		if rollEnabled && p.SettlementsHeld+1 < maxHold && (!live || executor.PerpOpenQty(ctx, p.Symbol) > executionQtyTolerance) {
			snap, ok, ferr := planner.FundingSnapshotFor(ctx, p.Symbol)
			switch {
			case ferr != nil:
				c.appendControllerLog("Bybit 续持复核拉取费率失败 %s(本轮按平仓处理): %v", p.Symbol, ferr)
			case !ok || !bybitFundingStillFavorable(snap.LastFundingRate, p.Negative):
				// 与币安滚动持有同口径:开仓后往返手续费已是沉没成本,再多持有一个结算「不额外收费」,故只要下期资金费仍对
				// 本方向有利(正 carry:>0;负 carry:<0,边际净 carry>0)就续持收下一次;直到费率转向不利(继续持有会倒贴)才平。
				c.appendControllerLog("Bybit 续持复核未通过 %s(下期费率转向不利,继续持有会倒贴),平仓", p.Symbol)
			case !snap.NextFundingTime.After(now):
				// 费率达标,但交易所 nextFundingTime 尚未翻进下一个结算(刚结算后的短暂窗口):短暂保持持仓、下一轮再复核,
				// 既不平仓也不推进计数——避免把一笔仍健康、费率仍好的仓在结算时间翻转前误平(这正是之前「吃一次就走」的根因)。
				// 安全上限:若结算后超过 bybitMaxRollWait 仍未翻进(交易所异常),放弃续持转平仓,避免无限持有。
				if now.Before(p.NextFundingTime.Add(bybitMaxRollWait)) {
					c.appendControllerLog("Bybit 续持待定 %s:下期费率=%.4f%% 达标,但 nextFundingTime 尚未翻进,保持持仓下轮复核", p.Symbol, snap.LastFundingRate*100)
					continue
				}
				c.appendControllerLog("Bybit 续持超时 %s:结算后 %s 内 nextFundingTime 仍未翻进,放弃续持转平仓", p.Symbol, bybitMaxRollWait)
			default:
				if !live {
					c.appendControllerLog("[bybit-shadow][拟续持] %s 下期费率=%.4f%% 续持到 %s", p.Symbol, snap.LastFundingRate*100, snap.NextFundingTime.Format(time.RFC3339))
					continue
				}
				rolled := p
				rolled.NextFundingTime = snap.NextFundingTime
				rolled.FundingRate = snap.LastFundingRate
				rolled.SettlementsHeld = p.SettlementsHeld + 1
				// 落盘失败也照常续持(内存兜底接管):此前"落盘失败按平仓处理"会把仍健康的仓提前平掉。
				c.bybitPersistPosition(store, rolled, "续持")
				c.appendControllerLog("Bybit 续持 %s:已收 %d 次资金费,下期费率=%.4f%% 续持到 %s(上限 %d 次)",
					p.Symbol, rolled.SettlementsHeld, snap.LastFundingRate*100, snap.NextFundingTime.UTC().Format("15:04:05Z"), maxHold)
				c.bybitNotifyTrade("Bybit 续持", fmt.Sprintf("已收 %d 次资金费,下期费率=%.4f%%,续持到 %s",
					rolled.SettlementsHeld, snap.LastFundingRate*100, snap.NextFundingTime.UTC().Format("15:04:05Z")), string(p.Symbol))
				continue
			}
		}
		if !live {
			c.appendControllerLog("[bybit-shadow][拟平仓] %s(结算后到期)", p.Symbol)
			continue
		}
		closeRes, closeErr := executor.CloseDeltaNeutral(ctx, p)
		if closeErr != nil {
			c.appendControllerLog("Bybit 平仓失败(保留持仓,下轮重试) %s: %v", p.Symbol, closeErr)
			c.bybitNotifyWarn("Bybit 平仓失败", closeErr.Error(), string(p.Symbol))
			continue
		}
		if rmErr := store.remove(p.Symbol); rmErr != nil {
			c.appendControllerLog("Bybit 平仓后清记录失败 %s: %v", p.Symbol, rmErr)
		}
		delete(c.bybitPendingPersist, p.Symbol)
		settlements := p.SettlementsHeld + 1
		// 资金费 + 实现盈亏:优先用真实查流水口径;查不到则退回按开仓费率估算(并明确标注)。
		// 估算资金费收入按 |费率| 计(正/负 carry 都是收方:正 carry 空头收正费率,负 carry 多头收负费率)。
		fundingLine := fmt.Sprintf("估算资金费收入 ≈ %+.4f USDT(%d×%.4f%%×名义)", float64(settlements)*math.Abs(p.FundingRate)*p.PerpNotional, settlements, math.Abs(p.FundingRate)*100)
		realizedLine := ""
		if closeRes.FundingOK {
			fundingLine = fmt.Sprintf("真实资金费收入 = %+.4f USDT(共 %d 次结算)", closeRes.RealFunding, settlements)
			verdict := "赚"
			if closeRes.RealizedPnL < 0 {
				verdict = "赔"
			}
			realizedLine = fmt.Sprintf("\n【实现盈亏 = %+.4f USDT(%s)】= 价差 %+.4f + 资金费 %+.4f − 手续费 %.4f", closeRes.RealizedPnL, verdict, closeRes.PricePnL, closeRes.RealFunding, closeRes.TotalFeesUSDT)
		}
		openPerpLabel, openSpotLabel, closePerpLabel, closeSpotLabel := "永续空", "现货多", "永续买回", "现货卖出"
		if p.Negative {
			openPerpLabel, openSpotLabel, closePerpLabel, closeSpotLabel = "永续多", "借币空现货", "永续卖出", "现货买回还币"
		}
		closeDetail := fmt.Sprintf(
			"%s 已平仓(持有 %d 次结算)\n开仓: %s %.2fU @%.6f(费 %.6f %s) + %s %.2fU @%.6f(费 %.6f %s)\n平仓: %s @%.6f(费 %.6f %s) + %s %.6f @%.6f(费 %.6f %s)\n%s%s",
			p.Symbol, settlements,
			openPerpLabel, p.PerpNotional, p.PerpEntryPrice, p.OpenPerpFee, p.OpenPerpFeeAsset,
			openSpotLabel, p.SpotNotional, p.SpotEntryPrice, p.OpenSpotFee, p.OpenSpotFeeAsset,
			closePerpLabel, closeRes.PerpClosePrice, closeRes.PerpCloseFee, closeRes.PerpFeeAsset,
			closeSpotLabel, closeRes.SpotSoldQty, closeRes.SpotClosePrice, closeRes.SpotCloseFee, closeRes.SpotFeeAsset,
			fundingLine, realizedLine)
		c.appendControllerLog("Bybit 已平仓 %s", closeDetail)
		c.bybitNotifyTrade("Bybit 已平仓", closeDetail, string(p.Symbol))
		c.appendBybitSettlement(p, closeRes, settlements, now)
	}

	// 重新读取在场持仓(平仓可能已删除若干);内存兜底里仍未落盘的也要占并发名额。
	open, err = store.load()
	if err != nil {
		c.appendControllerLog("Bybit 重载持仓失败: %v", err)
		return
	}
	open = c.bybitFlushPendingPersist(store, open)
	held := make(map[venue.Symbol]bool, len(open))
	for _, p := range open {
		held[p.Symbol] = true
	}
	if len(open) >= maxConcurrent {
		c.setBybitStatus(open, fmt.Sprintf("在场 %d/%d,已满并发上限", len(open), maxConcurrent))
		return
	}

	// 2) 扫描规划(只读),在结算前窗口内开新仓。
	plan, err := planner.PlanCycle(ctx)
	if err != nil {
		c.appendControllerLog("Bybit 扫描失败: %v", err)
		return
	}
	// 实盘可见性:live 模式整轮原本静默(shadow 才有 LogShadowPlan),导致"就绪后再无动静"无法判因。
	// 这里补一段每轮诊断:候选/通过/放弃数 + 放弃原因 + Top 候选距开仓窗口还有多久,纯日志、不改交易决策。
	if live {
		c.bybitLogCycleDiag(now, lead, plan, held, maxConcurrent, len(open), cfg)
	}
	// 修 PROJECT_AUDIT C-5(余额裁剪):planner 只信配置 AllocatedUSDT,从不对照真实账户。
	// 配置与余额脱节时按名义下单只会制造无谓的失败样本。开仓前拉一次统一账户 USDT 可用余额,
	// 名义超过可用即跳过(拉取失败时退回旧行为,不阻塞开仓)。
	availUSDT := math.Inf(1)
	if live && len(plan.Accepted) > 0 {
		if bals, berr := adapter.FetchBalances(ctx); berr != nil {
			c.appendControllerLog("Bybit 开仓前拉余额失败(本轮跳过余额校验): %v", berr)
		} else if usdt, ok := bals.ByAsset["USDT"]; ok {
			availUSDT = usdt.Free
		}
	}
	for _, p := range plan.Accepted {
		if len(held) >= maxConcurrent {
			break
		}
		if held[p.Symbol] {
			continue
		}
		// 仅在 [结算-lead, 结算) 窗口内开仓。
		if p.NextFundingTime.IsZero() || now.Before(p.NextFundingTime.Add(-lead)) || !now.Before(p.NextFundingTime) {
			continue
		}
		if capN := cfg.BybitExecutor.MaxPositionNotionalUSDT; capN > 0 && (p.SpotNotional+p.PerpNotional) > capN {
			c.appendControllerLog("Bybit 跳过 %s:两腿名义 %.2f 超单标的上限 %.2f", p.Symbol, p.SpotNotional+p.PerpNotional, capN)
			continue
		}
		// 真实余额闸:正 carry 需要 现货名义 + 永续保证金(保守按 1x 全额计);负 carry 现货腿是借币卖出
		// (反而收 USDT),只需永续保证金。
		requiredUSDT := p.SpotNotional + p.PerpNotional
		if p.Negative {
			requiredUSDT = p.PerpNotional
		}
		if requiredUSDT > availUSDT {
			c.appendControllerLog("Bybit 跳过 %s:开仓需要 ≈%.2f USDT,统一账户可用仅 %.2f(配置分配与真实余额脱节)", p.Symbol, requiredUSDT, availUSDT)
			continue
		}
		if !live {
			c.appendControllerLog("[bybit-shadow][拟开仓] %s 费率=%.4f%% 数量=%.6f 净利率=%.4f%%(结算 %s)",
				p.Symbol, p.FundingRate*100, p.BaseQty, p.EstNetProfitRate*100, p.NextFundingTime.Format(time.RFC3339))
			continue
		}
		idem := bybitIdemPrefix(p.Symbol, p.NextFundingTime)
		pos, openErr := executor.OpenDeltaNeutral(ctx, p, idem, c.currentRunID())
		if openErr != nil {
			// 开仓异常但已留下永续敞口(回补失败的裸腿)→ 落盘为「立即到期、不滚动」的待平持仓,交由下轮平仓清理,绝不失管。
			if pos.BaseQty > executionQtyTolerance {
				pos.SettlementsHeld = 1 << 30 // 远超 maxHold → 不续持,直接平
				pos.NextFundingTime = now.Add(-time.Hour)
				if c.bybitPersistPosition(store, pos, "开仓裸腿") {
					c.appendControllerLog("Bybit 开仓异常留有永续敞口 %s(≈%.6f),已落盘待平", pos.Symbol, pos.BaseQty)
				}
			}
			c.appendControllerLog("Bybit 开仓失败 %s: %v", p.Symbol, openErr)
			c.bybitNotifyWarn("Bybit 开仓失败", openErr.Error(), string(p.Symbol))
			continue
		}
		c.bybitPersistPosition(store, pos, "新开持仓")
		openPerpLabel, openSpotLabel := "永续空", "现货多"
		if pos.Negative {
			openPerpLabel, openSpotLabel = "永续多", "借币空现货"
		}
		openDetail := fmt.Sprintf(
			"%s 已开仓 delta-neutral(%s)\n%s %.6f@%.6f(名义 %.2f USDT,实扣费 %.6f %s)\n%s %.6f@%.6f(名义 %.2f USDT,实扣费 %.6f %s)\n预估费率 %.4f%% | 预估净利率 %.4f%% | 结算 %s",
			pos.Symbol, bybitCarryLabel(pos.Negative),
			openPerpLabel, pos.BaseQty, pos.PerpEntryPrice, pos.PerpNotional, pos.OpenPerpFee, pos.OpenPerpFeeAsset,
			openSpotLabel, pos.BaseQty, pos.SpotEntryPrice, pos.SpotNotional, pos.OpenSpotFee, pos.OpenSpotFeeAsset,
			p.FundingRate*100, p.EstNetProfitRate*100, pos.NextFundingTime.UTC().Format("15:04:05Z"))
		c.appendControllerLog("Bybit %s", openDetail)
		c.bybitNotifyTrade("Bybit 已开仓", openDetail, string(p.Symbol))
		held[pos.Symbol] = true
		open = append(open, pos)
		availUSDT -= requiredUSDT
	}
	c.setBybitStatus(open, fmt.Sprintf("在场 %d/%d", len(open), maxConcurrent))
}

// bybitLogCycleDiag 为 live 模式打印每轮规划诊断:解释为何尚未开仓(无候选 / 被净利率等放弃 / 未到 60s 窗口)。
// 纯可见性日志,不影响交易决策。候选已被 TopN 截断(默认 1),故每轮日志量很小。
func (c *RuntimeController) bybitLogCycleDiag(
	now time.Time,
	lead time.Duration,
	plan BybitCyclePlan,
	held map[venue.Symbol]bool,
	maxConcurrent int,
	openCount int,
	cfg EngineConfig,
) {
	if len(plan.Candidates) == 0 {
		dirNote := "正费率"
		if cfg.BybitExecutor.EnableNegativeCarry {
			dirNote = "正/负费率"
		}
		c.appendControllerLog("[bybit] 本轮无%s候选(需|费率|≥%.4f%%);在场 %d/%d", dirNote, cfg.BybitExecutor.MinFundingRate*100, openCount, maxConcurrent)
		return
	}
	c.appendControllerLog("[bybit] 候选=%d 通过定量=%d 放弃=%d 在场=%d/%d",
		len(plan.Candidates), len(plan.Accepted), len(plan.Rejected), openCount, maxConcurrent)
	for _, r := range plan.Rejected {
		c.appendControllerLog("[bybit][放弃] %s: %s", r.Symbol, r.Reason)
	}
	for _, p := range plan.Accepted {
		switch {
		case held[p.Symbol]:
			c.appendControllerLog("[bybit][已持有] %s,跳过", p.Symbol)
		case p.NextFundingTime.IsZero():
			c.appendControllerLog("[bybit][等待] %s 无结算时间", p.Symbol)
		case now.Before(p.NextFundingTime.Add(-lead)):
			wait := p.NextFundingTime.Add(-lead).Sub(now).Truncate(time.Second)
			c.appendControllerLog("[bybit][等待开仓窗口] %s 费率=%.4f%% 净利率=%.4f%% 距开仓窗口(结算前%ds)还有 %s,结算=%s",
				p.Symbol, p.FundingRate*100, p.EstNetProfitRate*100, cfg.BybitExecutor.LeadSeconds, wait, p.NextFundingTime.UTC().Format("15:04:05Z"))
		case !now.Before(p.NextFundingTime):
			c.appendControllerLog("[bybit][已过结算] %s 本次结算已过,等待下一周期", p.Symbol)
		default:
			c.appendControllerLog("[bybit][窗口内] %s 费率=%.4f%% 净利率=%.4f%%,满足条件,准备下单",
				p.Symbol, p.FundingRate*100, p.EstNetProfitRate*100)
		}
	}
}

// bybitNotifyTrade 推送 Bybit 交易/生命周期通知(开仓/平仓/启停):info 级,不受 MinSeverity 过滤、不去重,
// 这是用户主动要的「每笔都通知」。分发器未配置 Telegram 时为 nil,nil-safe。
func (c *RuntimeController) bybitNotifyTrade(title, detail, symbol string) {
	if c == nil || c.bybitAlerts == nil {
		return
	}
	c.bybitAlerts.NotifyTrade(Alert{
		Level:      EventLevelInfo,
		Title:      title,
		Detail:     detail,
		Symbol:     symbol,
		OccurredAt: time.Now().UTC(),
	})
}

// bybitNotifyWarn 推送 Bybit 异常告警(开/平失败、急停):warning 级,经 MinSeverity 与去重窗口,避免刷屏。
func (c *RuntimeController) bybitNotifyWarn(title, detail, symbol string) {
	if c == nil || c.bybitAlerts == nil {
		return
	}
	c.bybitAlerts.Notify(Alert{
		Level:      EventLevelWarning,
		Title:      title,
		Detail:     detail,
		Symbol:     symbol,
		OccurredAt: time.Now().UTC(),
	})
}

// setBybitStatus 把在场持仓与一句话状态写入面板状态(轻量,不复用币安那套庞大快照)。
func (c *RuntimeController) setBybitStatus(open []BybitLivePosition, message string) {
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
	if strings.TrimSpace(message) != "" {
		c.status.LastMessage = message
	}
}
