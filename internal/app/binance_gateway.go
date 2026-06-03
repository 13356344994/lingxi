package app

import (
	"context"
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
	params.Set("recvWindow", strconv.FormatInt(engine.cfg.ExecutionConfig.RecvWindow, 10))
	params.Set("timestamp", strconv.FormatInt(engine.currentServerTimeMillis(), 10))

	query := params.Encode()
	query = query + "&signature=" + exchange.Sign(engine.client.SecretKey(), query)

	req, err := http.NewRequestWithContext(ctx, method, endpoint+"?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("创建签名请求失败: %w", err)
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
	engine.client.NoteUsedWeightFromHeaders(resp.Header)

	body, err := io.ReadAll(resp.Body)
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
			Body:       strings.TrimSpace(string(body)),
		}
		if markErr := engine.notePlatformRequestResult(endpoint, requestErr); markErr != nil {
			return nil, markErr
		}
		return nil, requestErr
	}
	engine.notePlatformRequestSuccess()
	return body, nil
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
		if markErr := engine.notePlatformRequestResult(endpoint, err); markErr != nil {
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
