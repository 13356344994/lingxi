package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// trading.go 实现币安 USDT 永续 + 现货的私有写动作(签名 POST/DELETE):下单/撤单/查单/设杠杆/切保证金模式。
//
// 数量口径:venue.OrderRequest.Qty 统一为「币数量」(ContractBasedPerp=false)。下单前按 exchangeInfo
// LOT_SIZE stepSize 向下取整;newClientOrderId 超长截断至 36 字符。

func binanceSide(s venue.Side) string {
	if s == venue.SideSell {
		return "SELL"
	}
	return "BUY"
}

// binanceOrderTypeTIF 把中性 OrderType 映射成币安 (type, timeInForce)。
func binanceOrderTypeTIF(t venue.OrderType) (orderType, tif string, err error) {
	switch t {
	case venue.OrderTypeMarket:
		return "MARKET", "", nil
	case venue.OrderTypeLimit:
		return "LIMIT", "GTC", nil
	case venue.OrderTypeIOC:
		return "LIMIT", "IOC", nil
	case venue.OrderTypePostOnly:
		return "LIMIT", "GTX", nil
	default:
		return "", "", fmt.Errorf("binance: 未知订单类型 %v", t)
	}
}

func (a *Adapter) orderEndpoint(m venue.Market) string {
	if m == venue.MarketSpot {
		return a.spotBaseURL() + PathSpotOrder
	}
	return a.futuresBaseURL() + PathFuturesOrder
}

func (a *Adapter) placeOrderEndpoint(req venue.OrderRequest) string {
	if req.Market == venue.MarketSpot && isMarginSpotOrder(req) {
		return a.spotBaseURL() + PathMarginOrder
	}
	return a.orderEndpoint(req.Market)
}

// isMarginSpotOrder 判定是否走全仓杠杆现货单(/sapi/v1/margin/order)。
func isMarginSpotOrder(req venue.OrderRequest) bool {
	return req.Market == venue.MarketSpot &&
		(req.MarginEffect == venue.MarginEffectBorrow || req.MarginEffect == venue.MarginEffectRepay)
}

// marginSpotSideEffectType 映射中性 MarginEffect+Side 到币安 sideEffectType。
func marginSpotSideEffectType(req venue.OrderRequest) (string, error) {
	switch req.MarginEffect {
	case venue.MarginEffectRepay:
		return "AUTO_REPAY", nil
	case venue.MarginEffectBorrow:
		if req.Side == venue.SideBuy {
			return "MARGIN_BUY", nil
		}
		return "AUTO_BORROW_REPAY", nil
	default:
		return "", fmt.Errorf("binance: 非保证金现货单不应调用 marginSpotSideEffectType")
	}
}

// applyMarginSpotOrderParams 为全仓杠杆现货单追加 isIsolated/sideEffectType。
func applyMarginSpotOrderParams(params url.Values, req venue.OrderRequest) error {
	sideEffect, err := marginSpotSideEffectType(req)
	if err != nil {
		return err
	}
	params.Set("isIsolated", "FALSE")
	params.Set("sideEffectType", sideEffect)
	return nil
}

// buildOrderParams 把中性 OrderRequest 翻译成币安下单 query 参数(纯函数路径,便于单测)。
func (a *Adapter) buildOrderParams(ctx context.Context, req venue.OrderRequest) (url.Values, error) {
	raw, err := a.ToVenue(req.Symbol, req.Market)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, fmt.Errorf("binance: 空符号")
	}
	if req.Qty <= 0 {
		return nil, fmt.Errorf("binance: 下单数量必须为正, got %v", req.Qty)
	}
	inst, err := a.lookupInstrument(ctx, req.Market, req.Symbol)
	if err != nil {
		return nil, err
	}
	qty := roundDownStep(req.Qty, inst.StepSize)
	if qty <= 0 {
		return nil, fmt.Errorf("binance: 数量经 stepSize=%v 取整后为 0", inst.StepSize)
	}
	orderType, tif, err := binanceOrderTypeTIF(req.Type)
	if err != nil {
		return nil, err
	}
	// 现货不支持 timeInForce=GTX:现货的 post-only 是独立订单类型 LIMIT_MAKER(无 TIF 参数),
	// GTX 仅永续合法。不区分会让现货 post-only 单被 API 直接拒绝。
	if req.Market == venue.MarketSpot && req.Type == venue.OrderTypePostOnly {
		orderType, tif = "LIMIT_MAKER", ""
	}
	params := url.Values{}
	params.Set("symbol", raw)
	params.Set("side", binanceSide(req.Side))
	params.Set("type", orderType)
	params.Set("quantity", formatQtyByStep(qty, inst.StepSize))
	if orderType == "LIMIT" || orderType == "LIMIT_MAKER" {
		if req.Price <= 0 {
			return nil, fmt.Errorf("binance: 限价/IOC/PostOnly 单需正价格, got %v", req.Price)
		}
		// 价格经 tickSize 取整后若归零(瞬时坏盘口/价格 < tick),拒绝下「0 价单」(否则被
		// binance 以 -4001 Price less than 0 拒绝,语义混淆且浪费一次裸腿窗口)。
		if roundDownStep(req.Price, inst.TickSize) <= 0 {
			return nil, fmt.Errorf("binance: 价格 %v 经 tickSize=%v 取整后为 0,拒绝下 0 价单", req.Price, inst.TickSize)
		}
		params.Set("price", formatPriceByTick(req.Price, inst.TickSize))
		if tif != "" {
			params.Set("timeInForce", tif)
		}
	}
	if req.Market == venue.MarketPerp && req.ReduceOnly {
		params.Set("reduceOnly", "true")
	}
	if req.ClientOrderID != "" {
		params.Set("newClientOrderId", sanitizeClientOrderID(req.ClientOrderID))
	}
	if req.Market == venue.MarketPerp && req.MarginEffect != venue.MarginEffectNone {
		return nil, fmt.Errorf("binance: 永续单不支持 MarginEffect=%v", req.MarginEffect)
	}
	if isMarginSpotOrder(req) {
		if err := applyMarginSpotOrderParams(params, req); err != nil {
			return nil, err
		}
	}
	return params, nil
}

type futuresOrderAck struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Status        string `json:"status"`
	Symbol        string `json:"symbol"`
}

type spotOrderAck struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Status        string `json:"status"`
	Symbol        string `json:"symbol"`
}

// PlaceOrder 下单(签名 POST)。返回受理回执 Status=new;成交明细由 QueryOrder 富化。
func (a *Adapter) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	params, err := a.buildOrderParams(ctx, req)
	if err != nil {
		return venue.OrderResult{}, err
	}
	endpoint := a.placeOrderEndpoint(req)
	if req.Market == venue.MarketPerp {
		var ack futuresOrderAck
		if err := a.signedPostForm(ctx, endpoint, params, &ack); err != nil {
			return venue.OrderResult{}, err
		}
		return venue.OrderResult{
			Symbol:          req.Symbol,
			Market:          req.Market,
			ClientOrderID:   firstNonEmpty(ack.ClientOrderID, sanitizeClientOrderID(req.ClientOrderID)),
			ExchangeOrderID: strconv.FormatInt(ack.OrderID, 10),
			Status:          normalizeVenueOrderStatus(firstNonEmpty(ack.Status, "NEW")),
			RawCode:         ack.Status,
		}, nil
	}
	var ack spotOrderAck
	if err := a.signedPostForm(ctx, endpoint, params, &ack); err != nil {
		return venue.OrderResult{}, err
	}
	return venue.OrderResult{
		Symbol:          req.Symbol,
		Market:          req.Market,
		ClientOrderID:   firstNonEmpty(ack.ClientOrderID, sanitizeClientOrderID(req.ClientOrderID)),
		ExchangeOrderID: strconv.FormatInt(ack.OrderID, 10),
		Status:          normalizeVenueOrderStatus(firstNonEmpty(ack.Status, "NEW")),
		RawCode:         ack.Status,
	}, nil
}

// CancelOrder 按 origClientOrderId 撤单(DELETE)。
func (a *Adapter) CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("symbol", raw)
	params.Set("origClientOrderId", clientOrderID)
	_, err = a.signedRequest(ctx, http.MethodDelete, a.orderEndpoint(m), params)
	return err
}

// QueryOrder 按 origClientOrderId 查单,富化成交明细。
func (a *Adapter) QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.OrderResult{}, err
	}
	params := url.Values{}
	params.Set("symbol", raw)
	params.Set("origClientOrderId", clientOrderID)
	body, err := a.signedRequest(ctx, http.MethodGet, a.orderEndpoint(m), params)
	if err != nil {
		return venue.OrderResult{}, err
	}
	if m == venue.MarketPerp {
		return a.parseFuturesQueryOrder(body, s, m, clientOrderID)
	}
	return a.parseSpotQueryOrder(body, s, m, clientOrderID)
}

func (a *Adapter) parseFuturesQueryOrder(body []byte, s venue.Symbol, m venue.Market, clientOrderID string) (venue.OrderResult, error) {
	var o struct {
		OrderID       int64  `json:"orderId"`
		ClientOrderID string `json:"clientOrderId"`
		Status        string `json:"status"`
		ExecutedQty   string `json:"executedQty"`
		AvgPrice      string `json:"avgPrice"`
		CumQuote      string `json:"cumQuote"`
		UpdateTime    int64  `json:"updateTime"`
	}
	if err := json.Unmarshal(body, &o); err != nil {
		return venue.OrderResult{}, fmt.Errorf("binance: 解析合约查单回执失败: %w", err)
	}
	result := venue.OrderResult{
		Symbol:          s,
		Market:          m,
		ClientOrderID:   firstNonEmpty(o.ClientOrderID, clientOrderID),
		ExchangeOrderID: strconv.FormatInt(o.OrderID, 10),
		Status:          normalizeVenueOrderStatus(o.Status),
		ExecutedQty:     parseFloat(o.ExecutedQty),
		AvgPrice:        parseFloat(o.AvgPrice),
		CumQuoteUSDT:    parseFloat(o.CumQuote),
		// fapi 查单响应不含手续费明细;真实扣费需 userTrades 另行拉取,此处填 0。
		Fee:      0,
		FeeAsset: "",
		RawCode:  o.Status,
	}
	if o.UpdateTime > 0 {
		result.CreatedAt = time.UnixMilli(o.UpdateTime).UTC()
	}
	return result, nil
}

func (a *Adapter) parseSpotQueryOrder(body []byte, s venue.Symbol, m venue.Market, clientOrderID string) (venue.OrderResult, error) {
	var o struct {
		OrderID             int64  `json:"orderId"`
		ClientOrderID       string `json:"clientOrderId"`
		Status              string `json:"status"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
		TransactTime        int64  `json:"transactTime"`
		Fills               []struct {
			Price           string `json:"price"`
			Qty             string `json:"qty"`
			Commission      string `json:"commission"`
			CommissionAsset string `json:"commissionAsset"`
		} `json:"fills"`
	}
	if err := json.Unmarshal(body, &o); err != nil {
		return venue.OrderResult{}, fmt.Errorf("binance: 解析现货查单回执失败: %w", err)
	}
	result := venue.OrderResult{
		Symbol:          s,
		Market:          m,
		ClientOrderID:   firstNonEmpty(o.ClientOrderID, clientOrderID),
		ExchangeOrderID: strconv.FormatInt(o.OrderID, 10),
		Status:          normalizeVenueOrderStatus(o.Status),
		ExecutedQty:     parseFloat(o.ExecutedQty),
		CumQuoteUSDT:    parseFloat(o.CummulativeQuoteQty),
		RawCode:         o.Status,
	}
	if o.TransactTime > 0 {
		result.CreatedAt = time.UnixMilli(o.TransactTime).UTC()
	}
	if len(o.Fills) > 0 {
		feeSum := 0.0
		feeAsset := ""
		for _, fill := range o.Fills {
			feeSum += parseFloat(fill.Commission)
			if feeAsset == "" && strings.TrimSpace(fill.CommissionAsset) != "" {
				feeAsset = strings.ToUpper(fill.CommissionAsset)
			}
		}
		result.Fee = feeSum
		result.FeeAsset = feeAsset
		if result.ExecutedQty > 0 {
			quote := parseFloat(o.CummulativeQuoteQty)
			if quote > 0 {
				result.AvgPrice = quote / result.ExecutedQty
			}
		}
	}
	return result, nil
}

// SetLeverage 设置合约杠杆倍数。
func (a *Adapter) SetLeverage(ctx context.Context, s venue.Symbol, lev int) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("symbol", raw)
	params.Set("leverage", strconv.Itoa(lev))
	_, err = a.signedRequest(ctx, http.MethodPost, a.futuresBaseURL()+PathFuturesLeverage, params)
	return err
}

// SetMarginMode 切换逐仓/全仓(CROSSED/ISOLATED)。
func (a *Adapter) SetMarginMode(ctx context.Context, s venue.Symbol, mode venue.MarginMode) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	marginType := "CROSSED"
	if mode == venue.MarginModeIsolated {
		marginType = "ISOLATED"
	}
	params := url.Values{}
	params.Set("symbol", raw)
	params.Set("marginType", marginType)
	_, err = a.signedRequest(ctx, http.MethodPost, a.futuresBaseURL()+PathFuturesMarginType, params)
	if isMarginTypeNoChange(err) {
		return nil
	}
	return err
}
