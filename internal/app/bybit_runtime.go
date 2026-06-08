package app

import (
	"context"
	"fmt"
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
		AllocatedUSDT:    cfg.AllocatedCapitalUSDT,
		TopN:             topN,
		MinFundingRate:   bx.MinFundingRate,
		MinNetProfitRate: bx.MinNetProfitRate,
		SpotTakerFee:     cfg.HistoricalConfig.SpotTakerFeeRate,
		PerpTakerFee:     cfg.HistoricalConfig.FuturesTakerFeeRate,
	}
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
		c.bybitManageCycle(ctx, cfg, store, planner, executor, live)
		select {
		case <-ctx.Done():
			c.appendControllerLog("Bybit 执行器已停止")
			c.bybitNotifyTrade("Bybit 执行器已停止", "任务已手动停止", "")
			return "Bybit 执行器已停止", ctx.Err()
		case <-ticker.C:
		}
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
			// 永续已平。但若现货 base 仍有余额(典型:历史平仓腿因「按名义卖出 > 真实余额」失败而残留),
			// 保留持仓 → 交由 manage 循环用修复后的 CloseDeltaNeutral 按真实余额把裸现货腿平掉,避免孤儿敞口。
			if spotBal := bybitSpotFreeBalance(bals, p.Symbol); spotBal > executionQtyTolerance {
				c.appendControllerLog("Bybit reconcile:%s 永续已平但现货仍有余额≈%.6f(裸现货腿),保留持仓待 manage 按真实余额平掉", p.Symbol, spotBal)
				continue
			}
			c.appendControllerLog("Bybit reconcile:%s 交易所无永续持仓且现货无余额,清除落盘记录", p.Symbol)
			_ = store.remove(p.Symbol)
			continue
		}
		c.appendControllerLog("Bybit reconcile:%s 恢复持仓监控(永续≈%.6f),将按结算时间平仓", p.Symbol, actualBySym[p.Symbol])
	}
	// 孤儿仓兜底:交易所有永续持仓但本地无台账(开仓回补失败/落盘失败遗留)。出于「账户可能有其他人工持仓」的
	// 保守考虑,这里只告警提示人工核对,不自动平仓(避免误平无关持仓);本机产生的裸腿在开仓失败分支已落盘待平。
	for sym, qty := range actualBySym {
		if qty <= executionQtyTolerance || tracked[sym] {
			continue
		}
		c.appendControllerLog("Bybit reconcile:发现未登记的永续持仓 %s(≈%.6f),疑似开仓异常/落盘失败遗留,请人工核对", sym, qty)
		c.bybitNotifyWarn("Bybit 发现未登记持仓", fmt.Sprintf("%s 永续≈%.6f 无本地台账,疑似开仓异常遗留,请人工核对", sym, qty), string(sym))
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
			case !ok || snap.LastFundingRate <= 0:
				// 与币安滚动持有同口径:开仓后往返手续费已是沉没成本,再多持有一个结算「不额外收费」,故只要下期资金费仍为正
				// (边际净 carry>0)就续持收下一次;直到费率转 ≤0(继续持有会倒贴)才平。不再用开仓地板 MinFundingRate 当续持门槛。
				c.appendControllerLog("Bybit 续持复核未通过 %s(下期费率≤0/转负,继续持有会倒贴),平仓", p.Symbol)
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
				if upErr := store.upsert(rolled); upErr != nil {
					c.appendControllerLog("Bybit 续持落盘失败 %s(本轮按平仓处理): %v", p.Symbol, upErr)
				} else {
					c.appendControllerLog("Bybit 续持 %s:已收 %d 次资金费,下期费率=%.4f%% 续持到 %s(上限 %d 次)",
						p.Symbol, rolled.SettlementsHeld, snap.LastFundingRate*100, snap.NextFundingTime.UTC().Format("15:04:05Z"), maxHold)
					c.bybitNotifyTrade("Bybit 续持", fmt.Sprintf("已收 %d 次资金费,下期费率=%.4f%%,续持到 %s",
						rolled.SettlementsHeld, snap.LastFundingRate*100, snap.NextFundingTime.UTC().Format("15:04:05Z")), string(p.Symbol))
					continue
				}
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
		settlements := p.SettlementsHeld + 1
		estFunding := float64(settlements) * p.FundingRate * p.PerpNotional
		closeDetail := fmt.Sprintf(
			"%s 已平仓(持有 %d 次结算各收 1 次资金费)\n开仓实扣费: 永续 %.6f %s + 现货 %.6f %s\n平仓实扣费: 永续 %.6f %s + 现货 %.6f %s\n名义≈%.2f USDT | 估算资金费收入≈%.4f USDT(%d×%.4f%%×名义)\n开仓均价 永续%.6f/现货%.6f → 平仓均价 永续%.6f/现货%.6f",
			p.Symbol, settlements,
			p.OpenPerpFee, p.OpenPerpFeeAsset, p.OpenSpotFee, p.OpenSpotFeeAsset,
			closeRes.PerpCloseFee, closeRes.PerpFeeAsset, closeRes.SpotCloseFee, closeRes.SpotFeeAsset,
			p.PerpNotional, estFunding, settlements, p.FundingRate*100,
			p.PerpEntryPrice, p.SpotEntryPrice, closeRes.PerpClosePrice, closeRes.SpotClosePrice)
		c.appendControllerLog("Bybit 已平仓 %s", closeDetail)
		c.bybitNotifyTrade("Bybit 已平仓", closeDetail, string(p.Symbol))
	}

	// 重新读取在场持仓(平仓可能已删除若干)。
	open, err = store.load()
	if err != nil {
		c.appendControllerLog("Bybit 重载持仓失败: %v", err)
		return
	}
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
				if upErr := store.upsert(pos); upErr != nil {
					c.appendControllerLog("Bybit 开仓裸腿落盘失败 %s(务必人工核对!): %v", pos.Symbol, upErr)
				} else {
					c.appendControllerLog("Bybit 开仓异常留有永续敞口 %s(≈%.6f),已落盘待平", pos.Symbol, pos.BaseQty)
				}
			}
			c.appendControllerLog("Bybit 开仓失败 %s: %v", p.Symbol, openErr)
			c.bybitNotifyWarn("Bybit 开仓失败", openErr.Error(), string(p.Symbol))
			continue
		}
		if upErr := store.upsert(pos); upErr != nil {
			c.appendControllerLog("Bybit 持仓落盘失败(已开仓,务必人工核对!) %s: %v", pos.Symbol, upErr)
		}
		openDetail := fmt.Sprintf(
			"%s 已开仓 delta-neutral\n永续空 %.6f@%.6f(名义 %.2f USDT,实扣费 %.6f %s)\n现货多 %.6f@%.6f(名义 %.2f USDT,实扣费 %.6f %s)\n预估费率 %.4f%% | 预估净利率 %.4f%% | 结算 %s",
			pos.Symbol,
			pos.BaseQty, pos.PerpEntryPrice, pos.PerpNotional, pos.OpenPerpFee, pos.OpenPerpFeeAsset,
			pos.BaseQty, pos.SpotEntryPrice, pos.SpotNotional, pos.OpenSpotFee, pos.OpenSpotFeeAsset,
			p.FundingRate*100, p.EstNetProfitRate*100, pos.NextFundingTime.UTC().Format("15:04:05Z"))
		c.appendControllerLog("Bybit %s", openDetail)
		c.bybitNotifyTrade("Bybit 已开仓", openDetail, string(p.Symbol))
		held[pos.Symbol] = true
		open = append(open, pos)
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
		c.appendControllerLog("[bybit] 本轮无正费率候选(需≥%.4f%%);在场 %d/%d", cfg.BybitExecutor.MinFundingRate*100, openCount, maxConcurrent)
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
