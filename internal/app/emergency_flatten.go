package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
)

// emergency_flatten.go 补全 S7-B「dead-man 兜底」的盲区:交易所侧 dead-man(countdownCancelAll)只能在进程
// 崩溃/失联后撤掉**未成交挂单**,而**已成交的对冲腿(裸仓)**无法靠它平掉。本子命令供外部看门狗(确认主
// 进程已死、且本地未平仓台账非空时)或人工应急触发:从落盘的未平仓台账(SSOT)重建活跃对冲,复用方向感知的
// 双腿强平(panicFlattenManagedExposure)把现货/合约两腿一并收敛(正 carry 卖现货、负 carry 全仓杠杆买回还币),
// 再核对台账残留。幂等、有界、全程告警(含 Telegram 生命周期通知)。
//
// 用法: lingxi flatten [reason] [shadow]
//   reason  可选,写入告警文案区分触发来源(默认「外部看门狗/人工触发」)。
//   shadow  可选,置为 "shadow"/"dry-run" 时只预演不真正下单(signedRequest 咽喉点拦截),用于演练。
//
// 安全前提:必须在确认主进程**确已退出**后触发,避免与存活主进程并发平仓。外部看门狗应先判活(心跳文件年龄
// + /healthz)再触发;单写租约(S7-A)亦保证不会与新拉起的实例并发动用同一账户。

const defaultFlattenReason = "外部看门狗/人工触发"

// runEmergencyFlatten 是 flatten / emergency-flatten 子命令入口:加载配置、构建引擎(真实客户端)、执行应急强平。
func runEmergencyFlatten(ctx context.Context) error {
	cfg, err := loadEngineConfig()
	if err != nil {
		return fmt.Errorf("加载主配置失败: %w", err)
	}
	reason := defaultFlattenReason
	dryRun := false
	for _, arg := range os.Args[2:] {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
			// 跳过空参
		case "shadow", "dry-run", "--dry-run":
			dryRun = true
		default:
			reason = strings.TrimSpace(arg)
		}
	}
	if dryRun {
		cfg.ExecutionConfig.ShadowTrading = true
		log.Printf("[flatten] SHADOW 预演:决策照常、任何真实下单将在咽喉点被拦截,不会触达交易所")
	}
	engine, err := NewFundingRateSniperEngineWithConfig("secret.yaml", cfg)
	if err != nil {
		return fmt.Errorf("构建引擎失败: %w", err)
	}
	return engine.emergencyFlatten(ctx, reason, dryRun)
}

// emergencyFlatten 执行一次性的应急强平:安全互锁(单写租约)→ 轻量准备(时间同步 + 精度元数据)→ 从台账重建裸仓 →
// 方向感知双腿强平 → 核对残留。不调用完整 initialize():应急场景下不应被风控自检/熔断态/成本探针阻断,只取强平所需的最小依赖。
func (e *FundingRateSniperEngine) emergencyFlatten(ctx context.Context, reason string, dryRun bool) error {
	e.runCtx = ctx
	// 安全互锁:真实强平前抢单写租约(S7-A)。若主进程仍存活(持有 flock),这里会失败 → 拒绝强平,避免与存活进程
	// 并发平仓(双平/撞单)。主进程确已退出时 flock 由内核自动释放,抢锁成功后再执行,期间持锁、结束释放。
	// 预演(shadow)不下真实单、无并发风险,跳过互锁以便在主进程存活时也能预览将强平的裸仓。
	if !dryRun {
		lease, leaseErr := acquireSingleWriterLease(e.cfg.NetworkConfig.RecordDir)
		if leaseErr != nil {
			detail := fmt.Sprintf("拒绝强平:疑似主进程仍存活并持有单写租约,请先确认主进程已退出再触发 flatten。底层: %v", leaseErr)
			e.logger.Printf("[flatten] %s", detail)
			e.notifyLifecycle(EventLevelCritical, "紧急强平被拒绝(主进程疑似存活)", detail)
			return fmt.Errorf("获取单写租约失败,拒绝并发强平: %w", leaseErr)
		}
		defer func() { _ = lease.Release() }()
	}
	if err := e.prepareForEmergencyFlatten(ctx); err != nil {
		return fmt.Errorf("紧急强平准备(时间同步/精度元数据)失败: %w", err)
	}
	hedges, err := e.recoverActiveOpenHedgesForFlatten()
	if err != nil {
		return fmt.Errorf("从台账恢复未平仓对冲失败: %w", err)
	}
	if len(hedges) == 0 {
		msg := "未平仓台账为空,无裸仓需收敛(交易所侧 dead-man 已兜底未成交挂单)"
		e.logger.Printf("[flatten] %s", msg)
		e.notifyLifecycle(EventLevelWarning, "紧急强平:无裸仓", msg)
		return nil
	}
	e.setCurrentPositionsFromHedges(hedges)

	labels := make([]string, 0, len(hedges))
	for _, h := range hedges {
		labels = append(labels, fmt.Sprintf("%s/%s", h.Target.Symbol, h.carryDirection().positionLabel(h.Target.SpotBaseAsset, h.Target.Symbol)))
	}
	startDetail := fmt.Sprintf("触发=%s 裸仓=%d 笔: %s", reason, len(hedges), strings.Join(labels, ", "))
	e.logger.Printf("[flatten] 开始紧急强平: %s", startDetail)
	e.notifyLifecycle(EventLevelCritical, "紧急强平已触发", startDetail)

	e.panicFlattenManagedExposure(fmt.Sprintf("emergency-flatten CLI: %s", reason))

	// 强平后核对:panicFlatten 对每笔成功收敛者已从 activeOpenHedges 移除并同步台账,残留即为未成功收敛者。
	remaining := e.snapshotActiveOpenHedges()
	doneDetail := fmt.Sprintf("已对 %d 笔裸仓执行方向感知双腿收敛;台账残留 %d 笔。请人工到交易所二次核对合约持仓与全仓借贷已清零。",
		len(hedges), len(remaining))
	level := EventLevelCritical
	if len(remaining) > 0 {
		doneDetail = "⚠ 部分裸仓未能自动收敛,需立即人工介入! " + doneDetail
	}
	e.logger.Printf("[flatten] 紧急强平完成: %s", doneDetail)
	e.notifyLifecycle(level, "紧急强平完成", doneDetail)
	if len(remaining) > 0 {
		return fmt.Errorf("紧急强平后仍有 %d 笔裸仓未收敛,需人工介入", len(remaining))
	}
	return nil
}

// prepareForEmergencyFlatten 只做强平所必需的最小准备:同步服务器时间偏移 + 拉取交易规则(现货/合约精度),
// 供平仓下单的数量/价格舍入使用。刻意不走 initialize() 的风控自检/成本探针/环境校验,避免应急场景被它们阻断。
func (e *FundingRateSniperEngine) prepareForEmergencyFlatten(ctx context.Context) error {
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return err
	}
	spotSymbols, futuresSymbols, err := e.client.FetchExchangeInfos(ctx)
	if err != nil {
		return err
	}
	for _, symbol := range spotSymbols {
		e.spotMeta[symbol.Symbol] = symbol
		e.spotRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}
	for _, symbol := range futuresSymbols {
		e.futuresMeta[symbol.Symbol] = symbol
		e.futuresRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}
	return nil
}

// recoverActiveOpenHedgesForFlatten 从落盘台账重建用于强平的 OpenHedge 列表。除生命周期骨架外,额外补上
// 负 carry 还币所需的 SpotBaseQty 与名义校验所需的参考现货价(MarkPrice),让方向感知强平拿到正确数量与价格。
func (e *FundingRateSniperEngine) recoverActiveOpenHedgesForFlatten() ([]OpenHedge, error) {
	entries, err := e.loadPersistedOpenHedges()
	if err != nil {
		return nil, err
	}
	hedges := make([]OpenHedge, 0, len(entries))
	for _, entry := range entries {
		hedge := rebuildOpenHedgeFromEntry(entry)
		if entry.SpotBaseQty > 0 {
			hedge.SpotBaseQty = formatFloat(entry.SpotBaseQty)
		}
		if entry.ReferenceSpotPrice > 0 {
			hedge.Target.MarkPrice = entry.ReferenceSpotPrice
		}
		hedges = append(hedges, hedge)
	}
	return hedges, nil
}
