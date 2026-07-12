package fingerprint

import (
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

func TestDynamicMinNotionalFromPIT(t *testing.T) {
	v := datatruth.InstrumentVersion{
		InstrumentID: "BTC-USDT", Venue: "okx", Symbol: "BTC-USDT-SWAP",
		TickSize: "0.1", LotSize: "0.01", ContractValue: "0.01",
		ListedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		KnownFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	c := buildInstrumentConstraints(v, 100000)
	if c.Status != StatusAvailable {
		t.Fatalf("status=%s", c.Status)
	}
	if c.DynamicMinNotionalUSDT != 10 {
		t.Fatalf("dynamic=%v want 10", c.DynamicMinNotionalUSDT)
	}
}

func TestInverseContractFailClosed(t *testing.T) {
	v := datatruth.InstrumentVersion{
		InstrumentID: "BTC-USD-SWAP", Venue: "okx", Symbol: "BTC-USD-SWAP",
		TickSize: "0.1", LotSize: "1", ContractValue: "100",
		ListedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		KnownFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	c := buildInstrumentConstraints(v, 100000)
	if c.Status != StatusUnknown {
		t.Fatalf("inverse must fail closed: %+v", c)
	}
}

func TestStaticMinNotionalNotUsedForAvailability(t *testing.T) {
	v := datatruth.InstrumentVersion{
		InstrumentID: "BTC-USDT", Venue: "okx", Symbol: "BTC-USDT-SWAP",
		TickSize: "0.1", LotSize: "0.01", ContractValue: "0.01", MinNotional: "5",
		ListedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		KnownFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	c := buildInstrumentConstraints(v, 100000)
	if c.MinNotional != "5" {
		t.Fatal("static retained for audit")
	}
	if c.ContractType != ctTypeLinear {
		t.Fatalf("ct_type=%q want linear", c.ContractType)
	}
	if c.DynamicMinNotionalUSDT <= 0 {
		t.Fatal("dynamic must drive eligibility")
	}
}

func TestOKXLinearUSDTEvidenceWithoutExplicitCtType(t *testing.T) {
	v := datatruth.InstrumentVersion{
		InstrumentID: "ETH-USDT", Venue: "okx", Symbol: "ETH-USDT",
		TickSize: "0.01", LotSize: "0.1", ContractValue: "0.1",
		ListedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		KnownFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	c := buildInstrumentConstraints(v, 2000)
	if c.ContractType != ctTypeLinear {
		t.Fatalf("ct_type=%q want linear from USDT evidence", c.ContractType)
	}
	if c.Status != StatusAvailable {
		t.Fatalf("status=%s want AVAILABLE", c.Status)
	}
	if c.DynamicMinNotionalUSDT < 19.99 || c.DynamicMinNotionalUSDT > 20.01 {
		t.Fatalf("dynamic=%v want ~20", c.DynamicMinNotionalUSDT)
	}
}

func TestKnownLinearInstrumentFixtureHelper(t *testing.T) {
	c := KnownLinearInstrument("0.01", "1", "0.1", 100)
	if c.Status != StatusAvailable || c.ContractType != ctTypeLinear {
		t.Fatalf("fixture helper: %+v", c)
	}
	if c.DynamicMinNotionalUSDT != 1 {
		t.Fatalf("dynamic=%v want 1", c.DynamicMinNotionalUSDT)
	}
	if c.MinSize != "0.01" {
		t.Fatalf("min_sz=%q", c.MinSize)
	}
}
