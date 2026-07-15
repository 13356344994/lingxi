// Package binancereadonly exposes only the two documented Binance USD-M
// asynchronous order-history GET calls. It deliberately has no generic HTTP
// method, URL, or request-body surface.
package binancereadonly

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	BaseURL                  = "https://fapi.binance.com"
	OrderAsyncEndpoint       = "/fapi/v1/order/asyn"
	OrderAsyncStatusEndpoint = "/fapi/v1/order/asyn/id"
	MaxWindow                = 7 * 24 * time.Hour
	DefaultRecvWindow        = 5000
	MaxRecvWindow            = 60000
	CompletedWindowBuffer    = 10 * time.Minute
)

var approvedHosts = map[string]bool{
	"fapi.binance.com": true,
}

// Kept separate from API hosts. This candidate is taken only from the official
// response example; production remains disabled by
// OfficialDownloadHostVerified until Binance publishes a normative host rule.
var candidateDownloadHosts = map[string]bool{
	"www.binance.com": true,
}

type Credentials struct {
	APIKey string
	Secret string
}

func (c Credentials) Complete() bool {
	return strings.TrimSpace(c.APIKey) != "" && strings.TrimSpace(c.Secret) != ""
}

type Client struct {
	HTTP        *http.Client
	BaseURL     string
	Credentials Credentials
	Now         func() time.Time
}

type DownloadReceipt struct {
	DownloadID      string
	Raw             []byte
	QuotaSideEffect string
	ReceiveTime     time.Time
}

type DownloadStatus struct {
	URL         string
	Status      string
	ReceiveTime time.Time
}

// RequestOrderHistory starts exactly one fixed, at-most-seven-day async
// download job. Binance documents this GET as an asynchronous job side effect;
// callers must persist the returned receipt before requesting its status.
func (c *Client) RequestOrderHistory(ctx context.Context, start, end time.Time, recvWindow int64) (DownloadReceipt, error) {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	if start.Location() != time.UTC || end.Location() != time.UTC || !start.Equal(PilotStart) || !end.Equal(PilotEnd) ||
		end.Sub(start) != MaxWindow || end.After(now().UTC().Add(-CompletedWindowBuffer)) {
		return DownloadReceipt{}, errors.New("binancereadonly: invalid fixed history window")
	}
	if err := validRecvWindow(recvWindow); err != nil {
		return DownloadReceipt{}, err
	}
	response, err := c.get(ctx, OrderAsyncEndpoint, url.Values{
		"startTime":  []string{strconv.FormatInt(start.UnixMilli(), 10)},
		"endTime":    []string{strconv.FormatInt(end.UnixMilli(), 10)},
		"recvWindow": []string{strconv.FormatInt(recvWindow, 10)},
	})
	if err != nil {
		return DownloadReceipt{}, err
	}
	var body struct {
		DownloadID string `json:"downloadId"`
		Data       struct {
			DownloadID string `json:"downloadId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.body, &body); err != nil {
		return DownloadReceipt{}, newError("invalid_json_envelope", 0, "")
	}
	id := body.DownloadID
	if id == "" {
		id = body.Data.DownloadID
	}
	if !safeID(id) {
		return DownloadReceipt{}, newError("invalid_async_receipt", 0, "")
	}
	return DownloadReceipt{
		DownloadID: id, Raw: append([]byte(nil), response.body...),
		QuotaSideEffect: "ASYNC_ORDER_HISTORY_JOB_REQUESTED", ReceiveTime: response.received,
	}, nil
}

// GetDownloadStatus requests the second, documented async GET endpoint only.
func (c *Client) GetDownloadStatus(ctx context.Context, downloadID string, recvWindow int64) (DownloadStatus, error) {
	if !safeID(downloadID) {
		return DownloadStatus{}, errors.New("binancereadonly: invalid download id")
	}
	if err := validRecvWindow(recvWindow); err != nil {
		return DownloadStatus{}, err
	}
	response, err := c.get(ctx, OrderAsyncStatusEndpoint, url.Values{
		"downloadId": []string{downloadID},
		"recvWindow": []string{strconv.FormatInt(recvWindow, 10)},
	})
	if err != nil {
		return DownloadStatus{}, err
	}
	var body struct {
		URL    string `json:"url"`
		Status string `json:"status"`
		Data   struct {
			URL    string `json:"url"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.body, &body); err != nil {
		return DownloadStatus{}, newError("invalid_json_envelope", 0, "")
	}
	if body.URL == "" && body.Status == "" {
		body.URL, body.Status = body.Data.URL, body.Data.Status
	}
	if body.URL != "" {
		if _, err := ValidateDownloadURL(body.URL); err != nil {
			return DownloadStatus{}, err
		}
	}
	if body.URL == "" && body.Status == "" {
		return DownloadStatus{}, newError("invalid_async_status", 0, "")
	}
	return DownloadStatus{URL: body.URL, Status: body.Status, ReceiveTime: response.received}, nil
}

// ValidateDownloadURL admits only direct HTTPS downloads from official Binance
// API hosts. It intentionally rejects redirects and caller-selected URLs.
func ValidateDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || !candidateDownloadHosts[strings.ToLower(u.Hostname())] {
		return nil, errors.New("binancereadonly: download URL rejected")
	}
	return u, nil
}

type response struct {
	body     []byte
	received time.Time
}

func (c *Client) get(ctx context.Context, endpoint string, query url.Values) (response, error) {
	if endpoint != OrderAsyncEndpoint && endpoint != OrderAsyncStatusEndpoint {
		return response{}, errors.New("binancereadonly: endpoint rejected")
	}
	if !c.Credentials.Complete() {
		return response{}, errors.New("binancereadonly: private endpoint requires complete credentials")
	}
	base := c.BaseURL
	if base == "" {
		base = BaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil || !approvedHosts[strings.ToLower(u.Hostname())] || u.Path != "" && u.Path != "/" {
		return response{}, errors.New("binancereadonly: host rejected")
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	query.Set("timestamp", strconv.FormatInt(now().UTC().UnixMilli(), 10))
	query.Del("signature")
	signed := query.Encode()
	mac := hmac.New(sha256.New, []byte(c.Credentials.Secret))
	_, _ = mac.Write([]byte(signed))
	query.Set("signature", hex.EncodeToString(mac.Sum(nil)))
	u.Path = endpoint
	u.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return response{}, errors.New("binancereadonly: failed to construct approved GET")
	}
	request.Header.Set("X-MBX-APIKEY", c.Credentials.APIKey)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	safeHTTP := *httpClient
	safeHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpResponse, err := safeHTTP.Do(request)
	if err != nil {
		return response{}, newError("transport_failed", 0, "")
	}
	defer httpResponse.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, 64<<20))
	if err != nil {
		return response{}, newError("body_read_failed", 0, "")
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return response{}, newError("http_rejected", httpResponse.StatusCode, extractCode(body))
	}
	return response{body: body, received: now().UTC()}, nil
}

func validRecvWindow(value int64) error {
	if value < 1 || value > MaxRecvWindow {
		return errors.New("binancereadonly: recvWindow rejected")
	}
	return nil
}

func safeID(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func extractCode(body []byte) string {
	var envelope struct {
		Code any `json:"code"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	switch code := envelope.Code.(type) {
	case string:
		if len(code) <= 12 && strings.Trim(code, "-0123456789") == "" {
			return code
		}
	case float64:
		return strconv.FormatInt(int64(code), 10)
	}
	return ""
}

type safeError struct {
	kind   string
	status int
	code   string
}

func newError(kind string, status int, code string) error {
	if status < 100 || status > 599 {
		status = 0
	}
	if len(code) > 12 || strings.Trim(code, "-0123456789") != "" {
		code = ""
	}
	return safeError{kind: kind, status: status, code: code}
}

func (e safeError) Error() string {
	if e.status != 0 && e.code != "" {
		return "binancereadonly: " + e.kind + " (http_status=" + strconv.Itoa(e.status) + " api_code=" + e.code + ")"
	}
	if e.status != 0 {
		return "binancereadonly: " + e.kind + " (http_status=" + strconv.Itoa(e.status) + ")"
	}
	if e.code != "" {
		return "binancereadonly: " + e.kind + " (api_code=" + e.code + ")"
	}
	return "binancereadonly: " + e.kind
}
