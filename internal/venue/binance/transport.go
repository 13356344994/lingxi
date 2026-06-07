package binance

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"lingxi/internal/exchange"
)

// HTTPOutcome 是一次币安 REST 调用的**原始** HTTP 结果(传输层成功,但 HTTP 状态可能非 200)。
//
// 错误分类(网络失败 / 非 200 / 币安业务错误码)由引擎侧中间件按既有 signedRequestError 语义封装,
// 本包刻意不引入 app 层错误类型,避免 app ↔ venue 反向依赖(import cycle)。
type HTTPOutcome struct {
	Body       []byte
	StatusCode int
	Status     string
}

// SignedSend 执行一次币安签名请求:克隆参数后盖 recvWindow/timestamp、按币安 HMAC-SHA256 方案
// 把 "&signature=" 追加到 query、设置 X-MBX-APIKEY 头、发起 HTTP、并从响应头记账 used-weight。
//
// 这是 P1「下沉签名/端点知识」的落点:**怎么签**(query 编码 + 签名 + 鉴权头)收敛进 binance 包;
// 引擎侧 BinanceGateway 仅保留与所无关的通用中间件(熔断 / 限频 / used-weight 预算 / -1021 重试编排 /
// 平台请求计数)。返回的 error **仅**代表请求构造 / 网络 / 读取失败;HTTP 非 200 通过 HTTPOutcome 上报,
// 由调用方映射为 signedRequestError —— 与下沉前逐字节一致。
//
// timestampMillis 由调用方传入(引擎用 currentServerTimeMillis 校时);每次调用都用新鲜时间重签,
// 故 -1021 重试时拿到的是新 timestamp。
func (a *Adapter) SignedSend(ctx context.Context, method, fullURL string, params url.Values, recvWindow, timestampMillis int64) (HTTPOutcome, error) {
	signed := url.Values{}
	for key, items := range params {
		signed[key] = append([]string(nil), items...)
	}
	signed.Set("recvWindow", strconv.FormatInt(recvWindow, 10))
	signed.Set("timestamp", strconv.FormatInt(timestampMillis, 10))

	query := signed.Encode()
	query = query + "&signature=" + exchange.Sign(a.client.SecretKey(), query)

	req, err := http.NewRequestWithContext(ctx, method, fullURL+"?"+query, nil)
	if err != nil {
		return HTTPOutcome{}, err
	}
	req.Header.Set("X-MBX-APIKEY", a.client.APIKey())

	resp, err := a.client.HTTPClient().Do(req)
	if err != nil {
		return HTTPOutcome{}, err
	}
	defer resp.Body.Close()
	a.client.NoteUsedWeightFromHeaders(resp.Header)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPOutcome{}, err
	}
	return HTTPOutcome{Body: body, StatusCode: resp.StatusCode, Status: resp.Status}, nil
}
