package entryproof

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	CtTypeLinear  = "linear"
	CtTypeInverse = "inverse"
	CtTypeUnknown = "unknown"
)

// DynamicMinNotionalUSDT computes OKX linear contract minimum notional:
// min_sz * ct_val * price. Inverse/unknown contract types fail closed.
func DynamicMinNotionalUSDT(minSz, ctVal string, ctType string, price float64) (float64, string, error) {
	ctType = normalizeCtType(ctType)
	if ctType == CtTypeInverse || ctType == CtTypeUnknown {
		return 0, ctType, fmt.Errorf("entryproof: contract type %q not eligible for dynamic min notional", ctType)
	}
	if price <= 0 {
		return 0, ctType, fmt.Errorf("entryproof: price must be positive")
	}
	ms, err := parsePositiveDecimal(minSz)
	if err != nil {
		return 0, ctType, fmt.Errorf("entryproof: min_sz: %w", err)
	}
	cv, err := parsePositiveDecimal(ctVal)
	if err != nil {
		return 0, ctType, fmt.Errorf("entryproof: ct_val: %w", err)
	}
	return ms * cv * price, ctType, nil
}

func normalizeCtType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "linear", "usdt", "usdt-m", "swap_linear", "linear_perpetual":
		return CtTypeLinear
	case "inverse", "coin", "coin-m", "swap_inverse", "reverse":
		return CtTypeInverse
	default:
		return CtTypeUnknown
	}
}

func parsePositiveDecimal(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return v, nil
}
