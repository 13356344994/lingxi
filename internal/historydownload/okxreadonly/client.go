// Package okxreadonly implements the small, explicit OKX GET surface required
// by historydownload. It has no generic request method by design.
package okxreadonly

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/historydownload"
)

const BaseURL = "https://www.okx.com"

var endpoints = map[string]bool{
	"/api/v5/trade/orders-history-archive": true,
	"/api/v5/trade/fills-history":          true,
	"/api/v5/public/instruments":           true,
	"/api/v5/account/instruments":          true,
	"/api/v5/public/market-data-history":   true,
}

var privateEndpoints = map[string]bool{
	"/api/v5/trade/orders-history-archive": true,
	"/api/v5/trade/fills-history":          true, "/api/v5/account/instruments": true,
}

var instrumentTypes = map[string]bool{"SWAP": true, "SPOT": true, "MARGIN": true, "FUTURES": true}
var safeInstrument = regexp.MustCompile(`^[A-Z0-9-]{3,40}$`)

type Client struct {
	HTTP        *http.Client
	BaseURL     string
	Credentials historydownload.Credentials
	Limiter     *historydownload.Limiter
	Now         func() time.Time
}

type Response struct {
	Raw         []byte
	Data        []map[string]any
	ReceiveTime time.Time
}

func (c *Client) Fetch(ctx context.Context, request historydownload.Request) (map[string]any, error) {
	response, err := c.FetchEnvelope(ctx, request)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(response.Raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) FetchEnvelope(ctx context.Context, request historydownload.Request) (Response, error) {
	if !endpoints[request.Endpoint] {
		return Response{}, errors.New("okxreadonly: endpoint rejected")
	}
	if request.Private != privateEndpoints[request.Endpoint] {
		return Response{}, errors.New("okxreadonly: endpoint privacy mismatch")
	}
	if request.Private && !c.Credentials.Complete() {
		return Response{}, errors.New("okxreadonly: private endpoint requires complete credentials")
	}
	if err := validateQuery(request.Endpoint, request.Query); err != nil {
		return Response{}, err
	}
	base := c.BaseURL
	if base == "" {
		base = BaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "www.okx.com") {
		return Response{}, errors.New("okxreadonly: host rejected")
	}
	u.Path = request.Endpoint
	query := u.Query()
	for key, value := range request.Query {
		if key == "" || strings.ContainsAny(key, "&=") {
			return Response{}, errors.New("okxreadonly: query key rejected")
		}
		query.Set(key, value)
	}
	u.RawQuery = query.Encode()
	if c.Limiter != nil {
		c.Limiter.Wait()
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	safeHTTP := *httpClient
	safeHTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Response{}, errors.New("okxreadonly: failed to construct approved GET")
	}
	if request.Private {
		c.sign(httpRequest)
	}
	response, err := safeHTTP.Do(httpRequest)
	if err != nil {
		return Response{}, historydownload.NewSafeFetchError(historydownload.SafeTransportFailed, 0, "")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return Response{}, historydownload.NewSafeFetchError(historydownload.SafeBodyReadFailed, 0, "")
	}
	var envelope struct {
		Code string           `json:"code"`
		Msg  string           `json:"msg"`
		Data []map[string]any `json:"data"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = json.Unmarshal(body, &envelope)
		return Response{}, historydownload.NewSafeFetchError(historydownload.SafeHTTPRejected, response.StatusCode, envelope.Code)
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Response{}, historydownload.NewSafeFetchError(historydownload.SafeInvalidJSON, 0, "")
	}
	if envelope.Code != "0" {
		return Response{}, historydownload.NewSafeFetchError(historydownload.SafeAPIRejected, 0, envelope.Code)
	}
	return Response{Raw: body, Data: envelope.Data, ReceiveTime: time.Now().UTC()}, nil
}

func validateQuery(endpoint string, query map[string]string) error {
	allowed := map[string]bool{}
	switch endpoint {
	case historydownload.OrdersEndpoint:
		allowed = map[string]bool{"instType": true, "after": true, "limit": true}
	case historydownload.FillsEndpoint:
		allowed = map[string]bool{"instType": true, "after": true, "limit": true}
	case historydownload.PublicInstruments, historydownload.AccountInstruments:
		allowed = map[string]bool{"instType": true, "instId": true}
	case historydownload.MarketDataDiscovery:
		allowed = map[string]bool{
			"module": true, "instType": true, "instIdList": true,
			"dateAggrType": true, "begin": true, "end": true,
		}
	}
	for key := range query {
		if !allowed[key] {
			return fmt.Errorf("okxreadonly: query key %q rejected", key)
		}
	}
	if value := query["instType"]; value != "" && !instrumentTypes[value] {
		return errors.New("okxreadonly: instType rejected")
	}
	if value := query["instId"]; value != "" && !safeInstrument.MatchString(value) {
		return errors.New("okxreadonly: instId rejected")
	}
	if value := query["limit"]; value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			return errors.New("okxreadonly: limit rejected")
		}
	}
	if endpoint == historydownload.MarketDataDiscovery {
		if query["module"] != "1" {
			return errors.New("okxreadonly: discovery module rejected")
		}
		if !instrumentTypes[query["instType"]] {
			return errors.New("okxreadonly: discovery instType rejected")
		}
		instrument := query["instIdList"]
		if !safeInstrument.MatchString(instrument) || strings.Contains(instrument, ",") {
			return errors.New("okxreadonly: discovery requires one safe instrument")
		}
		if query["dateAggrType"] != "daily" {
			return errors.New("okxreadonly: discovery date aggregation rejected")
		}
		begin, beginErr := strconv.ParseInt(query["begin"], 10, 64)
		end, endErr := strconv.ParseInt(query["end"], 10, 64)
		if beginErr != nil || endErr != nil || begin < 0 || end != begin+int64(24*time.Hour/time.Millisecond)-1 {
			return errors.New("okxreadonly: discovery requires exactly one UTC day")
		}
		if time.UnixMilli(begin).UTC().Truncate(24*time.Hour).UnixMilli() != begin {
			return errors.New("okxreadonly: discovery begin is not UTC day boundary")
		}
	}
	return nil
}

func (c *Client) sign(req *http.Request) {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	timestamp := now().UTC().Format("2006-01-02T15:04:05.000Z")
	path := req.URL.EscapedPath()
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}
	mac := hmac.New(sha256.New, []byte(c.Credentials.Secret))
	_, _ = mac.Write([]byte(timestamp + "GET" + path))
	req.Header.Set("OK-ACCESS-KEY", c.Credentials.Key)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.Credentials.Passphrase)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-SIGN", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// SortedQuery returns a deterministic query copy useful for checkpoint binding.
func SortedQuery(in map[string]string) []string {
	out := make([]string, 0, len(in))
	for k, v := range in {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
