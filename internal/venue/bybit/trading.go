package bybit

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"lingxi/internal/venue"
)

// trading.go 实现 Bybit v5 私有写动作(签名 POST):下单/批量/撤单/设杠杆/切保证金模式 + 活动订单查询。
//
// 数量口径:venue.OrderRequest.Qty 统一为「币数量」(base coin)。Bybit linear 永续天然按币数量;
// 现货市价单默认按 quote 计量,故对现货市价单显式置 marketUnit=baseCoin,与中性口径对齐。
//
// 安全:这些方法仅在引擎集成(Stage 3C)后才会被真实调用;当前无调用方,纯适配器实现,offline 可测。
// 真实下单的活体验证需已注资的 Bybit 账户(见 Stage 3D)。

// bybitCategory 把中性 Market 映射成 Bybit category。
func bybitCategory(m venue.Market) string {
	if m == venue.MarketSpot {
		return categorySpot
	}
	return categoryLinear
}

// bybitSide 把中性 Side 映射成 Bybit 的 "Buy"/"Sell"。
func bybitSide(s venue.Side) string {
	if s == venue.SideSell {
		return "Sell"
	}
	return "Buy"
}

// bybitOrderTypeTIF 把中性 OrderType 映射成 Bybit (orderType, timeInForce)。
// Bybit orderType 仅 Market/Limit;IOC/PostOnly 通过 Limit + timeInForce 表达。
func bybitOrderTypeTIF(t venue.OrderType) (orderType, tif string) {
	switch t {
	case venue.OrderTypeMarket:
		return "Market", ""
	case venue.OrderTypeIOC:
		return "Limit", "IOC"
	case venue.OrderTypePostOnly:
		return "Limit", "PostOnly"
	default: // OrderTypeLimit
		return "Limit", "GTC"
	}
}

// fmtNum 以非科学计数法格式化数量/价格(交易所要求纯小数字符串)。
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// bybitCreateOrderBody 是 /v5/order/create 的请求体(只设非零字段;omitempty 让市价单不带 price)。
type bybitCreateOrderBody struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Qty         string `json:"qty"`
	Price       string `json:"price,omitempty"`
	TimeInForce string `json:"timeInForce,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	MarketUnit  string `json:"marketUnit,omitempty"`
	// IsLeverage=1 启用现货杠杆(UTA spot margin):卖出超过持有量时自动借入 base 做空,买回时自动还币。
	// 仅在中性 OrderRequest 显式标注 MarginEffect(Borrow/Repay)且为现货时设置;普通现货/永续不设(=0)。
	IsLeverage int `json:"isLeverage,omitempty"`
}

// buildCreateBody 把中性 OrderRequest 翻译成 Bybit 下单体(纯函数,便于单测)。
func (a *Adapter) buildCreateBody(req venue.OrderRequest) (bybitCreateOrderBody, error) {
	raw, err := a.ToVenue(req.Symbol, req.Market)
	if err != nil {
		return bybitCreateOrderBody{}, err
	}
	if raw == "" {
		return bybitCreateOrderBody{}, fmt.Errorf("bybit: 空符号")
	}
	if req.Qty <= 0 {
		return bybitCreateOrderBody{}, fmt.Errorf("bybit: 下单数量必须为正, got %v", req.Qty)
	}
	orderType, tif := bybitOrderTypeTIF(req.Type)
	body := bybitCreateOrderBody{
		Category:    bybitCategory(req.Market),
		Symbol:      raw,
		Side:        bybitSide(req.Side),
		OrderType:   orderType,
		Qty:         fmtNum(req.Qty),
		TimeInForce: tif,
		OrderLinkID: req.ClientOrderID,
		ReduceOnly:  req.ReduceOnly,
	}
	if orderType == "Limit" {
		if req.Price <= 0 {
			return bybitCreateOrderBody{}, fmt.Errorf("bybit: 限价/IOC 单需正价格, got %v", req.Price)
		}
		body.Price = fmtNum(req.Price)
	}
	// 现货市价单按币数量计:显式 marketUnit=baseCoin,与中性 Qty(币数量)对齐。
	if req.Market == venue.MarketSpot && orderType == "Market" {
		body.MarketUnit = "baseCoin"
	}
	// 负 carry:现货做空(借币卖出)/平仓还币(买回) → isLeverage=1 启用现货杠杆自动借/还。
	if req.Market == venue.MarketSpot && (req.MarginEffect == venue.MarginEffectBorrow || req.MarginEffect == venue.MarginEffectRepay) {
		body.IsLeverage = 1
	}
	return body, nil
}

// bybitOrderAck 是 create/cancel 的最小返回(orderId/orderLinkId)。create 不含成交明细,需 QueryOrder 富化。
type bybitOrderAck struct {
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
}

// PlaceOrder 下单(签名 POST)。返回受理回执:Status=new + 交易所/客户端单号;成交明细由 QueryOrder 拉取。
func (a *Adapter) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	body, err := a.buildCreateBody(req)
	if err != nil {
		return venue.OrderResult{}, err
	}
	var ack bybitOrderAck
	if err := a.t.signedPost(ctx, pathOrderCreate, body, &ack); err != nil {
		return venue.OrderResult{}, err
	}
	return venue.OrderResult{
		Symbol:          req.Symbol,
		Market:          req.Market,
		ClientOrderID:   firstNonEmpty(ack.OrderLinkID, req.ClientOrderID),
		ExchangeOrderID: ack.OrderID,
		Status:          "new",
	}, nil
}

// PlaceBatch 批量下单(/v5/order/create-batch)。要求同 category;逐条回执按入参顺序对齐。
// 超出 MaxBatchOrders 由调用方分批;此处只做单批提交。
func (a *Adapter) PlaceBatch(ctx context.Context, reqs []venue.OrderRequest) ([]venue.OrderResult, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	category := bybitCategory(reqs[0].Market)
	items := make([]bybitCreateOrderBody, 0, len(reqs))
	for _, r := range reqs {
		if bybitCategory(r.Market) != category {
			return nil, fmt.Errorf("bybit: 批量下单要求同 category(混合 spot/linear 不支持)")
		}
		b, err := a.buildCreateBody(r)
		if err != nil {
			return nil, err
		}
		b.Category = "" // 批量体里 category 在外层,逐条不带
		items = append(items, b)
	}
	reqBody := struct {
		Category string                 `json:"category"`
		Request  []bybitCreateOrderBody `json:"request"`
	}{Category: category, Request: items}

	var res struct {
		List []bybitOrderAck `json:"list"`
	}
	if err := a.t.signedPost(ctx, pathOrderCreateBatch, reqBody, &res); err != nil {
		return nil, err
	}
	out := make([]venue.OrderResult, 0, len(reqs))
	for i, r := range reqs {
		ack := bybitOrderAck{}
		if i < len(res.List) {
			ack = res.List[i]
		}
		out = append(out, venue.OrderResult{
			Symbol:          r.Symbol,
			Market:          r.Market,
			ClientOrderID:   firstNonEmpty(ack.OrderLinkID, r.ClientOrderID),
			ExchangeOrderID: ack.OrderID,
			Status:          "new",
		})
	}
	return out, nil
}

// CancelOrder 按 clientOrderID(orderLinkId)撤单。
func (a *Adapter) CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return err
	}
	body := struct {
		Category    string `json:"category"`
		Symbol      string `json:"symbol"`
		OrderLinkID string `json:"orderLinkId"`
	}{Category: bybitCategory(m), Symbol: raw, OrderLinkID: clientOrderID}
	return a.t.signedPost(ctx, pathOrderCancel, body, nil)
}

// QueryOrder 查活动订单(/v5/order/realtime),富化成交明细。
func (a *Adapter) QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.OrderResult{}, err
	}
	q := url.Values{}
	q.Set("category", bybitCategory(m))
	q.Set("symbol", raw)
	q.Set("orderLinkId", clientOrderID)
	var res struct {
		List []struct {
			OrderID      string `json:"orderId"`
			OrderLinkID  string `json:"orderLinkId"`
			OrderStatus  string `json:"orderStatus"`
			CumExecQty   string `json:"cumExecQty"`
			AvgPrice     string `json:"avgPrice"`
			CumExecValue string `json:"cumExecValue"`
			CumExecFee   string `json:"cumExecFee"`
			FeeCurrency  string `json:"feeCurrency"`
		} `json:"list"`
	}
	if err := a.t.signedGet(ctx, pathOrderRealtime, q, &res); err != nil {
		return venue.OrderResult{}, err
	}
	if len(res.List) == 0 {
		return venue.OrderResult{Symbol: s, Market: m, ClientOrderID: clientOrderID, Status: "unknown"}, nil
	}
	o := res.List[0]
	return venue.OrderResult{
		Symbol:          s,
		Market:          m,
		ClientOrderID:   firstNonEmpty(o.OrderLinkID, clientOrderID),
		ExchangeOrderID: o.OrderID,
		Status:          normalizeOrderStatus(o.OrderStatus),
		ExecutedQty:     parseFloat(o.CumExecQty),
		AvgPrice:        parseFloat(o.AvgPrice),
		CumQuoteUSDT:    parseFloat(o.CumExecValue),
		Fee:             parseFloat(o.CumExecFee),
		FeeAsset:        o.FeeCurrency,
		RawCode:         o.OrderStatus,
	}, nil
}

// SetLeverage 设置买/卖双向杠杆(linear)。
func (a *Adapter) SetLeverage(ctx context.Context, s venue.Symbol, lev int) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	levStr := strconv.Itoa(lev)
	body := struct {
		Category     string `json:"category"`
		Symbol       string `json:"symbol"`
		BuyLeverage  string `json:"buyLeverage"`
		SellLeverage string `json:"sellLeverage"`
	}{Category: categoryLinear, Symbol: raw, BuyLeverage: levStr, SellLeverage: levStr}
	return a.t.signedPost(ctx, pathSetLeverage, body, nil)
}

// SetMarginMode 切换逐仓/全仓(switch-isolated:tradeMode 0=cross,1=isolated)。
func (a *Adapter) SetMarginMode(ctx context.Context, s venue.Symbol, mode venue.MarginMode) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	tradeMode := 0
	if mode == venue.MarginModeIsolated {
		tradeMode = 1
	}
	body := struct {
		Category     string `json:"category"`
		Symbol       string `json:"symbol"`
		TradeMode    int    `json:"tradeMode"`
		BuyLeverage  string `json:"buyLeverage"`
		SellLeverage string `json:"sellLeverage"`
	}{Category: categoryLinear, Symbol: raw, TradeMode: tradeMode, BuyLeverage: "1", SellLeverage: "1"}
	return a.t.signedPost(ctx, pathSwitchIsolated, body, nil)
}

// normalizeOrderStatus 把 Bybit 订单状态归一到中性集合(new/filled/partially_filled/canceled/rejected)。
func normalizeOrderStatus(s string) string {
	switch strings.ToLower(s) {
	case "filled":
		return "filled"
	case "partiallyfilled":
		return "partially_filled"
	case "cancelled", "partiallyfilledcanceled":
		return "canceled"
	case "rejected":
		return "rejected"
	case "new", "created", "untriggered":
		return "new"
	default:
		return strings.ToLower(s)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
