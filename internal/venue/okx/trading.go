package okx

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"lingxi/internal/venue"
)

// trading.go 实现 OKX v5 私有写动作(签名 POST):下单/撤单/查单/设杠杆。
//
// 数量口径:venue.OrderRequest.Qty 统一为「币数量」(base coin)。OKX SWAP 的 sz 为「张」,
// PlaceOrder 内按 ctVal 换算并按 lotSz 向下取整;现货市价买单须 tgtCcy=base_ccy 与中性 Qty 对齐。

func okxSide(s venue.Side) string {
	if s == venue.SideSell {
		return "sell"
	}
	return "buy"
}

func okxOrderType(t venue.OrderType) (string, error) {
	switch t {
	case venue.OrderTypeMarket:
		return "market", nil
	case venue.OrderTypeLimit:
		return "limit", nil
	case venue.OrderTypeIOC:
		return "ioc", nil
	case venue.OrderTypePostOnly:
		return "post_only", nil
	default:
		return "", fmt.Errorf("okx: 未知订单类型 %v", t)
	}
}

func okxTdMode(m venue.Market) string {
	if m == venue.MarketSpot {
		return "cash"
	}
	return "cross"
}

func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// sanitizeClOrdID 净化客户端订单号:OKX clOrdId 仅允许字母数字、最长 32 字符(不允许连字符)。
// 引擎侧 clientOrderID 常带 UUID 连字符,直接提交会撞参数错误。
func sanitizeClOrdID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

func roundDownStep(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	// 加微小 epsilon 再向下取整,避免浮点表示把恰好落在网格上的值(如 3.0 存成 2.9999999)误降一格。
	return math.Floor(v/step+1e-9) * step
}

// stepDecimals 返回步进 step 隐含的小数位数,用于把下单数量格式化到交易所网格。
func stepDecimals(step float64) int {
	if step <= 0 || step >= 1 {
		return 0
	}
	s := strconv.FormatFloat(step, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

// fmtStepDown 把 v 向下取整到 step 网格,并按 step 精度格式化后去除多余尾零。
// 关键:用 FormatFloat(..., 定精度) 而非 -1 精度,杜绝 0.30000000000000004 这类浮点尾巴,
// 否则 OKX 会以 51121「Order quantity must be a multiple of the lot size」拒单。
func fmtStepDown(v, step float64) string {
	if step <= 0 {
		return fmtNum(v)
	}
	q := roundDownStep(v, step)
	s := strconv.FormatFloat(q, 'f', stepDecimals(step), 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func qtyToContractSz(qty, ctVal, lotSz float64) (string, error) {
	if ctVal <= 0 {
		return "", fmt.Errorf("okx: ctVal 无效")
	}
	szStr := fmtStepDown(qty/ctVal, lotSz)
	if v, _ := strconv.ParseFloat(szStr, 64); v <= 0 {
		return "", fmt.Errorf("okx: 换算后张数为 0(qty=%v ctVal=%v lotSz=%v)", qty, ctVal, lotSz)
	}
	return szStr, nil
}

type okxPlaceOrderBody struct {
	InstID     string `json:"instId"`
	TdMode     string `json:"tdMode"`
	Side       string `json:"side"`
	OrdType    string `json:"ordType"`
	Sz         string `json:"sz"`
	Px         string `json:"px,omitempty"`
	ClOrdID    string `json:"clOrdId,omitempty"`
	TgtCcy     string `json:"tgtCcy,omitempty"`
	ReduceOnly bool   `json:"reduceOnly,omitempty"`
}

func (a *Adapter) buildPlaceOrderBody(ctx context.Context, req venue.OrderRequest) (okxPlaceOrderBody, error) {
	raw, err := a.ToVenue(req.Symbol, req.Market)
	if err != nil {
		return okxPlaceOrderBody{}, err
	}
	if req.Qty <= 0 {
		return okxPlaceOrderBody{}, fmt.Errorf("okx: 下单数量必须为正, got %v", req.Qty)
	}
	ordType, err := okxOrderType(req.Type)
	if err != nil {
		return okxPlaceOrderBody{}, err
	}
	inst, err := a.lookupInstrument(ctx, req.Market, req.Symbol)
	if err != nil {
		return okxPlaceOrderBody{}, err
	}
	sz := fmtStepDown(req.Qty, inst.StepSize)
	if req.Market == venue.MarketPerp {
		sz, err = qtyToContractSz(req.Qty, inst.ContractSize, inst.StepSize)
		if err != nil {
			return okxPlaceOrderBody{}, err
		}
	}
	body := okxPlaceOrderBody{
		InstID:     raw,
		TdMode:     okxTdMode(req.Market),
		Side:       okxSide(req.Side),
		OrdType:    ordType,
		Sz:         sz,
		ClOrdID:    sanitizeClOrdID(req.ClientOrderID),
		ReduceOnly: req.ReduceOnly,
	}
	if ordType == "limit" || ordType == "ioc" || ordType == "post_only" {
		if req.Price <= 0 {
			return okxPlaceOrderBody{}, fmt.Errorf("okx: 限价/IOC 单需正价格, got %v", req.Price)
		}
		body.Px = fmtNum(req.Price)
	}
	if req.Market == venue.MarketSpot && ordType == "market" {
		body.TgtCcy = "base_ccy"
	}
	return body, nil
}

func (a *Adapter) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	body, err := a.buildPlaceOrderBody(ctx, req)
	if err != nil {
		return venue.OrderResult{}, err
	}
	var rows []struct {
		OrdID   string `json:"ordId"`
		ClOrdID string `json:"clOrdId"`
	}
	if err := a.t.signedPost(ctx, pathPlaceOrder, body, &rows); err != nil {
		return venue.OrderResult{}, err
	}
	ackOrdID, ackClOrdID := "", ""
	if len(rows) > 0 {
		ackOrdID = rows[0].OrdID
		ackClOrdID = rows[0].ClOrdID
	}
	return venue.OrderResult{
		Symbol:          req.Symbol,
		Market:          req.Market,
		ClientOrderID:   firstNonEmpty(ackClOrdID, body.ClOrdID, req.ClientOrderID),
		ExchangeOrderID: ackOrdID,
		Status:          "new",
	}, nil
}

func (a *Adapter) CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return err
	}
	body := struct {
		InstID  string `json:"instId"`
		ClOrdID string `json:"clOrdId"`
	}{InstID: raw, ClOrdID: sanitizeClOrdID(clientOrderID)}
	return a.t.signedPost(ctx, pathCancelOrder, body, nil)
}

func (a *Adapter) QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.OrderResult{}, err
	}
	q := url.Values{}
	q.Set("instId", raw)
	q.Set("clOrdId", sanitizeClOrdID(clientOrderID))
	var rows []struct {
		OrdID     string `json:"ordId"`
		ClOrdID   string `json:"clOrdId"`
		State     string `json:"state"`
		AvgPx     string `json:"avgPx"`
		AccFillSz string `json:"accFillSz"`
		Fee       string `json:"fee"`
		FeeCcy    string `json:"feeCcy"`
		CTime     string `json:"cTime"`
	}
	if err := a.t.signedGet(ctx, pathQueryOrder, q, &rows); err != nil {
		return venue.OrderResult{}, err
	}
	if len(rows) == 0 {
		return venue.OrderResult{Symbol: s, Market: m, ClientOrderID: clientOrderID, Status: "unknown"}, nil
	}
	o := rows[0]
	executed := parseFloat(o.AccFillSz)
	if m == venue.MarketPerp {
		// SWAP 的 accFillSz 是「张」,必须乘 ctVal 才是币数;拉取失败绝不能静默把张数当币数留下
		// (账本漂移根因,见 HANDOFF §3)。失败重试一次后仍不行就报错。
		inst, ierr := a.lookupInstrument(ctx, m, s)
		if ierr != nil || inst.ContractSize <= 0 {
			inst, ierr = a.lookupInstrument(ctx, m, s)
		}
		if ierr != nil || inst.ContractSize <= 0 {
			return venue.OrderResult{}, fmt.Errorf("okx: 取 %s ctVal 失败,拒绝把张数当币数误报(err=%v ContractSize=%v)", s, ierr, inst.ContractSize)
		}
		executed *= inst.ContractSize
	}
	result := venue.OrderResult{
		Symbol:          s,
		Market:          m,
		ClientOrderID:   firstNonEmpty(o.ClOrdID, clientOrderID),
		ExchangeOrderID: o.OrdID,
		Status:          normalizeOrderStatus(o.State),
		ExecutedQty:     executed,
		AvgPrice:        parseFloat(o.AvgPx),
		Fee:             mathAbs(parseFloat(o.Fee)),
		FeeAsset:        o.FeeCcy,
		RawCode:         o.State,
	}
	if ms := msStringToTime(o.CTime); !ms.IsZero() {
		result.CreatedAt = ms
	}
	if result.AvgPrice > 0 {
		result.CumQuoteUSDT = result.ExecutedQty * result.AvgPrice
	}
	return result, nil
}

func (a *Adapter) SetLeverage(ctx context.Context, s venue.Symbol, lev int) error {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return err
	}
	body := struct {
		InstID  string `json:"instId"`
		Lever   string `json:"lever"`
		MgnMode string `json:"mgnMode"`
	}{InstID: raw, Lever: strconv.Itoa(lev), MgnMode: "cross"}
	return a.t.signedPost(ctx, pathSetLeverage, body, nil)
}

func (a *Adapter) SetMarginMode(ctx context.Context, s venue.Symbol, mode venue.MarginMode) error {
	return venue.ErrUnsupported
}

func normalizeOrderStatus(s string) string {
	switch strings.ToLower(s) {
	case "filled":
		return "filled"
	case "partially_filled":
		return "partially_filled"
	case "canceled", "cancelled":
		return "canceled"
	case "live", "effective":
		return "new"
	default:
		return strings.ToLower(s)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
