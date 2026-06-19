package okx

import (
	"context"
	"math"
	"strconv"
	"testing"

	"lingxi/internal/venue"
)

// okxPerpOrderCoinQty 把 OrderRequest.Qty(币) 按适配器 PlaceOrder 口径换算为实际成交币数量。
func okxPerpOrderCoinQty(qty, ctVal, lotSz float64) (float64, error) {
	sz, err := qtyToContractSz(qty, ctVal, lotSz)
	if err != nil {
		return 0, err
	}
	contracts, err := strconv.ParseFloat(sz, 64)
	if err != nil {
		return 0, err
	}
	return contracts * ctVal, nil
}

// TestPerpCoinToContractRoundTrip 钉死:下单 N 币 → sz 张数 → ×ctVal 还原币数,与 normalize 口径一致。
func TestPerpCoinToContractRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		qty      float64
		ctVal    float64
		lotSz    float64
		wantSz   string
		wantCoin float64
	}{
		{"HOME lot1", 250, 100, 1, "2", 200},
		{"H lot2 ct10", 30, 10, 2, "2", 20},
		{"H lot2 ct10 larger", 70, 10, 2, "6", 60},
		{"BTC", 0.05, 0.01, 0.001, "5", 0.05},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := fakeOKX(t, map[string]string{
				pathInstruments: `{"code":"0","msg":"","data":[{"instId":"H-USDT-SWAP","instType":"SWAP","baseCcy":"H","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"` +
					formatLot(tc.lotSz) + `","minSz":"1","ctVal":"` + formatLot(tc.ctVal) + `","ctValCcy":"H"}]}`,
			})
			body, err := a.buildPlaceOrderBody(context.Background(), venue.OrderRequest{
				Market: venue.MarketPerp, Symbol: "H-USDT", Side: venue.SideBuy,
				Type: venue.OrderTypeMarket, Qty: tc.qty,
			})
			if err != nil {
				if tc.wantSz == "" {
					return
				}
				t.Fatalf("buildPlaceOrderBody: %v", err)
			}
			if body.Sz != tc.wantSz {
				t.Fatalf("sz=%q want %q", body.Sz, tc.wantSz)
			}
			coin, err := okxPerpOrderCoinQty(tc.qty, tc.ctVal, tc.lotSz)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(coin-tc.wantCoin) > 1e-9 {
				t.Fatalf("coin qty=%v want %v", coin, tc.wantCoin)
			}
		})
	}
}

func formatLot(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
