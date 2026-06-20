package bybit

// transport.go 实现 Bybit v5 的 HTTP 传输:公共 GET + 签名 GET(HMAC-SHA256)+ 统一信封解析。
//
// 签名规则(官方 v5):sign = hex(HMAC_SHA256(secret, timestamp + apiKey + recvWindow + queryString)),
// 头 X-BAPI-API-KEY / X-BAPI-SIGN / X-BAPI-SIGN-TYPE=2 / X-BAPI-TIMESTAMP / X-BAPI-RECV-WINDOW。
// 关键:用于签名的 queryString 必须与实际附在 URL 上的串逐字节一致——本文件用同一个 encoded 串两用,确保一致。
//
// Stage 1 只做只读(公共行情 + 签名只读账户),不下单/不划转,故无 POST 签名分支(留待 Stage 3)。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultBaseURL    = "https://api.bybit.com"
	defaultRecvWindow = "5000"
	defaultTimeout    = 8 * time.Second
)

// transport 持有 Bybit REST 访问所需的最小状态;不依赖任何币安类型,自包含。
type transport struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	apiSecret  string
	recvWindow string
	nowFn      func() time.Time // 可注入,便于测试固定时间戳
	// timeOffsetMs 是「Bybit 服务器时间 − 本地时间」的毫秒偏移(SyncServerTime 写入,签名时间戳叠加)。
	// 修 PROJECT_AUDIT C-5:此前签名直接用本地时钟,VPS 漂移超 recvWindow(5s)即全部签名请求被拒且无自愈。
	timeOffsetMs atomic.Int64
}

func newTransport(baseURL, apiKey, apiSecret string) *transport {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &transport{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    base,
		apiKey:     strings.TrimSpace(apiKey),
		apiSecret:  strings.TrimSpace(apiSecret),
		recvWindow: defaultRecvWindow,
		nowFn:      time.Now,
	}
}

// envelope 是 Bybit v5 统一响应外壳。retCode==0 为成功,否则取 retMsg 报错。
type envelope struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
	Time    int64           `json:"time"`
}

// signString 是纯函数:对 payload 用 secret 做 HMAC-SHA256,返回小写 hex。便于单测对照向量。
func signString(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// signaturePayload 组装 v5 GET 待签名串:timestamp + apiKey + recvWindow + queryString。
func signaturePayload(timestamp, apiKey, recvWindow, queryString string) string {
	return timestamp + apiKey + recvWindow + queryString
}

// signTimestampMs 返回签名用毫秒时间戳 = 本地时钟 + 已同步的服务器偏移。
func (t *transport) signTimestampMs() string {
	return strconv.FormatInt(t.nowFn().UTC().UnixMilli()+t.timeOffsetMs.Load(), 10)
}

// syncServerTime 拉 /v5/market/time 计算「服务器 − 本地」偏移(以请求中点近似),写入 timeOffsetMs
// 供后续签名使用,并返回偏移量。失败不改动现有偏移。
func (t *transport) syncServerTime(ctx context.Context) (time.Duration, error) {
	var res struct {
		TimeSecond string `json:"timeSecond"`
		TimeNano   string `json:"timeNano"`
	}
	start := t.nowFn()
	if err := t.do(ctx, "/v5/market/time", nil, false, &res); err != nil {
		return 0, fmt.Errorf("bybit: 拉取服务器时间失败: %w", err)
	}
	rtt := t.nowFn().Sub(start)
	serverMs := int64(0)
	if nano, err := strconv.ParseInt(strings.TrimSpace(res.TimeNano), 10, 64); err == nil && nano > 0 {
		serverMs = nano / int64(time.Millisecond)
	} else if sec, err := strconv.ParseInt(strings.TrimSpace(res.TimeSecond), 10, 64); err == nil && sec > 0 {
		serverMs = sec * 1000
	} else {
		return 0, fmt.Errorf("bybit: 服务器时间响应无法解析: timeSecond=%q timeNano=%q", res.TimeSecond, res.TimeNano)
	}
	localMidMs := start.Add(rtt / 2).UTC().UnixMilli()
	offsetMs := serverMs - localMidMs
	t.timeOffsetMs.Store(offsetMs)
	return time.Duration(offsetMs) * time.Millisecond, nil
}

// publicGet 发公共 GET(无需签名),把 result 解进 out。
func (t *transport) publicGet(ctx context.Context, path string, query url.Values, out any) error {
	return t.do(ctx, path, query, false, out)
}

// signedGet 发签名 GET(只读私有端点),把 result 解进 out。
func (t *transport) signedGet(ctx context.Context, path string, query url.Values, out any) error {
	if t.apiKey == "" || t.apiSecret == "" {
		return fmt.Errorf("bybit: 缺少 API key/secret,无法发签名请求")
	}
	return t.do(ctx, path, query, true, out)
}

// signedPost 发签名 POST(下单/撤单/设杠杆等写动作)。v5 规则:待签名串 = ts+apiKey+recvWindow+jsonBody,
// body 为 reqBody 的 JSON 序列化结果(与实际发送字节逐字一致)。把 result 解进 out。
func (t *transport) signedPost(ctx context.Context, path string, reqBody any, out any) error {
	if t.apiKey == "" || t.apiSecret == "" {
		return fmt.Errorf("bybit: 缺少 API key/secret,无法发签名写请求")
	}
	bodyBytes := []byte("{}")
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("bybit: 序列化请求体失败: %w", err)
		}
		bodyBytes = b
	}
	ts := t.signTimestampMs()
	sign := signString(t.apiSecret, signaturePayload(ts, t.apiKey, t.recvWindow, string(bodyBytes)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", t.apiKey)
	req.Header.Set("X-BAPI-SIGN", sign)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", t.recvWindow)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bybit: HTTP %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("bybit: 解析信封失败: %w", err)
	}
	if env.RetCode != 0 {
		// 带上 path 与请求体(下单体不含密钥,密钥在 header)以便定位"未知参数"等参数级错误,避免靠猜。
		return fmt.Errorf("bybit: retCode=%d retMsg=%q path=%s body=%s", env.RetCode, env.RetMsg, path, truncate(string(bodyBytes), 512))
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("bybit: 解析 result 失败: %w", err)
	}
	return nil
}

// doGetMaxAttempts/doGetBackoff:GET 幂等,对瞬时故障(网络错误/5xx/限频 retCode=10006)做有限重试,
// 修 PROJECT_AUDIT C-6:此前任何一次网络抖动都会让 manage 整轮跳过(平仓顺延一个 tick)。
const (
	doGetMaxAttempts = 3
	doGetBackoff     = 300 * time.Millisecond
)

func (t *transport) do(ctx context.Context, path string, query url.Values, signed bool, out any) error {
	var lastErr error
	for attempt := 1; attempt <= doGetMaxAttempts; attempt++ {
		retryable, err := t.doOnce(ctx, path, query, signed, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == doGetMaxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(doGetBackoff * time.Duration(attempt)):
		}
	}
	return lastErr
}

// doOnce 单次 GET。返回 (是否可安全重试, 错误)。
func (t *transport) doOnce(ctx context.Context, path string, query url.Values, signed bool, out any) (bool, error) {
	qs := ""
	if query != nil {
		qs = query.Encode() // Go 按 key 排序;签名与 URL 用同一串,保证一致
	}
	full := t.baseURL + path
	if qs != "" {
		full += "?" + qs
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return false, err
	}
	if signed {
		// 时间戳/签名每次重试重算,避免重试时超出 recvWindow。
		ts := t.signTimestampMs()
		sign := signString(t.apiSecret, signaturePayload(ts, t.apiKey, t.recvWindow, qs))
		req.Header.Set("X-BAPI-API-KEY", t.apiKey)
		req.Header.Set("X-BAPI-SIGN", sign)
		req.Header.Set("X-BAPI-SIGN-TYPE", "2")
		req.Header.Set("X-BAPI-TIMESTAMP", ts)
		req.Header.Set("X-BAPI-RECV-WINDOW", t.recvWindow)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		// 网络层错误(连接重置/超时):GET 幂等,可重试;但 ctx 已取消则不重试。
		return ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ctx.Err() == nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode >= 500, fmt.Errorf("bybit: HTTP %d: %s", resp.StatusCode, truncate(string(body), 256))
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return false, fmt.Errorf("bybit: 解析信封失败: %w", err)
	}
	if env.RetCode != 0 {
		// 10006 = 访问过频(限频),退避后重试一次是安全的。
		return env.RetCode == 10006, fmt.Errorf("bybit: retCode=%d retMsg=%q", env.RetCode, env.RetMsg)
	}
	if out == nil {
		return false, nil
	}
	if len(env.Result) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return false, fmt.Errorf("bybit: 解析 result 失败: %w", err)
	}
	return false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// parseFloat 宽松解析 Bybit 字符串数字(空串/非法 → 0),供行情/账户解析复用。
func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
