package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

// runtime_capital_venue.go 让面板「调整仓位」支持非币安交易所:复用同一套 HTTP 路由(?venue=)、控制令牌与
// per-venue Controller,但资金快照/调仓改走 venue.CapitalManagement 适配器(Bybit:统一交易账户↔资金账户)。
//
// 设计铁律:币安路径(runtime_capital_adjustment.go)逐字节不变;这里只服务 venueName != "" / != "binance" 的所。
// UTA 模型无 25/25/50 钱包预切,「输入总额→落位」= 把交易总额放进统一交易账户、余量留资金账户(现货/合约 50/50 下单时分)。

// isNonBinanceVenue 判断本控制器是否归属非币安所(需走适配器资金路径)。
func (c *RuntimeController) isNonBinanceVenue() bool {
	name := strings.ToLower(strings.TrimSpace(c.venueName))
	return name != "" && name != binance.VenueName
}

// venueCapitalManagerFactory 是构造资金管理适配器的可注入入口(测试可替换为 fake,避免触网/真实划转)。
var venueCapitalManagerFactory = buildVenueCapitalManager

// buildVenueCapitalManager 按 venue 名加载凭证并构造适配器(只用于资金管理:只读快照 + 所内划转)。
func buildVenueCapitalManager(venueName, secretPath string) (venue.CapitalManagement, venue.Capabilities, error) {
	sec, err := LoadVenueSecretConfig(secretPath, venueName)
	if err != nil {
		return nil, venue.Capabilities{}, err
	}
	switch strings.ToLower(strings.TrimSpace(venueName)) {
	case bybit.VenueName:
		adp, err := bybit.New(venue.Config{Name: bybit.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret})
		if err != nil {
			return nil, venue.Capabilities{}, err
		}
		return adp, adp.Capabilities(), nil
	default:
		return nil, venue.Capabilities{}, fmt.Errorf("venue %q 暂不支持面板资金管理", venueName)
	}
}

// venueSnapshotToRuntime 把 venue 中性钱包快照 + 目标计划映射成面板用 RuntimeCapitalSnapshot(填 Wallets[] 动态卡片)。
// 余量钱包(plan.RemainderWallet)的目标 = 总额 − 各目标和(非负)。
func venueSnapshotToRuntime(snaps []venue.WalletSnapshot, total float64, plan venue.CapitalAllocationPlan) RuntimeCapitalSnapshot {
	sumTargets := 0.0
	for _, amt := range plan.Targets {
		sumTargets += amt
	}
	remainderDesired := math.Max(total-sumTargets, 0)

	out := RuntimeCapitalSnapshot{TotalMovableUSDT: total}
	for _, s := range snaps {
		desired := plan.Targets[s.Wallet]
		if s.Wallet == plan.RemainderWallet {
			desired = remainderDesired
		}
		out.Wallets = append(out.Wallets, RuntimeWalletBalance{
			Wallet:      string(s.Wallet),
			Label:       s.Label,
			FreeUSDT:    s.FreeUSDT,
			DesiredUSDT: desired,
		})
		// 兼容旧字段:资金账户映射到 FundingFreeUSDT,便于既有汇总/展示逻辑。
		if s.Wallet == bybit.WalletFunding {
			out.FundingFreeUSDT = s.FreeUSDT
			out.DesiredFundingUSDT = desired
		}
	}
	return out
}

// currentVenueCapitalSnapshot 拉某非币安所的只读资金快照(带 15s/60s 缓存,避免面板 5s 轮询频繁触网)。
func (c *RuntimeController) currentVenueCapitalSnapshot() (RuntimeCapitalSnapshot, error) {
	c.mu.Lock()
	secretPath := strings.TrimSpace(c.status.SecretPath)
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	cfg := c.cfg
	cachedSnapshot := c.cachedCapitalSnapshot
	cachedAt := c.cachedCapitalSnapshotAt
	cachedSecretKey := c.cachedCapitalSnapshotSecretKey
	c.mu.Unlock()

	target := configuredTradingCapitalUSDT(cfg)
	cacheKey := "venue|" + c.venueName + "|" + secretPath + "|" + fmt.Sprintf("%.2f", target)
	if cachedSecretKey == cacheKey && !cachedAt.IsZero() && time.Since(cachedAt) <= runtimeCapitalSnapshotCacheTTL {
		return cachedSnapshot, nil
	}

	mgr, caps, err := venueCapitalManagerFactory(c.venueName, secretPath)
	if err != nil {
		return RuntimeCapitalSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(c.rootCtx, 10*time.Second)
	defer cancel()
	snaps, total, err := mgr.CaptureMovableSnapshot(ctx)
	if err != nil {
		// 触网失败时回退到 stale 缓存(若有),避免面板瞬时报错。
		if cachedSecretKey == cacheKey && !cachedAt.IsZero() && time.Since(cachedAt) <= runtimeCapitalSnapshotStaleTTL {
			return cachedSnapshot, nil
		}
		return RuntimeCapitalSnapshot{}, err
	}
	plan := mgr.PlanAllocation(math.Min(target, total), caps)
	snapshot := venueSnapshotToRuntime(snaps, total, plan)

	c.mu.Lock()
	c.cachedCapitalSnapshot = snapshot
	c.cachedCapitalSnapshotAt = time.Now()
	c.cachedCapitalSnapshotSecretKey = cacheKey
	c.mu.Unlock()
	return snapshot, nil
}

// adjustVenueTradingCapital 为某非币安所执行「输入总额→落位」:把交易总额放进交易钱包,余量留资金账户。
func (c *RuntimeController) adjustVenueTradingCapital(mode RuntimeMode, secretPath string, targetTradingUSDT float64) (RuntimeCapitalAdjustmentResult, error) {
	if mode == RuntimeModeMonthly {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("多所暂不支持模拟(monthly)调仓,请用实盘模式")
	}
	c.mu.Lock()
	if c.status.Running {
		c.mu.Unlock()
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("该所后台任务运行中,不能调整真实资金仓位")
	}
	c.mu.Unlock()

	mgr, caps, err := venueCapitalManagerFactory(c.venueName, secretPath)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, err
	}
	ctx, cancel := context.WithTimeout(c.rootCtx, runtimeCapitalAdjustTimeout)
	defer cancel()

	// 调仓第一步:全部平掉 + 清币归一(按用户要求只留 MNT 付手续费):
	//   1) 台账在场持仓 → 正规平仓(两腿一起平 + 写结算流水 + 删台账);
	//   2) 存量永续孤儿 → 市价 reduceOnly 平掉;
	//   3) 统一账户非白名单杂币 → 现货市价清成 USDT。
	// 任一平仓失败立即中止(绝不在有活腿时清现货)。适配器不支持交易接口时跳过。
	var sweepReport []string
	if sweeper, ok := mgr.(bybitSweepVenue); ok {
		if trader, isTrader := mgr.(bybitTrader); isTrader {
			closeReport, cerr := c.closeBybitLedgerPositionsForSweep(ctx, trader)
			if cerr != nil {
				return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("调仓前平仓失败: %w", cerr)
			}
			sweepReport = append(sweepReport, closeReport...)
		}
		moreReport, serr := sweepBybitResidualAssetsToUSDT(ctx, sweeper, c.cfg.NetworkConfig.RecordDir, func(format string, args ...any) {
			c.appendControllerLog("[调仓] "+format, args...)
		})
		if serr != nil {
			return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("调仓前清币失败: %w", serr)
		}
		sweepReport = append(sweepReport, moreReport...)
	}

	beforeSnaps, totalBefore, err := mgr.CaptureMovableSnapshot(ctx)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("读取调仓前快照失败: %w", err)
	}
	if totalBefore <= executionQtyTolerance {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("该所所有账户都没有可调的 USDT 余额")
	}
	if totalBefore+executionQtyTolerance < targetTradingUSDT {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("可调 USDT 不足:目标=%.2f,可调=%.2f", targetTradingUSDT, totalBefore)
	}

	plan := mgr.PlanAllocation(targetTradingUSDT, caps)
	before := venueSnapshotToRuntime(beforeSnaps, totalBefore, plan)

	transfers, err := mgr.ExecuteRebalance(ctx, plan)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("执行划转失败: %w", err)
	}

	afterSnaps, totalAfter, err := mgr.CaptureMovableSnapshot(ctx)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("读取调仓后快照失败: %w", err)
	}
	after := venueSnapshotToRuntime(afterSnaps, totalAfter, plan)

	rtTransfers := make([]RuntimeCapitalTransfer, 0, len(transfers))
	for _, tr := range transfers {
		rtTransfers = append(rtTransfers, RuntimeCapitalTransfer{
			From:         string(tr.From),
			To:           string(tr.To),
			TransferType: tr.VenueRef,
			AmountUSDT:   tr.AssetUSDT,
		})
	}

	tradingWallet := tradingTargetWallet(plan)
	message := fmt.Sprintf("[%s] 目标交易仓位已调整为 %.2f USDT:%s=%.2f,余量留资金账户=%.2f。%s",
		c.venueName, targetTradingUSDT, tradingWallet, plan.Targets[venue.Wallet(tradingWallet)],
		math.Max(totalAfter-targetTradingUSDT, 0), summarizeSweepReport(sweepReport))

	c.mu.Lock()
	applyTradingCapitalTargetToConfig(&c.cfg, targetTradingUSDT)
	c.status.SecretPath = secretPath
	c.status.LastMessage = message
	// 失效缓存,使下次快照即时反映新分配。
	c.cachedCapitalSnapshotAt = time.Time{}
	updatedConfig := runtimeRiskConfigFromEngineConfig(c.cfg)
	c.mu.Unlock()

	// 持久化分配资金覆盖:跨进程重启仍以调仓目标为准(否则进程重启回退 config.yaml 旧值)。
	if werr := writeVenueCapitalOverride(c.cfg.NetworkConfig.RecordDir, targetTradingUSDT); werr != nil {
		c.appendControllerLog("持久化分配资金覆盖失败(进程重启后将回退 config.yaml): %v", werr)
	}

	c.appendControllerTraceLog(c.currentRunID(), c.currentCycleID(), "%s", message)
	return RuntimeCapitalAdjustmentResult{
		RequestedTradingUSDT: targetTradingUSDT,
		AppliedTradingUSDT:   targetTradingUSDT,
		PerLegTradingUSDT:    targetTradingUSDT / 2,
		Before:               before,
		After:                after,
		Transfers:            rtTransfers,
		Message:              message,
		Config:               updatedConfig,
	}, nil
}

// tradingTargetWallet 返回计划里非余量钱包的标识(展示用);找不到则回退 "unified"。
func tradingTargetWallet(plan venue.CapitalAllocationPlan) string {
	for w := range plan.Targets {
		if w != plan.RemainderWallet {
			return string(w)
		}
	}
	return string(bybit.WalletUnified)
}
