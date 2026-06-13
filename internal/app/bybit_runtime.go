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
		PerpLeverage:        bybitEffectiveLeverage(bx.PerpLeverage),
		EVGateEnabled:       bx.EVGateEnabled,
		RollEnabled:         bx.RollEnabled,
		MaxHoldSettlements:  bx.MaxHoldSettlements,
		BorrowHourlyRate:    bx.BorrowCostHourly,
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

	// 页面调仓后的分配资金覆盖(capital_override.json)优先于 config.yaml:
	// 调仓把账户调到 127U 而配置还是 60U 时,引擎应按 127U 跑,不让钱闲置。
	if ov, ok := loadVenueCapitalOverride(cfg.NetworkConfig.RecordDir); ok {
		c.appendControllerLog("分配资金按页面调仓覆盖生效: %.2f USDT(config.yaml=%.2f)", ov, cfg.AllocatedCapitalUSDT)
		cfg.AllocatedCapitalUSDT = ov
	}
	execCfg := bybitExecConfigFromEngine(cfg)
	logf := func(format string, args ...any) { c.appendControllerLog(format, args...) }
	planner := NewBybitShadowExecutor(adapter, execCfg, logf)
	executor := NewBybitLiveExecutor(adapter, logf, cfg.NetworkConfig.RecordDir)
	executor.SetPerpMakerFirst(cfg.BybitExecutor.PerpMakerFirst)
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

	// 启动清扫(R14 的 Bybit 散户路径):set-dcp(disconnect-cancel-all)官方文档明确仅机构客户可用,
	// 散户进程死亡时残留挂单没有交易所侧 dead-man 兜底 → 改为「进程被 systemd/watchdog 拉起后立即
	// 撤掉本账户全部活动挂单」。放在 reconcile 之后:先按已成交仓位收编对账,再清纯挂单(撤的只是
	// 未成交挂单,不动持仓)。暴露窗口 = 重启延迟(systemd 秒级 / watchdog 分钟级),可控。
	if live {
		type allCanceler interface {
			CancelAllOrders(context.Context, venue.Market) (int, error)
		}
		if ac, ok := adapter.(allCanceler); ok {
			for _, m := range []venue.Market{venue.MarketPerp, venue.MarketSpot} {
				if n, cerr := ac.CancelAllOrders(ctx, m); cerr != nil {
					c.appendControllerLog("Bybit 启动清扫(%s)失败,残留挂单可能仍在,请人工核对: %v", m, cerr)
				} else if n > 0 {
					c.appendControllerLog("Bybit 启动清扫: 已撤销 %s 残留挂单 %d 笔(上次进程退出前未成交的腿)", m, n)
					c.bybitNotifyWarn("Bybit 启动清扫", fmt.Sprintf("已撤销 %s 残留挂单 %d 笔", m, n), "")
				}
			}
		}
	}

	readyMsg := fmt.Sprintf("模式=%s 分配资金=%.2f TopN=%d 最小费率=%.4f%% 提前=%ds 持有=%ds 并发上限=%d",
		bybitModeLabel(cfg), execCfg.AllocatedUSDT, execCfg.TopN, execCfg.MinFundingRate*100,
		cfg.BybitExecutor.LeadSeconds, cfg.BybitExecutor.HoldAfterSeconds, c.bybitMaxConcurrent(cfg))
	c.appendControllerLog("Bybit 执行器已就绪:%s", readyMsg)

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
		if isCrossVenueSymbolHeld(c.cfg.NetworkConfig.RecordDir, sym) {
			c.appendControllerLog("Bybit reconcile:跳过 %s(跨所台账持有中,非单所孤儿,不收编)", sym)
			continue
		}
		if adopted, orderLinkID, ok := c.bybitProbeOrphanOwnership(ctx, adapter, ep); ok {
			if c.bybitPersistPosition(store, adopted, "孤儿仓收编") {
				c.appendControllerLog("Bybit reconcile:未登记持仓 %s(≈%.6f)确认为本机所开(orderLinkId 命中),已收编落盘待平", sym, qty)
			}
			c.bybitNotifyWarn("Bybit 孤儿仓已收编", fmt.Sprintf("%s 永续≈%.6f 确认为本机遗留,已自动收编待平", sym, qty), string(sym))
			continue
		} else if orderLinkID != "" && strings.HasPrefix(orderLinkID, crossVenueOrderPrefix) {
			c.appendControllerLog("Bybit reconcile:跳过 %s(orderLinkId=%s 属跨所引擎所开,非单所孤儿,不收编)", sym, orderLinkID)
			continue
		}
		c.appendControllerLog("Bybit reconcile:发现未登记的永续持仓 %s(≈%.6f),回查未命中本机订单,请人工核对", sym, qty)
		c.bybitNotifyWarn("Bybit 发现未登记持仓", fmt.Sprintf("%s 永续≈%.6f 无本地台账且回查未命中本机订单,请人工核对", sym, qty), string(sym))
	}
}

// bybitOrphanProbeHours 是孤儿仓归属回查的整点窗口数(Bybit 结算时刻总在整点;realtime 接口可查近期已成交单)。
const bybitOrphanProbeHours = 48

const (
	bybitSingleVenueOrderPrefix = "lx-"
	crossVenueOrderPrefix       = "cv-"
)

func bybitMatchedOrderLinkID(queried, returned string) string {
	returned = strings.TrimSpace(returned)
	if returned != "" {
		return returned
	}
	return strings.TrimSpace(queried)
}

// bybitProbeOrphanOwnership 回查某未登记永续持仓是否本机所开:用确定性幂等前缀重构开仓腿
// orderLinkId 逐整点探测,命中已成交单即返回可收编的「立即到期待平」持仓。
// 第二返回值是命中的 orderLinkId(供日志);跨所 cv- 前缀或单所 lx- 以外的一律不收编。
func (c *RuntimeController) bybitProbeOrphanOwnership(ctx context.Context, adapter bybitFullVenue, ep venue.Position) (BybitLivePosition, string, bool) {
	now := time.Now().UTC()
	hourTop := now.Truncate(time.Hour)
	for h := 0; h <= bybitOrphanProbeHours; h++ {
		funding := hourTop.Add(time.Duration(1-h) * time.Hour) // 从下一个整点往回探(开仓时记录的是未来的结算时刻)
		clientID := bybitLegClientID(bybitIdemPrefix(ep.Symbol, funding), "op")
		res, err := adapter.QueryOrder(ctx, venue.MarketPerp, ep.Symbol, clientID)
		if err != nil || res.Status != "filled" || res.ExecutedQty <= executionQtyTolerance {
			continue
		}
		orderLinkID := bybitMatchedOrderLinkID(clientID, res.ClientOrderID)
		if strings.HasPrefix(orderLinkID, crossVenueOrderPrefix) {
			return BybitLivePosition{}, orderLinkID, false
		}
		if !strings.HasPrefix(orderLinkID, bybitSingleVenueOrderPrefix) {
			return BybitLivePosition{}, orderLinkID, false
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
			ForceClose:      true,               // 不续持,直接平
		}
		return adopted, orderLinkID, true
	}
	return BybitLivePosition{}, "", false
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

// bybitEffectivePositionNotionalCap 返回单标的两腿名义的有效硬顶:max(配置值, 1.6×分配资金)。
// 配置静态值会跟页面调仓打架(60U 时代设的 100 顶,调仓到 127U 后满额两腿 ≈167 → 永远开不出单,
// 2026-06-12 实锤空转一夜)。杠杆 ≤3x 时合法两腿名义最大 = 分配×2×lev/(lev+1) ≤ 1.5×分配,
// 1.6 倍留滑点/取整余量;同时仍能拦住「数量算错一个数量级」的灾难单(兜底语义保留)。
func bybitEffectivePositionNotionalCap(cfg EngineConfig) float64 {
	capN := cfg.BybitExecutor.MaxPositionNotionalUSDT
	if capN <= 0 {
		return 0 // 0=不限,沿用原语义
	}
	if dynamic := cfg.AllocatedCapitalUSDT * 1.6; dynamic > capN {
		return dynamic
	}
	return capN
}

// bybitOpenBanDuration:不可交易类开仓失败的拉黑时长。账户级设置(抵押开通)短期不会自己变,
// 12h 足以避免整天反复撞墙烧手续费;重启清零,给人工修复后的重试留口子。
const bybitOpenBanDuration = 12 * time.Hour

// isBybitUntradableOpenError 识别「该标的当前账户不可交易」类开仓错误(重试必然再失败、只会烧费):
// 170037 = 现货未开通保证金抵押设置(负 carry 借币空现货的前置条件缺失)。
func isBybitUntradableOpenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "170037") || strings.Contains(msg, "not opened collateral settings")
}

// bybitBorrowUnavailableBan:借币池借不到(170207 类)的拉黑时长。池子可能回补,不必像 170037 一拉 12h;
// 30 分钟足以覆盖当前结算窗口、杜绝同窗 30s 一轮的重试;后续窗口由开仓前借币预检(零下单)继续把关。
const bybitBorrowUnavailableBan = 30 * time.Minute

// isBybitBorrowUnavailableError 识别「借币池当前借不到」类现货杠杆错误(短期重试大概率再失败):
// 170207 = The loan amount of the platform is not enough.(平台借币池无可借额度)
// 2026-06-12 HOME 实锤:此前不识别该码 → 同一窗口重试 3 次、下一小时窗口原样重演,白烧开+回补手续费。
func isBybitBorrowUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "170207") || strings.Contains(msg, "loan amount of the platform")
}

// bybitBorrowQuotaSource 是可选能力:查某 base 资产「可借+可卖」最大数量(*bybit.Adapter 满足;
// 测试 fake 可不实现 → 预检自动跳过,fail-open)。
type bybitBorrowQuotaSource interface {
	MaxBorrowable(ctx context.Context, asset string) (float64, error)
}

// bybitBanOpen 把某标的加入开仓黑名单直到 until(取更晚者,绝不缩短已有拉黑)。仅 Bybit 管理协程访问。
func (c *RuntimeController) bybitBanOpen(sym venue.Symbol, until time.Time) {
	if c.bybitOpenSkipUntil == nil {
		c.bybitOpenSkipUntil = make(map[venue.Symbol]time.Time)
	}
	if until.After(c.bybitOpenSkipUntil[sym]) {
		c.bybitOpenSkipUntil[sym] = until
	}
}

// bybitPreSettleCheckLead:结算前多久开始做「去留复核」。Bybit 预测费率在结算后重置回基准值、
// 临近结算才收敛,因此复核必须贴近结算;150s 时预测已基本定型,且仍留足平仓执行时间,
// 并落在开仓窗口(lead=240s)内——换仓比较时新候选恰好也在窗口里,时机对齐。
const bybitPreSettleCheckLead = 150 * time.Second

// bybitCloseAndSettle 平掉一笔在场持仓并完成全部收尾:删台账、清 pending、写统一结算流水、日志+通知。
// settlements 是该仓累计「已收资金费次数」(结算前离场=SettlementsHeld,本期未收;结算后平仓=SettlementsHeld+1)。
// 返回是否成功;失败保留台账,下轮重试。
func (c *RuntimeController) bybitCloseAndSettle(
	ctx context.Context,
	store *bybitPositionStore,
	executor *BybitLiveExecutor,
	p BybitLivePosition,
	settlements int,
	now time.Time,
) bool {
	closeRes, closeErr := executor.CloseDeltaNeutral(ctx, p)
	if closeErr != nil {
		c.appendControllerLog("Bybit 平仓失败(保留持仓,下轮重试) %s: %v", p.Symbol, closeErr)
		c.bybitNotifyWarn("Bybit 平仓失败", closeErr.Error(), string(p.Symbol))
		return false
	}
	if rmErr := store.remove(p.Symbol); rmErr != nil {
		c.appendControllerLog("Bybit 平仓后清记录失败 %s: %v", p.Symbol, rmErr)
	}
	delete(c.bybitPendingPersist, p.Symbol)
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
	return true
}

// bybitRollSwitchFactor:续持复核时,新候选的「费率/小时」至少要达到当前持仓下期的该倍数才值得换仓。
// 换仓要付新仓开+平费用,而续持是零边际成本,所以必须显著更肥才动;3 倍是保守的工程取值。
const bybitRollSwitchFactor = 3.0

// bybitRollSwitchReason 续持换仓判断:返回非空字符串(原因)表示应平掉当前持仓、让位给更肥的新候选。
// 条件全部满足才换:① 新候选此刻就在开仓窗口内(平掉立即能接上,不让资金闲置等窗口);
// ② 不是同一标的(同标的换仓纯烧手续费);③ 新候选费率/小时 ≥ 当前下期费率/小时 × bybitRollSwitchFactor;
// ④ 新候选已通过全部既有定量/净利门(在 plan.Accepted 里)。规划失败时返回 ""(保守续持,绝不因扫描故障误平)。
func (c *RuntimeController) bybitRollSwitchReason(
	ctx context.Context,
	planner *BybitShadowExecutor,
	held BybitLivePosition,
	heldSnap venue.FundingSnapshot,
	now time.Time,
	lead time.Duration,
) string {
	plan, err := planner.PlanCycle(ctx, 1, 0)
	if err != nil || len(plan.Accepted) == 0 {
		return ""
	}
	heldPerHour := bybitRatePerHour(heldSnap.LastFundingRate, heldSnap.IntervalHours)
	for _, cand := range plan.Accepted {
		if cand.Symbol == held.Symbol {
			continue
		}
		if cand.NextFundingTime.IsZero() || now.Before(cand.NextFundingTime.Add(-lead)) || !now.Before(cand.NextFundingTime) {
			continue // 不在开仓窗口内:换过去也要闲置等窗口,不如继续续持
		}
		candPerHour := bybitRatePerHour(cand.FundingRate, cand.IntervalHours)
		if candPerHour <= 0 || candPerHour < heldPerHour*bybitRollSwitchFactor {
			continue
		}
		return fmt.Sprintf("新候选 %s 费率/小时=%.4f%%(间隔%dh) ≥ 当前 %s 下期 %.4f%% 的 %.0f 倍且已在开仓窗口",
			cand.Symbol, candPerHour*100, cand.IntervalHours, held.Symbol, heldPerHour*100, bybitRollSwitchFactor)
	}
	return ""
}

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

	// 1) 在场持仓管理。滚动模式的核心时序(修「续持读到重置底值」根因):
	//    Bybit 每次结算后把下期预测费率**重置回基准 0.00125%/interval**,随后一小时才随盘口溢价收敛成真实值。
	//    旧逻辑在「结算后 20s」复核,永远读到无信息量的正底值 → 永远续持(MBOX 实锤连付两期负费率)。
	//    资金费只在结算瞬间收付、结算之间持有零成本,因此:
	//    A) 去留决定挪到「下次结算前 bybitPreSettleCheckLead」——预测已收敛可信:不利→结算前离场(一分不付);
	//       有显著更肥的窗口内候选→换仓让位(此时其它币开仓窗口也开着,时机天然对齐);
	//    B) 结算后只做记账翻期(收次数+1、更新下期结算时间),不再做费率判断。
	rollEnabled := cfg.BybitExecutor.RollEnabled
	maxHold := cfg.BybitExecutor.MaxHoldSettlements
	if maxHold <= 0 {
		maxHold = 1
	}
	for _, p := range open {
		if p.NextFundingTime.IsZero() {
			continue
		}
		// A) 结算前复核(滚动 + live;shadow 不做真实下单,沿用 B 的拟平仓日志)。
		if rollEnabled && live && now.Before(p.NextFundingTime) && now.After(p.NextFundingTime.Add(-bybitPreSettleCheckLead)) {
			snap, ok, ferr := planner.FundingSnapshotFor(ctx, p.Symbol)
			if ferr == nil && ok {
				exitReason := ""
				if !bybitFundingStillFavorable(snap.LastFundingRate, p.Negative) {
					exitReason = fmt.Sprintf("下期费率=%.4f%% 已转不利,结算前离场避免倒贴", snap.LastFundingRate*100)
				} else if sw := c.bybitRollSwitchReason(ctx, planner, p, snap, now, lead); sw != "" {
					exitReason = "换仓让位:" + sw
				}
				if exitReason != "" {
					c.appendControllerLog("Bybit 结算前离场 %s:%s", p.Symbol, exitReason)
					// 结算前离场:本期资金费未收,settlements=已收次数(不 +1)。
					c.bybitCloseAndSettle(ctx, store, executor, p, p.SettlementsHeld, now)
					continue
				}
			}
			continue // 费率仍有利(或快照暂不可得):穿过结算收下一笔,下轮再核
		}
		// B) 结算后(holdAfter 缓冲确保资金费已入账)。
		if !now.After(p.NextFundingTime.Add(holdAfter)) {
			continue
		}
		// 滚动且未达上限:记账翻期。安全闸:待平残腿(ForceClose)绝不翻期;只在永续对冲腿确认在场时翻期,绝不滚动裸仓。
		if rollEnabled && !p.ForceClose && p.SettlementsHeld+1 < maxHold && (!live || executor.PerpOpenQty(ctx, p.Symbol) > executionQtyTolerance) {
			snap, ok, ferr := planner.FundingSnapshotFor(ctx, p.Symbol)
			switch {
			case ferr != nil || !ok:
				// 快照暂不可得:持有零成本,保持持仓下轮重试;超过 bybitMaxRollWait 仍不可得才放弃翻期转平仓。
				if now.Before(p.NextFundingTime.Add(bybitMaxRollWait)) {
					c.appendControllerLog("Bybit 续持记账拉取费率失败 %s(保持持仓下轮重试): %v", p.Symbol, ferr)
					continue
				}
				c.appendControllerLog("Bybit 续持记账超时 %s(费率快照持续不可得),转平仓", p.Symbol)
			case !snap.NextFundingTime.After(now):
				// 交易所 nextFundingTime 尚未翻进下一个结算(刚结算后的短暂窗口):保持持仓下轮再记账。
				// 安全上限:超过 bybitMaxRollWait 仍未翻进(交易所异常)→ 放弃翻期转平仓,避免无限持有。
				if now.Before(p.NextFundingTime.Add(bybitMaxRollWait)) {
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
				c.appendControllerLog("Bybit 续持记账 %s:已收 %d 次资金费,翻进 %s 结算(上限 %d 次;去留在结算前复核)",
					p.Symbol, rolled.SettlementsHeld, snap.NextFundingTime.UTC().Format("15:04:05Z"), maxHold)
				continue
			}
		}
		// 非滚动 / 满次 / 永续腿不在场 / 记账超时 → 平仓(已收完本期资金费,settlements +1)。
		if !live {
			c.appendControllerLog("[bybit-shadow][拟平仓] %s(结算后到期)", p.Symbol)
			continue
		}
		settle := p.SettlementsHeld + 1
		if p.ForceClose {
			settle = p.SettlementsHeld // 待平残腿没有「收完本期资金费」一说,不虚增结算次数
		}
		c.bybitCloseAndSettle(ctx, store, executor, p, settle, now)
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

	// 2) 扫描规划(只读),在结算前窗口内开新仓。资金按「可开槽位」均分(而非候选数),
	// 预算扣掉在场持仓已占用的部分(现货名义 + 永续保证金),避免多槽位时双重承诺资金。
	lev := bybitEffectiveLeverage(cfg.BybitExecutor.PerpLeverage)
	deployedUSDT := 0.0
	for _, p := range open {
		deployedUSDT += p.SpotNotional + p.PerpNotional/float64(lev)
	}
	plan, err := planner.PlanCycle(ctx, maxConcurrent-len(open), cfg.AllocatedCapitalUSDT-deployedUSDT)
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
		// 开仓黑名单:此前被「不可交易类」错误(170037 未开通抵押设置等)拒绝过的标的,截止前不再尝试。
		if until, banned := c.bybitOpenSkipUntil[p.Symbol]; banned && now.Before(until) {
			continue
		}
		if isCrossVenueSymbolHeld(c.cfg.NetworkConfig.RecordDir, p.Symbol) {
			c.appendControllerLog("Bybit 跳过 %s:跨所引擎在场持仓(同账户同币会互相净仓),本窗口让位", p.Symbol)
			continue
		}
		// 仅在 [结算-lead, 结算) 窗口内开仓。
		if p.NextFundingTime.IsZero() || now.Before(p.NextFundingTime.Add(-lead)) || !now.Before(p.NextFundingTime) {
			continue
		}
		if capN := bybitEffectivePositionNotionalCap(cfg); capN > 0 && (p.SpotNotional+p.PerpNotional) > capN {
			c.appendControllerLog("Bybit 跳过 %s:两腿名义 %.2f 超单标的上限 %.2f", p.Symbol, p.SpotNotional+p.PerpNotional, capN)
			continue
		}
		// 真实余额闸:正 carry 需要 现货名义 + 永续保证金(名义/杠杆;计划缺失时保守按 1x 全额计);
		// 负 carry 现货腿是借币卖出(反而收 USDT),只需永续保证金。
		perpMargin := p.PerpMarginUSDT
		if perpMargin <= 0 {
			perpMargin = p.PerpNotional
		}
		requiredUSDT := p.SpotNotional + perpMargin
		if p.Negative {
			requiredUSDT = perpMargin
		}
		if requiredUSDT > availUSDT {
			c.appendControllerLog("Bybit 跳过 %s:开仓需要 ≈%.2f USDT,统一账户可用仅 %.2f(配置分配与真实余额脱节)", p.Symbol, requiredUSDT, availUSDT)
			continue
		}
		// 负 carry 借币预检(R29 Bybit 版):开仓要借 base 卖出,先查「可借+可卖」总量(只读,零下单)。
		// 不足则本结算窗口拉黑——绝不先开腿撞 170207 再回补白烧手续费(2026-06-12 HOME 实锤:两个整点
		// 窗口各重试 3 次,还留过一次 84U 裸腿)。查询失败/能力缺失时放行(advisory,不误杀;执行器
		// 现货腿先行兜底,失败也零敞口)。
		if live && p.Negative {
			if bq, ok := adapter.(bybitBorrowQuotaSource); ok {
				if maxSellable, qerr := bq.MaxBorrowable(ctx, baseAssetOf(string(p.Symbol))); qerr == nil && maxSellable < p.BaseQty {
					c.bybitBanOpen(p.Symbol, p.NextFundingTime)
					c.appendControllerLog("Bybit 借币预检拦截 %s:可借+可卖 ≈%.6f < 需借卖出 %.6f,本结算窗口跳过(未下任何单)", p.Symbol, maxSellable, p.BaseQty)
					continue
				} else if qerr != nil {
					c.appendControllerLog("Bybit 借币预检查询失败 %s(放行,由现货先行腿兜底): %v", p.Symbol, qerr)
				}
			}
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
				pos.ForceClose = true // 不续持,直接平
				pos.NextFundingTime = now.Add(-time.Hour)
				if c.bybitPersistPosition(store, pos, "开仓裸腿") {
					c.appendControllerLog("Bybit 开仓异常留有永续敞口 %s(≈%.6f),已落盘待平", pos.Symbol, pos.BaseQty)
				}
			}
			switch {
			case isBybitBorrowUnavailableError(openErr):
				// 借币池借不到:同结算窗口内绝不再试(每次重试=白下单;旧版还会白开永续又回补)。
				until := now.Add(bybitBorrowUnavailableBan)
				if p.NextFundingTime.After(until) {
					until = p.NextFundingTime
				}
				c.bybitBanOpen(p.Symbol, until)
				c.appendControllerLog("Bybit 拉黑开仓 %s 至 %s:借币池借不到(170207 类),期间不再重试", p.Symbol, until.UTC().Format("15:04:05Z"))
			case isBybitUntradableOpenError(openErr):
				c.bybitBanOpen(p.Symbol, now.Add(bybitOpenBanDuration))
				c.appendControllerLog("Bybit 拉黑开仓 %s %s:交易所判定不可交易(%v),期间不再尝试", p.Symbol, bybitOpenBanDuration, openErr)
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

// bybitNotifyTrade 推送 Bybit 交易执行通知(开仓/平仓):info 级,不受 MinSeverity 过滤、不去重,
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
