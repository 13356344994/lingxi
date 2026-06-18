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
// 进程已死、且本地未平仓台账非空时)或人工应急触发,覆盖:
//   - legacy 币安资费裸仓: records/live_open_hedges.json → panicFlattenManagedExposure
//   - 五类策略持仓台账: crossvenue / single_venue_spot_perp / cross_venue_spot_perp /
//     single_venue_perp_spot / cross_venue_perp_spot 的 positions.json → 各执行器 CloseAllPositions
//
// 用法: lingxi flatten [reason] [shadow]
//   reason  可选,写入告警文案区分触发来源(默认「外部看门狗/人工触发」)。
//   shadow  可选,置为 "shadow"/"dry-run" 时只预演不真正下单(signedRequest 咽喉点拦截),用于演练。
//
// 安全前提:必须在确认主进程**确已退出**后触发,避免与存活主进程并发平仓。外部看门狗应先判活(心跳文件年龄
// + /healthz)再触发;单写租约(S7-A)亦保证不会与新拉起的实例并发动用同一账户。
// 未覆盖:各 venue 子目录 bybit_positions.json / funding_positions.json 等单所专用台账(见 watchdog 注释)。

const (
	defaultFlattenReason          = "外部看门狗/人工触发"
	emergencyFlattenCoverageScope = "legacy live_open_hedges + 五类策略 positions.json(cross_venue_perp_perp/single_venue_spot_perp/cross_venue_spot_perp/single_venue_perp_spot/cross_venue_perp_spot)"
)

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

// emergencyFlatten 执行一次性的应急强平:安全互锁(单写租约)→ 轻量准备 → legacy 裸仓强平 → 五策略 CloseAllPositions。
// 不调用完整 initialize():应急场景下不应被风控自检/熔断态/成本探针阻断,只取强平所需的最小依赖。
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
	strategyActive, strategyByName, serr := countActiveStrategyPositions(e.cfg.NetworkConfig.RecordDir)
	if serr != nil {
		return fmt.Errorf("统计五类策略在场仓失败: %w", serr)
	}
	hasLegacy := len(hedges) > 0
	hasStrategy := strategyActive > 0
	if !hasLegacy && !hasStrategy {
		msg := fmt.Sprintf("覆盖范围: %s;均未发现在场仓(交易所侧 dead-man 已兜底未成交挂单)", emergencyFlattenCoverageScope)
		e.logger.Printf("[flatten] %s", msg)
		e.notifyLifecycle(EventLevelWarning, "紧急强平:无在场仓", msg)
		return nil
	}

	var exitErr error
	live := !dryRun
	startParts := []string{fmt.Sprintf("触发=%s", reason), fmt.Sprintf("覆盖=%s", emergencyFlattenCoverageScope)}
	if hasLegacy {
		startParts = append(startParts, fmt.Sprintf("legacy裸仓=%d", len(hedges)))
	}
	if hasStrategy {
		startParts = append(startParts, fmt.Sprintf("策略在场=%d(%s)", strategyActive, formatStrategyActiveCounts(strategyByName)))
	}
	startDetail := strings.Join(startParts, " ")
	e.logger.Printf("[flatten] 开始紧急强平: %s", startDetail)
	e.notifyLifecycle(EventLevelCritical, "紧急强平已触发", startDetail)

	if hasLegacy {
		if lerr := e.emergencyFlattenLegacyHedges(hedges, reason); lerr != nil {
			exitErr = lerr
		}
	}

	secretPath := defaultSecretPath()
	deps := buildEmergencyStrategyFlattenDeps(e.cfg, secretPath, e.logger)
	stratResp := flattenStrategyPositionsReport(ctx, deps, live)
	for _, result := range stratResp.Results {
		if result.Skipped {
			e.logger.Printf("[flatten] 策略 %s 跳过: %s", result.Strategy, result.Message)
			continue
		}
		if result.Closed > 0 || len(result.Errors) > 0 || result.Message != "" {
			e.logger.Printf("[flatten] 策略 %s: closed=%d errs=%d msg=%s", result.Strategy, result.Closed, len(result.Errors), result.Message)
		}
		for _, errMsg := range result.Errors {
			if exitErr == nil {
				exitErr = fmt.Errorf("%s: %s", result.Strategy, errMsg)
			} else {
				exitErr = fmt.Errorf("%w; %s: %s", exitErr, result.Strategy, errMsg)
			}
		}
	}

	doneParts := []string{fmt.Sprintf("覆盖=%s", emergencyFlattenCoverageScope)}
	if hasLegacy {
		remaining := e.snapshotActiveOpenHedges()
		doneParts = append(doneParts, fmt.Sprintf("legacy已收敛=%d 残留=%d", len(hedges)-len(remaining), len(remaining)))
		if len(remaining) > 0 && exitErr == nil {
			exitErr = fmt.Errorf("legacy 紧急强平后仍有 %d 笔裸仓未收敛,需人工介入", len(remaining))
		}
	}
	doneParts = append(doneParts, fmt.Sprintf("五策略已平=%d", stratResp.TotalClosed))
	doneDetail := strings.Join(doneParts, "; ") + "。请人工到交易所二次核对各腿持仓与借贷已清零。"
	level := EventLevelCritical
	if exitErr != nil {
		doneDetail = "⚠ 部分仓位未能自动收敛,需立即人工介入! " + doneDetail
	}
	e.logger.Printf("[flatten] 紧急强平完成: %s", doneDetail)
	e.notifyLifecycle(level, "紧急强平完成", doneDetail)
	return exitErr
}

// emergencyFlattenLegacyHedges 对 legacy live_open_hedges 台账执行方向感知双腿强平。
func (e *FundingRateSniperEngine) emergencyFlattenLegacyHedges(hedges []OpenHedge, reason string) error {
	e.setCurrentPositionsFromHedges(hedges)
	labels := make([]string, 0, len(hedges))
	for _, h := range hedges {
		labels = append(labels, fmt.Sprintf("%s/%s", h.Target.Symbol, h.carryDirection().positionLabel(h.Target.SpotBaseAsset, h.Target.Symbol)))
	}
	e.logger.Printf("[flatten] legacy 裸仓 %d 笔: %s", len(hedges), strings.Join(labels, ", "))
	e.panicFlattenManagedExposure(fmt.Sprintf("emergency-flatten CLI legacy: %s", reason))
	remaining := e.snapshotActiveOpenHedges()
	if len(remaining) > 0 {
		return fmt.Errorf("legacy 紧急强平后仍有 %d 笔裸仓未收敛,需人工介入", len(remaining))
	}
	return nil
}

// buildEmergencyStrategyFlattenDeps 在 CLI 进程内装配五策略执行器(仅 flatten,不启动循环)。
// orch=nil 时 venue 适配器走 buildCrossVenueReadOnlyAdapter(secret.yaml),与 startLiveStack 冷启动一致。
func buildEmergencyStrategyFlattenDeps(cfg EngineConfig, secretPath string, logger *log.Logger) strategyFlattenDeps {
	if logger == nil {
		logger = log.Default()
	}
	if secretPath == "" {
		secretPath = defaultSecretPath()
	}
	rotVenues := buildCrossVenueExecutorAdapters(nil, cfg, secretPath, logger)
	rd := cfg.NetworkConfig.RecordDir
	var deps strategyFlattenDeps
	crossExec, err := NewCrossVenueExecutor(cfg, nil, nil, secretPath, logger)
	if err != nil {
		logger.Printf("[flatten] 跨所执行器构造失败,跳过 cross_venue_perp_perp: %v", err)
	} else {
		deps.CrossVenue = crossExec
	}
	deps.SVSP = NewSingleVenueSpotPerpExecutor(cfg, rotVenues, rd, logger)
	deps.CVSP = NewCrossVenueSpotPerpExecutor(cfg, rotVenues, rd, logger)
	deps.SVPS = NewSingleVenuePerpSpotExecutor(cfg, rotVenues, rd, logger)
	deps.CVPS = NewCrossVenuePerpSpotExecutor(cfg, rotVenues, rd, logger)
	return deps
}

// countActiveStrategyPositions 统计五类策略 positions.json 中的非 closed 持仓数。
func countActiveStrategyPositions(recordDir string) (total int, byStrategy map[string]int, err error) {
	byStrategy = make(map[string]int)
	count := func(name string, n int) {
		if n > 0 {
			byStrategy[name] = n
			total += n
		}
	}
	cvStore, err := newCrossVenuePositionStore(recordDir)
	if err != nil {
		return 0, nil, err
	}
	if ps, lerr := cvStore.load(); lerr != nil {
		return 0, nil, lerr
	} else {
		count("cross_venue_perp_perp", len(activeCrossVenuePositions(ps)))
	}
	svspStore, err := newSingleVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return 0, nil, err
	}
	if ps, lerr := svspStore.load(); lerr != nil {
		return 0, nil, lerr
	} else {
		count("single_venue_spot_perp", len(activeSingleVenueSpotPerpPositions(ps)))
	}
	cvspStore, err := newCrossVenueSpotPerpPositionStore(recordDir)
	if err != nil {
		return 0, nil, err
	}
	if ps, lerr := cvspStore.load(); lerr != nil {
		return 0, nil, lerr
	} else {
		count("cross_venue_spot_perp", len(activeCrossVenueSpotPerpPositions(ps)))
	}
	svpsStore, err := newSingleVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return 0, nil, err
	}
	if ps, lerr := svpsStore.load(); lerr != nil {
		return 0, nil, lerr
	} else {
		count("single_venue_perp_spot", len(activeSingleVenuePerpSpotPositions(ps)))
	}
	cvpsStore, err := newCrossVenuePerpSpotPositionStore(recordDir)
	if err != nil {
		return 0, nil, err
	}
	if ps, lerr := cvpsStore.load(); lerr != nil {
		return 0, nil, lerr
	} else {
		count("cross_venue_perp_spot", len(activeCrossVenuePerpSpotPositions(ps)))
	}
	return total, byStrategy, nil
}

func formatStrategyActiveCounts(byStrategy map[string]int) string {
	if len(byStrategy) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(byStrategy))
	for name, n := range byStrategy {
		parts = append(parts, fmt.Sprintf("%s=%d", name, n))
	}
	return strings.Join(parts, ",")
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
