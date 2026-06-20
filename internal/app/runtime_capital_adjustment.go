package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"lingxi/internal/venue/binance"
)

// runtimeCapitalAdjustTimeout 覆盖「清币归一(拉规则+逐币市价单)+ USDT 划转」全流程。
const runtimeCapitalAdjustTimeout = 120 * time.Second

const (
	runtimeCapitalSnapshotCacheTTL = 15 * time.Second
	runtimeCapitalSnapshotStaleTTL = 60 * time.Second
)

const (
	capitalAccountSpot    = "spot"
	capitalAccountFutures = "um_futures"
	capitalAccountMargin  = "cross_margin"
	capitalAccountFunding = "funding"
)

// 跨所对冲腿仅跑 U 本位永续:调仓时把交易总额全部放进 um_futures,余量留 funding。
const (
	tradingCapitalSpotRatio    = 0.0
	tradingCapitalMarginRatio  = 0.0
	tradingCapitalFuturesRatio = 1.0
)

type RuntimeCapitalAdjustmentRequest struct {
	SecretPath        string  `json:"secret_path"`
	Mode              string  `json:"mode"`
	TargetTradingUSDT float64 `json:"target_trading_usdt"`
}

type RuntimeCapitalTransfer struct {
	From         string  `json:"from"`
	To           string  `json:"to"`
	TransferType string  `json:"transfer_type"`
	AmountUSDT   float64 `json:"amount_usdt"`
}

type RuntimeCapitalSnapshot struct {
	SpotFreeUSDT           float64 `json:"spot_free_usdt"`
	FuturesAvailableUSDT   float64 `json:"futures_available_usdt"`
	CrossMarginFreeUSDT    float64 `json:"cross_margin_free_usdt"`
	FundingFreeUSDT        float64 `json:"funding_free_usdt"`
	TotalMovableUSDT       float64 `json:"total_movable_usdt"`
	DesiredSpotUSDT        float64 `json:"desired_spot_usdt,omitempty"`
	DesiredCrossMarginUSDT float64 `json:"desired_cross_margin_usdt,omitempty"`
	DesiredFuturesUSDT     float64 `json:"desired_futures_usdt,omitempty"`
	DesiredFundingUSDT     float64 `json:"desired_funding_usdt,omitempty"`
	// Wallets 是 venue 中性的钱包列表,供前端动态渲染任意交易所的钱包(币安 4 钱包 / Bybit 统一+资金)。
	// 币安路径同样回填它(由上面 4 组字段映射),让面板用同一套动态渲染;为空时前端回退旧的固定卡片。
	Wallets []RuntimeWalletBalance `json:"wallets,omitempty"`
}

// RuntimeWalletBalance 是面板动态渲染用的单钱包条目(venue 中性)。
type RuntimeWalletBalance struct {
	Wallet      string  `json:"wallet"`       // 钱包标识:spot/um_futures/cross_margin/funding/unified
	Label       string  `json:"label"`        // UI 展示名
	FreeUSDT    float64 `json:"free_usdt"`    // 当前可用
	DesiredUSDT float64 `json:"desired_usdt"` // 目标分配(无目标则为 0)
}

func applyConfiguredCapitalTargets(snapshot *RuntimeCapitalSnapshot, cfg EngineConfig) {
	if snapshot == nil {
		return
	}
	configuredTradingUSDT := configuredTradingCapitalUSDT(cfg)
	if configuredTradingUSDT <= 0 {
		return
	}
	spotUSDT, marginUSDT, futuresUSDT := tradingCapitalTargets(configuredTradingUSDT)
	snapshot.DesiredSpotUSDT = spotUSDT
	snapshot.DesiredCrossMarginUSDT = marginUSDT
	snapshot.DesiredFuturesUSDT = futuresUSDT
	snapshot.DesiredFundingUSDT = maxFloat(snapshot.TotalMovableUSDT-configuredTradingUSDT, 0)
}

type RuntimeCapitalAdjustmentResult struct {
	RequestedTradingUSDT float64                  `json:"requested_trading_usdt"`
	AppliedTradingUSDT   float64                  `json:"applied_trading_usdt"`
	PerLegTradingUSDT    float64                  `json:"per_leg_trading_usdt"`
	Before               RuntimeCapitalSnapshot   `json:"before"`
	After                RuntimeCapitalSnapshot   `json:"after"`
	Transfers            []RuntimeCapitalTransfer `json:"transfers"`
	Message              string                   `json:"message"`
	Config               RuntimeRiskConfig        `json:"config"`
}

func transferTypeBetweenCapitalAccounts(from string, to string) string {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	switch {
	case from == capitalAccountSpot && to == capitalAccountFutures:
		return "MAIN_UMFUTURE"
	case from == capitalAccountFutures && to == capitalAccountSpot:
		return "UMFUTURE_MAIN"
	case from == capitalAccountSpot && to == capitalAccountMargin:
		return "MAIN_MARGIN"
	case from == capitalAccountMargin && to == capitalAccountSpot:
		return "MARGIN_MAIN"
	case from == capitalAccountSpot && to == capitalAccountFunding:
		return "MAIN_FUNDING"
	case from == capitalAccountFunding && to == capitalAccountSpot:
		return "FUNDING_MAIN"
	case from == capitalAccountFunding && to == capitalAccountFutures:
		return "FUNDING_UMFUTURE"
	case from == capitalAccountFutures && to == capitalAccountFunding:
		return "UMFUTURE_FUNDING"
	case from == capitalAccountMargin && to == capitalAccountFutures:
		return "MARGIN_UMFUTURE"
	case from == capitalAccountFutures && to == capitalAccountMargin:
		return "UMFUTURE_MARGIN"
	default:
		return ""
	}
}

func directOrHubTransferPlan(from string, to string) []string {
	if transferType := transferTypeBetweenCapitalAccounts(from, to); transferType != "" {
		return []string{transferType}
	}
	switch {
	case from == capitalAccountFunding && to == capitalAccountFutures:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountFunding, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountFutures),
		}
	case from == capitalAccountFutures && to == capitalAccountFunding:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountFutures, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountFunding),
		}
	case from == capitalAccountFunding && to == capitalAccountMargin:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountFunding, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountMargin),
		}
	case from == capitalAccountMargin && to == capitalAccountFunding:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountMargin, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountFunding),
		}
	case from == capitalAccountMargin && to == capitalAccountFutures:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountMargin, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountFutures),
		}
	case from == capitalAccountFutures && to == capitalAccountMargin:
		return []string{
			transferTypeBetweenCapitalAccounts(capitalAccountFutures, capitalAccountSpot),
			transferTypeBetweenCapitalAccounts(capitalAccountSpot, capitalAccountMargin),
		}
	default:
		return nil
	}
}

func (e *FundingRateSniperEngine) fetchFundingAccountFreeBalances(ctx context.Context) (map[string]float64, error) {
	body, err := e.signedRequest(ctx, http.MethodPost, e.client.SpotBaseURL()+binance.PathAssetFundingAsset, nil)
	if err != nil {
		return nil, err
	}
	var payload []struct {
		Asset       string `json:"asset"`
		Free        string `json:"free"`
		Locked      string `json:"locked"`
		Freeze      string `json:"freeze"`
		Withdrawing string `json:"withdrawing"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析资金账户快照失败: %w", err)
	}
	balances := make(map[string]float64, len(payload))
	for _, asset := range payload {
		name := strings.ToUpper(strings.TrimSpace(asset.Asset))
		if name == "" {
			continue
		}
		balances[name] = parseFloat(asset.Free)
	}
	return balances, nil
}

func (e *FundingRateSniperEngine) captureRuntimeCapitalSnapshot(ctx context.Context) (RuntimeCapitalSnapshot, error) {
	spotFree, _, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return RuntimeCapitalSnapshot{}, fmt.Errorf("读取现货可用 USDT 失败: %w", err)
	}
	_, futuresAvailable, _, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return RuntimeCapitalSnapshot{}, fmt.Errorf("读取合约可用 USDT 失败: %w", err)
	}
	marginState, err := e.fetchCrossMarginAccountState(ctx)
	if err != nil {
		return RuntimeCapitalSnapshot{}, fmt.Errorf("读取全仓可用 USDT 失败: %w", err)
	}
	fundingFree, err := e.fetchFundingAccountFreeBalances(ctx)
	if err != nil {
		return RuntimeCapitalSnapshot{}, fmt.Errorf("读取资金账户可用 USDT 失败: %w", err)
	}
	snapshot := RuntimeCapitalSnapshot{
		SpotFreeUSDT:         maxFloat(spotFree["USDT"], 0),
		FuturesAvailableUSDT: maxFloat(futuresAvailable, 0),
		CrossMarginFreeUSDT:  maxFloat(marginState.USDT.Free, 0),
		FundingFreeUSDT:      maxFloat(fundingFree["USDT"], 0),
	}
	snapshot.TotalMovableUSDT = snapshot.SpotFreeUSDT + snapshot.FuturesAvailableUSDT + snapshot.CrossMarginFreeUSDT + snapshot.FundingFreeUSDT
	return snapshot, nil
}

// isBinanceTransferOutLimitedError 识别币安全仓/账户「可转出上限」类拒绝(-3020 等),与真实故障(网络/权限)区分。
func isBinanceTransferOutLimitedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "-3020") ||
		strings.Contains(msg, "transfer out amount exceeds max amount") ||
		strings.Contains(msg, "exceeds max amount") ||
		strings.Contains(msg, "可转出受限") {
		return true
	}
	var reqErr signedRequestError
	if errors.As(err, &reqErr) {
		body := strings.ToLower(reqErr.Body)
		if strings.Contains(body, "-3020") ||
			strings.Contains(body, "transfer out amount exceeds max amount") ||
			strings.Contains(body, "exceeds max amount") {
			return true
		}
	}
	return false
}

// transferUSDTAssetToleratingOutLimit 调仓路径专用:全仓转出超额时尽量部分归拢,零头转不出则 WARN 后继续。
func (e *FundingRateSniperEngine) transferUSDTAssetToleratingOutLimit(ctx context.Context, transferType string, amount float64) (float64, error) {
	actual, err := e.transferAssetBetweenAccounts(ctx, transferType, "USDT", amount)
	if err == nil {
		return actual, nil
	}
	if !isBinanceTransferOutLimitedError(err) {
		return 0, err
	}
	if isCrossMarginTransferOut(transferType) {
		maxTransferable, qerr := e.fetchCrossMarginMaxTransferableUSDT(ctx, "USDT")
		if qerr == nil && maxTransferable > executionQtyTolerance {
			// -3020 说明首次 cap 仍偏高,用更保守系数再试一次「能转多少转多少」。
			retry := math.Floor(maxTransferable*0.99*1e8) / 1e8
			if retry > executionQtyTolerance {
				retryActual, retryErr := e.transferAssetBetweenAccounts(ctx, transferType, "USDT", retry)
				if retryErr == nil && retryActual > executionQtyTolerance {
					if e.logger != nil {
						e.logger.Printf("WARN: 全仓 USDT 可转出受限 max=%.8f requested=%.8f actual=%.8f，剩余未归拢", maxTransferable, amount, retryActual)
					}
					return retryActual, nil
				}
				if retryErr != nil && !isBinanceTransferOutLimitedError(retryErr) {
					return 0, retryErr
				}
			}
		}
	}
	if e.logger != nil {
		e.logger.Printf("WARN: %s USDT 可转出受限 requested=%.8f，剩余未归拢: %v", transferType, amount, err)
	}
	return 0, nil
}

func (e *FundingRateSniperEngine) transferUSDTBetweenCapitalAccounts(ctx context.Context, from string, to string, amount float64) ([]RuntimeCapitalTransfer, float64, error) {
	amount = math.Floor(maxFloat(amount, 0)*1e8) / 1e8
	if amount <= executionQtyTolerance || strings.TrimSpace(from) == strings.TrimSpace(to) {
		return nil, 0, nil
	}
	plan := directOrHubTransferPlan(from, to)
	if len(plan) == 0 {
		return nil, 0, fmt.Errorf("不支持的 USDT 划转路径: %s -> %s", from, to)
	}
	transfers := make([]RuntimeCapitalTransfer, 0, len(plan))
	currentFrom := from
	var movedFromSource float64
	for hopIndex, transferType := range plan {
		if strings.TrimSpace(transferType) == "" {
			return transfers, movedFromSource, fmt.Errorf("USDT 划转路径不完整: %s -> %s", from, to)
		}
		actual, err := e.transferUSDTAssetToleratingOutLimit(ctx, transferType, amount)
		if err != nil {
			return transfers, movedFromSource, err
		}
		if actual <= executionQtyTolerance {
			break
		}
		if hopIndex == 0 {
			movedFromSource = actual
		}
		amount = actual
		nextTo := to
		switch transferType {
		case "FUNDING_MAIN", "UMFUTURE_MAIN", "MARGIN_MAIN":
			nextTo = capitalAccountSpot
		case "MAIN_FUNDING", "UMFUTURE_FUNDING":
			nextTo = capitalAccountFunding
		case "MAIN_UMFUTURE", "FUNDING_UMFUTURE", "MARGIN_UMFUTURE":
			nextTo = capitalAccountFutures
		case "MAIN_MARGIN", "UMFUTURE_MARGIN":
			nextTo = capitalAccountMargin
		}
		transfers = append(transfers, RuntimeCapitalTransfer{
			From:         currentFrom,
			To:           nextTo,
			TransferType: transferType,
			AmountUSDT:   actual,
		})
		currentFrom = nextTo
	}
	return transfers, movedFromSource, nil
}

func tradingCapitalTargets(totalTradingUSDT float64) (spotUSDT float64, marginUSDT float64, futuresUSDT float64) {
	totalTradingUSDT = maxFloat(totalTradingUSDT, 0)
	spotUSDT = totalTradingUSDT * tradingCapitalSpotRatio
	marginUSDT = totalTradingUSDT * tradingCapitalMarginRatio
	futuresUSDT = totalTradingUSDT - spotUSDT - marginUSDT
	return spotUSDT, marginUSDT, futuresUSDT
}

func applyTradingCapitalTargetToConfig(cfg *EngineConfig, tradingCapitalUSDT float64) {
	if cfg == nil {
		return
	}
	tradingCapitalUSDT = maxFloat(tradingCapitalUSDT, 0)
	perLeg := tradingCapitalUSDT / 2
	cfg.StrategyConfig.PerSymbolQuoteUSDT = perLeg
	cfg.PerSymbolQuoteUSDT = perLeg
	cfg.RiskConfig.MaxCycleOpenNotionalUSDT = tradingCapitalUSDT
	// Bybit 执行器的分配资金只读这个字段——漏掉它会出现「页面调仓到 127U,电报却报分配资金=60U」
	// (资金划转成功但引擎仍按 config.yaml 旧值跑,多余的钱永久闲置)。
	cfg.AllocatedCapitalUSDT = tradingCapitalUSDT
}

func configuredTradingCapitalUSDT(cfg EngineConfig) float64 {
	return math.Max(maxFloat(cfg.RiskConfig.MaxCycleOpenNotionalUSDT, 0), maxFloat(cfg.StrategyConfig.PerSymbolQuoteUSDT*2, 0))
}

func buildSimulationCapitalAdjustmentResult(targetTradingUSDT float64) RuntimeCapitalAdjustmentResult {
	desiredSpotUSDT, desiredMarginUSDT, desiredFuturesUSDT := tradingCapitalTargets(targetTradingUSDT)
	return RuntimeCapitalAdjustmentResult{
		RequestedTradingUSDT: targetTradingUSDT,
		AppliedTradingUSDT:   targetTradingUSDT,
		PerLegTradingUSDT:    targetTradingUSDT / 2,
		After: RuntimeCapitalSnapshot{
			TotalMovableUSDT:       targetTradingUSDT,
			DesiredSpotUSDT:        desiredSpotUSDT,
			DesiredCrossMarginUSDT: desiredMarginUSDT,
			DesiredFuturesUSDT:     desiredFuturesUSDT,
			DesiredFundingUSDT:     0,
		},
		Message: fmt.Sprintf("历史模拟名义仓位已设置为 %.2f USDT：UM futures=%.2f(100%%)",
			targetTradingUSDT, desiredFuturesUSDT),
	}
}

func (e *FundingRateSniperEngine) AdjustTradingCapitalUSDT(ctx context.Context, targetTradingUSDT float64) (RuntimeCapitalAdjustmentResult, error) {
	if e == nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("引擎未初始化")
	}
	if !strings.EqualFold(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset), "USDT") {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("当前仅支持 USDT 口径调仓，live funding asset=%s", e.cfg.StrategyConfig.LiveFundingAsset)
	}
	if targetTradingUSDT <= 0 {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("目标交易仓位必须大于 0")
	}
	// 调仓第一步:清币归一——把所有非白名单杂币(保留 BNB 付手续费)市价清成 USDT,
	// 让随后的快照与划转拿到完整的可调资金。台账有未平仓对冲时这里会硬拒绝。
	sweepReport, err := e.sweepResidualAssetsToUSDT(ctx)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("调仓前清币失败: %w", err)
	}
	before, err := e.captureRuntimeCapitalSnapshot(ctx)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, err
	}
	// 清币有真实成交/划转时,卖出所得入账存在秒级延迟(2026-06-10 实锤:ENA 卖出 38.84U 后
	// 立刻拍快照少算了这笔,误判「可调不足」拒绝调仓)。这里轮询等待入账:总额达标即继续,
	// 最多重试 5 次(约 7.5s)后以最后一次快照为准走原有校验。
	for attempt := 0; attempt < 5 && sweepReportHasExecution(sweepReport) && before.TotalMovableUSDT+executionQtyTolerance < targetTradingUSDT; attempt++ {
		select {
		case <-ctx.Done():
			return RuntimeCapitalAdjustmentResult{}, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
		refreshed, rerr := e.captureRuntimeCapitalSnapshot(ctx)
		if rerr != nil {
			break
		}
		before = refreshed
	}
	if before.TotalMovableUSDT <= executionQtyTolerance {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("当前所有账户都没有可调的 USDT 余额")
	}
	if before.TotalMovableUSDT+executionQtyTolerance < targetTradingUSDT {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("实盘总可调 USDT 不足：目标=%.2f，可调=%.2f；已拒绝继续运行", targetTradingUSDT, before.TotalMovableUSDT)
	}
	appliedTradingUSDT := targetTradingUSDT
	desiredSpotUSDT, desiredMarginUSDT, desiredFuturesUSDT := tradingCapitalTargets(appliedTradingUSDT)
	desiredFundingUSDT := maxFloat(before.TotalMovableUSDT-appliedTradingUSDT, 0)

	current := map[string]float64{
		capitalAccountSpot:    before.SpotFreeUSDT,
		capitalAccountFutures: before.FuturesAvailableUSDT,
		capitalAccountMargin:  before.CrossMarginFreeUSDT,
		capitalAccountFunding: before.FundingFreeUSDT,
	}
	desired := map[string]float64{
		capitalAccountSpot:    desiredSpotUSDT,
		capitalAccountFutures: desiredFuturesUSDT,
		capitalAccountMargin:  desiredMarginUSDT,
		capitalAccountFunding: desiredFundingUSDT,
	}

	allTransfers := make([]RuntimeCapitalTransfer, 0, 8)
	move := func(from string, to string, amount float64) error {
		amount = math.Min(maxFloat(amount, 0), current[from])
		if amount <= executionQtyTolerance {
			return nil
		}
		transfers, actualMoved, err := e.transferUSDTBetweenCapitalAccounts(ctx, from, to, amount)
		if err != nil {
			// 兜底:单账户零头 dust 不应阻断其它账户归拢/分配。
			if isBinanceTransferOutLimitedError(err) {
				if e.logger != nil {
					e.logger.Printf("WARN: %s→%s 可转出受限 requested=%.8f actual=%.8f，剩余未归拢", from, to, amount, actualMoved)
				}
				if actualMoved > executionQtyTolerance {
					current[from] -= actualMoved
					current[to] += actualMoved
					allTransfers = append(allTransfers, transfers...)
				}
				return nil
			}
			return err
		}
		if actualMoved <= executionQtyTolerance {
			return nil
		}
		current[from] -= actualMoved
		current[to] += actualMoved
		allTransfers = append(allTransfers, transfers...)
		return nil
	}

	for _, account := range []string{capitalAccountFutures, capitalAccountMargin} {
		if current[account] > desired[account]+executionQtyTolerance {
			if err := move(account, capitalAccountSpot, current[account]-desired[account]); err != nil {
				return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("归拢 %s 余额失败: %w", account, err)
			}
		}
	}
	totalMainNeed := desired[capitalAccountSpot]
	for _, account := range []string{capitalAccountFutures, capitalAccountMargin} {
		if desired[account] > current[account]+executionQtyTolerance {
			totalMainNeed += desired[account] - current[account]
		}
	}
	if current[capitalAccountSpot]+executionQtyTolerance < totalMainNeed && current[capitalAccountFunding] > desired[capitalAccountFunding]+executionQtyTolerance {
		need := totalMainNeed - current[capitalAccountSpot]
		availableFunding := current[capitalAccountFunding] - desired[capitalAccountFunding]
		if err := move(capitalAccountFunding, capitalAccountSpot, math.Min(need, availableFunding)); err != nil {
			return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("从 funding 归拢交易资金失败: %w", err)
		}
	}
	for _, account := range []string{capitalAccountFutures, capitalAccountMargin} {
		if desired[account] > current[account]+executionQtyTolerance {
			if err := move(capitalAccountSpot, account, desired[account]-current[account]); err != nil {
				return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("补足 %s 交易余额失败: %w", account, err)
			}
		}
	}
	if current[capitalAccountSpot] > desired[capitalAccountSpot]+executionQtyTolerance {
		if err := move(capitalAccountSpot, capitalAccountFunding, current[capitalAccountSpot]-desired[capitalAccountSpot]); err != nil {
			return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("将主账户余量转入 funding 失败: %w", err)
		}
	}
	if desired[capitalAccountSpot] > current[capitalAccountSpot]+executionQtyTolerance && current[capitalAccountFunding] > desired[capitalAccountFunding]+executionQtyTolerance {
		need := desired[capitalAccountSpot] - current[capitalAccountSpot]
		availableFunding := current[capitalAccountFunding] - desired[capitalAccountFunding]
		if err := move(capitalAccountFunding, capitalAccountSpot, math.Min(need, availableFunding)); err != nil {
			return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("补足 spot 交易余额失败: %w", err)
		}
	}

	after := RuntimeCapitalSnapshot{
		SpotFreeUSDT:           current[capitalAccountSpot],
		FuturesAvailableUSDT:   current[capitalAccountFutures],
		CrossMarginFreeUSDT:    current[capitalAccountMargin],
		FundingFreeUSDT:        current[capitalAccountFunding],
		TotalMovableUSDT:       before.TotalMovableUSDT,
		DesiredSpotUSDT:        desiredSpotUSDT,
		DesiredCrossMarginUSDT: desiredMarginUSDT,
		DesiredFuturesUSDT:     desiredFuturesUSDT,
		DesiredFundingUSDT:     desiredFundingUSDT,
	}
	message := fmt.Sprintf("目标交易仓位已调整为 %.2f USDT：UM futures=%.2f(100%%)，funding 余量=%.2f。%s",
		appliedTradingUSDT, desiredFuturesUSDT, desiredFundingUSDT, summarizeSweepReport(sweepReport))
	return RuntimeCapitalAdjustmentResult{
		RequestedTradingUSDT: targetTradingUSDT,
		AppliedTradingUSDT:   appliedTradingUSDT,
		PerLegTradingUSDT:    appliedTradingUSDT / 2,
		Before:               before,
		After:                after,
		Transfers:            allTransfers,
		Message:              message,
	}, nil
}

func (c *RuntimeController) AdjustTradingCapital(req RuntimeCapitalAdjustmentRequest) (RuntimeCapitalAdjustmentResult, error) {
	if c == nil {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("后台控制器未初始化")
	}
	targetTradingUSDT := req.TargetTradingUSDT
	if targetTradingUSDT <= 0 {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("目标交易仓位必须大于 0")
	}
	mode := RuntimeMode(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = RuntimeModeLive
	}
	if mode != RuntimeModeLive && mode != RuntimeModeMonthly {
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("不支持的仓位调整模式: %s", req.Mode)
	}
	secretPath := strings.TrimSpace(req.SecretPath)
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	// 非币安所走 venue.CapitalManagement 适配器路径(币安以下逻辑逐字节不变)。
	if c.isNonBinanceVenue() {
		return c.adjustVenueTradingCapital(mode, secretPath, targetTradingUSDT)
	}

	c.mu.Lock()
	if c.status.Running {
		c.mu.Unlock()
		return RuntimeCapitalAdjustmentResult{}, fmt.Errorf("后台任务运行中，不能调整真实资金仓位")
	}
	cfg := c.cfg
	c.mu.Unlock()

	if mode == RuntimeModeMonthly {
		result := buildSimulationCapitalAdjustmentResult(targetTradingUSDT)
		c.mu.Lock()
		applyTradingCapitalTargetToConfig(&c.cfg, result.AppliedTradingUSDT)
		c.status.LastMessage = result.Message
		updatedConfig := runtimeRiskConfigFromEngineConfig(c.cfg)
		c.mu.Unlock()
		result.Config = updatedConfig
		return result, nil
	}

	ctx, cancel := context.WithTimeout(c.rootCtx, runtimeCapitalAdjustTimeout)
	defer cancel()
	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, err
	}
	engine.logger = log.New(newRuntimeLogWriter(c.logs, "capital-adjust", nil, engine.traceIDs, engine.recordMetadata), "[CapitalAdjust] ", log.LstdFlags|log.Lmicroseconds)
	result, err := engine.AdjustTradingCapitalUSDT(ctx, targetTradingUSDT)
	if err != nil {
		return RuntimeCapitalAdjustmentResult{}, err
	}

	c.mu.Lock()
	applyTradingCapitalTargetToConfig(&c.cfg, result.AppliedTradingUSDT)
	c.status.SecretPath = secretPath
	c.status.LastMessage = result.Message
	updatedConfig := runtimeRiskConfigFromEngineConfig(c.cfg)
	c.mu.Unlock()

	result.Config = updatedConfig
	c.appendControllerTraceLog(c.currentRunID(), c.currentCycleID(), "%s", result.Message)
	return result, nil
}

func (c *RuntimeController) CurrentCapitalSnapshot() (RuntimeCapitalSnapshot, error) {
	if c == nil {
		return RuntimeCapitalSnapshot{}, fmt.Errorf("后台控制器未初始化")
	}
	// 非币安所走 venue 适配器只读快照路径(币安以下逻辑不变)。
	if c.isNonBinanceVenue() {
		return c.currentVenueCapitalSnapshot()
	}
	c.mu.Lock()
	secretPath := strings.TrimSpace(c.status.SecretPath)
	cfg := c.cfg
	cachedSnapshot := c.cachedCapitalSnapshot
	cachedAt := c.cachedCapitalSnapshotAt
	cachedSecretKey := c.cachedCapitalSnapshotSecretKey
	c.mu.Unlock()
	if secretPath == "" {
		snapshot := RuntimeCapitalSnapshot{}
		applyConfiguredCapitalTargets(&snapshot, cfg)
		return snapshot, nil
	}
	cacheKey := secretPath + "|" + fmt.Sprintf("%.2f", configuredTradingCapitalUSDT(cfg))
	if cachedSecretKey == cacheKey && !cachedAt.IsZero() && time.Since(cachedAt) <= runtimeCapitalSnapshotCacheTTL {
		return cachedSnapshot, nil
	}
	ctx, cancel := context.WithTimeout(c.rootCtx, 10*time.Second)
	defer cancel()
	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		return RuntimeCapitalSnapshot{}, err
	}
	snapshot, err := engine.captureRuntimeCapitalSnapshot(ctx)
	if err != nil {
		if cachedSecretKey == cacheKey && !cachedAt.IsZero() && time.Since(cachedAt) <= runtimeCapitalSnapshotStaleTTL {
			if c.logger != nil {
				c.logger.Printf("读取账户资金快照失败，回退到 %.1fs 内缓存: %v", time.Since(cachedAt).Seconds(), err)
			}
			return cachedSnapshot, nil
		}
		return RuntimeCapitalSnapshot{}, err
	}
	applyConfiguredCapitalTargets(&snapshot, cfg)
	c.mu.Lock()
	c.cachedCapitalSnapshot = snapshot
	c.cachedCapitalSnapshotAt = time.Now()
	c.cachedCapitalSnapshotSecretKey = cacheKey
	c.mu.Unlock()
	return snapshot, nil
}
