package okx

// private_events.go 是 OKX v5 私有 WS(订单/持仓)通道的骨架:登录签名构造、订阅帧构造、
// 推送报文解析全部是纯函数(不触网,可离线单测);真实的网络鉴权 + 连接 + 心跳 + 重连循环
// 尚未实现——见文件末尾 Dialer/DialLivePrivateStream 的显式未实现声明,禁止在未验证真实网络
// 鉴权路径的情况下被误接入生产 wiring。
//
// 参考:OKX v5 WS 私有登录(https://www.okx.com/docs-v5/en/#overview-websocket-login):
//
//	timestamp = Unix Epoch 秒(字符串,非毫秒)
//	sign      = Base64(HMAC_SHA256(secret, timestamp + "GET" + "/users/self/verify"))
//
// 与 REST 签名(transport.go)共用同一套 HMAC-SHA256 + Base64 算法(signString),
// 只是待签名串与时间戳精度不同,因此本文件直接复用 signString 而不重新实现哈希逻辑。
import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

const (
	privateLoginMethod = "GET"
	privateLoginPath   = "/users/self/verify"

	// PrivateWSURL 是 OKX v5 私有频道默认地址(生产环境)。真实网络实现应支持通过配置覆盖
	// (模拟盘/AWS 就近节点),本文件只固化默认值,不在此处发起任何连接。
	PrivateWSURL = "wss://ws.okx.com:8443/ws/v5/private"
)

// PrivateLoginArg 对应 OKX WS 登录帧 args[0] 的字段(json 标签与官方文档一致)。
type PrivateLoginArg struct {
	APIKey     string `json:"apiKey"`
	Passphrase string `json:"passphrase"`
	Timestamp  string `json:"timestamp"`
	Sign       string `json:"sign"`
}

// PrivateLoginFrame 是登录操作帧:{"op":"login","args":[...]}。
type PrivateLoginFrame struct {
	Op   string            `json:"op"`
	Args []PrivateLoginArg `json:"args"`
}

// privateLoginTimestamp 返回登录签名用的 Unix Epoch 秒字符串(注意:与 REST 的
// ISO8601 毫秒时间戳格式不同,WS 登录严格要求「秒」,混用会导致签名校验失败)。
func privateLoginTimestamp(at time.Time) string {
	return strconv.FormatInt(at.UTC().Unix(), 10)
}

// BuildPrivateLoginArg 构造单个登录 arg(纯函数,给定输入必然产出给定输出,可离线单测)。
func BuildPrivateLoginArg(apiKey, apiSecret, passphrase string, at time.Time) PrivateLoginArg {
	ts := privateLoginTimestamp(at)
	sign := signString(apiSecret, ts+privateLoginMethod+privateLoginPath)
	return PrivateLoginArg{APIKey: apiKey, Passphrase: passphrase, Timestamp: ts, Sign: sign}
}

// BuildPrivateLoginFrame 构造完整登录帧的 JSON 字节,供真实 WS 实现直接 WriteMessage。
func BuildPrivateLoginFrame(apiKey, apiSecret, passphrase string, at time.Time) ([]byte, error) {
	frame := PrivateLoginFrame{Op: "login", Args: []PrivateLoginArg{BuildPrivateLoginArg(apiKey, apiSecret, passphrase, at)}}
	return json.Marshal(frame)
}

// PrivateSubscribeArg 是订阅帧单个 channel 描述(instId 留空表示不按标的过滤,由 instType 控制范围)。
type PrivateSubscribeArg struct {
	Channel  string `json:"channel"`
	InstType string `json:"instType,omitempty"`
	InstID   string `json:"instId,omitempty"`
}

// PrivateSubscribeFrame 是订阅操作帧:{"op":"subscribe","args":[...]}。
type PrivateSubscribeFrame struct {
	Op   string                `json:"op"`
	Args []PrivateSubscribeArg `json:"args"`
}

// BuildOrdersSubscribeFrame 构造订单通道订阅帧的 JSON 字节。instType 为空时默认 "ANY"
// (订阅全部合约类型的订单更新,含 SPOT/SWAP)。
func BuildOrdersSubscribeFrame(instType string) ([]byte, error) {
	if strings.TrimSpace(instType) == "" {
		instType = "ANY"
	}
	frame := PrivateSubscribeFrame{Op: "subscribe", Args: []PrivateSubscribeArg{{Channel: "orders", InstType: instType}}}
	return json.Marshal(frame)
}

// BuildPositionsSubscribeFrame 构造持仓频道订阅帧。instType 为空时默认 ANY。
func BuildPositionsSubscribeFrame(instType string) ([]byte, error) {
	if strings.TrimSpace(instType) == "" {
		instType = "ANY"
	}
	frame := PrivateSubscribeFrame{Op: "subscribe", Args: []PrivateSubscribeArg{{Channel: "positions", InstType: instType}}}
	return json.Marshal(frame)
}

// PrivateOrderData 是 OKX "orders" 私有频道单条推送数据的中性解析结果(字段取自官方文档
// order 通道 push data 结构;只保留订单真相底座需要的子集,其余字段解析时忽略)。
type PrivateOrderData struct {
	InstType   string `json:"instType"`
	InstID     string `json:"instId"`
	OrdID      string `json:"ordId"`
	ClOrdID    string `json:"clOrdId"`
	OrdType    string `json:"ordType"`
	Side       string `json:"side"`
	State      string `json:"state"`
	Px         string `json:"px"`
	Sz         string `json:"sz"`
	AvgPx      string `json:"avgPx"`
	AccFillSz  string `json:"accFillSz"`
	FillSz     string `json:"fillSz"`
	FillPx     string `json:"fillPx"`
	FillPnl    string `json:"fillPnl"`
	TradeID    string `json:"tradeId"`
	FeeCcy     string `json:"feeCcy"`
	Fee        string `json:"fee"`
	ReduceOnly string `json:"reduceOnly"`
	Category   string `json:"category"`
	Code       string `json:"code"`
	Msg        string `json:"msg"`
	UTime      string `json:"uTime"`
	CTime      string `json:"cTime"`
}

// PrivateOrdersPush 是 orders 频道整条推送报文的中性解析结果。
type PrivateOrdersPush struct {
	Arg struct {
		Channel  string `json:"channel"`
		InstType string `json:"instType"`
	} `json:"arg"`
	Data []PrivateOrderData `json:"data"`
}

// ParsePrivateOrdersPush 把 orders 频道原始推送字节解析成中性结构。纯函数,不做符号翻译/
// 张数换算(那需要 instrument 缓存,留给调用方按需用 Adapter.ToCanonical / lookupInstrument
// 二次翻译,见 PrivateOrderData.ToOrderResult 的用法注释)。
func ParsePrivateOrdersPush(raw []byte) (PrivateOrdersPush, error) {
	var push PrivateOrdersPush
	if err := json.Unmarshal(raw, &push); err != nil {
		return PrivateOrdersPush{}, fmt.Errorf("okx: 解析私有 orders 推送失败: %w", err)
	}
	if push.Arg.Channel != "" && push.Arg.Channel != "orders" {
		return PrivateOrdersPush{}, fmt.Errorf("okx: 非 orders 频道推送: %q", push.Arg.Channel)
	}
	return push, nil
}

// ToOrderResult 把单条 orders 推送翻译成中性 venue.OrderResult。ctVal 是该 instId 的合约面值
// (SWAP 下单位为「张」,需换算成币数量;现货传 1)——真实调用方应通过 Adapter.lookupInstrument
// 拿到,本函数本身不触网、不做该查询,避免把网络依赖藏进「解析」这一步。
func (d PrivateOrderData) ToOrderResult(symbol venue.Symbol, market venue.Market, ctVal float64) venue.OrderResult {
	if ctVal <= 0 {
		ctVal = 1
	}
	executed := parseFloat(d.AccFillSz)
	if market == venue.MarketPerp {
		executed *= ctVal
	}
	result := venue.OrderResult{
		Symbol:          symbol,
		Market:          market,
		ClientOrderID:   d.ClOrdID,
		ExchangeOrderID: d.OrdID,
		Status:          normalizeOrderStatus(d.State),
		ExecutedQty:     executed,
		AvgPrice:        parseFloat(d.AvgPx),
		Fee:             mathAbs(parseFloat(d.Fee)),
		FeeAsset:        d.FeeCcy,
		RawCode:         d.State,
	}
	if ct := msStringToTime(d.CTime); !ct.IsZero() {
		result.CreatedAt = ct
	}
	if result.AvgPrice > 0 {
		result.CumQuoteUSDT = result.ExecutedQty * result.AvgPrice
	}
	return result
}

// FillQty / FillPrice / FillTradeID 是本条推送里「本次成交增量」的字段(与 AccFillSz 的
// 累计口径不同),供调用方翻译成 execution.Fill 时取用;SWAP 下 FillQty 同样需要乘 ctVal。
func (d PrivateOrderData) FillQty(market venue.Market, ctVal float64) float64 {
	if ctVal <= 0 {
		ctVal = 1
	}
	qty := parseFloat(d.FillSz)
	if market == venue.MarketPerp {
		qty *= ctVal
	}
	return qty
}

func (d PrivateOrderData) FillPrice() float64  { return parseFloat(d.FillPx) }
func (d PrivateOrderData) FillTradeID() string { return strings.TrimSpace(d.TradeID) }

// HasFill 报告本条推送是否携带一笔新成交增量(FillSz>0 且带 tradeId)。
func (d PrivateOrderData) HasFill() bool {
	return d.FillTradeID() != "" && parseFloat(d.FillSz) > 0
}

// ToExecutionEvents 把一条 OKX orders 推送拆成 execution 的订单事件和可选成交事件。
// ctVal 必须由 instrument 快照提供；手续费币种可能不是 USDT，因此这里不擅自换算 FeeMicros，
// 真实接线层应在有币种估值时补齐。该函数纯逻辑、无网络。
func (d PrivateOrderData) ToExecutionEvents(symbol venue.Symbol, market venue.Market, ctVal float64, receivedAt time.Time) []execution.PrivateEvent {
	state := execution.OrderStateUnknown
	switch strings.ToLower(strings.TrimSpace(d.State)) {
	case "live", "effective":
		state = execution.OrderStateAcked
	case "partially_filled":
		state = execution.OrderStatePartiallyFilled
	case "filled":
		state = execution.OrderStateFilled
	case "canceled", "cancelled", "mmp_canceled":
		state = execution.OrderStateCanceled
	}
	order := execution.OrderSnapshot{
		Symbol: string(symbol), ClientOrderID: d.ClOrdID, ExchangeOrderID: d.OrdID,
		State: state, ExecutedQty: d.ToOrderResult(symbol, market, ctVal).ExecutedQty,
		AvgPrice: parseFloat(d.AvgPx),
	}
	events := []execution.PrivateEvent{{Order: &order, ReceivedAt: receivedAt}}
	if d.HasFill() {
		side := execution.SideBuy
		if strings.EqualFold(strings.TrimSpace(d.Side), "sell") {
			side = execution.SideSell
		}
		fill := execution.Fill{
			Symbol: string(symbol), ClientOrderID: d.ClOrdID, ExchangeOrderID: d.OrdID,
			TradeID: d.FillTradeID(), Side: side, Qty: d.FillQty(market, ctVal),
			Price: d.FillPrice(), ExchangeTime: msStringToTime(d.UTime), ReceiveTime: receivedAt,
		}
		events = append(events, execution.PrivateEvent{Fill: &fill, ReceivedAt: receivedAt})
	}
	return events
}

// IsUnknownState 报告该推送的 state 字段是否为「订单真相底座」认识以外的值——OKX 目前公开
// 的 state 取值为 live/partially_filled/filled/canceled/mmp_canceled 等;出现未知取值时,
// 调用方应把该订单归入 execution.OrderStateUnknown 而不是猜测归一化,防止悄悄吞掉一个真正
// 的新状态语义。
func (d PrivateOrderData) IsUnknownState() bool {
	switch strings.ToLower(strings.TrimSpace(d.State)) {
	case "live", "effective", "partially_filled", "filled", "canceled", "cancelled", "mmp_canceled":
		return false
	default:
		return true
	}
}

// PrivatePositionData 是 OKX "positions" 私有频道推送中恢复所需的最小字段集合。
type PrivatePositionData struct {
	InstType string `json:"instType"`
	InstID   string `json:"instId"`
	PosSide  string `json:"posSide"`
	Pos      string `json:"pos"`
	AvgPx    string `json:"avgPx"`
	MarkPx   string `json:"markPx"`
	UTime    string `json:"uTime"`
}

// PrivatePositionsPush 是 positions 频道整条推送报文。
type PrivatePositionsPush struct {
	Arg struct {
		Channel  string `json:"channel"`
		InstType string `json:"instType"`
	} `json:"arg"`
	Data []PrivatePositionData `json:"data"`
}

// ParsePrivatePositionsPush 纯解析 positions 推送；张数到币数量的 ctVal 换算由接入层完成。
func ParsePrivatePositionsPush(raw []byte) (PrivatePositionsPush, error) {
	var push PrivatePositionsPush
	if err := json.Unmarshal(raw, &push); err != nil {
		return PrivatePositionsPush{}, fmt.Errorf("okx: 解析私有 positions 推送失败: %w", err)
	}
	if push.Arg.Channel != "" && push.Arg.Channel != "positions" {
		return PrivatePositionsPush{}, fmt.Errorf("okx: 非 positions 频道推送: %q", push.Arg.Channel)
	}
	return push, nil
}

// SignedQty 把 OKX pos/posSide 归一成有符号数量；net 模式沿用 pos 自身符号，
// long/short 模式分别强制正/负。SWAP 调用方传真实 ctVal，现货/未知传 1。
func (d PrivatePositionData) SignedQty(ctVal float64) float64 {
	if ctVal <= 0 {
		ctVal = 1
	}
	qty := parseFloat(d.Pos) * ctVal
	switch strings.ToLower(strings.TrimSpace(d.PosSide)) {
	case "long":
		return mathAbs(qty)
	case "short":
		return -mathAbs(qty)
	default:
		return qty
	}
}
