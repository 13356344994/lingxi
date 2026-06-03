package money

import (
	"math"
	"testing"
)

func TestFromUSDTRounding(t *testing.T) {
	cases := map[float64]Micros{
		0:           0,
		1:           1_000_000,
		-2.5:        -2_500_000,
		0.0000005:   1, // 半单位向上（远离零）
		-0.0000005:  -1,
		0.00000049:  0,
		123.456789:  123_456_789,
		-123.456789: -123_456_789,
	}
	for in, want := range cases {
		if got := FromUSDT(in); got != want {
			t.Fatalf("FromUSDT(%v)=%d want %d", in, got, want)
		}
	}
	if FromUSDT(math.NaN()) != 0 || FromUSDT(math.Inf(1)) != 0 {
		t.Fatalf("non-finite should map to 0")
	}
}

func TestRoundTrip(t *testing.T) {
	for _, usdt := range []float64{0, 1, 12.5, -3.25, 1000.123456} {
		if got := FromUSDT(usdt).USDT(); math.Abs(got-usdt) > 1e-9 {
			t.Fatalf("round trip %v -> %v", usdt, got)
		}
	}
}

// TestSumEliminatesFloatDrift 是本内核的核心动机：0.1 这类二进制不可精确表示的数大量累加时，
// 朴素 float64 连加会累积可观的漂移，而定点求和逐位精确。
func TestSumEliminatesFloatDrift(t *testing.T) {
	const n = 1_000_000
	naive := 0.0
	vals := make([]float64, n)
	for i := range vals {
		vals[i] = 0.1
		naive += 0.1
	}
	exact := SumUSDT(vals...)
	if exact != float64(n)*0.1 {
		// 期望精确等于 100000；先确认定点结果就是整 100000.000000
		if FromUSDT(exact) != FromUSDT(100000) {
			t.Fatalf("fixed-point sum=%v want 100000", exact)
		}
	}
	// 朴素连加应当与精确值产生可测的偏差，证明问题真实存在。
	if math.Abs(naive-100000) < 1e-6 {
		t.Logf("naive float drift below 1e-6 on this platform: %v", naive)
	}
	if FromUSDT(exact) != 100_000*scale {
		t.Fatalf("exact micros=%d want %d", FromUSDT(exact), int64(100_000*scale))
	}
}

func TestMulRate(t *testing.T) {
	notional := FromUSDT(10000) // 10000 USDT
	fee := notional.MulRate(0.0008)
	if fee != FromUSDT(8) {
		t.Fatalf("fee=%s want 8.000000", fee)
	}
	if notional.MulRate(math.Inf(1)) != 0 {
		t.Fatalf("non-finite rate should yield 0")
	}
}

func TestArithmeticAndHelpers(t *testing.T) {
	a := FromUSDT(5)
	b := FromUSDT(3)
	if a.Add(b) != FromUSDT(8) || a.Sub(b) != FromUSDT(2) || b.Sub(a) != FromUSDT(-2) {
		t.Fatalf("arithmetic mismatch")
	}
	if !FromUSDT(-1).IsNegative() || !FromUSDT(1).IsPositive() || !FromUSDT(0).IsZero() {
		t.Fatalf("sign helpers mismatch")
	}
	if FromUSDT(-7).Abs() != FromUSDT(7) || FromUSDT(-7).Neg() != FromUSDT(7) {
		t.Fatalf("abs/neg mismatch")
	}
}

func TestString(t *testing.T) {
	cases := map[float64]string{
		0:          "0.000000",
		1.5:        "1.500000",
		-2.000001:  "-2.000001",
		123.456789: "123.456789",
	}
	for in, want := range cases {
		if got := FromUSDT(in).String(); got != want {
			t.Fatalf("String(%v)=%q want %q", in, got, want)
		}
	}
}
