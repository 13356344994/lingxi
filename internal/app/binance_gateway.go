package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"lingxi/internal/exchange"
)

// BinanceGateway 收口底层 Binance 请求入口，先保留原有语义与限流/风控联动。
type BinanceGateway struct {
	engine *FundingRateSniperEngine
}

func newBinanceGateway(engine *FundingRateSniperEngine) *BinanceGateway {
	if engine == nil {
		return nil
	}
	return &BinanceGateway{engine: engine}
}

func (e *FundingRateSniperEngine) ensureBinanceGateway() *BinanceGateway {
	if e == nil {
		return nil
	}
	e.binanceGatewayOnce.Do(func() {
		e.binanceGateway = newBinanceGateway(e)
	})
	return e.binanceGateway
}

func (g *BinanceGateway) SignedRequest(ctx context.Context, method string, endpoint string, params url.Values) ([]byte, error) {
	engine := g.engine
	releaseUsedWeightBudget := func() {}
	if !isExposureManagerRequest(ctx) {
		if err := engine.checkPlatformFuse(); err != nil {
			return nil, err
		}
		release, err := engine.blockNonCriticalRequestByUsedWeight(method, endpoint, params)
		if err != nil {
			return nil, err
		}
		releaseUsedWeightBudget = release
	}
	defer releaseUsedWeightBudget()
	if params == nil {
		params = url.Values{}
	}
	params = cloneURLValues(params)
	if err := engine.blockOrderRequestByLocalRateLimit(method, endpoint, params); err != nil {
		return nil, err
	}

	body, requestErr := g.sendSignedOnce(ctx, method, endpoint, params)
	// -1021(timestamp 超出 recvWindow):本地与服务器时钟漂移导致的**执行前**拒绝(请求从未进入撮合,故重试幂等安全)。
	// 重同步服务器时间偏移后用新 timestamp 重签重试一次;首次失败不计入平台拒绝计数(避免时钟抖动误触熔断)。
	if requestErr != nil && isTimestampOutsideRecvWindowError(requestErr) {
		if engine.logger != nil {
			engine.logger.Printf("[time] 签名请求触发 -1021(时钟漂移),重同步服务器时间后重试一次: %s", endpoint)
		}
		if syncErr := engine.syncServerTimeOffset(ctx); syncErr == nil {
			body, requestErr = g.sendSignedOnce(ctx, method, endpoint, params)
		} else if engine.logger != nil {
			engine.logger.Printf("[time] -1021 后重同步服务器时间失败: %v", syncErr)
		}
	}
	if requestErr != nil {
		if markErr := engine.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	engine.notePlatformRequestSuccess()
	return body, nil
}

// sendSignedOnce 执行一次签名请求(每次调用都用当前 serverTimeMillis 重新盖 timestamp 并重签,确保重试用新鲜时间)。
// 不在此处记平台请求成败计数——交由 SignedRequest 在最终结果上统一处理,避免 -1021 重试时把首次失败误计为平台拒绝。
func (g *BinanceGateway) sendSignedOnce(ctx context.Context, method string, endpoint string, params url.Values) ([]byte, error) {
	engine := g.engine
	signed := cloneURLValues(params)
	signed.Set("recvWindow", strconv.FormatInt(engine.cfg.ExecutionConfig.RecvWindow, 10))
	signed.Set("timestamp", strconv.FormatInt(engine.currentServerTimeMillis(), 10))

	query := signed.Encode()
	query = query + "&signature=" + exchange.Sign(engine.client.SecretKey(), query)

	req, err := http.NewRequestWithContext(ctx, method, endpoint+"?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("创建签名请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", engine.client.APIKey())

	resp, err := engine.client.HTTPClient().Do(req)
	if err != nil {
		return nil, signedRequestError{Endpoint: endpoint, Cause: err}
	}
	defer resp.Body.Close()
	engine.client.NoteUsedWeightFromHeaders(resp.Header)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, signedRequestError{Endpoint: endpoint, Cause: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, signedRequestError{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	return body, nil
}

// isTimestampOutsideRecvWindowError 判定签名请求错误是否为币安 -1021(timestamp 超出 recvWindow)。
func isTimestampOutsideRecvWindowError(err error) bool {
	var requestErr signedRequestError
	if !errors.As(err, &requestErr) {
		return false
	}
	return strings.Contains(requestErr.Body, "-1021")
}

func (g *BinanceGateway) GetJSON(ctx context.Context, endpoint string, target any) error {
	engine := g.engine
	releaseUsedWeightBudget := func() {}
	if !isExposureManagerRequest(ctx) {
		if err := engine.checkPlatformFuse(); err != nil {
			return err
		}
		release, err := engine.blockNonCriticalRequestByUsedWeight(http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		releaseUsedWeightBudget = release
	}
	defer releaseUsedWeightBudget()
	if err := engine.client.GetJSON(ctx, endpoint, target); err != nil {
		// 只读公共行情 GET：用读路径计数，纯客户端超时/EOF 不计入致命的「连续异常请求」熔断，
		// 仅服务端封禁(403/418/429)才累计——避免一次只读取数失败 pause_cycle 停摆整轮。
		if markErr := engine.notePlatformReadRequestResult(endpoint, err); markErr != nil {
			return markErr
		}
		return err
	}
	engine.notePlatformRequestSuccess()
	return nil
}

func (g *BinanceGateway) APIKeyRequest(ctx context.Context, method string, endpoint string, body io.Reader) ([]byte, error) {
	engine := g.engine
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("创建 API KEY 请求失败: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", engine.client.APIKey())

	resp, err := engine.client.HTTPClient().Do(req)
	if err != nil {
		requestErr := signedRequestError{Endpoint: endpoint, Cause: err}
		if markErr := engine.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		requestErr := signedRequestError{Endpoint: endpoint, Cause: err}
		if markErr := engine.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	if resp.StatusCode != http.StatusOK {
		requestErr := signedRequestError{
			Endpoint:   endpoint,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(payload)),
		}
		if markErr := engine.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	engine.notePlatformRequestSuccess()
	return payload, nil
}
