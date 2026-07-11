package okx

import (
	"context"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestFetchRealizedFunding(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathAccountBills: `{"code":"0","msg":"","data":[
			{"instId":"BTC-USDT-SWAP","ccy":"USDT","balChg":"0.0123","pnl":"","ts":"1700000001000"},
			{"instId":"BTC-USDT-SWAP","ccy":"USDT","balChg":"-0.0040","pnl":"","ts":"1699999999000"}
		]}`,
	})
	since := time.UnixMilli(1700000000000).UTC()
	got, err := a.FetchRealizedFunding(context.Background(), venue.Symbol("BTC-USDT"), since)
	if err != nil {
		t.Fatalf("FetchRealizedFunding: %v", err)
	}
	if len(got) != 1 || got[0].Income != 0.0123 || got[0].Symbol != "BTC-USDT" || got[0].Asset != "USDT" {
		t.Fatalf("资金费流水解析错误: %+v", got)
	}
}
