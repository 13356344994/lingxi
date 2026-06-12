package gate

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// trading.go 实现 Gate v4 私有写动作:下单/撤单/查单/设杠杆。
//
// 数量口径:venue.OrderRequest.Qty 统一为「币数量」。Gate USDT 永续 size 为带符号整数张数(正=多,负=空),
// PlaceOrder 内按 quanto_multiplier 换算并向零取整;现货市价买单用 IOC 限价(Gate 市价买 amount 是 USDT 金额)。

func gateSide(s venue.Side) string {
	if s == venue.SideSell {
		return "sell"
	}
	return "buy"
}

func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// sanitizeOrderText 净化 Gate 客户端单号 text 字段。
// Gate 规则:text 必须以 "t-" 开头,"t-" 之后最长 28 字符,仅字母数字/下划线/连字符/点。
// 引擎侧 clientOrderID 常带 UUID 连字符等非法字符,须净化+截断+加前缀后再提交。
func sanitizeOrderText(id string) string {
	const prefix = "t-"
	const maxBody = 28
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	body := b.String()
	body = strings.TrimPrefix(body, prefix)
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	return prefix + body
}

// qtyToSignedContractSize 把币数量换算为 Gate 带符号整数张数(向零取整)。
// Side=Sell → 负数;为 0 时报错(与 OKX 张数换算不同,Gate 无 side 字段)。
func qtyToSignedContractSize(side venue.Side, qty, multiplier float64) (int64, error) {
	if multiplier <= 0 {
		return 0, fmt.Errorf("gate: quanto_multiplier 无效")
	}
	if qty <= 0 {
		return 0, fmt.Errorf("gate: 下单数量必须为正, got %v", qty)
	}
	contracts := qty / multiplier
	var n int64
	if side == venue.SideBuy {
		n = int64(contracts)
	} else {
		n = -int64(contracts)
	}
	if n == 0 {
		return 0, fmt.Errorf("gate: 换算后张数为 0(qty=%v multiplier=%v)", qty, multiplier)
	}
	return n, nil
}

type gateFuturesOrderBody struct {
	Contract   string `json:"contract"`
	Size       int64  `json:"size"`
	Price      string `json:"price"`
	TIF        string `json:"tif"`
	ReduceOnly bool   `json:"reduce_only,omitempty"`
	Text       string `json:"text,omitempty"`
}

type gateSpotOrderBody struct {
	CurrencyPair string `json:"currency_pair"`
	Side         string `json:"side"`
	Amount       string `json:"amount"`
	Price        string `json:"price,omitempty"`
	Type         string `json:"type"`
	Account      string `json:"account"`
	TimeInForce  string `json:"time_in_force,omitempty"`
	Text         string `json:"text,omitempty"`
}

func (a *Adapter) buildFuturesOrderBody(ctx context.Context, req venue.OrderRequest) (gateFuturesOrderBody, error) {
	raw, err := a.ToVenue(req.Symbol, req.Market)
	if err != nil {
		return gateFuturesOrderBody{}, err
	}
	inst, err := a.lookupInstrument(ctx, req.Market, req.Symbol)
	if err != nil {
		return gateFuturesOrderBody{}, err
	}
	size, err := qtyToSignedContractSize(req.Side, req.Qty, inst.ContractSize)
	if err != nil {
		return gateFuturesOrderBody{}, err
	}
	body := gateFuturesOrderBody{
		Contract:   raw,
		Size:       size,
		ReduceOnly: req.ReduceOnly,
		Text:       sanitizeOrderText(req.ClientOrderID),
	}
	switch req.Type {
	case venue.OrderTypeMarket:
		body.Price = "0"
		body.TIF = "ioc"
	case venue.OrderTypeIOC:
		if req.Price <= 0 {
			return gateFuturesOrderBody{}, fmt.Errorf("gate: IOC 单需正价格, got %v", req.Price)
		}
		body.Price = fmtNum(req.Price)
		body.TIF = "ioc"
	case venue.OrderTypePostOnly:
		if req.Price <= 0 {
			return gateFuturesOrderBody{}, fmt.Errorf("gate: post-only 单需正价格, got %v", req.Price)
		}
		body.Price = fmtNum(req.Price)
		body.TIF = "poc"
	default:
		if req.Price <= 0 {
			return gateFuturesOrderBody{}, fmt.Errorf("gate: 限价单需正价格, got %v", req.Price)
		}
		body.Price = fmtNum(req.Price)
		body.TIF = "gtc"
	}
	return body, nil
}

func (a *Adapter) buildSpotOrderBody(ctx context.Context, req venue.OrderRequest) (gateSpotOrderBody, error) {
	raw, err := a.ToVenue(req.Symbol, req.Market)
	if err != nil {
		return gateSpotOrderBody{}, err
	}
	if req.Qty <= 0 {
		return gateSpotOrderBody{}, fmt.Errorf("gate: 下单数量必须为正, got %v", req.Qty)
	}
	body := gateSpotOrderBody{
		CurrencyPair: raw,
		Side:         gateSide(req.Side),
		Account:      "spot",
		Text:         sanitizeOrderText(req.ClientOrderID),
	}
	switch req.Type {
	case venue.OrderTypeMarket:
		if req.Side == venue.SideBuy {
			// Gate 现货 market buy 的 amount 是计价币(USDT)金额,与 venue.Qty=基础币语义冲突;
			// 故现货买单一律用 IOC 限价单实现,调用方必须提供 req.Price。
			if req.Price <= 0 {
				return gateSpotOrderBody{}, fmt.Errorf("gate: 现货买单须用 IOC 限价(市价买 amount 是 USDT),需正价格 got %v", req.Price)
			}
			body.Type = "limit"
			body.TimeInForce = "ioc"
			body.Amount = fmtNum(req.Qty)
			body.Price = fmtNum(req.Price)
		} else {
			body.Type = "market"
			body.Amount = fmtNum(req.Qty)
		}
	case venue.OrderTypeIOC:
		if req.Price <= 0 {
			return gateSpotOrderBody{}, fmt.Errorf("gate: IOC 单需正价格, got %v", req.Price)
		}
		body.Type = "limit"
		body.TimeInForce = "ioc"
		body.Amount = fmtNum(req.Qty)
		body.Price = fmtNum(req.Price)
	case venue.OrderTypePostOnly:
		if req.Price <= 0 {
			return gateSpotOrderBody{}, fmt.Errorf("gate: post-only 单需正价格, got %v", req.Price)
		}
		body.Type = "limit"
		body.TimeInForce = "poc"
		body.Amount = fmtNum(req.Qty)
		body.Price = fmtNum(req.Price)
	default:
		if req.Price <= 0 {
			return gateSpotOrderBody{}, fmt.Errorf("gate: 限价单需正价格, got %v", req.Price)
		}
		body.Type = "limit"
		body.TimeInForce = "gtc"
		body.Amount = fmtNum(req.Qty)
		body.Price = fmtNum(req.Price)
	}
	return body, nil
}

type gateOrderAck struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// PlaceOrder 下单(签名 POST)。返回受理回执;成交明细由 QueryOrder 拉取。
func (a *Adapter) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if req.Market == venue.MarketPerp {
		body, err := a.buildFuturesOrderBody(ctx, req)
		if err != nil {
			return venue.OrderResult{}, err
		}
		var ack gateOrderAck
		if err := a.t.signedPost(ctx, pathFuturesOrders, nil, body, &ack); err != nil {
			return venue.OrderResult{}, err
		}
		return venue.OrderResult{
			Symbol:          req.Symbol,
			Market:          req.Market,
			ClientOrderID:   firstNonEmpty(ack.Text, body.Text, req.ClientOrderID),
			ExchangeOrderID: strconv.FormatInt(ack.ID, 10),
			Status:          "new",
		}, nil
	}
	body, err := a.buildSpotOrderBody(ctx, req)
	if err != nil {
		return venue.OrderResult{}, err
	}
	var ack gateOrderAck
	if err := a.t.signedPost(ctx, pathSpotOrders, nil, body, &ack); err != nil {
		return venue.OrderResult{}, err
	}
	return venue.OrderResult{
		Symbol:          req.Symbol,
		Market:          req.Market,
		ClientOrderID:   firstNonEmpty(ack.Text, body.Text, req.ClientOrderID),
		ExchangeOrderID: strconv.FormatInt(ack.ID, 10),
		Status:          "new",
	}, nil
}

// CancelOrder 按 clientOrderID(text)查单后 DELETE 交易所单号。
func (a *Adapter) CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error {
	res, err := a.QueryOrder(ctx, m, s, clientOrderID)
	if err != nil {
		return err
	}
	if res.ExchangeOrderID == "" || res.Status == "unknown" {
		return fmt.Errorf("gate: 找不到可撤订单 text=%s", sanitizeOrderText(clientOrderID))
	}
	if m == venue.MarketPerp {
		path := pathFuturesOrders + "/" + url.PathEscape(res.ExchangeOrderID)
		return a.t.signedDelete(ctx, path, nil, nil)
	}
	path := pathSpotOrders + "/" + url.PathEscape(res.ExchangeOrderID)
	return a.t.signedDelete(ctx, path, nil, nil)
}

type gateFuturesOrder struct {
	ID         int64   `json:"id"`
	Text       string  `json:"text"`
	Contract   string  `json:"contract"`
	Size       int64   `json:"size"`
	Left       int64   `json:"left"`
	Price      string  `json:"price"`
	FillPrice  string  `json:"fill_price"`
	Status     string  `json:"status"`
	FinishAs   string  `json:"finish_as"`
	CreateTime float64 `json:"create_time"`
}

type gateSpotOrder struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	CurrencyPair string `json:"currency_pair"`
	Side         string `json:"side"`
	Amount       string `json:"amount"`
	Left         string `json:"left"`
	Price        string `json:"price"`
	AvgDealPrice string `json:"avg_deal_price"`
	FilledTotal  string `json:"filled_total"`
	Status       string `json:"status"`
	Fee          string `json:"fee"`
	FeeCurrency  string `json:"fee_currency"`
	CreateTime   string `json:"create_time"`
}

// QueryOrder 优先用 text 查活动单 + finished 单,两者都查不到返回 unknown。
func (a *Adapter) QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	text := sanitizeOrderText(clientOrderID)
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.OrderResult{}, err
	}
	if m == venue.MarketPerp {
		for _, status := range []string{"open", "finished"} {
			q := url.Values{}
			q.Set("contract", raw)
			q.Set("status", status)
			q.Set("text", text)
			var rows []gateFuturesOrder
			if err := a.t.signedGet(ctx, pathFuturesOrders, q, &rows); err != nil {
				return venue.OrderResult{}, err
			}
			if len(rows) > 0 {
				return a.mapFuturesOrder(ctx, s, rows[0])
			}
		}
		return venue.OrderResult{Symbol: s, Market: m, ClientOrderID: clientOrderID, Status: "unknown"}, nil
	}
	for _, status := range []string{"open", "closed"} {
		q := url.Values{}
		q.Set("currency_pair", raw)
		q.Set("status", status)
		q.Set("text", text)
		var rows []gateSpotOrder
		if err := a.t.signedGet(ctx, pathSpotOrders, q, &rows); err != nil {
			return venue.OrderResult{}, err
		}
		if len(rows) > 0 {
			return a.mapSpotOrder(s, rows[0]), nil
		}
	}
	return venue.OrderResult{Symbol: s, Market: m, ClientOrderID: clientOrderID, Status: "unknown"}, nil
}

func (a *Adapter) mapFuturesOrder(ctx context.Context, s venue.Symbol, o gateFuturesOrder) (venue.OrderResult, error) {
	inst, _ := a.lookupInstrument(ctx, venue.MarketPerp, s)
	mult := inst.ContractSize
	if mult <= 0 {
		mult = 1
	}
	filledContracts := abs64(o.Size) - abs64(o.Left)
	if filledContracts < 0 {
		filledContracts = 0
	}
	executed := float64(filledContracts) * mult
	avgPrice := parseFloat(o.FillPrice)
	if avgPrice == 0 {
		avgPrice = parseFloat(o.Price)
	}
	result := venue.OrderResult{
		Symbol:          s,
		Market:          venue.MarketPerp,
		ClientOrderID:   o.Text,
		ExchangeOrderID: strconv.FormatInt(o.ID, 10),
		Status:          normalizeFuturesOrderStatus(o.Status, o.FinishAs, o.Left),
		ExecutedQty:     executed,
		AvgPrice:        avgPrice,
		RawCode:         o.FinishAs,
	}
	if avgPrice > 0 {
		result.CumQuoteUSDT = executed * avgPrice
	}
	if o.CreateTime > 0 {
		result.CreatedAt = time.Unix(int64(o.CreateTime), 0).UTC()
	}
	return result, nil
}

func (a *Adapter) mapSpotOrder(s venue.Symbol, o gateSpotOrder) venue.OrderResult {
	amount := parseFloat(o.Amount)
	left := parseFloat(o.Left)
	executed := amount - left
	if executed < 0 {
		executed = 0
	}
	avgPrice := parseFloat(o.AvgDealPrice)
	if avgPrice == 0 {
		avgPrice = parseFloat(o.Price)
	}
	result := venue.OrderResult{
		Symbol:          s,
		Market:          venue.MarketSpot,
		ClientOrderID:   o.Text,
		ExchangeOrderID: o.ID,
		Status:          normalizeSpotOrderStatus(o.Status),
		ExecutedQty:     executed,
		AvgPrice:        avgPrice,
		CumQuoteUSDT:    parseFloat(o.FilledTotal),
		Fee:             mathAbs(parseFloat(o.Fee)),
		FeeAsset:        o.FeeCurrency,
		RawCode:         o.Status,
	}
	if sec, err := strconv.ParseInt(strings.TrimSpace(o.CreateTime), 10, 64); err == nil && sec > 0 {
		result.CreatedAt = time.Unix(sec, 0).UTC()
	}
	return result
}

// SetLeverage 设逐仓杠杆(isolated 模式直接 leverage=N;全仓切换不在适配器内做)。
func (a *Adapter) SetLeverage(ctx context.Context, s venue.Symbol, lev int) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	path := pathFuturesPositions + "/" + url.PathEscape(raw) + "/leverage"
	q := url.Values{}
	q.Set("leverage", strconv.Itoa(lev))
	return a.t.signedPost(ctx, path, q, nil, nil)
}

// SetMarginMode Gate 无 tdMode 概念,逐仓/全仓由账户设置决定,不在适配器切换。
func (a *Adapter) SetMarginMode(ctx context.Context, s venue.Symbol, mode venue.MarginMode) error {
	return venue.ErrUnsupported
}

func normalizeFuturesOrderStatus(status, finishAs string, left int64) string {
	if strings.EqualFold(status, "open") {
		if left > 0 {
			return "new"
		}
		return "new"
	}
	switch strings.ToLower(finishAs) {
	case "filled":
		return "filled"
	case "cancelled", "liquidated", "ioc", "auto_deleveraging", "reduce_only", "position_closed":
		return "canceled"
	default:
		if strings.EqualFold(status, "finished") {
			return "filled"
		}
		return strings.ToLower(status)
	}
}

func normalizeSpotOrderStatus(status string) string {
	switch strings.ToLower(status) {
	case "open":
		return "new"
	case "closed":
		return "filled"
	case "cancelled":
		return "canceled"
	default:
		return strings.ToLower(status)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
