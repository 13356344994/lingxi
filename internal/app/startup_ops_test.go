package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"lingxi/internal/exchange"
)

func TestIsTimestampOutsideRecvWindowError(t *testing.T) {
	if !isTimestampOutsideRecvWindowError(signedRequestError{StatusCode: http.StatusBadRequest, Body: `{"code":-1021,"msg":"Timestamp for this request is outside of the recvWindow."}`}) {
		t.Fatal("应识别 -1021 为时钟漂移错误")
	}
	if isTimestampOutsideRecvWindowError(signedRequestError{StatusCode: http.StatusBadRequest, Body: `{"code":-2010,"msg":"Account has insufficient balance"}`}) {
		t.Fatal("-2010 不应被识别为 -1021")
	}
	if isTimestampOutsideRecvWindowError(nil) {
		t.Fatal("nil 不应被识别为 -1021")
	}
}

// TestVerifyCrossMarginReadySkipsWhenNegativeCarryDisabled 钉死:positive_only 模式下不触发任何全仓杠杆校验/网络调用。
func TestVerifyCrossMarginReadySkipsWhenNegativeCarryDisabled(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.StrategyConfig.LiveFundingDirectionMode = "positive_only"
	if err := engine.verifyCrossMarginReadyForNegativeCarry(context.Background()); err != nil {
		t.Fatalf("positive_only 不应校验全仓杠杆, got %v", err)
	}
}

// TestVerifyCrossMarginReadyRejectsBorrowDisabled 钉死:both 模式下全仓杠杆禁借时拒绝启动。
func TestVerifyCrossMarginReadyRejectsBorrowDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sapi/v1/margin/account" {
			_, _ = w.Write([]byte(`{"created":true,"borrowEnabled":false,"tradeEnabled":true,"userAssets":[]}`))
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()

	engine := newCrossMarginTestEngine(server)
	engine.cfg.StrategyConfig.LiveFundingDirectionMode = "both"
	if err := engine.verifyCrossMarginReadyForNegativeCarry(context.Background()); err == nil {
		t.Fatal("禁借的全仓杠杆账户应拒绝启动负 carry")
	}
}

// TestVerifyCrossMarginReadyAcceptsHealthyAccount 钉死:both 模式 + 全仓杠杆 created/borrow/trade 均开 → 通过。
func TestVerifyCrossMarginReadyAcceptsHealthyAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sapi/v1/margin/account" {
			_, _ = w.Write([]byte(`{"created":true,"borrowEnabled":true,"tradeEnabled":true,"userAssets":[]}`))
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()

	engine := newCrossMarginTestEngine(server)
	engine.cfg.StrategyConfig.LiveFundingDirectionMode = "both"
	if err := engine.verifyCrossMarginReadyForNegativeCarry(context.Background()); err != nil {
		t.Fatalf("健康的全仓杠杆账户应通过, got %v", err)
	}
}

func newCrossMarginTestEngine(server *httptest.Server) *FundingRateSniperEngine {
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	return engine
}
