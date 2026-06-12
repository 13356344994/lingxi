package gate

// transport.go 实现 Gate.io v4 的 HTTP 传输:公共 GET + 签名 GET/POST/DELETE(HMAC-SHA512 十六进制)。
//
// 签名规则(官方 v4):
//   待签名串 = METHOD + "\n" + 完整路径(含 /api/v4) + "\n" + 查询串 + "\n" + HEX(SHA512(body)) + "\n" + Timestamp
//   GET 无 body 时对空串做 SHA512;Timestamp 为 Unix 秒(字符串)。
//   请求头 KEY / Timestamp / SIGN = HEX(HMAC_SHA512(secret, 待签名串))。
// 成功响应直接返回 JSON(无统一包络);失败时 HTTP 非 2xx + {"label":"...","message":"..."}。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
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
	defaultBaseURL = "https://api.gateio.ws"
	defaultTimeout = 8 * time.Second
)

// transport 持有 Gate REST 访问所需的最小状态;不依赖任何其他交易所类型,自包含。
type transport struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	apiSecret  string
	nowFn      func() time.Time
	timeOffset atomic.Int64 // 服务器时间 − 本地时间(秒),签名时间戳叠加
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
		nowFn:      time.Now,
	}
}

// apiError 是 Gate 非 2xx 响应体。
type apiError struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	if e == nil {
		return "<nil gate error>"
	}
	if e.Label == "" && e.Message == "" {
		return "gate: API 错误"
	}
	return fmt.Sprintf("gate: label=%s message=%q", e.Label, e.Message)
}

// hashBodySHA512 对 body 做 SHA512 并返回小写 hex(空 body 亦需哈希)。
func hashBodySHA512(body []byte) string {
	sum := sha512.Sum512(body)
	return hex.EncodeToString(sum[:])
}

// signString 对 payload 用 secret 做 HMAC-SHA512,返回小写 hex。便于单测对照向量。
func signString(secret, payload string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// signaturePayload 组装 Gate v4 待签名串(五段换行拼接)。
func signaturePayload(method, path, queryString, bodyHash, timestamp string) string {
	return method + "\n" + path + "\n" + queryString + "\n" + bodyHash + "\n" + timestamp
}

func (t *transport) signTimestampSec() string {
	sec := t.nowFn().UTC().Unix() + t.timeOffset.Load()
	return strconv.FormatInt(sec, 10)
}

// syncServerTime 拉 /api/v4/spot/time 计算「服务器 − 本地」偏移(秒),写入 timeOffset 供签名使用。
func (t *transport) syncServerTime(ctx context.Context) (time.Duration, error) {
	var res struct {
		ServerTime int64 `json:"server_time"`
	}
	start := t.nowFn()
	if err := t.do(ctx, http.MethodGet, pathSpotTime, nil, false, nil, &res); err != nil {
		return 0, fmt.Errorf("gate: 拉取服务器时间失败: %w", err)
	}
	if res.ServerTime <= 0 {
		return 0, fmt.Errorf("gate: 服务器时间无效: %d", res.ServerTime)
	}
	localMidSec := start.Add(t.nowFn().Sub(start) / 2).UTC().Unix()
	offsetSec := res.ServerTime - localMidSec
	t.timeOffset.Store(offsetSec)
	return time.Duration(offsetSec) * time.Second, nil
}

func (t *transport) publicGet(ctx context.Context, path string, query url.Values, out any) error {
	return t.do(ctx, http.MethodGet, path, query, false, nil, out)
}

func (t *transport) signedGet(ctx context.Context, path string, query url.Values, out any) error {
	if t.apiKey == "" || t.apiSecret == "" {
		return fmt.Errorf("gate: 缺少 API key/secret,无法发签名请求")
	}
	return t.do(ctx, http.MethodGet, path, query, true, nil, out)
}

func (t *transport) signedPost(ctx context.Context, path string, query url.Values, reqBody any, out any) error {
	if t.apiKey == "" || t.apiSecret == "" {
		return fmt.Errorf("gate: 缺少 API key/secret,无法发签名写请求")
	}
	bodyBytes := []byte("")
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("gate: 序列化请求体失败: %w", err)
		}
		bodyBytes = b
	}
	return t.doRaw(ctx, http.MethodPost, path, query, true, bodyBytes, out)
}

func (t *transport) signedDelete(ctx context.Context, path string, query url.Values, out any) error {
	if t.apiKey == "" || t.apiSecret == "" {
		return fmt.Errorf("gate: 缺少 API key/secret,无法发签名 DELETE")
	}
	return t.doRaw(ctx, http.MethodDelete, path, query, true, nil, out)
}

const (
	doMaxAttempts = 3
	doBackoff     = 300 * time.Millisecond
)

func (t *transport) do(ctx context.Context, method, path string, query url.Values, signed bool, body []byte, out any) error {
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

func (t *transport) doRaw(ctx context.Context, method, path string, query url.Values, signed bool, body []byte, out any) error {
	return t.do(ctx, method, path, query, signed, body, out)
}

func queryString(query url.Values) string {
	if query == nil {
		return ""
	}
	return query.Encode()
}

func (t *transport) doOnce(ctx context.Context, method, path string, query url.Values, signed bool, body []byte, out any) (bool, error) {
	qs := queryString(query)
	full := t.baseURL + path
	if qs != "" {
		full += "?" + qs
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, bodyReader)
	if err != nil {
		return false, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed {
		ts := t.signTimestampSec()
		bodyHash := hashBodySHA512(body)
		sign := signString(t.apiSecret, signaturePayload(method, path, qs, bodyHash, ts))
		req.Header.Set("KEY", t.apiKey)
		req.Header.Set("Timestamp", ts)
		req.Header.Set("SIGN", sign)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae apiError
		if json.Unmarshal(respBody, &ae) == nil && (ae.Label != "" || ae.Message != "") {
			retryable := ae.Label == "TOO_MANY_REQUESTS" || resp.StatusCode >= 500
			return retryable, &ae
		}
		return resp.StatusCode >= 500, fmt.Errorf("gate: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 256))
	}
	if out == nil || len(respBody) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return false, fmt.Errorf("gate: 解析响应失败: %w", err)
	}
	return false, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}
