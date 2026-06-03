package strategy

import (
	"math"
	"testing"
)

func TestNormalizeFundingDirectionMode(t *testing.T) {
	cases := map[string]string{
		"":              FundingModeBoth,
		"garbage":       FundingModeBoth,
		" Both ":        FundingModeBoth,
		"POSITIVE_ONLY": FundingModePositive,
		"negative_only": FundingModeNegative,
	}
	for in, want := range cases {
		if got := NormalizeFundingDirectionMode(in); got != want {
			t.Fatalf("NormalizeFundingDirectionMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsValidFundingDirectionMode(t *testing.T) {
	if !IsValidFundingDirectionMode("both") || !IsValidFundingDirectionMode("POSITIVE_ONLY") {
		t.Fatal("expected supported modes to be valid")
	}
	if IsValidFundingDirectionMode("garbage") {
		t.Fatal("expected garbage mode to be invalid")
	}
}

// TestFundingPriorityScoreIgnoresRealizedRate 钉死 H1：打分只看点位预估，绝不接受真实费率。
func TestFundingPriorityScoreIgnoresRealizedRate(t *testing.T) {
	if got := FundingPriorityScore(0, FundingModePositive); got != 0 {
		t.Fatalf("missing estimate must score 0, got %.6f", got)
	}
	if got := FundingPriorityScore(0.01, FundingModePositive); got <= 0 {
		t.Fatalf("positive estimate should score positive, got %.6f", got)
	}
	if got := FundingPriorityScore(0.01, FundingModeNegative); got != 0 {
		t.Fatalf("positive rate under negative-only must score 0, got %.6f", got)
	}
	if got := FundingPriorityScore(-0.02, FundingModeNegative); math.Abs(got-0.02) > 1e-12 {
		t.Fatalf("negative-only should score magnitude of negative rate, got %.6f", got)
	}
	if got := FundingPriorityScore(-0.02, FundingModeBoth); math.Abs(got-0.02) > 1e-12 {
		t.Fatalf("both mode should score absolute value, got %.6f", got)
	}
}

func TestPreselectPriorityScorePrefersScreeningScore(t *testing.T) {
	if got := PreselectPriorityScore(0.7, 0.01, FundingModePositive); got != 0.7 {
		t.Fatalf("expected screening score to win, got %.6f", got)
	}
	if got := PreselectPriorityScore(0, 0.01, FundingModePositive); got <= 0 {
		t.Fatalf("expected fallback to estimate score, got %.6f", got)
	}
}

func TestComputeBasisRate(t *testing.T) {
	if got := ComputeBasisRate(0, 100); got != 0 {
		t.Fatalf("non-positive spot must yield 0, got %.6f", got)
	}
	if got := ComputeBasisRate(100, 101); math.Abs(got-(-0.01)) > 1e-12 {
		t.Fatalf("expected -0.01 basis, got %.6f", got)
	}
}

func TestComputeAdverseBasisPenalty(t *testing.T) {
	if got := ComputeAdverseBasisPenalty(-0.01, 1000, 1000); got != 0 {
		t.Fatalf("favorable basis must not be penalized, got %.6f", got)
	}
	if got := ComputeAdverseBasisPenalty(0.01, 800, 1000); math.Abs(got-10) > 1e-9 {
		t.Fatalf("expected penalty on larger leg notional, got %.6f", got)
	}
}

func TestRatioOrZero(t *testing.T) {
	if got := RatioOrZero(0, 10); got != 0 {
		t.Fatalf("non-positive numerator must yield 0, got %.6f", got)
	}
	if got := RatioOrZero(10, 0); got != 0 {
		t.Fatalf("non-positive denominator must yield 0, got %.6f", got)
	}
	if got := RatioOrZero(5, 10); got != 0.5 {
		t.Fatalf("expected 0.5, got %.6f", got)
	}
}

func TestComputeDrawdownRate(t *testing.T) {
	if got := ComputeDrawdownRate(0, 10); got != 0 {
		t.Fatalf("non-positive peak must yield 0, got %.6f", got)
	}
	if got := ComputeDrawdownRate(100, 120); got != 0 {
		t.Fatalf("equity above peak must yield 0, got %.6f", got)
	}
	if got := ComputeDrawdownRate(100, 75); math.Abs(got-0.25) > 1e-12 {
		t.Fatalf("expected 0.25 drawdown, got %.6f", got)
	}
}

func TestEstimateMarketImpactRate(t *testing.T) {
	if got := EstimateMarketImpactRate(0, 0); got != 0.0025 {
		t.Fatalf("missing data must yield conservative cap 0.0025, got %.6f", got)
	}
	if got := EstimateMarketImpactRate(1, 1_000_000); got != 0.0001 {
		t.Fatalf("tiny impact must clamp to floor 0.0001, got %.6f", got)
	}
	if got := EstimateMarketImpactRate(1_000_000, 1); got != 0.0025 {
		t.Fatalf("huge impact must clamp to cap 0.0025, got %.6f", got)
	}
}
