package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue/binance"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载资金费结算、订单查询/撤销与终态解析、周期账户
// 快照采集、交易环境校验、合约执行参数设置与现货/合约/全仓杠杆账户状态与划转等结算/账户域逻辑
// （同包搬移、行为不变），向单文件 ≤ 800 行门禁靠拢。

func (e *FundingRateSniperEngine) expectedFundingIncomeSummary(hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingEligibleResidents map[string]struct{}) (float64, []string) {
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	expectedFundingIncome := 0.0
	alerts := make([]string, 0)
	for symbol := range symbolSet {
		// 滚动续持下的 drop(平掉的常驻仓)开仓前持仓本就≠0,其本周期资金费应正常归因——经 fundingEligibleResidents
		// 放行,避免被「开仓前已有持仓」闸误跳过(导致 PnL 低估 / R8 投影刹车过保守)。单周期路径传 nil → 闸不变。
		if _, eligible := fundingEligibleResidents[strings.ToUpper(strings.TrimSpace(symbol))]; !eligible {
			beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
			if math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
				alerts = append(alerts, fmt.Sprintf("资金费归因已跳过 %s: 开仓前已有持仓 positionAmt=%.10f", symbol, beforeQty))
				continue
			}
		}
		expectedFundingIncome += expectedFundingBySymbol[symbol]
	}
	return expectedFundingIncome, alerts
}

func (e *FundingRateSniperEngine) settleFundingIncome(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingTime time.Time) (float64, float64, []string) {
	startTime := fundingTime.Add(-1 * time.Minute)
	endTime := fundingTime.Add(2 * time.Minute)
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	totalFundingIncome := 0.0
	expectedFundingIncome := 0.0
	alerts := make([]string, 0)
	for symbol := range symbolSet {
		beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
		if math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
			alerts = append(alerts, fmt.Sprintf("资金费归因已跳过 %s: 开仓前已有持仓 positionAmt=%.10f", symbol, beforeQty))
			continue
		}

		income, err := e.fetchFundingIncome(ctx, startTime, endTime, symbol)
		if err != nil {
			alerts = append(alerts, fmt.Sprintf("资金费查询失败 %s: %v", symbol, err))
			continue
		}

		actualFundingIncome := 0.0
		for _, item := range income {
			if item.Symbol != symbol {
				continue
			}
			actualFundingIncome += parseFloat(item.Income)
		}

		totalFundingIncome += actualFundingIncome
		expectedFundingIncome += expectedFundingBySymbol[symbol]
		diff := actualFundingIncome - expectedFundingBySymbol[symbol]
		if math.Abs(diff) >= e.cfg.RiskConfig.FundingIncomeDiffAlertUSDT {
			alerts = append(alerts, fmt.Sprintf("资金费到账偏差过大 %s: 实收=%.6f 理论=%.6f 偏差=%.6f", symbol, actualFundingIncome, expectedFundingBySymbol[symbol], diff))
		}
	}

	return totalFundingIncome, expectedFundingIncome, alerts
}

func (e *FundingRateSniperEngine) effectiveFundingSettlementDelay() time.Duration {
	if e == nil || e.fundingSettlementDelay <= 0 {
		return defaultFundingSettlementDelay
	}
	return e.fundingSettlementDelay
}

func (e *FundingRateSniperEngine) scheduleAsyncFundingSettlement(base CycleSettlement, hedges []OpenHedge) {
	e.scheduleAsyncFundingSettlementWithRoll(base, hedges, nil)
}

// scheduleAsyncFundingSettlementWithRoll 与 scheduleAsyncFundingSettlement 相同,但额外标记 rollingHoldSymbols
// (滚动续持 keep 仓),供 worker 放行「开仓前已有持仓」的资金费归因(见 PendingFundingSettlementRecord.RollingHoldSymbols)。
// 单周期路径以 nil 调用上面的薄封装 → 行为逐字节不变。
func (e *FundingRateSniperEngine) scheduleAsyncFundingSettlementWithRoll(base CycleSettlement, hedges []OpenHedge, rollingHoldSymbols []string) {
	if e == nil || e.reportStore == nil {
		return
	}
	hedgeCopy := append([]OpenHedge(nil), hedges...)
	task := PendingFundingSettlementRecord{
		TraceRecordMetadata: base.TraceRecordMetadata.normalized(),
		RunID:               strings.TrimSpace(base.RunID),
		CycleID:             strings.TrimSpace(base.CycleID),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		NextAttemptAt:       time.Now().UTC().Add(e.effectiveFundingSettlementDelay()).Format(time.RFC3339),
		Settlement:          base,
		Hedges:              hedgeCopy,
		RollingHoldSymbols:  append([]string(nil), rollingHoldSymbols...),
	}
	if err := e.reportStore.UpsertPendingFundingSettlement(task); err != nil {
		if e.logger != nil {
			e.logger.Printf("持久化待结算资金费任务失败: %v", err)
		}
		return
	}
	e.startFundingSettlementWorker(context.Background())
	e.wakeFundingSettlementWorker()
}

func filterOutFundingPendingAlerts(alerts []string) []string {
	filtered := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		if strings.Contains(alert, "资金费异步结算待处理") {
			continue
		}
		filtered = append(filtered, alert)
	}
	return filtered
}

func (e *FundingRateSniperEngine) fetchFundingIncome(ctx context.Context, startTime time.Time, endTime time.Time, symbol string) ([]FundingIncomeRecord, error) {
	params := url.Values{}
	params.Set("incomeType", "FUNDING_FEE")
	params.Set("startTime", strconv.FormatInt(startTime.UnixMilli(), 10))
	params.Set("endTime", strconv.FormatInt(endTime.UnixMilli(), 10))
	params.Set("limit", "100")
	if strings.TrimSpace(symbol) != "" {
		params.Set("symbol", symbol)
	}

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+binance.PathFuturesIncome, params)
	if err != nil {
		return nil, err
	}

	var result []FundingIncomeRecord
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析资金费流水失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) queryFuturesOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+binance.PathFuturesOrder, params)
	if err != nil {
		return FuturesBatchOrderResult{}, err
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return FuturesBatchOrderResult{}, fmt.Errorf("解析合约订单查询回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) cancelFuturesOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (FuturesBatchOrderResult, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodDelete, e.client.FuturesBaseURL()+binance.PathFuturesOrder, params)
	if err != nil {
		return FuturesBatchOrderResult{}, err
	}

	var result FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return FuturesBatchOrderResult{}, fmt.Errorf("解析合约撤单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) querySpotOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathSpotOrder, params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货订单查询回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) cancelSpotOrderByClientID(ctx context.Context, symbol string, clientOrderID string) (SpotOrderResponse, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientOrderID)

	body, err := e.signedRequest(ctx, http.MethodDelete, e.client.SpotBaseURL()+binance.PathSpotOrder, params)
	if err != nil {
		return SpotOrderResponse{}, err
	}

	var result SpotOrderResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return SpotOrderResponse{}, fmt.Errorf("解析现货撤单回执失败: %w", err)
	}
	return result, nil
}

func (e *FundingRateSniperEngine) resolveFuturesOrderExecution(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) (OrderExecutionSnapshot, error) {
	if clientOrderID != "" {
		if snapshot, blindErr := e.waitForFuturesOrderWithBlindDeadline(ctx, symbol, clientOrderID); blindErr != nil {
			return OrderExecutionSnapshot{}, blindErr
		} else {
			if snapshot.HasConfirmedExecution() {
				return snapshot, nil
			}
		}
		e.noteOrderUncertainty(symbol, "futures_order_confirm_timeout", fmt.Sprintf("clientOrderId=%s", clientOrderID))
	}
	return resolveExecutionSnapshotWithPolling(
		ctx,
		symbol,
		clientOrderID,
		initial,
		semanticViewFromFuturesResult,
		func(queryCtx context.Context) (FuturesBatchOrderResult, error) {
			return e.queryFuturesOrderByClientID(queryCtx, symbol, clientOrderID)
		},
		func(reconcileCtx context.Context) (FuturesBatchOrderResult, error) {
			return e.reconcileFuturesOrderTerminalState(reconcileCtx, symbol, clientOrderID)
		},
		futuresResultToSnapshot,
		func(symbol string, clientOrderID string) error {
			return fmt.Errorf("未能确认合约订单真实成交数量, symbol=%s clientOrderId=%s", symbol, clientOrderID)
		},
	)
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshot(ctx context.Context, targets []*MonitoredTarget) (CycleAccountSnapshot, error) {
	pairs := make([]string, 0, len(targets))
	symbols := make([]string, 0, len(targets))
	for _, target := range targets {
		pairs = append(pairs, target.Candidate.Pair)
		symbols = append(symbols, target.Candidate.Symbol)
	}
	return e.captureCycleAccountSnapshotForSymbols(ctx, pairs, symbols, []string{"BNB"})
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshotFromHedges(ctx context.Context, hedges []OpenHedge) (CycleAccountSnapshot, error) {
	pairs := make([]string, 0, len(hedges))
	symbols := make([]string, 0, len(hedges))
	extraAssets := []string{"BNB"}
	seenAssets := map[string]struct{}{"BNB": {}}
	for _, hedge := range hedges {
		pairs = append(pairs, hedge.Target.Pair)
		symbols = append(symbols, hedge.Target.Symbol)
		for _, fill := range append([]SpotFill{}, hedge.SpotOrder.Fills...) {
			asset := strings.ToUpper(strings.TrimSpace(fill.CommissionAsset))
			if asset == "" {
				continue
			}
			if _, ok := seenAssets[asset]; ok {
				continue
			}
			seenAssets[asset] = struct{}{}
			extraAssets = append(extraAssets, asset)
		}
	}
	return e.captureCycleAccountSnapshotForSymbols(ctx, pairs, symbols, extraAssets)
}

func (e *FundingRateSniperEngine) captureCycleAccountSnapshotForSymbols(ctx context.Context, pairs []string, symbols []string, extraAssets []string) (CycleAccountSnapshot, error) {
	spotBalances, spotUSDT, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return CycleAccountSnapshot{}, err
	}

	futuresWallet, futuresAvailable, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return CycleAccountSnapshot{}, err
	}

	snapshot := CycleAccountSnapshot{
		CapturedAt:             time.Now(),
		SpotUSDTFree:           spotUSDT,
		SpotAssetBalances:      make(map[string]float64),
		FuturesUSDTWallet:      futuresWallet,
		FuturesUSDTAvailable:   futuresAvailable,
		FuturesPositionAmounts: make(map[string]float64),
	}

	for _, pair := range pairs {
		meta, ok := e.spotMeta[pair]
		if !ok {
			continue
		}
		snapshot.SpotAssetBalances[meta.BaseAsset] = spotBalances[meta.BaseAsset]
	}
	for _, symbol := range symbols {
		snapshot.FuturesPositionAmounts[symbol] = positions[symbol]
	}
	for _, asset := range extraAssets {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" {
			continue
		}
		if _, ok := snapshot.SpotAssetBalances[asset]; ok {
			continue
		}
		snapshot.SpotAssetBalances[asset] = spotBalances[asset]
	}

	return snapshot, nil
}

func (e *FundingRateSniperEngine) verifyTradingEnvironment(ctx context.Context) error {
	dualSide, err := e.fetchFuturesPositionMode(ctx)
	if err != nil {
		return fmt.Errorf("校验合约持仓模式失败: %w", err)
	}
	if dualSide {
		return fmt.Errorf("当前合约账户仍处于双向持仓模式，程序要求单向持仓模式")
	}

	spotBalances, _, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货账户状态失败: %w", err)
	}
	_, _, positions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return fmt.Errorf("读取合约账户状态失败: %w", err)
	}
	if err := e.reconcilePersistedOpenHedges(spotBalances, positions); err != nil {
		return err
	}

	futuresIssues := make([]string, 0)
	for symbol, qty := range positions {
		if math.Abs(qty) <= e.cfg.ResidualPositionAlertQty {
			continue
		}
		futuresIssues = append(futuresIssues, fmt.Sprintf("%s=%.8f", symbol, qty))
	}
	if len(futuresIssues) > 0 {
		sort.Strings(futuresIssues)
		return fmt.Errorf("检测到存量合约仓位，拒绝启动: %s", strings.Join(futuresIssues, ", "))
	}

	spotIssues := make([]string, 0)
	for asset, balance := range spotBalances {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" || asset == "USDT" || asset == "BNB" {
			continue
		}
		if balance <= e.cfg.ResidualPositionAlertQty {
			continue
		}
		assetPrice, priceErr := e.fetchSpotAssetUSDTPrice(ctx, asset)
		if priceErr != nil || assetPrice <= 0 {
			continue
		}
		notionalUSDT := balance * assetPrice
		if notionalUSDT < e.cfg.ResidualSpotAlertUSDT {
			continue
		}
		spotIssues = append(spotIssues, fmt.Sprintf("%s=%.8f(%.6fUSDT)", asset, balance, notionalUSDT))
	}
	if len(spotIssues) > 0 {
		sort.Strings(spotIssues)
		return fmt.Errorf("检测到存量现货仓位，拒绝启动: %s", strings.Join(spotIssues, ", "))
	}

	if err := e.verifyCrossMarginReadyForNegativeCarry(ctx); err != nil {
		return err
	}

	e.logger.Printf("模块一环境校验通过：单向持仓、无显著残余现货/合约仓位")
	return nil
}

// verifyCrossMarginReadyForNegativeCarry 在启用负 carry(LiveFundingDirectionMode=negative_only/both)时,于启动前
// 校验全仓杠杆账户已开通且可借/可交易。负 carry 的现货腿走全仓杠杆借币(spotVenue=cross-margin):若账户未开通/
// 禁借/禁交易而直到首次开仓才发现,会在合约腿已成交后现货腿失败,留下裸合约敞口(只能靠回补/救援补救)。
// 故采用 fail-close 的启动闸:不可用即拒绝启动并给出可执行指引(开通杠杆/授信,或将方向模式改为 positive_only)。
func (e *FundingRateSniperEngine) verifyCrossMarginReadyForNegativeCarry(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if !liveFundingDirectionEnabled(e.cfg.StrategyConfig.LiveFundingDirectionMode, fundingCarryDirectionNegative) {
		return nil
	}
	state, err := e.fetchCrossMarginAccountState(ctx)
	if err != nil {
		return fmt.Errorf("启用负 carry(模式=%s)但读取全仓杠杆账户失败,拒绝启动(请确认已开通全仓杠杆账户,或将 LiveFundingDirectionMode 改为 positive_only): %w", e.cfg.StrategyConfig.LiveFundingDirectionMode, err)
	}
	missing := make([]string, 0, 3)
	if !state.Created {
		missing = append(missing, "账户未开通(created=false)")
	}
	if !state.BorrowEnabled {
		missing = append(missing, "禁止借币(borrowEnabled=false)")
	}
	if !state.TradeEnabled {
		missing = append(missing, "禁止交易(tradeEnabled=false)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("启用负 carry(模式=%s)但全仓杠杆账户不可用: %s。请在币安开通全仓杠杆并授信,或将 LiveFundingDirectionMode 改为 positive_only 后重启", e.cfg.StrategyConfig.LiveFundingDirectionMode, strings.Join(missing, "、"))
	}
	e.logger.Printf("全仓杠杆账户就绪校验通过(负 carry 已启用,模式=%s):created/borrowEnabled/tradeEnabled 均为真", e.cfg.StrategyConfig.LiveFundingDirectionMode)
	return nil
}

func normalizedFuturesMarginType(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ISOLATED":
		return "ISOLATED"
	default:
		return "CROSSED"
	}
}

func (e *FundingRateSniperEngine) ensureFuturesExecutionSettings(ctx context.Context, targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	marginType := normalizedFuturesMarginType(e.cfg.ExecutionConfig.FuturesMarginType)
	leverage := e.cfg.ExecutionConfig.FuturesLeverage
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		symbol := strings.ToUpper(strings.TrimSpace(target.Candidate.Symbol))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		if err := e.setFuturesMarginType(ctx, symbol, marginType); err != nil {
			return err
		}
		if err := e.setFuturesLeverage(ctx, symbol, leverage); err != nil {
			return err
		}
	}
	return nil
}

func (e *FundingRateSniperEngine) setFuturesMarginType(ctx context.Context, symbol string, marginType string) error {
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	params.Set("marginType", normalizedFuturesMarginType(marginType))
	body, err := e.signedRequest(ctx, http.MethodPost, e.client.FuturesBaseURL()+binance.PathFuturesMarginType, params)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no need to change margin type") || strings.Contains(err.Error(), "-4046") {
			return nil
		}
		return fmt.Errorf("设置 %s 保证金模式失败: %w", symbol, err)
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil && payload.Code != 0 {
		if strings.Contains(strings.ToLower(payload.Msg), "no need to change margin type") || payload.Code == -4046 {
			return nil
		}
		return fmt.Errorf("设置 %s 保证金模式失败: code=%d msg=%s", symbol, payload.Code, payload.Msg)
	}
	return nil
}

func (e *FundingRateSniperEngine) setFuturesLeverage(ctx context.Context, symbol string, leverage int) error {
	params := url.Values{}
	params.Set("symbol", strings.ToUpper(strings.TrimSpace(symbol)))
	params.Set("leverage", strconv.Itoa(leverage))
	if _, err := e.signedRequest(ctx, http.MethodPost, e.client.FuturesBaseURL()+binance.PathFuturesLeverage, params); err != nil {
		return fmt.Errorf("设置 %s 杠杆倍数失败: %w", symbol, err)
	}
	return nil
}

func (e *FundingRateSniperEngine) verifyPreOpenGoNoGo(ctx context.Context, targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	if err := e.checkLiveSafetyGuards("pre_open_go_nogo"); err != nil {
		return err
	}
	// S6-B:档位不允许开新仓时跳过本轮(非致命)。edge 衰减导致的 Observe 在此被拦截;
	// fuse/日限导致的 Observe 已由 checkLiveSafetyGuards 走致命停机路径,这里仅顺带兜底。
	if err := e.checkGearAllowsOpen(); err != nil {
		return err
	}
	maxAge := e.cfg.ExecutionConfig.PreOpenTickerMaxAge
	if maxAge <= 0 {
		maxAge = time.Second
	}
	issues := make([]string, 0, len(targets)*2)
	for _, target := range targets {
		_, spotIssue := e.preOpenTickerFreshnessIssue(target.Candidate.Pair, e.spotTickerCache, maxAge, "spot")
		_, futuresIssue := e.preOpenTickerFreshnessIssue(target.Candidate.Symbol, e.futuresTickerCache, maxAge, "futures")
		// 盘口陈旧/缺失:Binance @bookTicker 是「最优买/卖价变化才推」的事件流,薄盘冷门币顶档长时间不动 →
		// 该 symbol 久无 WS 推送,缓存只剩预热时 REST 补的旧价(实盘曾见 age≈2min 撞穿 maxAge 误拒)。此处仅在
		// 本轮确有腿陈旧时,用 REST 现拉一次真实最优买卖价补鲜再复检:拉到真·当前价→照常放行;拉失败/仍陈旧→
		// 保留原拒因放弃开仓(fail-safe)。流动性好的标的(WS 本就新鲜)不触发补鲜、零额外请求。
		if (spotIssue != "" || futuresIssue != "") && e.cfg.ExecutionConfig.PreOpenTickerRESTRefreshEnabled {
			e.refreshPreOpenTickersViaREST(ctx, target)
			_, spotIssue = e.preOpenTickerFreshnessIssue(target.Candidate.Pair, e.spotTickerCache, maxAge, "spot")
			_, futuresIssue = e.preOpenTickerFreshnessIssue(target.Candidate.Symbol, e.futuresTickerCache, maxAge, "futures")
		}
		if spotIssue != "" {
			issues = append(issues, spotIssue)
		}
		if futuresIssue != "" {
			issues = append(issues, futuresIssue)
		}
	}
	if len(issues) > 0 {
		sort.Strings(issues)
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}

func (e *FundingRateSniperEngine) preOpenTickerFreshnessIssue(symbol string, cache *BookTickerCache, maxAge time.Duration, label string) (BookTicker, string) {
	if cache == nil {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 缓存未初始化", label, symbol)
	}
	ticker, receivedAt, ok := cache.GetWithReceivedAt(symbol)
	if !ok || (parseFloat(ticker.AskPrice) <= 0 && parseFloat(ticker.BidPrice) <= 0) {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 缺失", label, symbol)
	}
	age := time.Since(receivedAt)
	if receivedAt.IsZero() || age > maxAge {
		return BookTicker{}, fmt.Sprintf("%s %s bookTicker 延迟过高: age=%s limit=%s", label, symbol, age.Round(time.Millisecond), maxAge)
	}
	return ticker, ""
}

func (e *FundingRateSniperEngine) verifySpotBNBBurnEnabled(ctx context.Context) error {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathBNBBurn, nil)
	if err != nil {
		return fmt.Errorf("读取 Binance BNB burn 开关失败: %w", err)
	}
	var payload struct {
		SpotBNBBurn bool `json:"spotBNBBurn"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析 Binance BNB burn 开关失败: %w", err)
	}
	if !payload.SpotBNBBurn {
		return fmt.Errorf("Binance 现货 BNB 抵扣手续费未开启，拒绝启动")
	}
	return nil
}

// apiKeyRestrictions 是 Binance GET /sapi/v1/account/apiRestrictions 的关键字段(S5-C / R26)。
type apiKeyRestrictions struct {
	IPRestrict        bool `json:"ipRestrict"`
	EnableWithdrawals bool `json:"enableWithdrawals"`
	EnableReading     bool `json:"enableReading"`
}

// evaluateAPIKeyRestrictions 纯判定 API Key 权限是否安全(可单测):带提现权限 → 致命拒绝;未开 IP 白名单 /
// 未开读取权限 → 非致命告警(返回 warnings,由调用方记日志/推送)。
func evaluateAPIKeyRestrictions(r apiKeyRestrictions, requireIPWhitelist bool) (warnings []string, err error) {
	if r.EnableWithdrawals {
		return nil, fmt.Errorf("API Key 启用了提现权限(enableWithdrawals=true),出于资金安全拒绝启动:请在 Binance 关闭该 Key 的提现权限后重试")
	}
	if !r.IPRestrict {
		if requireIPWhitelist {
			return nil, fmt.Errorf("API Key 未启用 IP 白名单(ipRestrict=false)且 RequireIPWhitelist=true(硬门):请在 Binance 限制该 Key 仅本机出口 IP 可用后重试,或将 require_ip_whitelist 置为 false 退回告警模式")
		}
		warnings = append(warnings, "API Key 未启用 IP 白名单(ipRestrict=false):强烈建议在 Binance 限制该 Key 仅本机出口 IP 可用,降低泄露风险(可置 require_ip_whitelist=true 转为硬门)")
	}
	if !r.EnableReading {
		warnings = append(warnings, "API Key 未启用读取权限(enableReading=false):账户/行情读取可能受限")
	}
	return warnings, nil
}

// verifyAPIKeyPermissions 启动期 API Key 权限自检(S5-C / R26):拉取 apiRestrictions,带提现权限即拒绝启动。
// 安全姿态由 RequireAPIKeyPermissionCheck 控制:默认 true(fail-close)——读取/解析失败即**拒绝启动**,因为这正是
// 最需要确认提现权限的时刻,不应带未知状态开跑;置 false 才退回「失败仅告警」的兼容行为(供不支持该端点的子账户 Key)。
// IP 白名单是否为硬门由 RequireIPWhitelist 控制。提现权限检测恒为硬闸。
func (e *FundingRateSniperEngine) verifyAPIKeyPermissions(ctx context.Context) error {
	failClose := e.cfg.RiskConfig.RequireAPIKeyPermissionCheck
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathAPIRestrictions, nil)
	if err != nil {
		if failClose {
			return fmt.Errorf("API Key 权限自检失败(fail-close,拒绝启动):读取 apiRestrictions 失败,无法确认提现权限;若该 Key 类型确实不支持该端点,可将 require_api_key_permission_check 置为 false: %w", err)
		}
		if e.logger != nil {
			e.logger.Printf("API Key 权限自检告警: 读取 apiRestrictions 失败(require_api_key_permission_check=false,不阻塞启动,但无法确认提现权限,请确保该 Key 无提现权限): %v", err)
		}
		return nil
	}
	var payload apiKeyRestrictions
	if err := json.Unmarshal(body, &payload); err != nil {
		if failClose {
			return fmt.Errorf("API Key 权限自检失败(fail-close,拒绝启动):解析 apiRestrictions 失败,无法确认提现权限: %w", err)
		}
		if e.logger != nil {
			e.logger.Printf("API Key 权限自检告警: 解析 apiRestrictions 失败(require_api_key_permission_check=false,不阻塞启动): %v", err)
		}
		return nil
	}
	warnings, evalErr := evaluateAPIKeyRestrictions(payload, e.cfg.RiskConfig.RequireIPWhitelist)
	for _, w := range warnings {
		if e.logger != nil {
			e.logger.Printf("API Key 权限告警: %s", w)
		}
	}
	return evalErr
}

func (e *FundingRateSniperEngine) fetchFuturesPositionMode(ctx context.Context) (bool, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+binance.PathFuturesPositionSideDual, nil)
	if err != nil {
		return false, err
	}

	var payload struct {
		DualSidePosition any `json:"dualSidePosition"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false, fmt.Errorf("解析合约持仓模式失败: %w", err)
	}
	switch typed := payload.DualSidePosition.(type) {
	case bool:
		return typed, nil
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true"), nil
	default:
		return false, fmt.Errorf("未知持仓模式字段: %v", payload.DualSidePosition)
	}
}

func (e *FundingRateSniperEngine) fetchSpotAccountBalances(ctx context.Context) (map[string]float64, float64, error) {
	freeBalances, balances, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return nil, 0, err
	}
	return balances, freeBalances["USDT"], nil
}

func (e *FundingRateSniperEngine) fetchSpotAccountFreeBalances(ctx context.Context) (map[string]float64, map[string]float64, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathSpotAccount, nil)
	if err != nil {
		return nil, nil, err
	}

	var payload struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("解析现货账户快照失败: %w", err)
	}

	freeBalances := make(map[string]float64, len(payload.Balances))
	balances := make(map[string]float64, len(payload.Balances))
	for _, balance := range payload.Balances {
		free := parseFloat(balance.Free)
		freeBalances[balance.Asset] = free
		total := parseFloat(balance.Free) + parseFloat(balance.Locked)
		balances[balance.Asset] = total
	}

	return freeBalances, balances, nil
}

func (e *FundingRateSniperEngine) fetchFuturesAccountState(ctx context.Context) (float64, float64, map[string]float64, error) {
	if e != nil {
		e.futuresAccountStateMu.Lock()
		cachedAt := e.futuresAccountStateCachedAt
		if !cachedAt.IsZero() && time.Since(cachedAt) <= futuresAccountStateCacheTTL {
			walletUSDT := e.futuresAccountWalletUSDT
			availableUSDT := e.futuresAccountAvailableUSDT
			positions := cloneFloat64Map(e.futuresAccountPositions)
			e.futuresAccountStateMu.Unlock()
			return walletUSDT, availableUSDT, positions, nil
		}
		e.futuresAccountStateMu.Unlock()
	}

	body, err := e.signedRequest(ctx, http.MethodGet, e.client.FuturesBaseURL()+binance.PathFuturesAccountV3, nil)
	if err != nil {
		if e != nil {
			e.futuresAccountStateMu.Lock()
			cachedAt := e.futuresAccountStateCachedAt
			if !cachedAt.IsZero() && time.Since(cachedAt) <= futuresAccountStateStaleTTL {
				walletUSDT := e.futuresAccountWalletUSDT
				availableUSDT := e.futuresAccountAvailableUSDT
				positions := cloneFloat64Map(e.futuresAccountPositions)
				e.futuresAccountStateMu.Unlock()
				if e.logger != nil {
					e.logger.Printf("合约账户快照读取失败，回退到 %.1fs 内缓存: %v", time.Since(cachedAt).Seconds(), err)
				}
				return walletUSDT, availableUSDT, positions, nil
			}
			e.futuresAccountStateMu.Unlock()
		}
		return 0, 0, nil, err
	}

	var payload struct {
		Assets []struct {
			Asset            string `json:"asset"`
			WalletBalance    string `json:"walletBalance"`
			AvailableBalance string `json:"availableBalance"`
		} `json:"assets"`
		Positions []struct {
			Symbol      string `json:"symbol"`
			PositionAmt string `json:"positionAmt"`
		} `json:"positions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, 0, nil, fmt.Errorf("解析合约账户快照失败: %w", err)
	}

	walletUSDT, availableUSDT := 0.0, 0.0
	for _, asset := range payload.Assets {
		if asset.Asset != "USDT" {
			continue
		}
		walletUSDT = parseFloat(asset.WalletBalance)
		availableUSDT = parseFloat(asset.AvailableBalance)
		break
	}

	positions := make(map[string]float64, len(payload.Positions))
	for _, position := range payload.Positions {
		positions[position.Symbol] = parseFloat(position.PositionAmt)
	}

	if e != nil {
		e.futuresAccountStateMu.Lock()
		e.futuresAccountStateCachedAt = time.Now()
		e.futuresAccountWalletUSDT = walletUSDT
		e.futuresAccountAvailableUSDT = availableUSDT
		e.futuresAccountPositions = cloneFloat64Map(positions)
		e.futuresAccountStateMu.Unlock()
	}

	return walletUSDT, availableUSDT, positions, nil
}

func (e *FundingRateSniperEngine) transferAssetBetweenAccounts(ctx context.Context, transferType string, asset string, amount float64) error {
	if amount <= executionQtyTolerance {
		return nil
	}
	params := url.Values{}
	params.Set("type", transferType)
	params.Set("asset", strings.ToUpper(strings.TrimSpace(asset)))
	params.Set("amount", formatRequestAmount(amount))

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.SpotBaseURL()+binance.PathAssetTransfer, params)
	if err != nil {
		return err
	}
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("解析账户划转回执失败: %w", err)
	}
	if payload.TranID == 0 {
		return fmt.Errorf("账户划转回执缺少 tranId")
	}
	return nil
}

func (e *FundingRateSniperEngine) fetchCrossMarginAccountState(ctx context.Context) (CrossMarginAccountState, error) {
	body, err := e.signedRequest(ctx, http.MethodGet, e.client.SpotBaseURL()+binance.PathMarginAccount, nil)
	if err != nil {
		return CrossMarginAccountState{}, err
	}

	var payload struct {
		Created                  bool   `json:"created"`
		BorrowEnabled            bool   `json:"borrowEnabled"`
		TradeEnabled             bool   `json:"tradeEnabled"`
		TransferInEnabled        bool   `json:"transferInEnabled"`
		TransferOutEnabled       bool   `json:"transferOutEnabled"`
		AccountType              string `json:"accountType"`
		TotalCollateralValueUSDT string `json:"TotalCollateralValueInUSDT"`
		TotalNetAssetBTC         string `json:"totalNetAssetOfBtc"`
		TotalLiabilityBTC        string `json:"totalLiabilityOfBtc"`
		MarginLevel              string `json:"marginLevel"`
		UserAssets               []struct {
			Asset    string `json:"asset"`
			Borrowed string `json:"borrowed"`
			Free     string `json:"free"`
			Interest string `json:"interest"`
			Locked   string `json:"locked"`
			NetAsset string `json:"netAsset"`
		} `json:"userAssets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return CrossMarginAccountState{}, fmt.Errorf("解析全仓账户快照失败: %w", err)
	}

	state := CrossMarginAccountState{
		Created:             payload.Created,
		BorrowEnabled:       payload.BorrowEnabled,
		TradeEnabled:        payload.TradeEnabled,
		TransferInEnabled:   payload.TransferInEnabled,
		TransferOutEnabled:  payload.TransferOutEnabled,
		AccountType:         payload.AccountType,
		TotalCollateralUSDT: parseFloat(payload.TotalCollateralValueUSDT),
		TotalNetAssetBTC:    parseFloat(payload.TotalNetAssetBTC),
		TotalLiabilityBTC:   parseFloat(payload.TotalLiabilityBTC),
		MarginLevel:         parseFloat(payload.MarginLevel),
		Assets:              make(map[string]CrossMarginAssetBalance, len(payload.UserAssets)),
	}
	for _, asset := range payload.UserAssets {
		entry := CrossMarginAssetBalance{
			Asset:    strings.ToUpper(strings.TrimSpace(asset.Asset)),
			Free:     parseFloat(asset.Free),
			Borrowed: parseFloat(asset.Borrowed),
			Interest: parseFloat(asset.Interest),
			Locked:   parseFloat(asset.Locked),
			NetAsset: parseFloat(asset.NetAsset),
		}
		if entry.Asset == "" {
			continue
		}
		state.Assets[entry.Asset] = entry
		if entry.Asset == "USDT" {
			state.USDT = entry
		}
	}
	return state, nil
}

func (e *FundingRateSniperEngine) borrowCrossMarginAsset(ctx context.Context, asset string, amount float64) (int64, error) {
	return e.crossMarginBorrowRepay(ctx, asset, amount, "BORROW")
}

func (e *FundingRateSniperEngine) repayCrossMarginAsset(ctx context.Context, asset string, amount float64) (int64, error) {
	return e.crossMarginBorrowRepay(ctx, asset, amount, "REPAY")
}

func (e *FundingRateSniperEngine) crossMarginBorrowRepay(ctx context.Context, asset string, amount float64, action string) (int64, error) {
	if amount <= executionQtyTolerance {
		return 0, fmt.Errorf("margin %s amount 必须大于 0", strings.ToLower(action))
	}
	params := url.Values{}
	params.Set("asset", strings.ToUpper(strings.TrimSpace(asset)))
	params.Set("isIsolated", "FALSE")
	params.Set("amount", formatRequestAmount(amount))
	params.Set("type", action)

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.SpotBaseURL()+binance.PathMarginBorrowRepay, params)
	if err != nil {
		return 0, err
	}
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("解析全仓%s回执失败: %w", strings.ToLower(action), err)
	}
	if payload.TranID == 0 {
		return 0, fmt.Errorf("全仓%s回执缺少 tranId", strings.ToLower(action))
	}
	return payload.TranID, nil
}
