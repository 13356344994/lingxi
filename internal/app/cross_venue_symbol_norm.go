package app

import (
	"math"
	"strconv"
	"strings"
)

// crossVenueAllowedMultipliers 计价倍数白名单:仅 10 的整数次幂,与 binance/bybit 1000x 命名一致。
var crossVenueAllowedMultipliers = []float64{1, 1000, 10000, 1000000}

const crossVenueCrossDenomPriceRatioTolerance = 0.02 // 跨计价配对:归一 mid 价比须在 ±2% 内

// crossVenueEconomicBase 从 venue-canonical 永续 symbol 解析经济 base 与计价倍数。
// 支持 1000/10000/1000000 前缀、k 前缀、SHIB1000 类后缀;无法唯一确定倍数时 ok=false(禁止跨计价配对)。
func crossVenueEconomicBase(canonicalSym string) (base string, mult float64, ok bool) {
	canonicalSym = strings.TrimSpace(canonicalSym)
	if canonicalSym == "" {
		return "", 0, false
	}
	basePart, quote, okSplit := crossVenueSplitSymbol(canonicalSym)
	if !okSplit || basePart == "" || quote == "" {
		return "", 0, false
	}
	parsedBase, parsedMult, okParse := crossVenueParseBaseMultiplier(basePart)
	if !okParse || parsedBase == "" {
		return "", 0, false
	}
	if !crossVenueMultiplierAllowed(parsedMult) {
		return "", 0, false
	}
	return parsedBase, parsedMult, true
}

func crossVenueSplitSymbol(sym string) (basePart, quote string, ok bool) {
	sym = strings.TrimSpace(sym)
	if sym == "" {
		return "", "", false
	}
	if i := strings.LastIndex(sym, "-"); i > 0 && i < len(sym)-1 {
		return sym[:i], sym[i+1:], true
	}
	return sym, "", true
}

func crossVenueEconomicPairKey(canonicalSym string) (pairKey string, base string, mult float64, multKnown bool) {
	basePart, quote, okSplit := crossVenueSplitSymbol(canonicalSym)
	if !okSplit {
		return canonicalSym, "", 1, false
	}
	parsedBase, parsedMult, okParse := crossVenueParseBaseMultiplier(basePart)
	if !okParse || !crossVenueMultiplierAllowed(parsedMult) {
		return canonicalSym, "", 1, false
	}
	if quote != "" {
		return parsedBase + "-" + quote, parsedBase, parsedMult, true
	}
	return parsedBase, parsedBase, parsedMult, true
}

func crossVenueParseBaseMultiplier(basePart string) (base string, mult float64, ok bool) {
	basePart = strings.TrimSpace(basePart)
	if basePart == "" {
		return "", 0, false
	}
	// 后缀变体: SHIB1000 / PEPE10000
	for _, m := range []float64{1000000, 10000, 1000} {
		suffix := formatCrossVenueMultiplier(m)
		if strings.HasSuffix(basePart, suffix) && len(basePart) > len(suffix) {
			return basePart[:len(basePart)-len(suffix)], m, true
		}
	}
	// 前缀变体: 1000PEPE / 10000SATS
	for _, m := range []float64{1000000, 10000, 1000} {
		prefix := formatCrossVenueMultiplier(m)
		if strings.HasPrefix(basePart, prefix) && len(basePart) > len(prefix) {
			return basePart[len(prefix):], m, true
		}
	}
	// k 前缀: kPEPE → 1000×PEPE
	if len(basePart) > 1 && (basePart[0] == 'k' || basePart[0] == 'K') {
		rest := basePart[1:]
		if rest != "" && crossVenueIsAlphaNumericBase(rest) {
			return rest, 1000, true
		}
	}
	if crossVenueIsAlphaNumericBase(basePart) {
		return basePart, 1, true
	}
	return "", 0, false
}

func formatCrossVenueMultiplier(m float64) string {
	return strconv.FormatFloat(m, 'f', 0, 64)
}

func crossVenueMultiplierAllowed(m float64) bool {
	for _, allowed := range crossVenueAllowedMultipliers {
		if math.Abs(m-allowed) < 1e-9 {
			return true
		}
	}
	return false
}

func crossVenueIsAlphaNumericBase(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func crossVenueOppHighSymbol(opp CrossVenueOpportunity) string {
	if s := strings.TrimSpace(opp.HighSymbol); s != "" {
		return s
	}
	return opp.Symbol
}

func crossVenueOppLowSymbol(opp CrossVenueOpportunity) string {
	if s := strings.TrimSpace(opp.LowSymbol); s != "" {
		return s
	}
	return opp.Symbol
}

func crossVenueOppHighMult(opp CrossVenueOpportunity) float64 {
	if opp.HighMult > 0 {
		return opp.HighMult
	}
	if _, m, ok := crossVenueEconomicBase(crossVenueOppHighSymbol(opp)); ok && m > 0 {
		return m
	}
	return 1
}

func crossVenueOppLowMult(opp CrossVenueOpportunity) float64 {
	if opp.LowMult > 0 {
		return opp.LowMult
	}
	if _, m, ok := crossVenueEconomicBase(crossVenueOppLowSymbol(opp)); ok && m > 0 {
		return m
	}
	return 1
}

func crossVenueIsCrossDenomPair(opp CrossVenueOpportunity) bool {
	return crossVenueOppHighSymbol(opp) != crossVenueOppLowSymbol(opp)
}

// crossVenueCrossDenomGuardOK 硬护栏:经济 base 相同、倍数可解析、归一 mid 价比≈倍数比(±2%)。
// 同计价对(HighSymbol==LowSymbol)直接通过,由 mid 偏离闸走原逻辑。
func crossVenueCrossDenomGuardOK(opp CrossVenueOpportunity, highMid, lowMid float64) bool {
	highSym := crossVenueOppHighSymbol(opp)
	lowSym := crossVenueOppLowSymbol(opp)
	if highSym == lowSym {
		return true
	}
	highBase, highMult, okH := crossVenueEconomicBase(highSym)
	lowBase, lowMult, okL := crossVenueEconomicBase(lowSym)
	if !okH || !okL || highBase == "" || lowBase == "" {
		return false
	}
	if !strings.EqualFold(highBase, lowBase) {
		return false
	}
	if highMult <= 0 || lowMult <= 0 {
		return false
	}
	if highMid <= 0 || lowMid <= 0 {
		return false
	}
	return crossVenueNormalizedMidPriceRatioOK(highMid, lowMid, highMult, lowMult)
}

func crossVenueNormalizedMidPriceRatioOK(highMid, lowMid, highMult, lowMult float64) bool {
	if highMid <= 0 || lowMid <= 0 || highMult <= 0 || lowMult <= 0 {
		return false
	}
	if math.Abs(highMult-lowMult) < 1e-9 {
		normHigh := highMid / highMult
		normLow := lowMid / lowMult
		if normHigh <= 0 || normLow <= 0 {
			return false
		}
		dev := math.Abs(normHigh-normLow) / math.Max(normHigh, normLow)
		return dev <= crossVenueCrossDenomPriceRatioTolerance
	}
	// 跨倍数:highMid/lowMid 应≈highMult/lowMult
	expectedRatio := highMult / lowMult
	actualRatio := highMid / lowMid
	if expectedRatio <= 0 || actualRatio <= 0 {
		return false
	}
	dev := math.Abs(actualRatio-expectedRatio) / expectedRatio
	return dev <= crossVenueCrossDenomPriceRatioTolerance
}

// crossVenueMidDeviation 两所 mid 偏离(同计价直接比;跨计价用倍数归一后比)。
func crossVenueMidDeviation(highMid, lowMid, highMult, lowMult float64) float64 {
	if highMid <= 0 || lowMid <= 0 {
		return math.MaxFloat64
	}
	if highMult <= 0 {
		highMult = 1
	}
	if lowMult <= 0 {
		lowMult = 1
	}
	if math.Abs(highMult-lowMult) < 1e-9 {
		return math.Abs(highMid-lowMid) / math.Max(highMid, lowMid)
	}
	normHigh := highMid / highMult
	normLow := lowMid / lowMult
	return math.Abs(normHigh-normLow) / math.Max(normHigh, normLow)
}

// crossVenueAdverseMidDeviation 返回「不利方向」的归一化基差(带符号)。
// 策略在高费腿做空、低费腿做多;价格腿盈亏 = 开仓基差 − 平仓基差,基差 B = P_high − P_low。
// 若基差收敛(B→0):B>0(做空的高腿更贵)→ 盈利(顺向有利);B<0(做多的低腿更贵)→ 亏损(不利)。
// 故定义不利偏离 = (P_low − P_high)/max:>0 表示做多腿更贵=不利(应计成本);≤0 表示顺向有利(不计)。
func crossVenueAdverseMidDeviation(highMid, lowMid, highMult, lowMult float64) float64 {
	if highMid <= 0 || lowMid <= 0 {
		return 0
	}
	if highMult <= 0 {
		highMult = 1
	}
	if lowMult <= 0 {
		lowMult = 1
	}
	normHigh := highMid / highMult
	normLow := lowMid / lowMult
	denom := math.Max(normHigh, normLow)
	if denom <= 0 {
		return 0
	}
	return (normLow - normHigh) / denom
}

func crossVenueLegSymbolFromOpp(opp CrossVenueOpportunity, highLeg bool) string {
	if highLeg {
		return crossVenueOppHighSymbol(opp)
	}
	return crossVenueOppLowSymbol(opp)
}

func crossVenuePositionHighSymbol(pos CrossVenuePosition) string {
	if s := strings.TrimSpace(pos.HighLeg.Symbol); s != "" {
		return s
	}
	return pos.Symbol
}

func crossVenuePositionLowSymbol(pos CrossVenuePosition) string {
	if s := strings.TrimSpace(pos.LowLeg.Symbol); s != "" {
		return s
	}
	return pos.Symbol
}
