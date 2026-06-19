package okx

import (
	"context"
	"testing"
)

// TestVerifyAPIKeyPosturePermParsing 钉死 perm 字段的真实解析:带提现权限的 key 是高危配置,
// 安全自检必须如实上报 CanWithdraw,硬编码 false 会把真实风险盖住。
func TestVerifyAPIKeyPosturePermParsing(t *testing.T) {
	cases := []struct {
		name             string
		perm             string
		ip               string
		wantTrade        bool
		wantWithdraw     bool
		wantIPRestricted bool
	}{
		{"只读+交易(期望姿态)", "read_only,trade", "1.2.3.4", true, false, true},
		{"含提现(高危,必须暴露)", "read_only,trade,withdraw", "", true, true, false},
		{"纯只读", "read_only", "", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := fakeOKX(t, map[string]string{
				pathAccountConfig: `{"code":"0","msg":"","data":[{"acctLv":"2","posMode":"net_mode","perm":"` + tc.perm + `","ip":"` + tc.ip + `"}]}`,
			})
			kp, err := a.VerifyAPIKeyPosture(context.Background())
			if err != nil {
				t.Fatalf("VerifyAPIKeyPosture: %v", err)
			}
			if kp.CanTrade != tc.wantTrade || kp.CanWithdraw != tc.wantWithdraw || kp.IPRestricted != tc.wantIPRestricted {
				t.Errorf("姿态解析错误: got trade=%t withdraw=%t ipRestricted=%t, want %t/%t/%t",
					kp.CanTrade, kp.CanWithdraw, kp.IPRestricted, tc.wantTrade, tc.wantWithdraw, tc.wantIPRestricted)
			}
			if kp.Notes == "" {
				t.Error("Notes 应含原始 perm 串")
			}
		})
	}
}

func TestFetchBorrowQuoteParseSuccess(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathInstruments:    `{"code":"0","msg":"","data":[{"instId":"H-USDT","instType":"SPOT","baseCcy":"H","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"0.1","minSz":"0.1"}]}`,
		pathInterestLimits: `{"code":"0","msg":"","data":[{"records":[{"ccy":"H","rate":"0.0024","surplusLmt":"888"}]}]}`,
	})
	q, err := a.FetchBorrowQuote(context.Background(), "H")
	if err != nil {
		t.Fatalf("FetchBorrowQuote: %v", err)
	}
	if q.Asset != "H" || !q.Available || q.MaxBorrowable != 888 {
		t.Fatalf("解析错误: %+v", q)
	}
	wantHourly := 0.0024 / 24.0
	if q.HourlyRate < wantHourly-1e-12 || q.HourlyRate > wantHourly+1e-12 {
		t.Fatalf("HourlyRate=%v want=%v", q.HourlyRate, wantHourly)
	}
}

func TestFetchBorrowQuoteResolveBorrowCcyFromCanonicalSymbol(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathInstruments:    `{"code":"0","msg":"","data":[{"instId":"ARB-USDT","instType":"SPOT","baseCcy":"ARB","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"0.1","minSz":"0.1"}]}`,
		pathInterestLimits: `{"code":"0","msg":"","data":[{"records":[{"ccy":"ARB","rate":"0.0012","surplusLmt":"12000"}]}]}`,
	})
	q, err := a.FetchBorrowQuote(context.Background(), "ARB-USDT")
	if err != nil {
		t.Fatalf("FetchBorrowQuote: %v", err)
	}
	if q.Asset != "ARB" || !q.Available || q.MaxBorrowable != 12000 {
		t.Fatalf("canonical symbol mapping: %+v", q)
	}
}

func TestResolveBorrowCcyUsesPerpCtValCcyWhenSpotMissing(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathInstruments: `{"code":"0","msg":"","data":[{"instId":"JUP-USDT-SWAP","instType":"SWAP","baseCcy":"","quoteCcy":"","settleCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"1","minSz":"1","ctVal":"1","ctValCcy":"JUP"}]}`,
	})
	ccy, err := a.resolveBorrowCcy(context.Background(), "JUP")
	if err != nil {
		t.Fatalf("resolveBorrowCcy: %v", err)
	}
	if ccy != "JUP" {
		t.Fatalf("ccy = %q want JUP", ccy)
	}
}

func TestFetchBorrowQuotePropagatesError(t *testing.T) {
	a := fakeOKX(t, map[string]string{})
	if _, err := a.FetchBorrowQuote(context.Background(), "H"); err == nil {
		t.Fatal("缺失路由应报错")
	}
}
