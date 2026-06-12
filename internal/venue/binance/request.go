package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

func (a *Adapter) now() time.Time {
	if a != nil && a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

func (a *Adapter) spotBaseURL() string {
	if a == nil || a.client == nil {
		return ""
	}
	return a.client.SpotBaseURL()
}

func (a *Adapter) futuresBaseURL() string {
	if a == nil || a.client == nil {
		return ""
	}
	return a.client.FuturesBaseURL()
}

// signedRequest 经 SignedSend 发起签名请求;HTTP 非 200 返回 error(由调用方映射 venue 错误)。
func (a *Adapter) signedRequest(ctx context.Context, method, fullURL string, params url.Values) ([]byte, error) {
	if a == nil || a.client == nil {
		return nil, fmt.Errorf("binance: client 未初始化")
	}
	if params == nil {
		params = url.Values{}
	}
	out, err := a.SignedSend(ctx, method, fullURL, params, defaultRecvWindow, a.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if out.StatusCode != http.StatusOK {
		return nil, httpError(method, fullURL, out.StatusCode, out.Status, string(out.Body))
	}
	return out.Body, nil
}

// publicGet 公共 GET(无需签名),委托 client.GetJSON。
func (a *Adapter) publicGet(ctx context.Context, fullURL string, target any) error {
	if a == nil || a.client == nil {
		return fmt.Errorf("binance: client 未初始化")
	}
	return a.client.GetJSON(ctx, fullURL, target)
}

// signedGetJSON 签名 GET 并解析 JSON。
func (a *Adapter) signedGetJSON(ctx context.Context, fullURL string, params url.Values, target any) error {
	body, err := a.signedRequest(ctx, http.MethodGet, fullURL, params)
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("binance: 解析 JSON 失败: %w", err)
	}
	return nil
}

// signedPostForm 签名 POST(币安下单/设杠杆等均为 query 参数 POST,body 为空)。
func (a *Adapter) signedPostForm(ctx context.Context, fullURL string, params url.Values, target any) error {
	body, err := a.signedRequest(ctx, http.MethodPost, fullURL, params)
	if err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("binance: 解析 JSON 失败: %w", err)
	}
	return nil
}
