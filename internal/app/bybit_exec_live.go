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
//   - **绝不裸腿**:开仓「永续空腿先行→确认成交→现货多腿对冲」;现货腿失败 → 立即回补永续(reduceOnly),回到平。
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
	// SettlementsHeld 已收过资金费的结算次数(滚动持有计数);开仓后首个结算收一次记 1。
	SettlementsHeld int `json:"settlements_held"`
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
}

// BybitLiveExecutor 用真实下单接口执行 delta-neutral 开/平仓。
type BybitLiveExecutor struct {
	trader         bybitTrader
	logf           func(string, ...any)
	confirmTimeout time.Duration // 单腿成交确认总时限
	confirmPoll    time.Duration // 轮询间隔
	now            func() time.Time
	costRecordPath string // 真实成交成本(实扣手续费 + 滑点)JSONL 落盘路径;空=不记录
}

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

// placeAndConfirm 下单(幂等:重复 clientOrderId 视为已受理)后确认成交。
func (e *BybitLiveExecutor) placeAndConfirm(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if _, err := e.trader.PlaceOrder(ctx, req); err != nil && !isBybitDuplicateOrderError(err) {
		return venue.OrderResult{}, err
	}
	return e.confirmFill(ctx, req.Market, req.Symbol, req.ClientOrderID)
}

// OpenDeltaNeutral 开正 carry delta-neutral:永续空腿先行→确认→现货多腿对冲;现货失败立即回补永续(不留裸腿)。
// idemPrefix 须在同一持仓的开/平/回补间稳定(由调用方用 runID+cycle+symbol 派生),保证幂等。
func (e *BybitLiveExecutor) OpenDeltaNeutral(ctx context.Context, plan BybitOpenPlan, idemPrefix, runID string) (BybitLivePosition, error) {
	if plan.BaseQty <= executionQtyTolerance {
		return BybitLivePosition{}, fmt.Errorf("开仓数量过小: %.10f", plan.BaseQty)
	}
	// 第一腿:永续空(正费率时空头收资金费)。
	perpReq := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        plan.Symbol,
		Side:          venue.SideSell,
		Type:          venue.OrderTypeMarket,
		Qty:           plan.BaseQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "op"),
	}
	perpExec, err := e.placeAndConfirm(ctx, perpReq)
	if err != nil {
		// 永续腿未确认成交:尝试撤单(幂等),不进入现货腿 → 无对冲敞口。reconcile 兜底核对真实持仓。
		_ = e.trader.CancelOrder(ctx, venue.MarketPerp, plan.Symbol, perpReq.ClientOrderID)
		return BybitLivePosition{}, fmt.Errorf("永续空腿开仓失败(未建仓): %w", err)
	}
	if perpExec.ExecutedQty <= executionQtyTolerance {
		return BybitLivePosition{}, fmt.Errorf("永续空腿成交数量为 0,放弃")
	}
	hedgeQty := perpExec.ExecutedQty
	e.recordExecutionCost("open", "perp", venue.SideSell, plan.PerpPrice, perpExec)

	// 第二腿:现货多(对冲永续空,按永续实际成交量对齐)。
	spotReq := venue.OrderRequest{
		Market:        venue.MarketSpot,
		Symbol:        plan.Symbol,
		Side:          venue.SideBuy,
		Type:          venue.OrderTypeMarket,
		Qty:           hedgeQty,
		ClientOrderID: bybitLegClientID(idemPrefix, "os"),
	}
	spotExec, spotErr := e.placeAndConfirm(ctx, spotReq)
	if spotErr != nil || spotExec.ExecutedQty <= executionQtyTolerance {
		// 现货腿失败 → 立即回补永续空腿(买入 reduceOnly),回到平,绝不裸腿。
		revertReq := venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        plan.Symbol,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeMarket,
			Qty:           hedgeQty,
			ClientOrderID: bybitLegClientID(idemPrefix, "rp"),
			ReduceOnly:    true,
		}
		_, revertErr := e.placeAndConfirm(ctx, revertReq)
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
			}
			return naked, fmt.Errorf("现货腿失败且永续回补失败(裸腿风险!): spotErr=%v revertErr=%w", spotErr, revertErr)
		}
		return BybitLivePosition{}, fmt.Errorf("现货多腿失败,已回补永续空腿(已平): %v", firstNonNilErr(spotErr, fmt.Errorf("现货成交为 0")))
	}
	e.recordExecutionCost("open", "spot", venue.SideBuy, plan.SpotPrice, spotExec)

	pos := BybitLivePosition{
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
		OpenPerpFee:      perpExec.Fee,
		OpenPerpFeeAsset: perpExec.FeeAsset,
		OpenSpotFee:      spotExec.Fee,
		OpenSpotFeeAsset: spotExec.FeeAsset,
	}
	e.logf("[bybit-live][开仓] %s 永续空 %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) + 现货多 %.6f@%.6f(名义%.4fU 实扣费%.6f%s 滑点%.4f%%) 预估费率%.4f%% 结算%s",
		pos.Symbol,
		perpExec.ExecutedQty, perpExec.AvgPrice, perpExec.CumQuoteUSDT, perpExec.Fee, perpExec.FeeAsset, directionalSlippageRate(venue.SideSell, perpExec.AvgPrice, plan.PerpPrice)*100,
		spotExec.ExecutedQty, spotExec.AvgPrice, spotExec.CumQuoteUSDT, spotExec.Fee, spotExec.FeeAsset, directionalSlippageRate(venue.SideBuy, spotExec.AvgPrice, plan.SpotPrice)*100,
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
	// 永续买回(reduceOnly)。
	perpReq := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        pos.Symbol,
		Side:          venue.SideBuy,
		Type:          venue.OrderTypeMarket,
		Qty:           pos.BaseQty,
		ClientOrderID: bybitLegClientID(pos.IdemPrefix, "cp"),
		ReduceOnly:    true,
	}
	if perpRes, err := e.placeAndConfirm(ctx, perpReq); err != nil {
		errs = append(errs, "永续平仓失败: "+err.Error())
	} else {
		result.PerpClosePrice = perpRes.AvgPrice
		result.PerpCloseFee = perpRes.Fee
		result.PerpFeeAsset = perpRes.FeeAsset
		e.recordExecutionCost("close", "perp", venue.SideBuy, 0, perpRes)
	}
	// 现货卖出。Bybit 现货买入的 taker 费在 base 资产里扣,故实际持有的 base 略少于开仓名义 BaseQty。
	// 若按 BaseQty 卖出会撞 -170131 "Insufficient balance" 并永久重试死循环(且永续腿已先平 → 留下裸现货多腿)。
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
	if len(errs) > 0 {
		return result, fmt.Errorf("平仓未完全成交(%s): %s", pos.Symbol, strings.Join(errs, "; "))
	}
	e.logf("[bybit-live][平仓] %s 永续买回@%.6f(实扣费%.6f%s) + 现货卖出 %.6f@%.6f(实扣费%.6f%s) 已双边平",
		pos.Symbol, result.PerpClosePrice, result.PerpCloseFee, result.PerpFeeAsset,
		result.SpotSoldQty, result.SpotClosePrice, result.SpotCloseFee, result.SpotFeeAsset)
	return result, nil
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
// 任一查询失败或找不到该资产 → ok=false,调用方回退到名义 BaseQty。
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
	b, found := bals.ByAsset[base]
	if !found {
		return 0, 0, false
	}
	return b.Free, step, true
}

// isBybitDuplicateOrderError 判断错误是否为「clientOrderId/orderLinkId 已存在」(幂等重试时视为已受理)。
func isBybitDuplicateOrderError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "orderlinkid") && (strings.Contains(s, "exist") || strings.Contains(s, "duplicat")) ||
		strings.Contains(s, "110072") || strings.Contains(s, "10005")
}

func firstNonNilErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
