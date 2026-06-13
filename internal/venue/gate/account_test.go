package gate

import (
	"context"
	"testing"
)

func TestFetchBorrowQuoteParseSuccess(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathUnifiedBorrowable:   `{"currency":"H","amount":"123.45"}`,
		pathUnifiedEstimateRate: `{"H":"0.00002"}`,
	})
	q, err := a.FetchBorrowQuote(context.Background(), "H")
	if err != nil {
		t.Fatalf("FetchBorrowQuote: %v", err)
	}
	if q.Asset != "H" || !q.Available || q.MaxBorrowable != 123.45 || q.HourlyRate != 0.00002 {
		t.Fatalf("解析错误: %+v", q)
	}
}

func TestFetchBorrowQuoteZeroAmountNotAvailable(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathUnifiedBorrowable:   `{"currency":"H","amount":"0"}`,
		pathUnifiedEstimateRate: `{"H":"0.00002"}`,
	})
	q, err := a.FetchBorrowQuote(context.Background(), "H")
	if err != nil {
		t.Fatalf("FetchBorrowQuote: %v", err)
	}
	if q.Available {
		t.Fatalf("amount=0 应不可借: %+v", q)
	}
}

func TestFetchBorrowQuotePropagatesError(t *testing.T) {
	a := fakeGate(t, map[string]string{})
	if _, err := a.FetchBorrowQuote(context.Background(), "H"); err == nil {
		t.Fatal("缺失路由应报错")
	}
}
