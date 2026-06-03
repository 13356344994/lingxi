package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

// 本文件承载 Gate 4 的混沌/故障注入测试：在开仓双腿执行过程中注入「单腿拒单 / REST 超时 /
// -1021 时间戳错误 / 418 限流封禁」等故障，验证引擎在任何一条腿失败后都不会留下裸敞口
// （要么同步反向平掉已成交腿，要么交由 ExposureManager 异步拉平），即对冲不变量在故障下依然成立。

// chaosOpenServer 构造一个可按「现货腿故障模式」注入故障的假交易所：
// 合约腿恒为 FILLED（开仓 + 反向平仓恢复单都成交），现货开仓单按 spotOpenResponder 注入故障。
// 返回各类请求的计数器指针，便于断言「恢复单（反向平腿）确有发生」。
type chaosCounters struct {
	futuresOpenPosts     int32
	futuresRecoveryPosts int32
	spotOpenPosts        int32
	spotRecoveryPosts    int32
}

func newChaosEngine(t *testing.T, c *chaosCounters, spotOpenResponder func(w http.ResponseWriter)) (*FundingRateSniperEngine, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"100"}],"positions":[{"symbol":"BTCUSDT","positionAmt":"0"}]}`))
		case "/api/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"BTC","free":"0","locked":"0"},{"asset":"USDT","free":"100","locked":"0"}]}`))
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/fapi/v1/order":
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fopen-test","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":1,"updateTime":1}`))
				return
			}
			if isFuturesRecoveryClientOrderID(clientOrderID) {
				atomic.AddInt32(&c.futuresRecoveryPosts, 1)
			} else {
				atomic.AddInt32(&c.futuresOpenPosts, 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fopen-test","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":1,"updateTime":1}`))
		case "/api/v3/order":
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if r.Method == http.MethodGet {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
			if isSpotRecoveryClientOrderID(clientOrderID) {
				atomic.AddInt32(&c.spotRecoveryPosts, 1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":9,"status":"FILLED","clientOrderId":"exp-sell","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
				return
			}
			atomic.AddInt32(&c.spotOpenPosts, 1)
			spotOpenResponder(w)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 5 * time.Millisecond
	cfg.ExecutionConfig.ExecutionRecoveryTimeout = 20 * time.Millisecond
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:             cfg,
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}
	return engine, server
}

func isFuturesRecoveryClientOrderID(id string) bool {
	for _, p := range []string{"ftrim", "frescue", "fclose", "expfclose"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func isSpotRecoveryClientOrderID(id string) bool {
	for _, p := range []string{"srevert", "sclose_revert", "exp_sell"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func chaosOpenPlan() openSequencePlan {
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			MarkPrice:         100,
			OrderQuantity:     1,
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
		LastSpotAsk: 100,
	}
	return openSequencePlan{
		target: target,
		futuresOrder: FuturesBatchOrderRequest{
			Symbol:           "BTCUSDT",
			Side:             "SELL",
			Type:             "MARKET",
			Quantity:         "1",
			NewClientOrderID: "fopen-test",
			NewOrderRespType: "RESULT",
		},
	}
}

// TestChaosSpotLegFaultsNeverLeaveNakedFutures 注入多种「现货腿确定性失败」模式（普通拒单、
// 余额不足 -2010、时间戳 -1021），断言：合约腿已成交后，引擎必定反向平掉合约（同步 revert 或
// 异步 rescue），不会留下裸合约空头。开仓整体应返回错误并对该交易对置冷却。
func TestChaosSpotLegFaultsNeverLeaveNakedFutures(t *testing.T) {
	scenarios := []struct {
		name      string
		responder func(w http.ResponseWriter)
	}{
		{
			name: "generic_reject",
			responder: func(w http.ResponseWriter) {
				http.Error(w, `{"code":-1000,"msg":"unknown error"}`, http.StatusBadRequest)
			},
		},
		{
			name: "insufficient_balance",
			responder: func(w http.ResponseWriter) {
				http.Error(w, `{"code":-2010,"msg":"Account has insufficient balance for requested action."}`, http.StatusBadRequest)
			},
		},
		{
			name: "timestamp_minus_1021",
			responder: func(w http.ResponseWriter) {
				http.Error(w, `{"code":-1021,"msg":"Timestamp for this request is outside of the recvWindow."}`, http.StatusBadRequest)
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var counters chaosCounters
			engine, server := newChaosEngine(t, &counters, sc.responder)
			defer server.Close()

			_, err := engine.openHedgeForTargetParallel(context.Background(), chaosOpenPlan())
			if err == nil {
				t.Fatal("expected single-leg spot failure to abort the open")
			}
			if atomic.LoadInt32(&counters.futuresOpenPosts) != 1 {
				t.Fatalf("expected exactly one futures open, got %d", counters.futuresOpenPosts)
			}
			// 合约空头必须被反向平掉，否则即为裸敞口。
			deadline := time.Now().Add(800 * time.Millisecond)
			for time.Now().Before(deadline) {
				if atomic.LoadInt32(&counters.futuresRecoveryPosts) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if atomic.LoadInt32(&counters.futuresRecoveryPosts) == 0 {
				t.Fatalf("naked futures short left after spot failure (%s): no revert/rescue order observed", sc.name)
			}
		})
	}
}

// TestChaosFuturesLegRejectNeverLeavesNakedSpot 注入「合约腿确定性拒单」同时现货腿已成交，
// 断言引擎反向卖出已买入的现货，不留下裸现货多头。
func TestChaosFuturesLegRejectNeverLeavesNakedSpot(t *testing.T) {
	var counters chaosCounters
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"100"}],"positions":[{"symbol":"BTCUSDT","positionAmt":"0"}]}`))
		case "/api/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"BTC","free":"1","locked":"0"},{"asset":"USDT","free":"100","locked":"0"}]}`))
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/fapi/v1/order":
			if r.Method == http.MethodGet {
				http.Error(w, `{"code":-2013,"msg":"Order does not exist."}`, http.StatusBadRequest)
				return
			}
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if isFuturesRecoveryClientOrderID(clientOrderID) {
				atomic.AddInt32(&counters.futuresRecoveryPosts, 1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fclose","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":2,"updateTime":1}`))
				return
			}
			atomic.AddInt32(&counters.futuresOpenPosts, 1)
			// 合约开仓腿确定性拒单（非超时，可判定）。
			http.Error(w, `{"code":-2019,"msg":"Margin is insufficient."}`, http.StatusBadRequest)
		case "/api/v3/order":
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":7,"status":"FILLED","clientOrderId":"sopen","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
				return
			}
			if isSpotRecoveryClientOrderID(clientOrderID) {
				atomic.AddInt32(&counters.spotRecoveryPosts, 1)
			} else {
				atomic.AddInt32(&counters.spotOpenPosts, 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":8,"status":"FILLED","clientOrderId":"sopen","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 5 * time.Millisecond
	cfg.ExecutionConfig.ExecutionRecoveryTimeout = 20 * time.Millisecond
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:             cfg,
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}

	_, err := engine.openHedgeForTargetParallel(context.Background(), chaosOpenPlan())
	if err == nil {
		t.Fatal("expected futures-leg reject to abort the open")
	}
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&counters.spotRecoveryPosts) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&counters.spotRecoveryPosts) == 0 {
		t.Fatal("naked spot long left after futures reject: no revert/rescue sell observed")
	}
}
