package venue

import "testing"

// recordTB 是 TB 的录制实现:统计 Errorf/Fatalf 次数,用于验证一致性套件本身能区分合规/违规适配器。
type recordTB struct{ errs int }

func (r *recordTB) Helper()               {}
func (r *recordTB) Errorf(string, ...any) { r.errs++ }
func (r *recordTB) Fatalf(string, ...any) { r.errs++ }

// goodStub 是一个完全合规的适配器:能力自洽、Classify 契约正确、所有能力声明为支持(故跳过 ErrUnsupported 检查)。
type goodStub struct{ stubVenue }

func (goodStub) Capabilities() Capabilities {
	return Capabilities{
		SupportsNegativeCarry:    true,
		FundingIntervalsHours:    []int{8},
		HasNativeDeadMan:         true,
		MaxBatchOrders:           1,
		SupportsCapitalRebalance: true,
		CapitalWallets:           []Wallet{"spot"},
	}
}

func (goodStub) Classify(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	return "request_error", "minor"
}

func TestRunConformanceGoodVenuePasses(t *testing.T) {
	rec := &recordTB{}
	RunConformance(rec, goodStub{stubVenue{name: "good"}}, ConformanceConfig{SampleSymbols: []Symbol{"BTC-USDT"}})
	if rec.errs != 0 {
		t.Fatalf("合规适配器应零失败, got %d", rec.errs)
	}
}

func TestRunConformanceCatchesBadVenue(t *testing.T) {
	// 原始 stubVenue:FundingIntervalsHours 空、MaxBatchOrders=0、Classify(nil) 非空、ExecuteRebalance 不返回
	// ErrUnsupported(而 Capabilities 默认 SupportsCapitalRebalance=false)——多处违约,套件必须报错。
	rec := &recordTB{}
	RunConformance(rec, stubVenue{name: "bad"}, ConformanceConfig{SampleSymbols: []Symbol{"BTC-USDT"}})
	if rec.errs == 0 {
		t.Fatalf("违规适配器应被套件捕获, got 0")
	}
}
