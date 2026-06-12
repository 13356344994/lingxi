package okx

// transport.go 实现 OKX v5 的 HTTP 传输:公共 GET + 签名 GET/POST(HMAC-SHA256 Base64)+ 统一信封解析。
//
// 签名规则(官方 v5):OK-ACCESS-SIGN = Base64(HMAC_SHA256(secret, timestamp + method + requestPath + body))。
// requestPath 含查询串(如 /api/v5/account/balance?ccy=BTC);GET 无 body;POST body 为 JSON 字符串,
// 签名串与实际发送字节必须逐字一致——本文件用同一份 bodyBytes 两用,确保一致。
// OK-ACCESS-TIMESTAMP 为 ISO8601 毫秒 UTC(如 2020-12-08T09:08:57.715Z)。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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
	defaultBaseURL = "https://www.okx.com"
	defaultTimeout = 8 * time.Second
)

// transport 持有 OKX REST 访问所需的最小状态;不依赖任何其他交易所类型,自包含。
type transport struct {
	httpClient   *http.Client
	baseURL      string
	apiKey       string
	apiSecret    string
	passphrase   string
	nowFn        func() time.Time // 可注入,便于测试固定时间戳
	timeOffsetMs atomic.Int64     // 服务器时间 − 本地时间(毫秒),签名时间戳叠加
}

func newTransport(baseURL, apiKey, apiSecret, passphrase string) *transport {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &transport{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    base,
		apiKey:     strings.TrimSpace(apiKey),
		apiSecret:  strings.TrimSpace(apiSecret),
		passphrase: strings.TrimSpace(passphrase),
		nowFn:      time.Now,
	}
}

// envelope 是 OKX v5 统一响应外壳。code=="0" 为成功,否则取 msg 报错。
type envelope struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// signString 对 payload 用 secret 做 HMAC-SHA256,返回 Base64。便于单测对照向量。
func signString(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// signaturePayload 组装 OKX 待签名串:timestamp + method + requestPath + body。
func signaturePayload(timestamp, method, requestPath, body string) string {
	return timestamp + method + requestPath + body
}

// signTimestampISO 返回签名用 ISO8601 毫秒 UTC 时间戳 = 本地时钟 + 已同步的服务器偏移。
func (t *transport) signTimestampISO() string {
	ms := t.nowFn().UTC().UnixMilli() + t.timeOffsetMs.Load()
	sec := ms / 1000
	nsec := (ms % 1000) * int64(time.Millisecond)
	return time.Unix(sec, nsec).UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// syncServerTime 拉 /api/v5/public/time 计算「服务器 − 本地」偏移(以请求中点近似),写入 timeOffsetMs
// 供后续签名使用,并返回偏移量。失败不改动现有偏移。
func (t *transport) syncServerTime(ctx context.Context) (time.Duration, error) {
	var rows []struct {
		TS string `json:"ts"`
	}
	start := t.nowFn()
	if err := t.do(ctx, http.MethodGet, pathServerTime, nil, false, &rows); err != nil {
		return 0, fmt.Errorf("okx: 拉取服务器时间失败: %w", err)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("okx: 服务器时间响应为空")
	}
	serverMs, err := strconv.ParseInt(strings.TrimSpace(rows[0].TS), 10, 64)
	if err != nil || serverMs <= 0 {
		return 0, fmt.Errorf("okx: 服务器时间无法解析: ts=%q", rows[0].TS)
	}
	localMidMs := start.Add(t.nowFn().Sub(start) / 2).UTC().UnixMilli()
	offsetMs := serverMs - localMidMs
	t.timeOffsetMs.Store(offsetMs)
	return time.Duration(offsetMs) * time.Millisecond, nil
}

// publicGet 发公共 GET(无需签名),把 data 解进 out(out 通常是指向切片的指针)。
func (t *transport) publicGet(ctx context.Context, path string, query url.Values, out any) error {
	return t.do(ctx, http.MethodGet, path, query, false, out)
}

// signedGet 发签名 GET(只读私有端点),把 data 解进 out。
func (t *transport) signedGet(ctx context.Context, path string, query url.Values, out any) error {
	if t.apiKey == "" || t.apiSecret == "" || t.passphrase == "" {
		return fmt.Errorf("okx: 缺少 API key/secret/passphrase,无法发签名请求")
	}
	return t.do(ctx, http.MethodGet, path, query, true, out)
}

// signedPost 发签名 POST(下单/撤单/设杠杆等写动作)。把 data 解进 out。
func (t *transport) signedPost(ctx context.Context, path string, reqBody any, out any) error {
	if t.apiKey == "" || t.apiSecret == "" || t.passphrase == "" {
		return fmt.Errorf("okx: 缺少 API key/secret/passphrase,无法发签名写请求")
	}
	bodyBytes := []byte("{}")
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("okx: 序列化请求体失败: %w", err)
		}
		bodyBytes = b
	}
	return t.doRaw(ctx, http.MethodPost, path, nil, true, bodyBytes, out)
}

const (
	doMaxAttempts = 3
	doBackoff     = 300 * time.Millisecond
)

// do 发 GET 并把 data 数组解进 out。
func (t *transport) do(ctx context.Context, method, path string, query url.Values, signed bool, out any) error {
	var lastErr error
	for attempt := 1; attempt <= doMaxAttempts; attempt++ {
		retryable, err := t.doOnce(ctx, method, path, query, signed, nil, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == doMaxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(doBackoff * time.Duration(attempt)):
		}
	}
	return lastErr
}

// doRaw 发带 body 的请求(POST)。
func (t *transport) doRaw(ctx context.Context, method, path string, query url.Values, signed bool, body []byte, out any) error {
	var lastErr error
	for attempt := 1; attempt <= doMaxAttempts; attempt++ {
		retryable, err := t.doOnce(ctx, method, path, query, signed, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == doMaxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(doBackoff * time.Duration(attempt)):
		}
	}
	return lastErr
}

// requestPath 组装签名用的 requestPath(含查询串,不含 host)。
func requestPath(path string, query url.Values) string {
	qs := ""
	if query != nil {
		qs = query.Encode()
	}
	if qs == "" {
		return path
	}
	return path + "?" + qs
}

// doOnce 单次 HTTP 请求。返回 (是否可安全重试, 错误)。
func (t *transport) doOnce(ctx context.Context, method, path string, query url.Values, signed bool, body []byte, out any) (bool, error) {
	reqPath := requestPath(path, query)
	full := t.baseURL + reqPath

	var bodyReader io.Reader
	bodyStr := ""
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
		bodyStr = string(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, full, bodyReader)
	if err != nil {
		return false, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed {
		ts := t.signTimestampISO()
		sign := signString(t.apiSecret, signaturePayload(ts, method, reqPath, bodyStr))
		req.Header.Set("OK-ACCESS-KEY", t.apiKey)
		req.Header.Set("OK-ACCESS-PASSPHRASE", t.passphrase)
		req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
		req.Header.Set("OK-ACCESS-SIGN", sign)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ctx.Err() == nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode >= 500, fmt.Errorf("okx: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}

	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return false, fmt.Errorf("okx: 解析信封失败: %w", err)
	}
	if env.Code != "0" {
		// 50011 = 限频,退避后重试是安全的。
		retryable := env.Code == "50011"
		return retryable, fmt.Errorf("okx: code=%s msg=%q", env.Code, env.Msg)
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "[]" {
		return false, nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return false, fmt.Errorf("okx: 解析 data 失败: %w", err)
	}
	return false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// parseFloat 宽松解析 OKX 字符串数字(空串/非法 → 0),供行情/账户解析复用。
func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
