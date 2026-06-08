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
	ts := strconv.FormatInt(t.nowFn().UTC().UnixMilli(), 10)
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
		return fmt.Errorf("bybit: retCode=%d retMsg=%q", env.RetCode, env.RetMsg)
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("bybit: 解析 result 失败: %w", err)
	}
	return nil
}

func (t *transport) do(ctx context.Context, path string, query url.Values, signed bool, out any) error {
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
		return err
	}
	if signed {
		ts := strconv.FormatInt(t.nowFn().UTC().UnixMilli(), 10)
		sign := signString(t.apiSecret, signaturePayload(ts, t.apiKey, t.recvWindow, qs))
		req.Header.Set("X-BAPI-API-KEY", t.apiKey)
		req.Header.Set("X-BAPI-SIGN", sign)
		req.Header.Set("X-BAPI-SIGN-TYPE", "2")
		req.Header.Set("X-BAPI-TIMESTAMP", ts)
		req.Header.Set("X-BAPI-RECV-WINDOW", t.recvWindow)
	}

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
		return fmt.Errorf("bybit: retCode=%d retMsg=%q", env.RetCode, env.RetMsg)
	}
	if out == nil {
		return nil
	}
	if len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("bybit: 解析 result 失败: %w", err)
	}
	return nil
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
