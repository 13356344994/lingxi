package app

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

// capital_sweep.go 实现「调仓前清币归一」:面板点「调整仓位」时,先把账户里所有非白名单杂币
// 市价清成 USDT(币安保留 BNB、Bybit 保留 MNT 作手续费抵扣储备,绝不动),再执行原有的 USDT 落位。
// 这样历史残腿(如 ENA 现货残留)、借币负债(如 TAO 欠款)与零散粉尘都会在调仓时一并收敛,
// 调仓后的账户只剩 USDT + 手续费币,资金口径干净。
//
// 安全铁律:本地未平仓台账非空时拒绝清扫(否则会把在场对冲的现货腿卖掉,留下裸永续)。
// 调仓本身已要求引擎停止,这里是第二道闸。

// binanceSweepKeepAssets / bybitSweepKeepAssets 是清扫白名单:这些币永不卖出(USDT 是目标币,
// BNB/MNT 是各所的手续费抵扣币,按用户要求只留它们付手续费)。
var binanceSweepKeepAssets = map[string]bool{"USDT": true, "BNB": true}
var bybitSweepKeepAssets = map[string]bool{"USDT": true, "MNT": true}

const (
	sweepAccountSpot   = "spot"
	sweepAccountMargin = "cross_margin"

	sweepActionSell        = "sell"         // 市价卖出为 USDT
	sweepActionRepayBuy    = "repay_buy"    // 市价买入并自动还币(AUTO_REPAY,清借币负债)
	sweepActionRepayDirect = "repay_direct" // 用既有余额直接归还借款(USDT 借款专用,不经买卖)
)

// capitalSweepAction 是一条待执行的清币动作(纯数据,便于单测计划逻辑)。
type capitalSweepAction struct {
	Account string
	Asset   string
	Action  string
	Qty     float64
}

// planBinanceCapitalSweep 根据现货/全仓快照生成清币计划(纯函数):
//   - 现货:非白名单且有可用余额 → 市价卖出;
//   - 全仓:先还债(借币+利息 → AUTO_REPAY 买入还币),再把非白名单可用余额市价卖出;
//   - 白名单(USDT/BNB)永不动;数量是否可下单(step/minNotional)由执行层结合交易规则判定。
func planBinanceCapitalSweep(spotFree map[string]float64, margin CrossMarginAccountState, keep map[string]bool) []capitalSweepAction {
	actions := make([]capitalSweepAction, 0, 8)
	assets := make([]string, 0, len(spotFree))
	for asset := range spotFree {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		name := strings.ToUpper(strings.TrimSpace(asset))
		if name == "" || keep[name] || spotFree[asset] <= executionQtyTolerance {
			continue
		}
		actions = append(actions, capitalSweepAction{Account: sweepAccountSpot, Asset: name, Action: sweepActionSell, Qty: spotFree[asset]})
	}

	marginAssets := make([]string, 0, len(margin.Assets))
	for asset := range margin.Assets {
		marginAssets = append(marginAssets, asset)
	}
	sort.Strings(marginAssets)
	for _, asset := range marginAssets {
		entry := margin.Assets[asset]
		name := strings.ToUpper(strings.TrimSpace(entry.Asset))
		if name == "USDT" {
			// USDT 借款用既有 USDT 余额直接归还(不经买卖),否则「可调 USDT」里混着借款,
			// 调仓会把借来的钱当成自有资金搬走,留下一笔吃利息的隐性负债。
			if debt := entry.Borrowed + entry.Interest; debt > executionQtyTolerance && entry.Free > executionQtyTolerance {
				actions = append(actions, capitalSweepAction{Account: sweepAccountMargin, Asset: name, Action: sweepActionRepayDirect, Qty: math.Min(debt, entry.Free)})
			}
			continue
		}
		if name == "" {
			continue
		}
		debt := entry.Borrowed + entry.Interest
		if debt > executionQtyTolerance {
			// 买入全部负债量并 AUTO_REPAY:不依赖既有余额,买到即还,杜绝 -3006(还币买入不借币)。
			actions = append(actions, capitalSweepAction{Account: sweepAccountMargin, Asset: name, Action: sweepActionRepayBuy, Qty: debt})
		}
		if keep[name] {
			continue
		}
		if entry.Free > executionQtyTolerance {
			actions = append(actions, capitalSweepAction{Account: sweepAccountMargin, Asset: name, Action: sweepActionSell, Qty: entry.Free})
		}
	}
	return actions
}

// planBybitCapitalSweep 根据统一账户余额生成 Bybit 清币计划(纯函数):非白名单(USDT/MNT)且有
// 可用余额 → 现货市价卖出。Bybit UTA 无独立借贷腿需要处理(借币空头平仓时已还)。
func planBybitCapitalSweep(balances venue.Balances, keep map[string]bool) []capitalSweepAction {
	actions := make([]capitalSweepAction, 0, 4)
	assets := make([]string, 0, len(balances.ByAsset))
	for asset := range balances.ByAsset {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		name := strings.ToUpper(strings.TrimSpace(asset))
		b := balances.ByAsset[asset]
		if name == "" || keep[name] || b.Free <= executionQtyTolerance {
			continue
		}
		actions = append(actions, capitalSweepAction{Account: sweepAccountSpot, Asset: name, Action: sweepActionSell, Qty: b.Free})
	}
	return actions
}

// sweepResidualAssetsToUSDT 执行币安清币归一:台账空仓校验 → 拉规则/同步时间 → 计划 → 逐条市价执行。
// 返回人类可读的执行报告(成功+跳过+失败),失败不阻断后续调仓(USDT 划转仍照常),由报告透出。
func (e *FundingRateSniperEngine) sweepResidualAssetsToUSDT(ctx context.Context) ([]string, error) {
	hedges, err := e.loadPersistedOpenHedges()
	if err != nil {
		return nil, fmt.Errorf("读取未平仓台账失败,拒绝清币: %w", err)
	}
	if len(hedges) > 0 {
		return nil, fmt.Errorf("本地仍有 %d 笔未平仓对冲,拒绝清币(先平仓或人工核对台账)", len(hedges))
	}
	if err := e.prepareForEmergencyFlatten(ctx); err != nil {
		return nil, fmt.Errorf("清币准备(时间同步/交易规则)失败: %w", err)
	}

	report := make([]string, 0, 8)

	// 第一步:平掉所有存量合约仓位(市价 reduceOnly)。台账为空时交易所上任何合约仓位都是
	// 事故残腿(如 2026-06-10 DEXE 裸空头:开仓熔断后未入台账,卡死启动前检查),按用户要求
	// 「调仓时除 BNB/MNT 外全部平掉」一并收敛,且必须先于现货清扫(裸腿是最大的价格风险)。
	_, _, futuresPositions, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取合约持仓失败: %w", err)
	}
	futuresSymbols := make([]string, 0, len(futuresPositions))
	for symbol := range futuresPositions {
		futuresSymbols = append(futuresSymbols, symbol)
	}
	sort.Strings(futuresSymbols)
	for _, symbol := range futuresSymbols {
		qty := futuresPositions[symbol]
		if math.Abs(qty) <= executionQtyTolerance {
			continue
		}
		line := e.closeBinanceFuturesPosition(ctx, symbol, qty)
		report = append(report, line)
		e.logger.Printf("[调仓清币] %s", line)
	}

	// 第二步:把资金账户里的非白名单杂币划回现货(资金账户不能直接交易,必须先 FUNDING_MAIN),
	// 随后与现货存量一起市价清掉。USDT 留在资金账户,由后续调仓划转统一落位。
	fundingFree, err := e.fetchFundingAccountFreeBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取资金账户余额失败: %w", err)
	}
	fundingAssets := make([]string, 0, len(fundingFree))
	for asset := range fundingFree {
		fundingAssets = append(fundingAssets, asset)
	}
	sort.Strings(fundingAssets)
	for _, asset := range fundingAssets {
		name := strings.ToUpper(strings.TrimSpace(asset))
		qty := fundingFree[asset]
		if name == "" || binanceSweepKeepAssets[name] || qty <= executionQtyTolerance {
			continue
		}
		line := ""
		if _, err := e.transferAssetBetweenAccounts(ctx, "FUNDING_MAIN", name, qty); err != nil {
			if strings.Contains(err.Error(), "-1102") {
				// 交易所按资产精度拒绝该划转(典型:PEPE 等粉尘资产),价值可忽略,降级为跳过。
				line = fmt.Sprintf("跳过 funding/%s:交易所拒绝粉尘划转(%.8f,精度/最小额不满足),忽略", name, qty)
			} else {
				line = fmt.Sprintf("失败 funding/%s:划回现货失败(%v)", name, err)
			}
		} else {
			line = fmt.Sprintf("完成 funding/%s:%.8f 已划回现货待清", name, qty)
		}
		report = append(report, line)
		e.logger.Printf("[调仓清币] %s", line)
	}

	spotFree, _, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取现货余额失败: %w", err)
	}
	marginState, err := e.fetchCrossMarginAccountState(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取全仓账户失败: %w", err)
	}

	actions := planBinanceCapitalSweep(spotFree, marginState, binanceSweepKeepAssets)
	if len(actions) == 0 && len(report) == 0 {
		return []string{"无杂币需要清扫(仅 USDT/BNB)"}, nil
	}

	for _, action := range actions {
		line := e.executeBinanceSweepAction(ctx, action)
		report = append(report, line)
		e.logger.Printf("[调仓清币] %s", line)
	}
	return report, nil
}

// sweepReportHasExecution 判断清币报告里是否有真实成交/划转(决定快照是否需要等待入账)。
func sweepReportHasExecution(report []string) bool {
	for _, line := range report {
		if strings.HasPrefix(line, "完成") {
			return true
		}
	}
	return false
}

// closeBinanceFuturesPosition 市价 reduceOnly 平掉一笔 U 本位合约仓位(正=多→卖,负=空→买回),返回一行报告。
func (e *FundingRateSniperEngine) closeBinanceFuturesPosition(ctx context.Context, symbol string, signedQty float64) string {
	side := "SELL"
	if signedQty < 0 {
		side = "BUY"
	}
	qty := math.Abs(signedQty)
	rules := e.futuresRules[symbol]
	formatted := formatQuantityForRules(qty, rules, 0)
	if parseFloat(formatted) <= executionQtyTolerance {
		return fmt.Sprintf("跳过 futures/%s:数量 %.8f 取整后为 0(粉尘仓位)", symbol, qty)
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", formatted)
	params.Set("reduceOnly", "true")
	if _, err := e.signedRequest(ctx, "POST", e.client.FuturesBaseURL()+binance.PathFuturesOrder, params); err != nil {
		return fmt.Sprintf("失败 futures/%s:市价平仓 %s %s 失败(%v)", symbol, side, formatted, err)
	}
	return fmt.Sprintf("完成 futures/%s:存量仓位 %.8f 已市价平掉(%s reduceOnly)", symbol, signedQty, side)
}

// executeBinanceSweepAction 执行单条清币动作,返回一行报告。任何失败只记录不中断(其余动作继续)。
func (e *FundingRateSniperEngine) executeBinanceSweepAction(ctx context.Context, action capitalSweepAction) string {
	if action.Action == sweepActionRepayDirect {
		params := url.Values{}
		params.Set("asset", action.Asset)
		params.Set("amount", formatFloat(action.Qty))
		params.Set("isIsolated", "FALSE")
		if _, err := e.signedRequest(ctx, "POST", e.client.SpotBaseURL()+"/sapi/v1/margin/repay", params); err != nil {
			return fmt.Sprintf("失败 %s/%s:归还借款 %.8f 失败(%v)", action.Account, action.Asset, action.Qty, err)
		}
		return fmt.Sprintf("完成 %s/%s:归还借款 %.8f", action.Account, action.Asset, action.Qty)
	}
	symbol := action.Asset + "USDT"
	rules, ok := e.spotRules[symbol]
	if !ok {
		return fmt.Sprintf("跳过 %s/%s:无 %s 现货交易对,无法转换", action.Account, action.Asset, symbol)
	}
	ticker, err := e.fetchSpotBookTicker(ctx, symbol)
	if err != nil {
		return fmt.Sprintf("失败 %s/%s:取价失败(%v)", action.Account, action.Asset, err)
	}
	refPrice := parseFloat(ticker.BidPrice)

	var qty float64
	switch action.Action {
	case sweepActionRepayBuy:
		qty = ceilToStep(action.Qty, rules.StepSize) // 还债宁多勿少
		if qty < rules.MinQty {
			qty = rules.MinQty
		}
		refPrice = parseFloat(ticker.AskPrice)
	default:
		qty = normalizeQuantity(action.Qty, rules, 0) // 卖出向下取整,绝不超卖
	}
	if qty <= executionQtyTolerance || !isQuantityTradable(qty, refPrice, rules) {
		return fmt.Sprintf("跳过 %s/%s:数量 %.8f 低于最小可下单门槛(粉尘,约 %.2f USDT)", action.Account, action.Asset, qty, qty*refPrice)
	}

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("type", "MARKET")
	params.Set("quantity", formatQuantityForRules(qty, rules, 0))
	params.Set("newOrderRespType", "FULL")
	endpoint := e.client.SpotBaseURL() + binance.PathSpotOrder
	verb := "卖出"
	switch action.Action {
	case sweepActionRepayBuy:
		params.Set("side", "BUY")
		params.Set("isIsolated", "FALSE")
		params.Set("sideEffectType", "AUTO_REPAY")
		endpoint = e.client.SpotBaseURL() + binance.PathMarginOrder
		verb = "买入还币"
	default:
		params.Set("side", "SELL")
		if action.Account == sweepAccountMargin {
			params.Set("isIsolated", "FALSE")
			endpoint = e.client.SpotBaseURL() + binance.PathMarginOrder
		}
	}
	if _, err := e.placeSignedOrder(ctx, endpoint, params); err != nil {
		return fmt.Sprintf("失败 %s/%s:%s %.8f 下单失败(%v)", action.Account, action.Asset, verb, qty, err)
	}
	return fmt.Sprintf("完成 %s/%s:市价%s %.8f(约 %.2f USDT)", action.Account, action.Asset, verb, qty, qty*refPrice)
}

// bybitSweepVenue 是 Bybit 清币所需的最小交易接口(由 bybit.Adapter 满足;测试可注入 fake)。
type bybitSweepVenue interface {
	FetchBalances(ctx context.Context) (venue.Balances, error)
	FetchInstruments(ctx context.Context) ([]venue.Instrument, error)
	FetchPositions(ctx context.Context) ([]venue.Position, error)
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
}

// closeBybitLedgerPositionsForSweep 把台账里的在场持仓走「正规平仓」收敛:CloseDeltaNeutral
// 两腿一起平 → 写统一结算流水 → 删台账。任何一笔平仓失败都立即中止(绝不在有活腿的情况下
// 继续清扫现货,否则会把对冲现货腿卖掉留下裸永续)。
func (c *RuntimeController) closeBybitLedgerPositionsForSweep(ctx context.Context, trader bybitTrader) ([]string, error) {
	store, err := newBybitPositionStore(c.cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, fmt.Errorf("打开 Bybit 持仓台账失败: %w", err)
	}
	positions, err := store.load()
	if err != nil {
		return nil, fmt.Errorf("读取 Bybit 持仓台账失败: %w", err)
	}
	if len(positions) == 0 {
		return nil, nil
	}
	executor := NewBybitLiveExecutor(trader, func(format string, args ...any) {
		c.appendControllerLog("[调仓平仓] "+format, args...)
	}, c.cfg.NetworkConfig.RecordDir)
	report := make([]string, 0, len(positions))
	for _, p := range positions {
		closeRes, cerr := executor.CloseDeltaNeutral(ctx, p)
		if cerr != nil {
			return report, fmt.Errorf("平仓 %s 失败,中止清币(保留台账下次重试): %w", p.Symbol, cerr)
		}
		c.appendBybitSettlement(p, closeRes, 0, time.Now().UTC())
		if rerr := store.remove(p.Symbol); rerr != nil {
			return report, fmt.Errorf("平仓 %s 成功但删除台账失败: %w", p.Symbol, rerr)
		}
		line := fmt.Sprintf("完成 平仓 %s:实现盈亏≈%.4f USDT(资金费%.4f+价差%.4f-费用%.4f)",
			p.Symbol, closeRes.RealizedPnL, closeRes.RealFunding, closeRes.PricePnL, closeRes.TotalFeesUSDT)
		report = append(report, line)
		c.appendControllerLog("[调仓平仓] %s", line)
	}
	return report, nil
}

// sweepBybitResidualAssetsToUSDT 执行 Bybit 清币归一:本地 Bybit 台账空仓校验(调用方须先走
// closeBybitLedgerPositionsForSweep)→ 平掉存量永续孤儿 → 现货市价卖出所有非白名单(USDT/MNT)余额。
// 返回报告;单条失败不阻断其余动作与后续调仓。
func sweepBybitResidualAssetsToUSDT(ctx context.Context, adp bybitSweepVenue, recordDir string, logf func(string, ...any)) ([]string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	store, err := newBybitPositionStore(recordDir)
	if err != nil {
		return nil, fmt.Errorf("打开 Bybit 持仓台账失败,拒绝清币: %w", err)
	}
	positions, err := store.load()
	if err != nil {
		return nil, fmt.Errorf("读取 Bybit 持仓台账失败,拒绝清币: %w", err)
	}
	if len(positions) > 0 {
		return nil, fmt.Errorf("Bybit 本地仍有 %d 笔在场持仓,拒绝清币(先停止任务并平仓)", len(positions))
	}

	report := make([]string, 0, 8)

	// 第一步:平掉所有存量永续仓位(台账为空时它们都是残腿/孤儿,市价 reduceOnly 收敛)。
	perpPositions, err := adp.FetchPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Bybit 永续持仓失败: %w", err)
	}
	for _, p := range perpPositions {
		if math.Abs(p.PositionAmt) <= executionQtyTolerance {
			continue
		}
		side := venue.SideSell
		if p.PositionAmt < 0 {
			side = venue.SideBuy
		}
		_, perr := adp.PlaceOrder(ctx, venue.OrderRequest{
			Market:     venue.MarketPerp,
			Symbol:     p.Symbol,
			Side:       side,
			Type:       venue.OrderTypeMarket,
			Qty:        math.Abs(p.PositionAmt),
			ReduceOnly: true,
		})
		line := ""
		if perr != nil {
			line = fmt.Sprintf("失败 perp/%s:存量仓位 %.8f 市价平仓失败(%v)", p.Symbol, p.PositionAmt, perr)
		} else {
			line = fmt.Sprintf("完成 perp/%s:存量仓位 %.8f 已市价平掉(reduceOnly)", p.Symbol, p.PositionAmt)
		}
		report = append(report, line)
		logf("[调仓清币] %s", line)
	}

	balances, err := adp.FetchBalances(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Bybit 余额失败: %w", err)
	}
	actions := planBybitCapitalSweep(balances, bybitSweepKeepAssets)
	if len(actions) == 0 {
		if len(report) == 0 {
			return []string{"无杂币需要清扫(仅 USDT/MNT)"}, nil
		}
		return report, nil
	}

	instruments, err := adp.FetchInstruments(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Bybit 交易规则失败: %w", err)
	}
	spotBySymbol := make(map[venue.Symbol]venue.Instrument, len(instruments))
	for _, in := range instruments {
		if in.Market == venue.MarketSpot {
			spotBySymbol[in.Symbol] = in
		}
	}

	for _, action := range actions {
		sym := venue.Symbol(action.Asset + "-USDT")
		in, ok := spotBySymbol[sym]
		if !ok {
			line := fmt.Sprintf("跳过 %s:无 %s 现货交易对,无法转换", action.Asset, sym)
			report = append(report, line)
			logf("[调仓清币] %s", line)
			continue
		}
		qty := action.Qty
		if in.StepSize > 0 {
			qty = math.Floor(qty/in.StepSize+1e-9) * in.StepSize
		}
		if qty <= executionQtyTolerance {
			line := fmt.Sprintf("跳过 %s:余额 %.8f 取整后为 0(粉尘)", action.Asset, action.Qty)
			report = append(report, line)
			logf("[调仓清币] %s", line)
			continue
		}
		_, err := adp.PlaceOrder(ctx, venue.OrderRequest{
			Market: venue.MarketSpot,
			Symbol: sym,
			Side:   venue.SideSell,
			Type:   venue.OrderTypeMarket,
			Qty:    qty,
		})
		line := ""
		if err != nil {
			line = fmt.Sprintf("失败 %s:市价卖出 %.8f 下单失败(%v)", action.Asset, qty, err)
		} else {
			line = fmt.Sprintf("完成 %s:市价卖出 %.8f", action.Asset, qty)
		}
		report = append(report, line)
		logf("[调仓清币] %s", line)
	}
	return report, nil
}

// summarizeSweepReport 把清币报告压成一段话拼进调仓结果消息。
func summarizeSweepReport(report []string) string {
	if len(report) == 0 {
		return ""
	}
	return "清币归一:" + strings.Join(report, ";") + "。"
}
