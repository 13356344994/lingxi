package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// bybit_exec_live.go 是 Bybit 精简执行器的 **live 写路径(B3)**:用真实下单接口执行正 carry 的 delta-neutral
// 开/平仓。设计铁律:
//   - **幂等**:每条腿用确定性 clientOrderId(idemPrefix+后缀),重试不会重复成交(交易所按 orderLinkId 去重)。
//   - **绝不裸腿**:正 carry 开仓「永续空腿先行→确认成交→现货多腿对冲」,现货腿失败 → 立即回补永续
//     (reduceOnly)回到平;负 carry 开仓「借币现货空腿先行→确认成交→永续多腿对冲」(借币是更容易失败的腿,
//     先验证它,170207 借不到时零敞口干净放弃),永续腿失败 → 立即买回现货还币回到平。
//   - **只读确认**:下单后用 QueryOrder 轮询终态,确认成交数量,不盲信受理回执。
//   - **类型隔离**:依赖最小 bybitTrader 接口(*bybit.Adapter 满足),离线用 fake 单测,杜绝误发单。
//
// 正 carry(统一账户、无借币):空高正费率永续(空头收资金费)+ 等额多现货,delta 中性;跨结算持有后双边平。

// executionQtyTol 复用包内既有的极小量容差(executionQtyTolerance,funding_sniper 引擎定义),此处别名以自解释。

// bybitTrader 是 live 执行器所需的最小读写能力子集(*bybit.Adapter 天然满足)。
type bybitTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	FetchPositions(ctx context.Context) ([]venue.Position, error)
	// FetchBalances / FetchInstruments 供平仓时按「真实可用现货余额」卖出(Bybit 现货买入 taker 费在 base
	// 资产里扣,实际持有的 base < 开仓名义 → 必须按真实余额卖,否则撞 -170131 "Insufficient balance" 死循环)。
	FetchBalances(ctx context.Context) (venue.Balances, error)
	FetchInstruments(ctx context.Context) ([]venue.Instrument, error)
	// FetchRealizedFunding 拉某标的自 since 起的真实资金费结算流水,供平仓时给出真实资金费收入与实现盈亏。
	FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error)
}

// BybitLivePosition 是一笔已开的 delta-neutral 持仓(落盘用于跨重启 reconcile/平仓)。
type BybitLivePosition struct {
	Symbol          venue.Symbol `json:"symbol"`
	BaseQty         float64      `json:"base_qty"`
	SpotEntryPrice  float64      `json:"spot_entry_price"`
	PerpEntryPrice  float64      `json:"perp_entry_price"`
	SpotNotional    float64      `json:"spot_notional"`
	PerpNotional    float64      `json:"perp_notional"`
	FundingRate     float64      `json:"funding_rate"`
	NextFundingTime time.Time    `json:"next_funding_time"`
	OpenedAt        time.Time    `json:"opened_at"`
	RunID           string       `json:"run_id"`
	IdemPrefix      string       `json:"idem_prefix"`
	// Negative=true 表示负 carry 持仓(多永续 + 借币做空现货);false(零值,向后兼容旧记录)=正 carry。
	// 平仓方向、PnL 符号、续持复核都据此区分。
	Negative bool `json:"negative,omitempty"`
	// SettlementsHeld 已收过资金费的结算次数(滚动持有计数);开仓后首个结算收一次记 1。
	SettlementsHeld int `json:"settlements_held"`
	// ForceClose=true 表示「立即到期、不续持」的待平残腿(开仓回补失败的裸腿/收编的孤儿仓)。
	// 旧版本用 SettlementsHeld=1<<30 当哨兵表达同一语义,导致平仓日志/结算台账出现
	// 「持有 1073741825 次结算」「期望资金费上亿」的荒谬显示;台账加载时兼容收编旧哨兵。
	ForceClose bool `json:"force_close,omitempty"`
	// 开仓真实实扣手续费(含 MNT 抵扣后的真实口径)+ 计费币种,用于平仓时算「开+平真实总费」与实现净利。
	OpenPerpFee      float64 `json:"open_perp_fee,omitempty"`
	OpenPerpFeeAsset string  `json:"open_perp_fee_asset,omitempty"`
	OpenSpotFee      float64 `json:"open_spot_fee,omitempty"`
	OpenSpotFeeAsset string  `json:"open_spot_fee_asset,omitempty"`
}

// bybitCloseResult 是一次平仓的真实成交明细(供详细通知/日志用实扣口径,而非估算)。
type bybitCloseResult struct {
	PerpClosePrice float64
	PerpCloseFee   float64
	PerpFeeAsset   string
	SpotClosePrice float64
	SpotCloseFee   float64
	SpotFeeAsset   string
	SpotSoldQty    float64
	// 真实实现盈亏(尽力):RealFunding=真实收到资金费(查流水);PricePnL=两腿价差盈亏;
	// TotalFeesUSDT=开+平真实手续费折 USDT;RealizedPnL=PricePnL+RealFunding−TotalFeesUSDT。FundingOK=资金费流水是否查到。
	RealFunding   float64
	SpotPricePnL  float64
	PerpPricePnL  float64
	PricePnL      float64
	TotalFeesUSDT float64
	RealizedPnL   float64
	FundingOK     bool
}

// BybitLiveExecutor 用真实下单接口执行 delta-neutral 开/平仓。
type BybitLiveExecutor struct {
	trader         bybitTrader
	logf           func(string, ...any)
	confirmTimeout time.Duration // 单腿成交确认总时限
	confirmPoll    time.Duration // 轮询间隔
	now            func() time.Time
	costRecordPath string        // 真实成交成本(实扣手续费 + 滑点)JSONL 落盘路径;空=不记录
	perpMakerFirst bool          // 永续腿 post-only 追价优先(taker 兜底);默认 false=纯 taker
	makerWait      time.Duration // 每轮 post-only 挂单等待成交时长(默认 bybitMakerAttemptWait;测试注入)
}

// SetPerpMakerFirst 开关永续腿 maker 优先执行(开/平仓两腿;现货 maker 与 taker 同费率,不做)。
func (e *BybitLiveExecutor) SetPerpMakerFirst(on bool) { e.perpMakerFirst = on }

// NewBybitLiveExecutor 构造 live 执行器。logf 为空时用 no-op;超时/轮询为 0 时用保守默认。
// recordDir 非空时,把每条腿的「真实实扣手续费 + 相对下单中价的滑点」追加到 <recordDir>/execution_costs.jsonl。
func NewBybitLiveExecutor(trader bybitTrader, logf func(string, ...any), recordDir string) *BybitLiveExecutor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	costPath := ""
	if strings.TrimSpace(recordDir) != "" {
		costPath = filepath.Join(recordDir, "execution_costs.jsonl")
	}
	return &BybitLiveExecutor{
		trader:         trader,
		logf:           logf,
		confirmTimeout: 8 * time.Second,
		confirmPoll:    400 * time.Millisecond,
		now:            func() time.Time { return time.Now().UTC() },
		costRecordPath: costPath,
		makerWait:      bybitMakerAttemptWait,
	}
}

// bybitExecutionCost 是一条腿的真实成交成本记录(实扣手续费 + 滑点),用于「估算 vs 真实」成本对照。
type bybitExecutionCost struct {
	RecordedAt   string  `json:"recorded_at"`
	Symbol       string  `json:"symbol"`
	Phase        string  `json:"phase"` // open / close
	Leg          string  `json:"leg"`   // perp / spot
	Side         string  `json:"side"`
	Qty          float64 `json:"qty"`
	AvgPrice     float64 `json:"avg_price"`
	RefPrice     float64 `json:"ref_price,omitempty"`     // 下单时中价(仅开仓腿有;用于算滑点)
	SlippageRate float64 `json:"slippage_rate,omitempty"` // (按方向)不利滑点占比,正=吃亏
	NotionalUSDT float64 `json:"notional_usdt"`
	Fee          float64 `json:"fee"`       // 交易所实扣手续费(含 BNB/MNT 抵扣后的真实口径)
	FeeAsset     string  `json:"fee_asset"` // 计费币种(注意:可能为 base 币/MNT,费率换算需按币种)
}

// recordExecutionCost 把一条腿的真实成交成本追加到 execution_costs.jsonl(失败仅告警,绝不影响交易)。
func (e *BybitLiveExecutor) recordExecutionCost(phase, leg string, side venue.Side, refPrice float64, res venue.OrderResult) {
	if e.costRecordPath == "" || res.ExecutedQty <= 0 {
		return
	}
	rec := bybitExecutionCost{
		RecordedAt:   e.now().Format(time.RFC3339),
		Symbol:       string(res.Symbol),
		Phase:        phase,
		Leg:          leg,
		Side:         string(side),
		Qty:          res.ExecutedQty,
		AvgPrice:     res.AvgPrice,
		RefPrice:     refPrice,
		SlippageRate: directionalSlippageRate(side, res.AvgPrice, refPrice),
		NotionalUSDT: res.CumQuoteUSDT,
		Fee:          res.Fee,
		FeeAsset:     res.FeeAsset,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(e.costRecordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		e.logf("[bybit-live] 记录成交成本失败: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// directionalSlippageRate 返回相对下单中价的「不利滑点」占比(买:成交价更高=吃亏;卖:成交价更低=吃亏)。ref<=0 时返回 0。
func directionalSlippageRate(side venue.Side, avg, ref float64) float64 {
	if ref <= 0 || avg <= 0 {
		return 0
	}
	if side == venue.SideBuy {
		return (avg - ref) / ref
	}
	return (ref - avg) / ref
}

// idem 生成某条腿的确定性 clientOrderId(≤36 字符,Bybit orderLinkId 限制内)。
func bybitLegClientID(prefix, leg string) string {
	id := prefix + "-" + leg
	if len(id) > 36 {
		id = id[len(id)-36:] // 取后缀保留腿标识
	}
	return id
}

// confirmFill 轮询 QueryOrder 直至终态;filled→成功,rejected/canceled→失败,超时→返回最后状态 + 超时错误。
func (e *BybitLiveExecutor) confirmFill(ctx context.Context, m venue.Market, s venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.now().Add(e.confirmTimeout)
	var last venue.OrderResult
	for {
		res, err := e.trader.QueryOrder(ctx, m, s, clientID)
		if err == nil {
			last = res
			switch res.Status {
			case "filled":
				return res, nil
			case "rejected", "canceled":
				return res, fmt.Errorf("订单终态=%s code=%s", res.Status, res.RawCode)
			}
		}
		if e.now().After(deadline) {
			if err != nil {
				return last, fmt.Errorf("确认成交超时(最后查询失败): %w", err)
			}
			return last, fmt.Errorf("确认成交超时,最后状态=%s 已成交=%.8f", last.Status, last.ExecutedQty)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(e.confirmPoll):
		}
	}
}

// bybitIdemStaleSlack 是幂等撞单判定的时钟容差:PlaceOrder 撞重复 orderLinkId 后查到的订单,
// 若创建时间早于「本次下单尝试开始时刻 - 容差」,说明撞上的是历史旧单(典型:同一 symbol+
// 结算窗口的仓位被平掉后再开,幂等前缀完全相同),绝不能按旧单的成交回执确认——否则产生
// 「台账有仓、交易所没仓」的幻影持仓(2026-06-10 MBOX-USDT 实锤:幻影记录占死并发名额 +
// 平仓死循环 2 小时)。崩溃重启后的真实在场腿由 reconcile 孤儿认领兜底,平仓重试由
// CloseDeltaNeutral 的真实仓位/余额自愈兜底,故这里可以放心从严。
const bybitIdemStaleSlack = 30 * time.Second

// placeAndConfirm 下单(幂等:重复 clientOrderId 视为已受理)后确认成交。
// 撞重复 ID 时会校验查到订单的创建时间,命中历史旧单则报错而非按旧成交确认。
func (e *BybitLiveExecutor) placeAndConfirm(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	attemptStart := e.now()
	duplicate := false
	if _, err := e.trader.PlaceOrder(ctx, req); err != nil {
		if !isBybitDuplicateOrderError(err) {
			return venue.OrderResult{}, err
		}
		duplicate = true
	}
	res, err := e.confirmFill(ctx, req.Market, req.Symbol, req.ClientOrderID)
	if err != nil {
		return res, err
	}
	if duplicate && !res.CreatedAt.IsZero() && res.CreatedAt.Before(attemptStart.Add(-bybitIdemStaleSlack)) {
		return venue.OrderResult{}, fmt.Errorf(
			"幂等ID %s 命中 %s 前创建的历史旧单(疑似同结算窗口重复开仓),拒绝按旧成交确认",
			req.ClientOrderID, attemptStart.Sub(res.CreatedAt).Truncate(time.Second))
	}
	return res, nil
}

// 永续腿 maker 追价参数:每次 post-only 挂单等 4s 不成交就撤单重挂;距硬截止(结算时刻)
// 保留 45s 给 taker 兜底;最多追 8 次。Bybit 永续 maker 0.02% vs taker 0.055%,每腿省 0.035%。
const (
	bybitMakerAttemptWait  = 4 * time.Second
	bybitMakerHardBuffer   = 45 * time.Second
	bybitMakerMaxAttempts  = 8
	bybitMakerCloseTimeout = 40 * time.Second // 平仓腿无结算死线,追价总预算
)

// bybitBookTickerSource 是可选能力:取盘口一档(maker 追价定价用)。适配器满足。
type bybitBookTickerSource interface {
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

// mergeOrderResults 合并多次成交(maker 部分成交 + taker 兜底)为一个结果:数量加权均价、费用累加。
func mergeOrderResults(a, b venue.OrderResult) venue.OrderResult {
	if a.ExecutedQty <= 0 {
		return b
	}
	if b.ExecutedQty <= 0 {
		return a
	}
	out := a
	total := a.ExecutedQty + b.ExecutedQty
	out.AvgPrice = (a.AvgPrice*a.ExecutedQty + b.AvgPrice*b.ExecutedQty) / total
	out.ExecutedQty = total
	out.CumQuoteUSDT = a.CumQuoteUSDT + b.CumQuoteUSDT
	out.Fee = a.Fee + b.Fee
	if out.FeeAsset == "" {
		out.FeeAsset = b.FeeAsset
	}
	out.Status = "filled"
	return out
}

// placePerpMakerFirst 永续腿「maker 优先」执行:post-only 挂最优被动价追价,不成交撤单重挂;
// 距 hardDeadline 不足缓冲或追满次数后,剩余数量市价 taker 兜底(用原幂等 ID,保持幂等语义)。
// 行情源不支持盘口/未启用 maker 时直接走原 taker 路径。崩溃残留的未成交挂单由 reconcile
// 孤儿检测兜底(成交会形成未登记持仓被发现,未成交挂单不形成仓位)。
func (e *BybitLiveExecutor) placePerpMakerFirst(ctx context.Context, req venue.OrderRequest, hardDeadline time.Time) (venue.OrderResult, error) {
	bts, ok := e.trader.(bybitBookTickerSource)
	if !e.perpMakerFirst || !ok {
		return e.placeAndConfirm(ctx, req)
	}
	if hardDeadline.IsZero() {
		hardDeadline = e.now().Add(bybitMakerCloseTimeout + bybitMakerHardBuffer)
	}
	var acc venue.OrderResult
	remaining := req.Qty
	for attempt := 1; attempt <= bybitMakerMaxAttempts; attempt++ {
		if !e.now().Add(e.makerWait + bybitMakerHardBuffer).Before(hardDeadline) {
			break // 时间不够再追一轮,留给 taker 兜底
		}
		bt, berr := bts.FetchBookTicker(ctx, venue.MarketPerp, req.Symbol)
		if berr != nil || bt.BidPrice <= 0 || bt.AskPrice <= 0 {
			break
		}
		price := bt.AskPrice // SELL 贴最优卖价(被动方)
		if req.Side == venue.SideBuy {
			price = bt.BidPrice
		}
		makerReq := req
		makerReq.Type = venue.OrderTypePostOnly
		makerReq.Price = price
		makerReq.Qty = remaining
		makerReq.ClientOrderID = bybitLegClientID(req.ClientOrderID, fmt.Sprintf("m%d", attempt))
		if _, perr := e.trader.PlaceOrder(ctx, makerReq); perr != nil {
			e.logf("[bybit-live][maker] %s 第%d次挂单失败(转下一轮/兜底): %v", req.Symbol, attempt, perr)
			break
		}
		// 等待成交窗口:成交→收针;post-only 因穿越被交易所取消→立刻下一轮。
		waitUntil := e.now().Add(e.makerWait)
		filled := false
		for e.now().Before(waitUntil) {
			res, qerr := e.trader.QueryOrder(ctx, venue.MarketPerp, req.Symbol, makerReq.ClientOrderID)
			if qerr == nil {
				if res.Status == "filled" {
					acc = mergeOrderResults(acc, res)
					remaining = 0
					filled = true
					break
				}
				if res.Status == "canceled" || res.Status == "rejected" {
					break // post-only 穿越被取消:换价重挂
				}
			}
			select {
			case <-ctx.Done():
				return acc, ctx.Err()
			case <-time.After(e.confirmPoll):
			}
		}
		if filled {
			break
		}
		// 未在窗口内全成:撤单并结算这一轮的部分成交。
		_ = e.trader.CancelOrder(ctx, venue.MarketPerp, req.Symbol, makerReq.ClientOrderID)
		if res, qerr := e.trader.QueryOrder(ctx, venue.MarketPerp, req.Symbol, makerReq.ClientOrderID); qerr == nil && res.ExecutedQty > 0 {
			acc = mergeOrderResults(acc, res)
			remaining -= res.ExecutedQty
		}
		if remaining <= executionQtyTolerance {
			break
		}
	}
	if remaining <= executionQtyTolerance && acc.ExecutedQty > 0 {
		e.logf("[bybit-live][maker] %s 全量 maker 成交 %.6f@%.6f(省 taker 差价)", req.Symbol, acc.ExecutedQty, acc.AvgPrice)
		return acc, nil
	}
	// taker 兜底:剩余数量按原幂等 ID 市价吃单。
	takerReq := req
	takerReq.Qty = remaining
	res, err := e.placeAndConfirm(ctx, takerReq)
	if err != nil {
		if acc.ExecutedQty > 0 {
			// maker 已有部分成交,兜底失败:返回已成交部分+错误,调用方按部分成交路径对齐两腿。
			return acc, fmt.Errorf("maker 部分成交 %.6f 后 taker 兜底失败: %w", acc.ExecutedQty, err)
		}
		return res, err
	}
	return mergeOrderResults(acc, res), nil
}

// OpenDeltaNeutral 开 delta-neutral 仓:
//   - 正 carry:永续空腿先行→确认→现货多腿对冲;现货失败立即回补永续(不留裸腿)。
//   - 负 carry:借币现货空腿先行(更容易失败的腿:借币池可能枯竭,170207),成交后再开永续多腿;
//     现货失败时零敞口干净放弃,永续失败时买回现货还币(见 openNegativeCarrySpotFirst)。
//
// idemPrefix 须在同一持仓的开/平/回补间稳定(由调用方用 runID+cycle+symbol 派生),保证幂等。
func (e *BybitLiveExecutor) OpenDeltaNeutral(ctx context.Context, plan BybitOpenPlan, idemPrefix, runID string) (BybitLivePosition, error) {
	if plan.BaseQty <= executionQtyTolerance {
		return BybitLivePosition{}, fmt.Errorf("开仓数量过小: %.10f", plan.BaseQty)
	}
	e.maybeSetLeverage(ctx, plan)
	if plan.Negative {
		return e.openNegativeCarrySpotFirst(ctx, plan, idemPrefix, runID)
	}
	// 正 carry:空永续(收正费率)+ 多现货。
	perpOpenSide, spotOpenSide := venue.SideSell, venue.SideBuy
	spotMargin := venue.MarginEffectNone
	perpRevertSide := venue.SideBuy // 现货失败时回补永续的反向
	// 第一腿:永续(收资金费的腿)先行。
	perpReq := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        plan.Symbol,
		Side:          perpOpenSide,
		Type:          venue.OrderTypeMarket,
		Qty:           plan.BaseQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "op"),
	}
	perpExec, err := e.placePerpMakerFirst(ctx, perpReq, plan.NextFundingTime)
	if err != nil {
		if perpExec.ExecutedQty > executionQtyTolerance {
			// maker 部分成交后兜底失败:已有真实永续敞口,立即按已成交量走现货对冲(下方逻辑按
			// hedgeQty=ExecutedQty 对齐),绝不丢着裸腿直接返回。
			e.logf("[bybit-live][开仓] %s 永续腿部分成交 %.6f 后出错(按已成交量继续对冲): %v", plan.Symbol, perpExec.ExecutedQty, err)
		} else {
			// 永续腿未确认成交:尝试撤单(幂等),不进入现货腿 → 无对冲敞口。reconcile 兜底核对真实持仓。
			_ = e.trader.CancelOrder(ctx, venue.MarketPerp, plan.Symbol, perpReq.ClientOrderID)
			return BybitLivePosition{}, fmt.Errorf("永续腿开仓失败(未建仓): %w", err)
		}
	}
	if perpExec.ExecutedQty <= executionQtyTolerance {
		return BybitLivePosition{}, fmt.Errorf("永续腿成交数量为 0,放弃")
	}
	hedgeQty := perpExec.ExecutedQty
	e.recordExecutionCost("open", "perp", perpOpenSide, plan.PerpPrice, perpExec)

	// 第二腿:现货对冲腿(正 carry 多现货 / 负 carry 借币做空现货),按永续实际成交量对齐。
	spotReq := venue.OrderRequest{
		Market:        venue.MarketSpot,
		Symbol:        plan.Symbol,
		Side:          spotOpenSide,
		Type:          venue.OrderTypeMarket,
		Qty:           hedgeQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "os"),
		MarginEffect:  spotMargin,
	}
	spotExec, spotErr := e.placeAndConfirm(ctx, spotReq)
	if spotErr != nil || spotExec.ExecutedQty <= executionQtyTolerance {
		// 现货腿失败 → 立即回补永续腿(反向 reduceOnly),回到平,绝不裸腿。
		revertReq := venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        plan.Symbol,
			Side:          perpRevertSide,
			Type:          venue.OrderTypeMarket,
			Qty:           hedgeQty,
			ClientOrderID: bybitLegClientID(idemPrefix, "rp"),
			ReduceOnly:    true,
		}
		_, revertErr := e.placeAndConfirm(ctx, revertReq)
		if revertErr != nil && isBybitPositionZeroError(revertErr) {
			// 仓位已为零 = 没有敞口需要回补(典型:永续腿其实是撞旧单的幻影成交,交易所本来就没仓)。
			// 视为已回补,绝不落「裸腿待平」的幻影记录。
			e.logf("[bybit-live][开仓] %s 永续回补返回仓位为零(110017),无敞口需回补", plan.Symbol)
			revertErr = nil
		}
		if revertErr != nil {
			// 回补也失败:留下永续空裸腿。返回带永续敞口的持仓 + 致命错误,调用方据此落盘待平,绝不失管。
			naked := BybitLivePosition{
				Symbol:          plan.Symbol,
				BaseQty:         hedgeQty,
				PerpEntryPrice:  perpExec.AvgPrice,
				PerpNotional:    perpExec.CumQuoteUSDT,
				FundingRate:     plan.FundingRate,
				NextFundingTime: plan.NextFundingTime,
				OpenedAt:        e.now(),
				RunID:           runID,
				IdemPrefix:      idemPrefix,
				Negative:        plan.Negative,
			}
			return naked, fmt.Errorf("现货腿失败且永续回补失败(裸腿风险!): spotErr=%v revertErr=%w", spotErr, revertErr)
		}
		return BybitLivePosition{}, fmt.Errorf("现货对冲腿失败,已回补永续腿(已平): %v", firstNonNilErr(spotErr, fmt.Errorf("现货成交为 0")))
	}
	e.recordExecutionCost("open", "spot", spotOpenSide, plan.SpotPrice, spotExec)

	// 修 FIX_PLAN #3:现货腿仅「部分成交」(< 永续对冲量)时,把永续多出的部分买回(reduceOnly 反向)让两腿严格对齐。
	// 否则会把「永续全量 + 现货半量」当成全量对冲落盘,差额=未对冲的裸永续腿(对冲系统最危险的静默敞口)。
	perpNotional := perpExec.CumQuoteUSDT
	if spotExec.ExecutedQty+executionQtyTolerance < hedgeQty {
		excess := hedgeQty - spotExec.ExecutedQty
		trimReq := venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        plan.Symbol,
			Side:          perpRevertSide,
			Type:          venue.OrderTypeMarket,
			Qty:           excess,
			ClientOrderID: bybitLegClientID(idemPrefix, "tp"),
			ReduceOnly:    true,
		}
		if _, trimErr := e.placeAndConfirm(ctx, trimReq); trimErr != nil {
			// 削减失败 → 残留 excess 的裸永续。按「永续全量」记持仓 + 返回错误:BaseQty=hedgeQty 让平仓按全永续
			// reduceOnly 收敛(现货按真实余额卖),调用方落盘待平/reconcile 兜底,绝不静默把半量当全量。
			naked := BybitLivePosition{
				Symbol:           plan.Symbol,
				BaseQty:          hedgeQty,
				SpotEntryPrice:   spotExec.AvgPrice,
				PerpEntryPrice:   perpExec.AvgPrice,
				SpotNotional:     spotExec.CumQuoteUSDT,
				PerpNotional:     perpExec.CumQuoteUSDT,
				FundingRate:      plan.FundingRate,
				NextFundingTime:  plan.NextFundingTime,
				OpenedAt:         e.now(),
				RunID:            runID,
				IdemPrefix:       idemPrefix,
				Negative:         plan.Negative,
				OpenPerpFee:      perpExec.Fee,
				OpenPerpFeeAsset: perpExec.FeeAsset,
				OpenSpotFee:      spotExec.Fee,
				OpenSpotFeeAsset: spotExec.FeeAsset,
			}
			return naked, fmt.Errorf("现货部分成交 %.6f/%.6f 且永续削减失败(残留裸永续 %.6f): %w", spotExec.ExecutedQty, hedgeQty, excess, trimErr)
		}
		e.logf("[bybit-live][开仓] %s 现货部分成交 %.6f/%.6f,已买回永续 %.6f 对齐两腿(避免裸永续)", plan.Symbol, spotExec.ExecutedQty, hedgeQty, excess)
		hedgeQty = spotExec.ExecutedQty
		perpNotional = hedgeQty * perpExec.AvgPrice // 名义按对齐后的真实持仓量重算
	}

	pos := BybitLivePosition{
		Symbol:           plan.Symbol,
		BaseQty:          hedgeQty,
		SpotEntryPrice:   spotExec.AvgPrice,
		PerpEntryPrice:   perpExec.AvgPrice,
		SpotNotional:     spotExec.CumQuoteUSDT,
		PerpNotional:     perpNotional,
		FundingRate:      plan.FundingRate,
		NextFundingTime:  plan.NextFundingTime,
		OpenedAt:         e.now(),
		RunID:            runID,
		IdemPrefix:       idemPrefix,
		Negative:         plan.Negative,
		OpenPerpFee:      perpExec.Fee,
		OpenPerpFeeAsset: perpExec.FeeAsset,
		OpenSpotFee:      spotExec.Fee,
		OpenSpotFeeAsset: spotExec.FeeAsset,
	}
	e.logf("[bybit-live][开仓] %s %s %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) + %s %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) 预估费率%.4f%% 结算%s",
		pos.Symbol,
		"永续空", perpExec.ExecutedQty, perpExec.AvgPrice, perpExec.CumQuoteUSDT, perpExec.Fee, perpExec.FeeAsset, directionalSlippageRate(perpOpenSide, perpExec.AvgPrice, plan.PerpPrice)*100,
		"现货多", spotExec.ExecutedQty, spotExec.AvgPrice, spotExec.CumQuoteUSDT, spotExec.Fee, spotExec.FeeAsset, directionalSlippageRate(spotOpenSide, spotExec.AvgPrice, plan.SpotPrice)*100,
		pos.FundingRate*100, pos.NextFundingTime.UTC().Format("15:04:05Z"))
	return pos, nil
}

// maybeSetLeverage 永续腿杠杆 >1x 时先设杠杆(降低保证金占用,放大可开名义)。设失败不阻塞开仓:
// 余额闸已按计划保证金校验过,真实杠杆不足时永续单会因保证金不足而失败,走正常失败路径。
func (e *BybitLiveExecutor) maybeSetLeverage(ctx context.Context, plan BybitOpenPlan) {
	if plan.PerpLeverage <= 1 {
		return
	}
	lv, ok := e.trader.(interface {
		SetLeverage(ctx context.Context, s venue.Symbol, lev int) error
	})
	if !ok {
		return
	}
	if lerr := lv.SetLeverage(ctx, plan.Symbol, plan.PerpLeverage); lerr != nil && !isBybitLeverageNotModified(lerr) {
		e.logf("[bybit-live][开仓] %s 设杠杆 %dx 失败(继续按现有杠杆尝试): %v", plan.Symbol, plan.PerpLeverage, lerr)
	}
}

// openNegativeCarrySpotFirst 负 carry 开仓(多永续收负费率 + 借币做空现货),**借币现货腿先行**:
// 借币是更容易失败的腿(借币池枯竭 → 170207 "The loan amount of the platform is not enough";
// 2026-06-12 HOME 实锤:永续先行时借币失败 → 白开永续又回补,纯烧手续费还短暂裸腿)。
//   - 现货借币卖出失败 → 账户零敞口,干净放弃(没动永续,一分手续费不烧);
//   - 永续多腿失败 → 立即买回现货还币(MarginEffect=Repay)回到平;
//   - 永续部分成交 → 买回现货多出部分对齐两腿,绝不留净敞口。
func (e *BybitLiveExecutor) openNegativeCarrySpotFirst(ctx context.Context, plan BybitOpenPlan, idemPrefix, runID string) (BybitLivePosition, error) {
	// 第一腿:现货借币卖出(UTA spot margin 自动借入)。
	spotReq := venue.OrderRequest{
		Market:        venue.MarketSpot,
		Symbol:        plan.Symbol,
		Side:          venue.SideSell,
		Type:          venue.OrderTypeMarket,
		Qty:           plan.BaseQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "os"),
		MarginEffect:  venue.MarginEffectBorrow,
	}
	spotExec, spotErr := e.placeAndConfirm(ctx, spotReq)
	if spotErr != nil && spotExec.ExecutedQty <= executionQtyTolerance {
		// 借币腿未成交:撤单(幂等),零敞口放弃。借不到币(170207)在此干净失败,不再动永续。
		_ = e.trader.CancelOrder(ctx, venue.MarketSpot, plan.Symbol, spotReq.ClientOrderID)
		return BybitLivePosition{}, fmt.Errorf("现货借币卖出腿失败(未建仓,未动永续): %w", spotErr)
	}
	if spotExec.ExecutedQty <= executionQtyTolerance {
		return BybitLivePosition{}, fmt.Errorf("现货借币卖出成交数量为 0,放弃")
	}
	if spotErr != nil {
		e.logf("[bybit-live][开仓] %s 现货借币腿部分成交 %.6f 后出错(按已成交量继续对冲): %v", plan.Symbol, spotExec.ExecutedQty, spotErr)
	}
	hedgeQty := spotExec.ExecutedQty
	e.recordExecutionCost("open", "spot", venue.SideSell, plan.SpotPrice, spotExec)

	// 第二腿:永续多(收负费率的腿),按现货实际成交量对齐。
	perpReq := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        plan.Symbol,
		Side:          venue.SideBuy,
		Type:          venue.OrderTypeMarket,
		Qty:           hedgeQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "op"),
	}
	perpExec, perpErr := e.placePerpMakerFirst(ctx, perpReq, plan.NextFundingTime)
	if perpErr != nil && perpExec.ExecutedQty <= executionQtyTolerance {
		// 永续腿未成交 → 撤单 + 买回现货还币,回到零敞口。
		_ = e.trader.CancelOrder(ctx, venue.MarketPerp, plan.Symbol, perpReq.ClientOrderID)
		revertReq := venue.OrderRequest{
			Market:        venue.MarketSpot,
			Symbol:        plan.Symbol,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeMarket,
			Qty:           hedgeQty,
			ClientOrderID: bybitLegClientID(idemPrefix, "rs"),
			MarginEffect:  venue.MarginEffectRepay,
		}
		if _, revertErr := e.placeAndConfirm(ctx, revertReq); revertErr != nil {
			// 回补失败:留下现货空裸腿。返回带现货敞口的持仓 + 致命错误,调用方落盘待平
			// (CloseDeltaNeutral 会自愈:永续真实仓位为零 → 跳过,现货按 BaseQty 买回还币),绝不失管。
			naked := BybitLivePosition{
				Symbol:           plan.Symbol,
				BaseQty:          hedgeQty,
				SpotEntryPrice:   spotExec.AvgPrice,
				SpotNotional:     spotExec.CumQuoteUSDT,
				FundingRate:      plan.FundingRate,
				NextFundingTime:  plan.NextFundingTime,
				OpenedAt:         e.now(),
				RunID:            runID,
				IdemPrefix:       idemPrefix,
				Negative:         true,
				OpenSpotFee:      spotExec.Fee,
				OpenSpotFeeAsset: spotExec.FeeAsset,
			}
			return naked, fmt.Errorf("永续腿失败且现货买回还币失败(裸现货空腿!): perpErr=%v revertErr=%w", perpErr, revertErr)
		}
		return BybitLivePosition{}, fmt.Errorf("永续多腿失败,已买回现货还币(已平): %v", perpErr)
	}
	if perpErr != nil {
		e.logf("[bybit-live][开仓] %s 永续腿部分成交 %.6f 后出错(按已成交量对齐现货): %v", plan.Symbol, perpExec.ExecutedQty, perpErr)
	}
	e.recordExecutionCost("open", "perp", venue.SideBuy, plan.PerpPrice, perpExec)

	// 永续仅部分成交(< 现货借入量)时,买回现货多出部分还币,两腿严格对齐(对称于正 carry 的 FIX_PLAN #3)。
	spotNotional := spotExec.CumQuoteUSDT
	if perpExec.ExecutedQty+executionQtyTolerance < hedgeQty {
		excess := hedgeQty - perpExec.ExecutedQty
		trimReq := venue.OrderRequest{
			Market:        venue.MarketSpot,
			Symbol:        plan.Symbol,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeMarket,
			Qty:           excess,
			ClientOrderID: bybitLegClientID(idemPrefix, "ts"),
			MarginEffect:  venue.MarginEffectRepay,
		}
		if _, trimErr := e.placeAndConfirm(ctx, trimReq); trimErr != nil {
			// 削减失败 → 残留 excess 的裸现货空。按「现货全量」记 BaseQty 让平仓收敛(永续按真实仓位平),
			// 调用方落盘待平/reconcile 兜底,绝不静默把半量当全量。
			naked := BybitLivePosition{
				Symbol:           plan.Symbol,
				BaseQty:          hedgeQty,
				SpotEntryPrice:   spotExec.AvgPrice,
				PerpEntryPrice:   perpExec.AvgPrice,
				SpotNotional:     spotExec.CumQuoteUSDT,
				PerpNotional:     perpExec.CumQuoteUSDT,
				FundingRate:      plan.FundingRate,
				NextFundingTime:  plan.NextFundingTime,
				OpenedAt:         e.now(),
				RunID:            runID,
				IdemPrefix:       idemPrefix,
				Negative:         true,
				OpenPerpFee:      perpExec.Fee,
				OpenPerpFeeAsset: perpExec.FeeAsset,
				OpenSpotFee:      spotExec.Fee,
				OpenSpotFeeAsset: spotExec.FeeAsset,
			}
			return naked, fmt.Errorf("永续部分成交 %.6f/%.6f 且现货买回对齐失败(残留裸现货空 %.6f): %w", perpExec.ExecutedQty, hedgeQty, excess, trimErr)
		}
		e.logf("[bybit-live][开仓] %s 永续部分成交 %.6f/%.6f,已买回现货 %.6f 还币对齐两腿(避免裸现货空)", plan.Symbol, perpExec.ExecutedQty, hedgeQty, excess)
		hedgeQty = perpExec.ExecutedQty
		spotNotional = hedgeQty * spotExec.AvgPrice // 名义按对齐后的真实持仓量重算
	}

	pos := BybitLivePosition{
		Symbol:           plan.Symbol,
		BaseQty:          hedgeQty,
		SpotEntryPrice:   spotExec.AvgPrice,
		PerpEntryPrice:   perpExec.AvgPrice,
		SpotNotional:     spotNotional,
		PerpNotional:     perpExec.CumQuoteUSDT,
		FundingRate:      plan.FundingRate,
		NextFundingTime:  plan.NextFundingTime,
		OpenedAt:         e.now(),
		RunID:            runID,
		IdemPrefix:       idemPrefix,
		Negative:         true,
		OpenPerpFee:      perpExec.Fee,
		OpenPerpFeeAsset: perpExec.FeeAsset,
		OpenSpotFee:      spotExec.Fee,
		OpenSpotFeeAsset: spotExec.FeeAsset,
	}
	e.logf("[bybit-live][开仓] %s %s %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) + %s %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) 预估费率%.4f%% 结算%s",
		pos.Symbol,
		"借币空现货", spotExec.ExecutedQty, spotExec.AvgPrice, spotExec.CumQuoteUSDT, spotExec.Fee, spotExec.FeeAsset, directionalSlippageRate(venue.SideSell, spotExec.AvgPrice, plan.SpotPrice)*100,
		"永续多", perpExec.ExecutedQty, perpExec.AvgPrice, perpExec.CumQuoteUSDT, perpExec.Fee, perpExec.FeeAsset, directionalSlippageRate(venue.SideBuy, perpExec.AvgPrice, plan.PerpPrice)*100,
		pos.FundingRate*100, pos.NextFundingTime.UTC().Format("15:04:05Z"))
	return pos, nil
}

// CloseDeltaNeutral 平仓:永续买回(reduceOnly)+ 现货卖出。两腿都尝试;任一腿未平干净返回错误(调用方保留持仓重试)。
// 返回 bybitCloseResult 携带两腿真实成交价/实扣手续费,供调用方打印/通知用真实口径(而非估算)。
func (e *BybitLiveExecutor) CloseDeltaNeutral(ctx context.Context, pos BybitLivePosition) (bybitCloseResult, error) {
	var result bybitCloseResult
	if pos.BaseQty <= executionQtyTolerance {
		return result, nil
	}
	var errs []string
	// 永续平仓(reduceOnly):正 carry 买回空头;负 carry 卖出多头。
	// 自愈:先查真实仓位——已为零(被强平/人工平/从未真正开出的幻影记录)→ 该腿视为已平,
	// 绝不再下 reduceOnly 单(否则撞 110017 "position is zero" 死循环,台账永远清不掉);
	// 真实仓位小于台账数量 → 按真实数量平(reduceOnly 容忍,但显式收敛口径更稳)。
	perpCloseQty := pos.BaseQty
	perpAlreadyFlat := false
	if actual := e.PerpOpenQty(ctx, pos.Symbol); actual >= 0 {
		if actual <= executionQtyTolerance {
			perpAlreadyFlat = true
			e.logf("[bybit-live][平仓] %s 永续仓位已为零,视为永续腿已平", pos.Symbol)
		} else if actual < perpCloseQty {
			e.logf("[bybit-live][平仓] %s 永续真实仓位 %.8f < 台账 %.8f,按真实数量平", pos.Symbol, actual, perpCloseQty)
			perpCloseQty = actual
		}
	}
	perpCloseSide := venue.SideBuy
	if pos.Negative {
		perpCloseSide = venue.SideSell
	}
	if !perpAlreadyFlat {
		perpReq := venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        pos.Symbol,
			Side:          perpCloseSide,
			Type:          venue.OrderTypeMarket,
			Qty:           perpCloseQty,
			ClientOrderID: bybitLegClientID(pos.IdemPrefix, "cp"),
			ReduceOnly:    true,
		}
		if perpRes, err := e.placePerpMakerFirst(ctx, perpReq, time.Time{}); err != nil {
			if isBybitPositionZeroError(err) {
				// 下单与查仓之间被外力平掉:同样视为已平。
				perpAlreadyFlat = true
				e.logf("[bybit-live][平仓] %s 永续平仓返回仓位为零(110017),视为永续腿已平", pos.Symbol)
			} else {
				errs = append(errs, "永续平仓失败: "+err.Error())
			}
		} else {
			result.PerpClosePrice = perpRes.AvgPrice
			result.PerpCloseFee = perpRes.Fee
			result.PerpFeeAsset = perpRes.FeeAsset
			e.recordExecutionCost("close", "perp", perpCloseSide, 0, perpRes)
		}
	}
	if pos.Negative && pos.SpotEntryPrice <= 0 {
		// 修 FIX_PLAN #5:负 carry 但现货腿从未建立(开仓回补失败留下的纯永续裸腿,SpotEntryPrice=0)。
		// 此时没有借币欠款,绝不跑「现货买回还币」——否则会凭空买入现货(乱买 + PnL 用 0 开仓价算错)。永续腿已在上方按 reduceOnly 平。
		e.logf("[bybit-live][平仓] %s 负 carry 但现货腿未建立(裸永续),跳过现货买回还币", pos.Symbol)
	} else if pos.Negative {
		// 负 carry 平现货腿:买回借入的 base 并还币(MarginEffect=Repay,UTA spot margin 自动还)。
		// 买回数量取开仓借入量 pos.BaseQty(还清欠款);买入费在 quote(USDT)里扣,不存在「持有 base 不足」问题。
		spotReq := venue.OrderRequest{
			Market:        venue.MarketSpot,
			Symbol:        pos.Symbol,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeMarket,
			Qty:           pos.BaseQty,
			ClientOrderID: bybitLegClientID(pos.IdemPrefix, "cs"),
			MarginEffect:  venue.MarginEffectRepay,
		}
		if spotRes, err := e.placeAndConfirm(ctx, spotReq); err != nil {
			errs = append(errs, "现货买回还币失败: "+err.Error())
		} else {
			result.SpotClosePrice = spotRes.AvgPrice
			result.SpotCloseFee = spotRes.Fee
			result.SpotFeeAsset = spotRes.FeeAsset
			result.SpotSoldQty = spotRes.ExecutedQty
			e.recordExecutionCost("close", "spot", venue.SideBuy, 0, spotRes)
		}
	} else {
		// 正 carry 平现货腿:卖出持有的 base。Bybit 现货买入的 taker 费在 base 资产里扣,故实际持有的 base 略少于
		// 开仓名义 BaseQty。若按 BaseQty 卖出会撞 -170131 "Insufficient balance" 死循环(且永续腿已先平 → 裸现货多腿)。
		// 因此按「真实可用余额(向下取整到现货 lot step)」卖出;查不到余额时回退到 BaseQty(保持原行为)。
		spotSellQty := pos.BaseQty
		if avail, step, ok := e.spotSellableQty(ctx, pos.Symbol); ok {
			if avail < spotSellQty {
				spotSellQty = avail
			}
			if step > 0 {
				spotSellQty = roundDownToStep(spotSellQty, step)
			}
		}
		if spotSellQty <= executionQtyTolerance {
			// 现货已无可卖余额(已平/被吃光),视为现货腿已平,不再下单。
			e.logf("[bybit-live][平仓] %s 现货可卖余额≈0,视为现货腿已平", pos.Symbol)
		} else {
			spotReq := venue.OrderRequest{
				Market:        venue.MarketSpot,
				Symbol:        pos.Symbol,
				Side:          venue.SideSell,
				Type:          venue.OrderTypeMarket,
				Qty:           spotSellQty,
				ClientOrderID: bybitLegClientID(pos.IdemPrefix, "cs"),
			}
			if spotRes, err := e.placeAndConfirm(ctx, spotReq); err != nil {
				errs = append(errs, "现货平仓失败: "+err.Error())
			} else {
				result.SpotClosePrice = spotRes.AvgPrice
				result.SpotCloseFee = spotRes.Fee
				result.SpotFeeAsset = spotRes.FeeAsset
				result.SpotSoldQty = spotRes.ExecutedQty
				e.recordExecutionCost("close", "spot", venue.SideSell, 0, spotRes)
			}
		}
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("平仓未完全成交(%s): %s", pos.Symbol, strings.Join(errs, "; "))
	}
	// 真实实现盈亏(尽力):真实资金费(查流水)+ 两腿价差盈亏 − 真实手续费(折 USDT)。
	// 查不到资金费流水时 FundingOK=false,调用方退回估算口径(失败方向偏保守:只是显示估算,不影响交易)。
	if incomes, ferr := e.trader.FetchRealizedFunding(ctx, pos.Symbol, pos.OpenedAt); ferr == nil {
		for _, in := range incomes {
			result.RealFunding += in.Income
		}
		result.FundingOK = true
	} else {
		e.logf("[bybit-live][平仓] %s 资金费流水查询失败(实现盈亏退回估算): %v", pos.Symbol, ferr)
	}
	// 价差盈亏按方向取符号:正 carry=现货多(收盘-开仓)+ 永续空(开仓-收盘);负 carry 两腿方向相反。
	// 某腿未实际成交(已为零自愈跳过,ClosePrice=0)时该腿价差计 0,避免用 0 收盘价算出荒谬盈亏。
	var spotPnl, perpPnl float64
	if result.PerpClosePrice > 0 {
		if pos.Negative {
			perpPnl = (result.PerpClosePrice - pos.PerpEntryPrice) * perpCloseQty // 永续多头价差
		} else {
			perpPnl = (pos.PerpEntryPrice - result.PerpClosePrice) * perpCloseQty // 永续空头价差
		}
	}
	if result.SpotClosePrice > 0 {
		if pos.Negative {
			spotPnl = (pos.SpotEntryPrice - result.SpotClosePrice) * result.SpotSoldQty // 现货空头价差(借币卖→买回)
		} else {
			spotPnl = (result.SpotClosePrice - pos.SpotEntryPrice) * result.SpotSoldQty // 现货多头价差
		}
	}
	result.SpotPricePnL = spotPnl
	result.PerpPricePnL = perpPnl
	result.PricePnL = spotPnl + perpPnl
	// 现货两腿手续费折 USDT:开仓腿(正 carry=BUY,费在 base;负 carry=SELL,费在 quote)、
	// 平仓腿(正 carry=SELL;负 carry=BUY)。feeAsset 缺失(Bybit feeCurrency 常为空)时按方向推断。
	openSpotFeeUSDT := bybitSpotFeeUSDT(pos.OpenSpotFee, pos.OpenSpotFeeAsset, pos.Symbol, !pos.Negative, pos.SpotEntryPrice)
	closeSpotFeeUSDT := bybitSpotFeeUSDT(result.SpotCloseFee, result.SpotFeeAsset, pos.Symbol, pos.Negative, result.SpotClosePrice)
	result.TotalFeesUSDT = pos.OpenPerpFee + openSpotFeeUSDT + result.PerpCloseFee + closeSpotFeeUSDT
	result.RealizedPnL = result.PricePnL + result.RealFunding - result.TotalFeesUSDT
	perpCloseLabel, spotCloseLabel := "永续买回", "现货卖出"
	if pos.Negative {
		perpCloseLabel, spotCloseLabel = "永续卖出", "现货买回还币"
	}
	e.logf("[bybit-live][平仓] %s %s@%.6f(实扣费%.6f%s) + %s %.6f@%.6f(实扣费%.6f%s) | 真实资金费%.6f(查到=%t) 价差盈亏%.6f 总费%.6fU → 实现盈亏%.6fU 已双边平",
		pos.Symbol, perpCloseLabel, result.PerpClosePrice, result.PerpCloseFee, result.PerpFeeAsset,
		spotCloseLabel, result.SpotSoldQty, result.SpotClosePrice, result.SpotCloseFee, result.SpotFeeAsset,
		result.RealFunding, result.FundingOK, result.PricePnL, result.TotalFeesUSDT, result.RealizedPnL)
	return result, nil
}

// isBybitLeverageNotModified 识别「杠杆未变化」回执(retCode=110043),等价成功。
func isBybitLeverageNotModified(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "110043") || strings.Contains(msg, "leverage not modified")
}

// bybitSpotFeeUSDT 把现货腿实扣手续费折成 USDT。Bybit 现货 taker 费规则:BUY 在 base 资产里扣、
// SELL 在 quote(USDT)里扣;feeCurrency 实测经常返回空串,为空时按买卖方向(isBuy)推断计费币种。
// base 计价时按腿成交价折算;价格缺失(0)折不了则原样返回(宁可少折也不虚构)。
func bybitSpotFeeUSDT(fee float64, feeAsset string, sym venue.Symbol, isBuy bool, price float64) float64 {
	if fee <= 0 {
		return 0
	}
	base := baseAssetOf(string(sym))
	inBase := strings.EqualFold(strings.TrimSpace(feeAsset), base) || (strings.TrimSpace(feeAsset) == "" && isBuy)
	if inBase && price > 0 {
		return fee * price
	}
	return fee
}

// PerpOpenQty 返回某标的当前永续持仓的绝对数量(用于滚动持有前确认对冲腿仍在场)。查询失败返回 -1(未知)。
func (e *BybitLiveExecutor) PerpOpenQty(ctx context.Context, sym venue.Symbol) float64 {
	positions, err := e.trader.FetchPositions(ctx)
	if err != nil {
		return -1
	}
	var q float64
	for _, p := range positions {
		if p.Symbol != sym {
			continue
		}
		amt := p.PositionAmt
		if amt < 0 {
			amt = -amt
		}
		q += amt
	}
	return q
}

// spotSellableQty 返回某标的现货 base 资产的「真实可用余额」与现货 lot step(用于平仓按真实余额卖出)。
// 余额接口失败 → ok=false(调用方回退名义 BaseQty);余额表无该币种条目 → 视为余额 0(ok=true)。
func (e *BybitLiveExecutor) spotSellableQty(ctx context.Context, sym venue.Symbol) (avail, step float64, ok bool) {
	bals, err := e.trader.FetchBalances(ctx)
	if err != nil {
		return 0, 0, false
	}
	base := baseAssetOf(string(sym))
	if instrs, ierr := e.trader.FetchInstruments(ctx); ierr == nil {
		for _, in := range instrs {
			if in.Symbol == sym && in.Market == venue.MarketSpot {
				step = in.StepSize
				if in.BaseAsset != "" {
					base = in.BaseAsset
				}
				break
			}
		}
	}
	// 余额表里查不到该币种 = 余额为零(Bybit UTA 不返回零余额币种的条目)。必须返回 (0, ok=true)
	// 让调用方判定「现货腿已平」;此前返回 ok=false 会回退按台账名义数量卖 → 永远撞 170131
	// "Insufficient balance" 死循环(2026-06-10 MBOX-USDT 实锤)。
	b, found := bals.ByAsset[base]
	if !found {
		return 0, step, true
	}
	return b.Free, step, true
}

// isBybitDuplicateOrderError 判断错误是否为「clientOrderId/orderLinkId 已存在」(幂等重试时视为已受理)。
func isBybitDuplicateOrderError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// 170141 = 现货 "Duplicate clientOrderId."(retMsg 不含 "orderlinkid" 字样,须按码匹配;
	// 实锤:05:52 现货腿撞旧 ID 报原始 170141,与永续腿"视为已受理"行为不一致)。
	return strings.Contains(s, "orderlinkid") && (strings.Contains(s, "exist") || strings.Contains(s, "duplicat")) ||
		strings.Contains(s, "110072") || strings.Contains(s, "10005") || strings.Contains(s, "170141")
}

// isBybitPositionZeroError 判断错误是否为 110017「current position is zero, cannot fix reduce-only order qty」:
// reduceOnly 平仓/回补时仓位已为零,意味着没有敞口需要收敛,应视为「该腿已平」而非失败。
func isBybitPositionZeroError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "110017") || strings.Contains(s, "current position is zero")
}

func firstNonNilErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
