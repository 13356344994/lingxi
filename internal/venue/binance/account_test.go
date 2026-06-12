package binance

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

func TestFetchBalancesMergeSpotAndFutures(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, PathFuturesBalance):
			_, _ = w.Write([]byte(`[
				{"asset":"USDT","balance":"1000","availableBalance":"800","crossWalletBalance":"1000"},
				{"asset":"BNB","balance":"2","availableBalance":"2","crossWalletBalance":"2"}
			]`))
		case strings.Contains(r.URL.Path, PathSpotAccount):
			_, _ = w.Write([]byte(`{"balances":[
				{"asset":"USDT","free":"100","locked":"50"},
				{"asset":"BTC","free":"0.1","locked":"0"}
			]}`))
		default:
			http.NotFound(w, r)
		}
	})
	bal, err := a.FetchBalances(context.Background())
	if err != nil {
		t.Fatalf("FetchBalances: %v", err)
	}
	usdt := bal.ByAsset["USDT"]
	if usdt.Free != 900 || usdt.Locked != 250 {
		t.Fatalf("USDT 合并口径错误 free=%v locked=%v (期望 900/250)", usdt.Free, usdt.Locked)
	}
	if bal.ByAsset["BTC"].Free != 0.1 {
		t.Fatalf("BTC free=%v", bal.ByAsset["BTC"].Free)
	}
}

func TestFetchPositionsFiltersZeroAndParsesShort(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","positionAmt":"0.000","entryPrice":"0","markPrice":"60000","unRealizedProfit":"0","leverage":"10","marginType":"cross"},
			{"symbol":"ETHUSDT","positionAmt":"-1.5","entryPrice":"3000","markPrice":"2900","unRealizedProfit":"150","leverage":"5","marginType":"isolated"}
		]`))
	})
	pos, err := a.FetchPositions(context.Background())
	if err != nil {
		t.Fatalf("FetchPositions: %v", err)
	}
	if len(pos) != 1 {
		t.Fatalf("应过滤零仓, got %d", len(pos))
	}
	if pos[0].Symbol != "ETH-USDT" || pos[0].PositionAmt != -1.5 {
		t.Fatalf("持仓解析错误: %+v", pos[0])
	}
	if pos[0].MarginMode != venue.MarginModeIsolated {
		t.Fatalf("marginType 应为 isolated")
	}
}

func TestVerifyAPIKeyPostureMapping(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"ipRestrict":true,
			"enableWithdrawals":false,
			"enableReading":true,
			"enableSpotAndMarginTrading":true,
			"enableFutures":false
		}`))
	})
	posture, err := a.VerifyAPIKeyPosture(context.Background())
	if err != nil {
		t.Fatalf("VerifyAPIKeyPosture: %v", err)
	}
	if !posture.CanTrade || posture.CanWithdraw || !posture.IPRestricted {
		t.Fatalf("KeyPosture 映射错误: %+v", posture)
	}
}

func TestFetchLeverageBracketsUnsupported(t *testing.T) {
	a := New(exchange.NewClient(exchange.ClientConfig{}))
	if _, err := a.FetchLeverageBrackets(context.Background(), "BTC-USDT"); err != venue.ErrUnsupported {
		t.Fatalf("FetchLeverageBrackets 应 ErrUnsupported, got %v", err)
	}
}

func TestArmDeadMansSwitchUnsupported(t *testing.T) {
	a := New(exchange.NewClient(exchange.ClientConfig{}))
	if err := a.ArmDeadMansSwitch(context.Background(), time.Minute); err != venue.ErrUnsupported {
		t.Fatalf("ArmDeadMansSwitch 应 ErrUnsupported, got %v", err)
	}
}
